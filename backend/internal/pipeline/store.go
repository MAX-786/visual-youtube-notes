package pipeline

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// allStages is the pipeline in order. Every run gets a row per Stage
// (ADR 0006): a bypassed transcribing Stage is marked skipped, never absent.
var allStages = []string{StageMetadata, StageCaptions, StageTranscribing, StageGenerating, StageIndexing}

// NoteRow is what the pipeline needs to know about a Note between Stages.
type NoteRow struct {
	ID                uuid.UUID
	UserID            uuid.UUID
	SourceID          uuid.UUID
	VideoID           string
	ProcessingVersion int
	ProcessingState   string
	CancelRequestedAt *time.Time
	TrashedAt         *time.Time
	DurationSeconds   *int
	Title             *string
	ChannelName       *string
	ActiveRevisionID  *uuid.UUID
}

// loadNote reads a Note with its Source, optionally locking it.
func loadNote(ctx context.Context, q querier, noteID uuid.UUID, lock bool) (*NoteRow, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF n"
	}
	var n NoteRow
	err := q.QueryRow(ctx, `
		SELECT n.id, n.user_id, n.source_id, s.video_id, n.processing_version, n.processing_state::text,
		       n.cancel_requested_at, n.trashed_at, s.duration_seconds, s.title, s.channel_name, n.active_revision_id
		  FROM notes n JOIN sources s ON s.id = n.source_id
		 WHERE n.id = $1`+suffix, noteID).
		Scan(&n.ID, &n.UserID, &n.SourceID, &n.VideoID, &n.ProcessingVersion, &n.ProcessingState, &n.CancelRequestedAt, &n.TrashedAt, &n.DurationSeconds, &n.Title, &n.ChannelName, &n.ActiveRevisionID)
	if err != nil {
		return nil, err
	}
	return &n, nil
}

type querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// stageRow is one note_stages row.
type stageRow struct {
	ID     uuid.UUID
	Status string
}

func loadStage(ctx context.Context, q querier, run Run, stage string) (*stageRow, error) {
	var s stageRow
	err := q.QueryRow(ctx, `
		SELECT id, status::text FROM note_stages
		 WHERE note_id = $1 AND processing_version = $2 AND stage = $3`, run.NoteID, run.ProcessingVersion, stage).
		Scan(&s.ID, &s.Status)
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// createStagesTx inserts the five pending Stage rows for a run.
func createStagesTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, run Run) error {
	for _, stage := range allStages {
		if _, err := tx.Exec(ctx, `
			INSERT INTO note_stages (user_id, note_id, processing_version, stage)
			VALUES ($1, $2, $3, $4)`, userID, run.NoteID, run.ProcessingVersion, stage); err != nil {
			return fmt.Errorf("create stage %s: %w", stage, err)
		}
	}
	return nil
}

// setStageJob records the River job that will run a Stage.
func setStageJob(ctx context.Context, tx pgx.Tx, run Run, stage string, jobID int64) error {
	_, err := tx.Exec(ctx, `
		UPDATE note_stages SET river_job_id = $4
		 WHERE note_id = $1 AND processing_version = $2 AND stage = $3`, run.NoteID, run.ProcessingVersion, stage, jobID)
	return err
}

// openStage marks a Stage running and mirrors it on the Note.
func openStage(ctx context.Context, pool *pgxpool.Pool, run Run, stage string, jobID int64) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `
		UPDATE note_stages SET status = 'running', started_at = coalesce(started_at, now()), finished_at = NULL, river_job_id = $4
		 WHERE note_id = $1 AND processing_version = $2 AND stage = $3`, run.NoteID, run.ProcessingVersion, stage, jobID); err != nil {
		return fmt.Errorf("open stage: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET processing_state = $2 WHERE id = $1 AND processing_version = $3`, run.NoteID, stage, run.ProcessingVersion); err != nil {
		return fmt.Errorf("mirror stage on note: %w", err)
	}
	return tx.Commit(ctx)
}

// openAttempt writes the Attempt row before any external call, so a crash
// leaves evidence (ADR 0006).
func openAttempt(ctx context.Context, pool *pgxpool.Pool, stageID uuid.UUID, attemptNo int, idempotencyKey string) (uuid.UUID, error) {
	var id uuid.UUID
	err := pool.QueryRow(ctx, `
		INSERT INTO stage_attempts (stage_id, attempt_no, idempotency_key)
		VALUES ($1, $2, $3)
		ON CONFLICT (stage_id, attempt_no) DO UPDATE
		   SET started_at = now(), finished_at = NULL, provider_request_id = NULL, response_hash = NULL,
		       response_bytes = NULL, error_code = NULL, error_message = NULL, is_permanent = NULL,
		       idempotency_key = EXCLUDED.idempotency_key
		RETURNING id`, stageID, attemptNo, idempotencyKey).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("open attempt: %w", err)
	}
	return id, nil
}

// closeAttemptOK records a response fingerprint.
func closeAttemptOK(ctx context.Context, pool *pgxpool.Pool, attemptID uuid.UUID, providerRequestID string, hash []byte, size int64) error {
	var prid *string
	if providerRequestID != "" {
		prid = &providerRequestID
	}
	_, err := pool.Exec(ctx, `
		UPDATE stage_attempts SET finished_at = now(), provider_request_id = $2, response_hash = $3, response_bytes = $4
		 WHERE id = $1`, attemptID, prid, hash, size)
	return err
}

// closeAttemptFailed records a classified failure.
func closeAttemptFailed(ctx context.Context, pool *pgxpool.Pool, attemptID uuid.UUID, se *StageError) error {
	message := se.Message
	if se.Cause != nil {
		message = fmt.Sprintf("%s (%v)", se.Message, se.Cause)
	}
	if len(message) > 2000 {
		message = message[:2000]
	}
	_, err := pool.Exec(ctx, `
		UPDATE stage_attempts SET finished_at = now(), error_code = $2, error_message = $3, is_permanent = $4
		 WHERE id = $1`, attemptID, se.Code, message, se.Verdict != Transient)
	return err
}

// failStage is the one writer of a failed Stage (ADR 0006): the Stage row,
// the Note's state and failure_stage, the terminal Sync event, and the
// progress frame.
func failStage(ctx context.Context, pool *pgxpool.Pool, run Run, stage string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, run.NoteID, true)
	if err != nil {
		return err
	}
	if note.ProcessingVersion != run.ProcessingVersion {
		return tx.Commit(ctx) // a newer run superseded this one
	}
	if _, err := tx.Exec(ctx, `
		UPDATE note_stages SET status = 'failed', started_at = coalesce(started_at, now()), finished_at = now()
		 WHERE note_id = $1 AND processing_version = $2 AND stage = $3`, run.NoteID, run.ProcessingVersion, stage); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET processing_state = 'failed', failure_stage = $2 WHERE id = $1`, run.NoteID, stage); err != nil {
		return err
	}
	if _, err := vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeUpdated, nil); err != nil {
		return err
	}
	if err := publishProgress(ctx, tx, note.UserID, run, "failed", stage, "failed", 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// cancelStage records a cancelled Stage after a Delete won the race.
func cancelStage(ctx context.Context, pool *pgxpool.Pool, run Run, stage string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := loadNote(ctx, tx, run.NoteID, true)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil // hard-deleted by Undo
		}
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE note_stages SET status = 'cancelled', started_at = coalesce(started_at, now()), finished_at = now()
		 WHERE note_id = $1 AND processing_version = $2 AND stage = $3 AND status IN ('pending', 'running')`,
		run.NoteID, run.ProcessingVersion, stage); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET processing_state = 'cancelled' WHERE id = $1 AND processing_version = $2`, run.NoteID, run.ProcessingVersion); err != nil {
		return err
	}
	if _, err := vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeUpdated, nil); err != nil {
		return err
	}
	if err := publishProgress(ctx, tx, note.UserID, run, "cancelled", stage, "cancelled", 0); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func publishProgress(ctx context.Context, exec vsync.Executor, userID uuid.UUID, run Run, state, stage, status string, attempt int) error {
	var kind, st *string
	if stage != "" {
		kind = &stage
	}
	if status != "" {
		st = &status
	}
	return vsync.Publish(ctx, exec, userID, vsync.Progress{
		NoteID:            run.NoteID,
		ProcessingVersion: run.ProcessingVersion,
		ProcessingState:   state,
		StageKind:         kind,
		StageStatus:       st,
		Attempt:           attempt,
	})
}

// CaptionTrack is the track the metadata extraction fetched, kept on the
// Source so the captions Stage and any later run read it without YouTube.
type CaptionTrack struct {
	Lang      string
	Automatic bool
	VTT       []byte
}

// storeSource writes what the metadata Stage learned, including the chosen
// caption track or its absence.
func storeSource(ctx context.Context, tx pgx.Tx, sourceID uuid.UUID, info *VideoInfo, duration int, published *time.Time, track *CaptionTrack) error {
	var lang *string
	var automatic *bool
	var vtt []byte
	if track != nil {
		lang, automatic, vtt = &track.Lang, &track.Automatic, track.VTT
	}
	_, err := tx.Exec(ctx, `
		UPDATE sources SET title = $2, channel_id = nullif($3, ''), channel_name = nullif($4, ''),
		       duration_seconds = $5, published_at = $6, metadata_fetched_at = now(),
		       caption_track_lang = $7, caption_track_automatic = $8, caption_track_vtt = $9
		 WHERE id = $1`, sourceID, info.Title, info.ChannelID, info.ChannelName(), duration, published, lang, automatic, vtt)
	return err
}

// loadCaptionTrack reads the stored track, or nil when the video has none.
func loadCaptionTrack(ctx context.Context, q querier, sourceID uuid.UUID) (*CaptionTrack, error) {
	var lang *string
	var automatic *bool
	var vtt []byte
	if err := q.QueryRow(ctx, `
		SELECT caption_track_lang, caption_track_automatic, caption_track_vtt FROM sources WHERE id = $1`, sourceID).
		Scan(&lang, &automatic, &vtt); err != nil {
		return nil, err
	}
	if lang == nil || automatic == nil {
		return nil, nil
	}
	return &CaptionTrack{Lang: *lang, Automatic: *automatic, VTT: vtt}, nil
}
