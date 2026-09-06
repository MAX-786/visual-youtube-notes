package pipeline

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/cost"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// CostGate is the Soft cap gate (ADR 0007), run as Hooks.BeforePaidStage
// inside the captions completion transaction with the Note locked: the
// first moment the run's cost is computable and before any paid call. Under
// the cap the next Stage is enqueued as usual; over it the Note is held with
// the frozen estimate, nothing is enqueued, and the Note's sync event tells
// every device (Web Push rides the same event).
func CostGate(ctx context.Context, tx pgx.Tx, note *NoteRow) (bool, error) {
	projection, err := projectRun(ctx, tx, note)
	if err != nil {
		return false, err
	}
	month, err := cost.MonthToDate(ctx, tx, note.UserID)
	if err != nil {
		return false, err
	}
	if month.Allows(projection.TotalUSD) {
		return true, nil
	}
	if _, err := tx.Exec(ctx, `
		UPDATE notes SET cost_hold_since = now(), cost_hold_estimate_usd = $2::numeric WHERE id = $1`, note.ID, projection.TotalUSD); err != nil {
		return false, err
	}
	if _, err := vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeUpdated, nil); err != nil {
		return false, err
	}
	return false, nil
}

// projectRun estimates every remaining paid Stage of the run: transcription
// when the transcribing Stage is still pending, then generation and
// embeddings from the Transcript when there is one and from the Source
// length otherwise.
func projectRun(ctx context.Context, q pgx.Tx, note *NoteRow) (*cost.Projection, error) {
	var transcribingStatus string
	if err := q.QueryRow(ctx, `
		SELECT status::text FROM note_stages WHERE note_id = $1 AND processing_version = $2 AND stage = $3`,
		note.ID, note.ProcessingVersion, StageTranscribing).Scan(&transcribingStatus); err != nil {
		return nil, err
	}
	in := cost.Inputs{NeedsSTT: transcribingStatus == "pending"}
	if note.DurationSeconds != nil {
		in.DurationSeconds = float64(*note.DurationSeconds)
	}
	if !in.NeedsSTT {
		err := q.QueryRow(ctx, `
			SELECT t.normalized_text, (SELECT count(*) FROM transcript_chunks c WHERE c.transcript_id = t.id)
			  FROM transcripts t WHERE t.note_id = $1`, note.ID).Scan(&in.TranscriptText, &in.TranscriptChunks)
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return nil, err
		}
		if errors.Is(err, pgx.ErrNoRows) {
			// No Transcript yet and none coming: size it from the video.
			in.NeedsSTT = true
		}
	}
	return cost.Project(ctx, q, note.UserID, in)
}

// ErrNoHold is a confirmation for a run that is not held, or for another
// run than the one held.
var ErrNoHold = errors.New("no cost hold on that run")

// ConfirmCost is the Owner's answer to a hold: the estimate is recomputed
// against the prices in force now, the hold cleared, and the withheld Stage
// enqueued on the same processing_version. One confirmation per run.
func (s *Service) ConfirmCost(ctx context.Context, userID, noteID uuid.UUID, version int) (string, error) {
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
	var held bool
	if err := tx.QueryRow(ctx, `SELECT cost_hold_since IS NOT NULL FROM notes WHERE id = $1`, noteID).Scan(&held); err != nil {
		return "", err
	}
	if !held || note.ProcessingVersion != version || note.CancelRequestedAt != nil {
		return "", ErrNoHold
	}
	projection, err := projectRun(ctx, tx, note)
	if err != nil {
		return "", err
	}
	var next string
	if err := tx.QueryRow(ctx, `
		SELECT stage::text FROM note_stages
		 WHERE note_id = $1 AND processing_version = $2 AND status = 'pending'
		 ORDER BY array_position($3::text[], stage::text) LIMIT 1`, noteID, version, allStages).Scan(&next); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNoHold
		}
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET cost_hold_since = NULL, cost_hold_estimate_usd = NULL WHERE id = $1`, noteID); err != nil {
		return "", err
	}
	if err := enqueueStage(ctx, s.client, tx, Run{NoteID: noteID, ProcessingVersion: version}, next, time.Time{}); err != nil {
		return "", err
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, nil); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", err
	}
	s.log.Info("cost hold confirmed", "note", noteID, "version", version, "stage", next, "estimate_usd", projection.TotalUSD)
	return projection.TotalUSD, nil
}
