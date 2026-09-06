package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// Deps is what every Stage worker shares.
type Deps struct {
	Pool      *pgxpool.Pool
	Client    *river.Client[pgx.Tx]
	Extractor Extractor
	TempDir   string
	Logger    *slog.Logger
	// AI reads role credentials and records verification verdicts; nil means
	// the paid Stages stay placeholders. AITransport, when set, is the
	// no-network double the tests inject beneath the provider clients.
	AI          *ai.Store
	AITransport http.RoundTripper
	// Media is FFmpeg for the transcribing Stage; nil keeps that Stage a
	// placeholder. TempMinFreeBytes is the preflight floor (ADR 0008) and
	// MaxUploadBytes the provider file limit above which audio is split
	// (zero means OpenAI's 25 MB).
	Media            Media
	TempMinFreeBytes int64
	MaxUploadBytes   int64
	// Hooks let later slices plug the paid Stages in without the runner
	// knowing them: the cost gate after captions, and the providers.
	Hooks Hooks
}

// Hooks are the seams the cost and generation slices fill.
type Hooks struct {
	// BeforePaidStage runs inside the captions completion transaction with
	// the Note locked. Returning false withholds the next Stage (a cost hold);
	// nil means no gate.
	BeforePaidStage func(ctx context.Context, tx pgx.Tx, note *NoteRow) (bool, error)
	// The work of the Stages later slices implement; nil means a placeholder
	// that fails the run and says what is missing.
	Transcribing work
	Generating   work
	Indexing     work
}

// result is what a Stage's work hands back to the runner.
type result struct {
	providerRequestID string
	fingerprint       []byte
	size              int64
	// apply writes the Stage's domain rows inside the completion transaction.
	apply func(ctx context.Context, tx pgx.Tx, note *NoteRow) error
	// next names the Stage to enqueue, or "" when this Stage ends the run.
	next string
	// skip names a Stage to mark skipped in the same transaction.
	skip string
	// terminal, when true, marks the Note ready.
	terminal bool
}

// StageRun is what a Stage's work knows about the attempt it runs in: the
// run, the Attempt row every provider call is charged to, and a scratch
// directory removed when the work returns.
type StageRun struct {
	Run       Run
	Stage     string
	AttemptID uuid.UUID
	AttemptNo int
	Dir       string
}

// work is one Stage's job: everything between opening the Attempt and the
// completion transaction.
type work func(ctx context.Context, d *Deps, note *NoteRow, sr *StageRun) (*result, error)

// runStage is the shape every Stage worker shares (docs/design/pipeline.md).
func runStage[T river.JobArgs](ctx context.Context, d *Deps, job *river.Job[T], run Run, stage string, do work) error {
	logger := d.Logger.With("note", run.NoteID, "version", run.ProcessingVersion, "stage", stage, "attempt", job.Attempt)

	// 1. Entry guard.
	note, err := loadNote(ctx, d.Pool, run.NoteID, false)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			logger.Info("note is gone; dropping job")
			return river.JobCancel(errors.New("note deleted"))
		}
		return err
	}
	if note.ProcessingVersion != run.ProcessingVersion {
		return river.JobCancel(errors.New("run superseded"))
	}
	if note.CancelRequestedAt != nil {
		if err := cancelStage(ctx, d.Pool, run, stage); err != nil {
			return err
		}
		return river.JobCancel(errors.New("cancelled"))
	}
	stageRow, err := loadStage(ctx, d.Pool, run, stage)
	if err != nil {
		return err
	}
	if stageRow.Status == "ready" || stageRow.Status == "skipped" {
		return nil // duplicate delivery
	}

	// 2. Open the Stage.
	if err := openStage(ctx, d.Pool, run, stage, job.ID); err != nil {
		return err
	}
	if err := publishProgress(ctx, d.Pool, note.UserID, run, stage, stage, "running", job.Attempt); err != nil {
		logger.Warn("progress publish failed", "err", err)
	}

	// 3. Open the Attempt before any external call.
	attemptKey := fmt.Sprintf("%s:%d:%s:%d", run.NoteID, run.ProcessingVersion, stage, job.Attempt)
	attemptID, err := openAttempt(ctx, d.Pool, stageRow.ID, job.Attempt, attemptKey)
	if err != nil {
		return err
	}
	dir := filepath.Join(d.TempDir, run.NoteID.String(), fmt.Sprint(run.ProcessingVersion), stage, attemptID.String())
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	// 7. Cleanup, whatever happens next.
	defer func() { _ = os.RemoveAll(dir) }()

	// 4. Do the work.
	res, workErr := do(ctx, d, note, &StageRun{Run: run, Stage: stage, AttemptID: attemptID, AttemptNo: job.Attempt, Dir: dir})
	if workErr != nil {
		se := classify(ctx, workErr)
		// A context cut short by a Delete (docs/design/pipeline.md,
		// "Cancellation mid-Stage"): the Stage ends cancelled, not retried.
		// A Stage timeout also cancels the context but requests nothing, so
		// it rides the ladder below.
		if ctx.Err() != nil {
			if fresh, err := loadNote(context.WithoutCancel(ctx), d.Pool, run.NoteID, false); err == nil && (fresh.CancelRequestedAt != nil || fresh.ProcessingVersion != run.ProcessingVersion) {
				se = Fail(Permanent, "cancelled", "The run was cancelled.", workErr)
				if err := closeAttemptFailed(context.WithoutCancel(ctx), d.Pool, attemptID, se); err != nil {
					logger.Error("close attempt", "err", err)
				}
				if err := cancelStage(context.WithoutCancel(ctx), d.Pool, run, stage); err != nil {
					return err
				}
				return river.JobCancel(errors.New("cancelled during work"))
			}
		}
		if err := closeAttemptFailed(context.WithoutCancel(ctx), d.Pool, attemptID, se); err != nil {
			logger.Error("close attempt", "err", err)
		}
		if se.Verdict != Transient {
			logger.Warn("stage failed permanently", "code", se.Code, "err", workErr)
			if err := failStage(context.WithoutCancel(ctx), d.Pool, run, stage); err != nil {
				return err
			}
			return river.JobCancel(workErr)
		}
		if job.Attempt >= job.MaxAttempts {
			logger.Warn("stage exhausted its attempts", "code", se.Code, "err", workErr)
			if err := failStage(context.WithoutCancel(ctx), d.Pool, run, stage); err != nil {
				return err
			}
		}
		return workErr
	}

	// 5. Close the Attempt with its fingerprint.
	if err := closeAttemptOK(ctx, d.Pool, attemptID, res.providerRequestID, res.fingerprint, res.size); err != nil {
		return err
	}

	// 6. The completion transaction: lock the Note, re-check cancellation,
	// write the domain rows, mark the Stage, complete this job and enqueue
	// the next, all or nothing.
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	locked, err := loadNote(ctx, tx, run.NoteID, true)
	if err != nil {
		return err
	}
	if locked.CancelRequestedAt != nil || locked.ProcessingVersion != run.ProcessingVersion {
		// Roll back: the late result is discarded and nothing is persisted.
		_ = tx.Rollback(ctx)
		if err := cancelStage(ctx, d.Pool, run, stage); err != nil {
			return err
		}
		return river.JobCancel(errors.New("cancelled during work"))
	}
	if res.apply != nil {
		if err := res.apply(ctx, tx, locked); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE note_stages SET status = 'ready', finished_at = now()
		 WHERE note_id = $1 AND processing_version = $2 AND stage = $3`, run.NoteID, run.ProcessingVersion, stage); err != nil {
		return err
	}
	if res.skip != "" {
		if _, err := tx.Exec(ctx, `
			UPDATE note_stages SET status = 'skipped', finished_at = now()
			 WHERE note_id = $1 AND processing_version = $2 AND stage = $3 AND status = 'pending'`, run.NoteID, run.ProcessingVersion, res.skip); err != nil {
			return err
		}
	}
	if err := publishProgress(ctx, tx, locked.UserID, run, stage, stage, "ready", job.Attempt); err != nil {
		logger.Warn("progress publish failed", "err", err)
	}
	if _, err := river.JobCompleteTx[*riverpgxv5.Driver](ctx, tx, job); err != nil {
		return err
	}
	if res.terminal {
		if _, err := tx.Exec(ctx, `UPDATE notes SET processing_state = 'ready', failure_stage = NULL WHERE id = $1`, run.NoteID); err != nil {
			return err
		}
		if _, err := vsync.Append(ctx, tx, locked.UserID, vsync.EntityNote, locked.ID, nil, vsync.ChangeUpdated, nil); err != nil {
			return err
		}
		if err := publishProgress(ctx, tx, locked.UserID, run, "ready", "", "", job.Attempt); err != nil {
			logger.Warn("progress publish failed", "err", err)
		}
	} else if res.next != "" {
		proceed := true
		// The gate sits after captions, the last free Stage, whatever the
		// next paid Stage is; one confirmation per run (ADR 0007).
		if stage == StageCaptions && d.Hooks.BeforePaidStage != nil {
			proceed, err = d.Hooks.BeforePaidStage(ctx, tx, locked)
			if err != nil {
				return err
			}
		}
		if proceed {
			if err := enqueueStage(ctx, d.Client, tx, run, res.next, time.Time{}); err != nil {
				return err
			}
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	logger.Info("stage ready")
	return nil
}

// enqueueStage inserts the next Stage's job and records it on its row.
func enqueueStage(ctx context.Context, client *river.Client[pgx.Tx], tx pgx.Tx, run Run, stage string, at time.Time) error {
	var args river.JobArgs
	switch stage {
	case StageMetadata:
		args = MetadataArgs{run}
	case StageCaptions:
		args = CaptionsArgs{run}
	case StageTranscribing:
		args = TranscribingArgs{run}
	case StageGenerating:
		args = GeneratingArgs{run}
	case StageIndexing:
		args = IndexingArgs{run}
	default:
		return fmt.Errorf("unknown stage %q", stage)
	}
	var opts *river.InsertOpts
	if !at.IsZero() {
		opts = &river.InsertOpts{ScheduledAt: at}
	}
	inserted, err := client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", stage, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE note_stages SET status = 'pending' WHERE note_id = $1 AND processing_version = $2 AND stage = $3 AND status = 'pending'`, run.NoteID, run.ProcessingVersion, stage); err != nil {
		return err
	}
	return setStageJob(ctx, tx, run, stage, inserted.Job.ID)
}

// classify maps any error onto a verdict. Typed StageErrors keep theirs; a
// cancelled context is transient; anything else is a fault of ours or of the
// network and earns a retry.
func classify(ctx context.Context, err error) *StageError {
	var se *StageError
	if errors.As(err, &se) {
		return se
	}
	if ctx.Err() != nil {
		return Fail(Transient, "timeout", "The step took too long and will be retried.", err)
	}
	return Fail(Transient, "internal", "Something failed on the server; it will be retried.", err)
}

// stageWorker adapts a work function to River's Worker interface.
type stageWorker[T river.JobArgs] struct {
	river.WorkerDefaults[T]
	deps  *Deps
	stage string
	do    work
	args  func(T) Run
}

func (w *stageWorker[T]) Work(ctx context.Context, job *river.Job[T]) error {
	return runStage(ctx, w.deps, job, w.args(job.Args), w.stage, w.do)
}

func (w *stageWorker[T]) Timeout(*river.Job[T]) time.Duration {
	return StageTimeouts[w.stage]
}

func (w *stageWorker[T]) NextRetry(job *river.Job[T]) time.Time {
	return NextRetry(job.Attempt, time.Now())
}
