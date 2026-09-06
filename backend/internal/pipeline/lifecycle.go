package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/cost"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// The refusals of the lifecycle operations (the contract's error codes).
var (
	ErrNoteNotFailed        = errors.New("the note has not failed")
	ErrNoteNotReady         = errors.New("the note is not ready")
	ErrNoteTrashed          = errors.New("the note is in the trash")
	ErrNoteNotTrashed       = errors.New("the note is not in the trash")
	ErrRunInFlight          = errors.New("a run is in flight")
	ErrRegenerationInFlight = errors.New("a regeneration is in flight")
)

// RunStart is what Retry and Regenerate answer: started, or held for cost
// confirmation with the frozen estimate.
type RunStart struct {
	Held        bool
	Version     int
	EstimateUSD string
}

// RegenerateArgs is the Regeneration job (ADR 0006): its own kind, so the
// one-per-Note lock cannot block a first Submission, carrying the base and
// the instruction the generating Stage needs. A held Regeneration is the
// same job scheduled far ahead; confirmation makes it available.
type RegenerateArgs struct {
	NoteID            uuid.UUID  `json:"note_id" river:"unique"`
	ProcessingVersion int        `json:"processing_version"`
	Instruction       string     `json:"instruction"`
	Base              *uuid.UUID `json:"base"`
}

// Kind implements river.JobArgs.
func (RegenerateArgs) Kind() string { return "regenerate" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (RegenerateArgs) InsertOpts() river.InsertOpts {
	opts := stageOpts(QueueDefault)
	opts.UniqueOpts = river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning, rivertype.JobStateScheduled}}
	return opts
}

// heldUntil is where a held Regeneration waits: never, unless confirmed.
var heldUntil = time.Date(2100, 1, 1, 0, 0, 0, 0, time.UTC)

// Retry starts a new run at the failed Stage, carrying the earlier Stages'
// results forward; a retry from a paid Stage is gated synchronously.
func (s *Service) Retry(ctx context.Context, userID, noteID uuid.UUID) (*RunStart, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, noteID, true)
	if err != nil {
		return nil, err
	}
	switch {
	case note.UserID != userID:
		return nil, pgx.ErrNoRows
	case note.TrashedAt != nil:
		return nil, ErrNoteTrashed
	case note.ProcessingState == "ready":
		return nil, ErrNoteNotFailed
	case note.ProcessingState != "failed" && note.ProcessingState != "cancelled":
		return nil, ErrRunInFlight
	}
	if err := rolesConfigured(ctx, tx, userID); err != nil {
		return nil, err
	}
	var failed string
	if err := tx.QueryRow(ctx, `
		SELECT stage::text FROM note_stages
		 WHERE note_id = $1 AND processing_version = $2 AND status IN ('failed', 'cancelled', 'pending', 'running')
		 ORDER BY array_position($3::text[], stage::text) LIMIT 1`, noteID, note.ProcessingVersion, allStages).Scan(&failed); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			failed = StageMetadata
		} else {
			return nil, err
		}
	}
	version := note.ProcessingVersion + 1
	if err := carryRun(ctx, tx, note, version, failed); err != nil {
		return nil, err
	}
	note.ProcessingVersion = version
	start := &RunStart{Version: version}
	if failed != StageMetadata && failed != StageCaptions {
		held, estimate, err := gateNow(ctx, tx, note)
		if err != nil {
			return nil, err
		}
		start.Held, start.EstimateUSD = held, estimate
	}
	if !start.Held {
		if err := enqueueStage(ctx, s.client, tx, Run{NoteID: noteID, ProcessingVersion: version}, failed, time.Time{}); err != nil {
			return nil, err
		}
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.log.Info("retry started", "note", noteID, "version", version, "stage", failed, "held", start.Held)
	return start, nil
}

// Regenerate records the Active Revision as the base and starts a new run
// at generating with the instruction; one Regeneration in flight per Note.
func (s *Service) Regenerate(ctx context.Context, userID, noteID uuid.UUID, instruction string) (*RunStart, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, noteID, true)
	if err != nil {
		return nil, err
	}
	switch {
	case note.UserID != userID:
		return nil, pgx.ErrNoRows
	case note.TrashedAt != nil:
		return nil, ErrNoteTrashed
	case note.ProcessingState == StageGenerating || note.ProcessingState == StageIndexing || note.ProcessingState == "queued":
		return nil, ErrRegenerationInFlight
	case note.ProcessingState != "ready" || note.ActiveRevisionID == nil:
		return nil, ErrNoteNotReady
	}
	if err := rolesConfigured(ctx, tx, userID); err != nil {
		return nil, err
	}
	var held bool
	if err := tx.QueryRow(ctx, `SELECT cost_hold_since IS NOT NULL FROM notes WHERE id = $1`, noteID).Scan(&held); err != nil {
		return nil, err
	}
	if held {
		return nil, ErrRegenerationInFlight
	}
	version := note.ProcessingVersion + 1
	if err := carryRun(ctx, tx, note, version, StageGenerating); err != nil {
		return nil, err
	}
	note.ProcessingVersion = version
	start := &RunStart{Version: version}
	start.Held, start.EstimateUSD, err = gateNow(ctx, tx, note)
	if err != nil {
		return nil, err
	}
	args := RegenerateArgs{NoteID: noteID, ProcessingVersion: version, Instruction: instruction, Base: note.ActiveRevisionID}
	var opts *river.InsertOpts
	if start.Held {
		opts = &river.InsertOpts{ScheduledAt: heldUntil}
	}
	inserted, err := s.client.InsertTx(ctx, tx, args, opts)
	if err != nil {
		return nil, err
	}
	if inserted.UniqueSkippedAsDuplicate {
		return nil, ErrRegenerationInFlight
	}
	if err := setStageJob(ctx, tx, Run{NoteID: noteID, ProcessingVersion: version}, StageGenerating, inserted.Job.ID); err != nil {
		return nil, err
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, nil); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	s.log.Info("regeneration started", "note", noteID, "version", version, "held", start.Held)
	return start, nil
}

// carryRun opens a new run: the Stages before `from` are carried from the
// previous run as they ended, the rest start pending, and the Note is
// queued again with its failure cleared.
func carryRun(ctx context.Context, tx pgx.Tx, note *NoteRow, version int, from string) error {
	carry := true
	for _, stage := range allStages {
		if stage == from {
			carry = false
		}
		if carry {
			if _, err := tx.Exec(ctx, `
				INSERT INTO note_stages (user_id, note_id, processing_version, stage, status, started_at, finished_at)
				SELECT user_id, note_id, $3, stage,
				       CASE WHEN status IN ('ready', 'skipped') THEN status ELSE 'ready' END, started_at, coalesce(finished_at, now())
				  FROM note_stages WHERE note_id = $1 AND processing_version = $2 AND stage = $4`,
				note.ID, note.ProcessingVersion, version, stage); err != nil {
				return fmt.Errorf("carry stage %s: %w", stage, err)
			}
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO note_stages (user_id, note_id, processing_version, stage)
			VALUES ($1, $2, $3, $4)`, note.UserID, note.ID, version, stage); err != nil {
			return fmt.Errorf("create stage %s: %w", stage, err)
		}
	}
	if _, err := tx.Exec(ctx, `
		UPDATE notes SET processing_version = $2, processing_state = 'queued', failure_stage = NULL, cancel_requested_at = NULL
		 WHERE id = $1`, note.ID, version); err != nil {
		return err
	}
	return nil
}

// gateNow is the synchronous Soft cap gate of ADR 0007 for a run whose
// cost is computable at submission: held with the frozen estimate, or not.
func gateNow(ctx context.Context, tx pgx.Tx, note *NoteRow) (bool, string, error) {
	projection, err := projectRun(ctx, tx, note)
	if err != nil {
		return false, "", err
	}
	month, err := cost.MonthToDate(ctx, tx, note.UserID)
	if err != nil {
		return false, "", err
	}
	if month.Allows(projection.TotalUSD) {
		return false, projection.TotalUSD, nil
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET cost_hold_since = now(), cost_hold_estimate_usd = $2::numeric WHERE id = $1`, note.ID, projection.TotalUSD); err != nil {
		return false, "", err
	}
	return true, projection.TotalUSD, nil
}

// rolesConfigured refuses a run when a required role holds no key.
func rolesConfigured(ctx context.Context, tx pgx.Tx, userID uuid.UUID) error {
	rows, err := tx.Query(ctx, `SELECT role::text FROM ai_roles_blocking_submission WHERE user_id = $1 ORDER BY role`, userID)
	if err != nil {
		return err
	}
	defer rows.Close()
	var blocking []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return err
		}
		blocking = append(blocking, role)
	}
	if len(blocking) > 0 {
		return &ErrRolesUnconfigured{Roles: blocking}
	}
	return rows.Err()
}

// releaseHeldRegeneration makes a held Regeneration's job available, or
// reports that there was none.
func (s *Service) releaseHeldRegeneration(ctx context.Context, tx pgx.Tx, noteID uuid.UUID, version int) (bool, error) {
	var jobID int64
	err := tx.QueryRow(ctx, `
		SELECT id FROM river_job
		 WHERE kind = 'regenerate' AND state = 'scheduled'
		   AND args->>'note_id' = $1 AND (args->>'processing_version')::int = $2`, noteID.String(), version).Scan(&jobID)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if _, err := s.client.JobRetryTx(ctx, tx, jobID); err != nil {
		return false, err
	}
	return true, nil
}

// Restore brings a Note back from Trash with the state it had; one deleted
// mid-processing comes back failed at the cancelled Stage (ADR 0006).
func (s *Service) Restore(ctx context.Context, userID, noteID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, noteID, true)
	if err != nil {
		return err
	}
	if note.UserID != userID {
		return pgx.ErrNoRows
	}
	if note.TrashedAt == nil {
		return ErrNoteNotTrashed
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET trashed_at = NULL, purge_after = NULL, cancel_requested_at = NULL WHERE id = $1`, noteID); err != nil {
		return err
	}
	if note.ProcessingState == "cancelled" {
		if _, err := tx.Exec(ctx, `
			UPDATE notes n SET processing_state = 'failed',
			       failure_stage = (SELECT s.stage FROM note_stages s WHERE s.note_id = n.id AND s.processing_version = n.processing_version AND s.status = 'cancelled'
			                        ORDER BY array_position($2::text[], s.stage::text) LIMIT 1)
			 WHERE n.id = $1`, noteID, allStages); err != nil {
			return err
		}
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, nil); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Purge deletes a trashed Note for good: every dependent row goes with it
// by cascade, the ledger keeps its spend with the Note reference cleared,
// and devices learn of the deletion through the sync event.
func (s *Service) Purge(ctx context.Context, userID, noteID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, noteID, true)
	if err != nil {
		return err
	}
	if note.UserID != userID {
		return pgx.ErrNoRows
	}
	if note.TrashedAt == nil {
		return ErrNoteNotTrashed
	}
	if err := purgeTx(ctx, tx, note); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func purgeTx(ctx context.Context, tx pgx.Tx, note *NoteRow) error {
	if _, err := vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeDeleted, nil); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `DELETE FROM notes WHERE id = $1`, note.ID); err != nil {
		return fmt.Errorf("purge note: %w", err)
	}
	// The Source outlives its Note only while another Note needs it.
	_, err := tx.Exec(ctx, `DELETE FROM sources s WHERE s.id = $1 AND NOT EXISTS (SELECT 1 FROM notes n WHERE n.source_id = s.id)`, note.SourceID)
	return err
}

// RetryBlocked retries every Note blocked by extractor breakage: the last
// step of the yt-dlp procedure. Each starts a new run at its failed Stage.
func (s *Service) RetryBlocked(ctx context.Context, userID uuid.UUID) ([]uuid.UUID, error) {
	rows, err := s.pool.Query(ctx, `SELECT note_id FROM notes_blocked_by_tooling WHERE user_id = $1 ORDER BY failed_at`, userID)
	if err != nil {
		return nil, err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	retried := make([]uuid.UUID, 0, len(ids))
	for _, id := range ids {
		if _, err := s.Retry(ctx, userID, id); err != nil {
			var unconfigured *ErrRolesUnconfigured
			if errors.As(err, &unconfigured) {
				return retried, err
			}
			s.log.Warn("blocked note not retried", "note", id, "err", err)
			continue
		}
		retried = append(retried, id)
	}
	return retried, nil
}

// TrashPurgeArgs is the hourly purge of Notes past their Trash window.
type TrashPurgeArgs struct{}

// Kind implements river.JobArgs.
func (TrashPurgeArgs) Kind() string { return "trash_purge" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (TrashPurgeArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 30 * time.Minute}}
}

type trashPurgeWorker struct {
	river.WorkerDefaults[TrashPurgeArgs]
	deps *Deps
}

// Work purges every Note whose purge_after has passed, one transaction each.
func (w *trashPurgeWorker) Work(ctx context.Context, _ *river.Job[TrashPurgeArgs]) error {
	return PurgeExpired(ctx, w.deps)
}

// PurgeExpired is the purge itself, callable from tests.
func PurgeExpired(ctx context.Context, d *Deps) error {
	rows, err := d.Pool.Query(ctx, `SELECT id FROM notes WHERE trashed_at IS NOT NULL AND purge_after <= now() ORDER BY purge_after`)
	if err != nil {
		return err
	}
	var ids []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		if err := purgeOne(ctx, d, id); err != nil {
			return err
		}
	}
	return nil
}

func purgeOne(ctx context.Context, d *Deps, id uuid.UUID) error {
	tx, err := d.Pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, id, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return err
	}
	if note.TrashedAt == nil {
		return nil
	}
	if err := purgeTx(ctx, tx, note); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return err
	}
	d.Logger.Info("purged a note past its trash window", "note", id)
	return nil
}

// RetentionPruneArgs is the daily prune of sync events and the mutation
// ledger past thirty days (docs/design/pipeline.md).
type RetentionPruneArgs struct{}

// Kind implements river.JobArgs.
func (RetentionPruneArgs) Kind() string { return "retention_prune" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (RetentionPruneArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 6 * time.Hour}}
}

type retentionPruneWorker struct {
	river.WorkerDefaults[RetentionPruneArgs]
	deps *Deps
}

// Work removes what the thirty-day windows no longer need.
func (w *retentionPruneWorker) Work(ctx context.Context, _ *river.Job[RetentionPruneArgs]) error {
	return PruneRetention(ctx, w.deps, 30*24*time.Hour)
}

// PruneRetention is the prune itself, callable from tests.
func PruneRetention(ctx context.Context, d *Deps, window time.Duration) error {
	cutoff := time.Now().Add(-window)
	if _, err := d.Pool.Exec(ctx, `DELETE FROM sync_events WHERE occurred_at < $1`, cutoff); err != nil {
		return err
	}
	_, err := d.Pool.Exec(ctx, `DELETE FROM note_mutations WHERE applied_at < $1`, cutoff)
	return err
}
