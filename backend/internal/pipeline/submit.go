package pipeline

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// Outcome is a Submission's typed result (ADR 0013).
type Outcome string

// The three outcomes.
const (
	OutcomeCreated   Outcome = "created"
	OutcomeDuplicate Outcome = "duplicate"
	OutcomeHeld      Outcome = "held"
)

// ErrRolesUnconfigured is returned when the roles the pipeline cannot reach
// ready without hold no key (ADR 0010).
type ErrRolesUnconfigured struct{ Roles []string }

func (e *ErrRolesUnconfigured) Error() string { return fmt.Sprintf("roles unconfigured: %v", e.Roles) }

// Submission is the result of Submit.
type Submission struct {
	Outcome         Outcome
	NoteID          uuid.UUID
	UndoUntil       time.Time
	MonthChargedUSD string
	SoftCapUSD      string
}

// UndoResult is the result of Undo.
type UndoResult string

// The two Undo results (docs/design/pipeline.md).
const (
	Undone  UndoResult = "undone"
	Trashed UndoResult = "trashed"
)

// Service creates and undoes Submissions from the API process.
type Service struct {
	pool   *pgxpool.Pool
	client *river.Client[pgx.Tx]
	log    *slog.Logger
}

// NewService builds the submission service around an insert-only River
// client.
func NewService(pool *pgxpool.Pool, client *river.Client[pgx.Tx], log *slog.Logger) *Service {
	return &Service{pool: pool, client: client, log: log}
}

// Submit creates a Note and its first Stage job transactionally (spec 8.1),
// or reports the existing Note for a duplicate Video ID.
func (s *Service) Submit(ctx context.Context, userID uuid.UUID, rawURL string, originSession *uuid.UUID) (*Submission, error) {
	videoID, err := VideoID(rawURL)
	if err != nil {
		return nil, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// Duplicate: one Note per Owner per Video ID, whatever its state.
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `
		SELECT n.id FROM notes n JOIN sources s ON s.id = n.source_id
		 WHERE n.user_id = $1 AND s.video_id = $2`, userID, videoID).Scan(&existing)
	if err == nil {
		return &Submission{Outcome: OutcomeDuplicate, NoteID: existing}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	// Refused before anything is written when the required roles hold no key.
	rows, err := tx.Query(ctx, `SELECT role::text FROM ai_roles_blocking_submission WHERE user_id = $1 ORDER BY role`, userID)
	if err != nil {
		return nil, err
	}
	var blocking []string
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			rows.Close()
			return nil, err
		}
		blocking = append(blocking, role)
	}
	rows.Close()
	if len(blocking) > 0 {
		return nil, &ErrRolesUnconfigured{Roles: blocking}
	}

	// A held outcome: the month already meets the Soft cap, so the run is
	// certain to stop at the gate (ADR 0015 reconciling ADR 0013 with 0007).
	charged, softCap, held, err := capReached(ctx, tx, userID)
	if err != nil {
		return nil, err
	}

	var sourceID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO sources (user_id, video_id, canonical_url) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, video_id) DO UPDATE SET canonical_url = EXCLUDED.canonical_url
		RETURNING id`, userID, videoID, CanonicalURL(videoID)).Scan(&sourceID); err != nil {
		return nil, fmt.Errorf("upsert source: %w", err)
	}
	var noteID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO notes (user_id, source_id) VALUES ($1, $2) RETURNING id`, userID, sourceID).Scan(&noteID); err != nil {
		return nil, fmt.Errorf("insert note: %w", err)
	}
	run := Run{NoteID: noteID, ProcessingVersion: 1}
	if err := createStagesTx(ctx, tx, userID, run); err != nil {
		return nil, err
	}
	undoUntil := time.Now().Add(UndoDelay)
	if err := enqueueStage(ctx, s.client, tx, run, StageMetadata, undoUntil); err != nil {
		return nil, err
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeCreated, originSession); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	out := &Submission{Outcome: OutcomeCreated, NoteID: noteID, UndoUntil: undoUntil}
	if held {
		out.Outcome = OutcomeHeld
		out.MonthChargedUSD = charged
		out.SoftCapUSD = softCap
	}
	return out, nil
}

// capReached compares month-to-date spend with the Soft cap in the account
// timezone (ADR 0007).
func capReached(ctx context.Context, tx pgx.Tx, userID uuid.UUID) (charged, softCap string, reached bool, err error) {
	err = tx.QueryRow(ctx, `
		WITH month AS (
		    SELECT date_trunc('month', now() AT TIME ZONE u.timezone)::date AS billing_month
		      FROM users u WHERE u.id = $1
		)
		SELECT coalesce((SELECT m.charged_usd FROM ai_monthly_usage m, month WHERE m.user_id = $1 AND m.billing_month = month.billing_month), 0)::text,
		       coalesce((SELECT soft_cap_usd FROM ai_cost_settings WHERE user_id = $1), 10)::text`, userID).Scan(&charged, &softCap)
	if err != nil {
		return "", "", false, err
	}
	c, err := strconv.ParseFloat(charged, 64)
	if err != nil {
		return "", "", false, fmt.Errorf("parse charged %q: %w", charged, err)
	}
	s, err := strconv.ParseFloat(softCap, 64)
	if err != nil {
		return "", "", false, fmt.Errorf("parse soft cap %q: %w", softCap, err)
	}
	return charged, softCap, c >= s && s > 0, nil
}

// Undo removes a Submission whose first Stage has not started, or trashes
// the Note and cancels the running Stage when it has (docs/design/pipeline.md).
func (s *Service) Undo(ctx context.Context, userID, noteID uuid.UUID) (UndoResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	note, err := loadNote(ctx, tx, noteID, true)
	if err != nil {
		return "", err
	}
	if note.UserID != userID {
		return "", pgx.ErrNoRows
	}
	var jobID *int64
	if err := tx.QueryRow(ctx, `
		SELECT river_job_id FROM note_stages
		 WHERE note_id = $1 AND processing_version = $2 AND stage = 'metadata'`, noteID, note.ProcessingVersion).Scan(&jobID); err != nil {
		return "", err
	}
	if note.ProcessingVersion == 1 && jobID != nil {
		if _, err := s.client.JobDeleteTx(ctx, tx, *jobID); err == nil {
			// The job never ran: the Note vanishes as if it were never saved.
			if _, err := tx.Exec(ctx, `DELETE FROM notes WHERE id = $1`, noteID); err != nil {
				return "", err
			}
			if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeDeleted, nil); err != nil {
				return "", err
			}
			if err := tx.Commit(ctx); err != nil {
				return "", err
			}
			return Undone, nil
		} else if !errors.Is(err, rivertype.ErrNotFound) && !errors.Is(err, rivertype.ErrJobRunning) {
			return "", err
		}
	}
	// Work has begun: Trash the Note and cancel the running Stage.
	if err := trashTx(ctx, tx, s.client, note); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	return Trashed, nil
}

// Trash moves a Note to Trash, cancelling its run if one is in flight. Used
// by Undo and by DELETE /notes/{id}.
func (s *Service) Trash(ctx context.Context, userID, noteID uuid.UUID) error {
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
	if note.TrashedAt != nil {
		return ErrAlreadyTrashed
	}
	if err := trashTx(ctx, tx, s.client, note); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ErrAlreadyTrashed is returned when a Note is already in Trash.
var ErrAlreadyTrashed = errors.New("note already trashed")

func trashTx(ctx context.Context, tx pgx.Tx, client *river.Client[pgx.Tx], note *NoteRow) error {
	inFlight := note.ProcessingState != "ready" && note.ProcessingState != "failed" && note.ProcessingState != "cancelled"
	if inFlight {
		if _, err := tx.Exec(ctx, `
			UPDATE notes SET trashed_at = now(), purge_after = now() + interval '30 days', cancel_requested_at = now()
			 WHERE id = $1`, note.ID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `
			SELECT river_job_id FROM note_stages
			 WHERE note_id = $1 AND processing_version = $2 AND river_job_id IS NOT NULL AND status IN ('pending', 'running')`, note.ID, note.ProcessingVersion)
		if err != nil {
			return err
		}
		var jobs []int64
		for rows.Next() {
			var id int64
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return err
			}
			jobs = append(jobs, id)
		}
		rows.Close()
		for _, id := range jobs {
			if _, err := client.JobCancelTx(ctx, tx, id); err != nil && !errors.Is(err, rivertype.ErrNotFound) {
				return err
			}
		}
	} else if _, err := tx.Exec(ctx, `
		UPDATE notes SET trashed_at = now(), purge_after = now() + interval '30 days' WHERE id = $1`, note.ID); err != nil {
		return err
	}
	_, err := vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeUpdated, nil)
	return err
}

// appendNoteUpdated records that a Note changed, from inside a Stage's
// completion transaction.
func appendNoteUpdated(ctx context.Context, tx pgx.Tx, note *NoteRow) error {
	_, err := vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeUpdated, nil)
	return err
}
