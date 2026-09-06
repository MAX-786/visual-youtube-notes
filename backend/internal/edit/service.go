// Package edit is the write side of a ready Note (ADR 0004): operation sets
// applied to an exact base Revision, manual and Conflict Revisions, the
// idempotency ledger, activation, restore and discard, the Takeaway's
// compare-and-set, Primary Topic and Tags, layout merges, and the Topic and
// Tag catalogue. Every write allocates a Sync cursor and appends to the
// outbox inside the same transaction.
package edit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/library"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// ErrNotFound is a Note, Revision, Topic or Tag the Owner does not have.
var ErrNotFound = errors.New("not found")

// Refusal is a typed reason the client can act on; Code is the contract's
// error code and Details its fixed detail keys.
type Refusal struct {
	Code    string
	Details map[string]any
}

func (r *Refusal) Error() string { return "refused: " + r.Code }

// ValidationError is an applied result that broke a structural invariant.
type ValidationError struct{ Rule *generation.RuleError }

func (e *ValidationError) Error() string { return e.Rule.Error() }

// mutationFloor is the idempotency ledger's retention and therefore the
// oldest mutation that may still be applied (ADR 0004).
const mutationFloor = 30 * 24 * time.Hour

// Service writes Notes.
type Service struct{ pool *pgxpool.Pool }

// New builds the service.
func New(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Mutation is the envelope of mutation-protocol.md.
type Mutation struct {
	ID             uuid.UUID
	BaseRevisionID uuid.UUID
	Ops            []generation.Op
}

// MutationResult is what every mutation answers.
type MutationResult struct {
	Outcome          string
	RevisionID       uuid.UUID
	ActiveRevisionID uuid.UUID
	SyncCursor       int64
	Replayed         bool
}

type noteRow struct {
	ID, UserID uuid.UUID
	State      string
	Trashed    bool
	Active     *uuid.UUID
	Title      string
	Channel    string
	Transcript *uuid.UUID
}

// lockNote reads and locks the Note. A Note that is gone but whose deletion
// the outbox remembers is reported as purged, so the client drops its queue.
func (s *Service) lockNote(ctx context.Context, tx pgx.Tx, userID, noteID uuid.UUID) (*noteRow, error) {
	var n noteRow
	var trashed *time.Time
	err := tx.QueryRow(ctx, `
		SELECT n.id, n.user_id, n.processing_state::text, n.trashed_at, n.active_revision_id,
		       coalesce(s.title, ''), coalesce(s.channel_name, ''),
		       (SELECT t.id FROM transcripts t WHERE t.note_id = n.id)
		  FROM notes n JOIN sources s ON s.id = n.source_id
		 WHERE n.id = $1 AND n.user_id = $2 FOR UPDATE OF n`, noteID, userID).
		Scan(&n.ID, &n.UserID, &n.State, &trashed, &n.Active, &n.Title, &n.Channel, &n.Transcript)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			var purged bool
			_ = tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM sync_events WHERE user_id = $1 AND entity_kind = 'note' AND entity_id = $2 AND change = 'deleted')`, userID, noteID).Scan(&purged)
			if purged {
				return nil, &Refusal{Code: "note_purged"}
			}
			return nil, ErrNotFound
		}
		return nil, err
	}
	n.Trashed = trashed != nil
	return &n, nil
}

// expired applies the thirty-day floor to a time-ordered (version 7)
// mutation id; ids without a timestamp cannot be judged and pass.
func expired(id uuid.UUID) bool {
	if id.Version() != 7 {
		return false
	}
	sec, nsec := id.Time().UnixTime()
	return time.Since(time.Unix(sec, nsec)) > mutationFloor
}

// Mutate is the server transaction of mutation-protocol.md.
func (s *Service) Mutate(ctx context.Context, userID, sessionID, noteID uuid.UUID, m Mutation) (*MutationResult, error) {
	if len(m.Ops) == 0 {
		return nil, &Refusal{Code: "invalid_request"}
	}
	if expired(m.ID) {
		return nil, &Refusal{Code: "mutation_expired"}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	// 1. Idempotency: a replay is answered from the ledger verbatim.
	var outcome string
	var resultRev *uuid.UUID
	var cursor *int64
	var reason *string
	err = tx.QueryRow(ctx, `SELECT outcome::text, result_revision_id, sync_cursor, rejection_reason FROM note_mutations WHERE mutation_id = $1 AND user_id = $2`, m.ID, userID).
		Scan(&outcome, &resultRev, &cursor, &reason)
	switch {
	case err == nil:
		if outcome == "rejected" {
			return nil, &Refusal{Code: *reason}
		}
		var active uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT active_revision_id FROM notes WHERE id = $1`, noteID).Scan(&active); err != nil {
			return nil, err
		}
		return &MutationResult{Outcome: outcome, RevisionID: *resultRev, ActiveRevisionID: active, SyncCursor: *cursor, Replayed: true}, nil
	case !errors.Is(err, pgx.ErrNoRows):
		return nil, err
	}

	// 2. Refusals, each recorded so a replay answers the same way.
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	recordRejection := func(code string) error {
		if _, err := tx.Exec(ctx, `
			INSERT INTO note_mutations (mutation_id, user_id, note_id, device_session_id, base_revision_id, outcome, rejection_reason)
			VALUES ($1, $2, $3, $4, $5, 'rejected', $6)`, m.ID, userID, noteID, sessionID, m.BaseRevisionID, code); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	refuse := func(code string, details map[string]any) (*MutationResult, error) {
		if err := recordRejection(code); err != nil {
			return nil, err
		}
		return nil, &Refusal{Code: code, Details: details}
	}
	if note.Trashed {
		return refuse("note_trashed", nil)
	}
	if note.State != "ready" || note.Active == nil {
		return refuse("note_not_ready", map[string]any{"processing_state": note.State})
	}

	// 3. The base Revision.
	var payload []byte
	var baseAwaiting *time.Time
	var baseOrigin string
	if err := tx.QueryRow(ctx, `SELECT payload, awaiting_decision_since, origin::text FROM note_revisions WHERE id = $1 AND note_id = $2`, m.BaseRevisionID, noteID).
		Scan(&payload, &baseAwaiting, &baseOrigin); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return refuse("invalid_request", map[string]any{"field": "base_revision_id"})
		}
		return nil, err
	}
	if baseAwaiting != nil && baseOrigin != "conflict" {
		// A generated Revision awaiting a decision is accepted or discarded,
		// not edited; a Conflict Revision may be extended by the offline chain
		// that produced it, so the branch stays one branch.
		return refuse("revision_not_active", map[string]any{"active_revision_id": note.Active.String()})
	}

	// 4-5. Apply and validate.
	var stored generation.StoredPayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, fmt.Errorf("decode base payload: %w", err)
	}
	if err := generation.Apply(&stored, m.Ops); err != nil {
		var re *generation.RuleError
		if errors.As(err, &re) {
			if err := recordRejection("validation_failed"); err != nil {
				return nil, err
			}
			return nil, &ValidationError{Rule: re}
		}
		return nil, err
	}
	if re := generation.ValidateEdited(&stored); re != nil {
		if err := recordRejection("validation_failed"); err != nil {
			return nil, err
		}
		return nil, &ValidationError{Rule: re}
	}
	raw, err := json.Marshal(&stored)
	if err != nil {
		return nil, err
	}

	// 6. Classify and write.
	applied := *note.Active == m.BaseRevisionID
	origin := "manual"
	if !applied {
		origin = "conflict"
	}
	revisionID, err := writeRevision(ctx, tx, note, origin, &m.BaseRevisionID, raw, !applied)
	if err != nil {
		return nil, err
	}
	var syncCursor int64
	active := *note.Active
	if applied {
		syncCursor, err = activate(ctx, tx, note, revisionID, &stored, &sessionID)
		active = revisionID
	} else {
		// Only the tip of a branch awaits a decision; the interior is history.
		if _, err = tx.Exec(ctx, `UPDATE note_revisions SET awaiting_decision_since = NULL WHERE id = $1 AND origin = 'conflict'`, m.BaseRevisionID); err == nil {
			syncCursor, err = vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, &sessionID)
		}
	}
	if err != nil {
		return nil, err
	}

	// 7. The ledger row, then commit.
	if _, err := tx.Exec(ctx, `
		INSERT INTO note_mutations (mutation_id, user_id, note_id, device_session_id, base_revision_id, result_revision_id, outcome, sync_cursor)
		VALUES ($1, $2, $3, $4, $5, $6, $7::mutation_outcome, $8)`,
		m.ID, userID, noteID, sessionID, m.BaseRevisionID, revisionID, outcomeName(applied), syncCursor); err != nil {
		return nil, fmt.Errorf("ledger: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &MutationResult{Outcome: outcomeName(applied), RevisionID: revisionID, ActiveRevisionID: active, SyncCursor: syncCursor}, nil
}

func outcomeName(applied bool) string {
	if applied {
		return "applied"
	}
	return "conflicted"
}

// writeRevision appends one immutable Revision.
func writeRevision(ctx context.Context, tx pgx.Tx, note *noteRow, origin string, parent *uuid.UUID, payload []byte, awaiting bool) (uuid.UUID, error) {
	var number int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision_number), 0) + 1 FROM note_revisions WHERE note_id = $1`, note.ID).Scan(&number); err != nil {
		return uuid.Nil, err
	}
	var since *time.Time
	if awaiting {
		now := time.Now()
		since = &now
	}
	var id uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO note_revisions (user_id, note_id, revision_number, origin, parent_revision_id, payload, awaiting_decision_since)
		VALUES ($1, $2, $3, $4::revision_origin, $5, $6, $7) RETURNING id`,
		note.UserID, note.ID, number, origin, parent, payload, since).Scan(&id); err != nil {
		return uuid.Nil, fmt.Errorf("write revision: %w", err)
	}
	return id, nil
}

// activate makes a Revision the Note's Active Revision: the pointer, the
// layout seeded for any element without a box (ADR 0011), the search
// children rebuilt (ADR 0005), and the Sync event.
func activate(ctx context.Context, tx pgx.Tx, note *noteRow, revisionID uuid.UUID, stored *generation.StoredPayload, session *uuid.UUID) (int64, error) {
	if _, err := tx.Exec(ctx, `UPDATE notes SET active_revision_id = $2 WHERE id = $1`, note.ID, revisionID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec(ctx, `UPDATE note_revisions SET awaiting_decision_since = NULL WHERE id = $1`, revisionID); err != nil {
		return 0, err
	}
	if err := library.SeedLayout(ctx, tx, note.UserID, note.ID, &stored.Diagram); err != nil {
		return 0, err
	}
	if err := library.RebuildChildren(ctx, tx, library.NoteHead{ID: note.ID, UserID: note.UserID, Title: note.Title, Channel: note.Channel, Payload: stored, Transcript: note.Transcript}); err != nil {
		return 0, err
	}
	return vsync.Append(ctx, tx, note.UserID, vsync.EntityNote, note.ID, nil, vsync.ChangeUpdated, session)
}

// Revision is one entry of version history; Payload is set by GetRevision.
type Revision struct {
	ID                    uuid.UUID
	Number                int
	Origin                string
	ParentID              *uuid.UUID
	GenerationInstruction *string
	AwaitingDecisionSince *time.Time
	IsActive              bool
	OriginDeviceSessionID *uuid.UUID
	CreatedAt             time.Time
	Payload               json.RawMessage
}

const revisionColumns = `r.id, r.revision_number, r.origin::text, r.parent_revision_id, r.generation_instruction,
	       r.awaiting_decision_since, r.created_at, r.id = n.active_revision_id,
	       (SELECT m.device_session_id FROM note_mutations m WHERE m.result_revision_id = r.id LIMIT 1)`

// ListRevisions is the history, newest first, without payloads.
func (s *Service) ListRevisions(ctx context.Context, userID, noteID uuid.UUID) ([]Revision, error) {
	if err := s.noteExists(ctx, userID, noteID); err != nil {
		return nil, err
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+revisionColumns+`
		  FROM note_revisions r JOIN notes n ON n.id = r.note_id
		 WHERE r.note_id = $1 ORDER BY r.revision_number DESC`, noteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Revision{}
	for rows.Next() {
		var r Revision
		if err := rows.Scan(&r.ID, &r.Number, &r.Origin, &r.ParentID, &r.GenerationInstruction, &r.AwaitingDecisionSince, &r.CreatedAt, &r.IsActive, &r.OriginDeviceSessionID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// GetRevision is one Revision with its payload.
func (s *Service) GetRevision(ctx context.Context, userID, noteID, revisionID uuid.UUID) (*Revision, error) {
	var r Revision
	err := s.pool.QueryRow(ctx, `
		SELECT `+revisionColumns+`, r.payload
		  FROM note_revisions r JOIN notes n ON n.id = r.note_id
		 WHERE r.note_id = $1 AND r.id = $2 AND n.user_id = $3`, noteID, revisionID, userID).
		Scan(&r.ID, &r.Number, &r.Origin, &r.ParentID, &r.GenerationInstruction, &r.AwaitingDecisionSince, &r.CreatedAt, &r.IsActive, &r.OriginDeviceSessionID, &r.Payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &r, nil
}

func (s *Service) noteExists(ctx context.Context, userID, noteID uuid.UUID) error {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM notes WHERE id = $1 AND user_id = $2)`, noteID, userID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrNotFound
	}
	return nil
}

// PointerResult answers activate, restore and discard.
type PointerResult struct {
	NoteID, RevisionID, ActiveRevisionID uuid.UUID
	SyncCursor                           int64
}

func (s *Service) revisionForDecision(ctx context.Context, tx pgx.Tx, noteID, revisionID uuid.UUID) (payload []byte, awaiting bool, err error) {
	var since *time.Time
	if err := tx.QueryRow(ctx, `SELECT payload, awaiting_decision_since FROM note_revisions WHERE id = $1 AND note_id = $2`, revisionID, noteID).Scan(&payload, &since); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, ErrNotFound
		}
		return nil, false, err
	}
	return payload, since != nil, nil
}

// Activate moves the pointer, with no copy, to a Revision awaiting a
// decision that was never active (ADR 0004).
func (s *Service) Activate(ctx context.Context, userID, sessionID, noteID, revisionID uuid.UUID) (*PointerResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if note.Trashed {
		return nil, &Refusal{Code: "note_trashed"}
	}
	payload, awaiting, err := s.revisionForDecision(ctx, tx, noteID, revisionID)
	if err != nil {
		return nil, err
	}
	if !awaiting {
		return nil, &Refusal{Code: "revision_not_awaiting_decision"}
	}
	var stored generation.StoredPayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, err
	}
	cursor, err := activate(ctx, tx, note, revisionID, &stored, &sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &PointerResult{NoteID: noteID, RevisionID: revisionID, ActiveRevisionID: revisionID, SyncCursor: cursor}, nil
}

// Restore copies a past Revision into a new restore Revision whose parent
// is the current Active Revision, and activates it. A Conflict Revision's
// edits are taken this way too.
func (s *Service) Restore(ctx context.Context, userID, sessionID, noteID, revisionID uuid.UUID) (*PointerResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if note.Trashed {
		return nil, &Refusal{Code: "note_trashed"}
	}
	if note.State != "ready" || note.Active == nil {
		return nil, &Refusal{Code: "note_not_ready", Details: map[string]any{"processing_state": note.State}}
	}
	payload, awaiting, err := s.revisionForDecision(ctx, tx, noteID, revisionID)
	if err != nil {
		return nil, err
	}
	var stored generation.StoredPayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		return nil, err
	}
	newID, err := writeRevision(ctx, tx, note, "restore", note.Active, payload, false)
	if err != nil {
		return nil, err
	}
	if awaiting {
		if _, err := tx.Exec(ctx, `UPDATE note_revisions SET awaiting_decision_since = NULL WHERE id = $1`, revisionID); err != nil {
			return nil, err
		}
	}
	cursor, err := activate(ctx, tx, note, newID, &stored, &sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &PointerResult{NoteID: noteID, RevisionID: newID, ActiveRevisionID: newID, SyncCursor: cursor}, nil
}

// Discard clears the decision flag; the Revision stays in history.
func (s *Service) Discard(ctx context.Context, userID, sessionID, noteID, revisionID uuid.UUID) (*PointerResult, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	_, awaiting, err := s.revisionForDecision(ctx, tx, noteID, revisionID)
	if err != nil {
		return nil, err
	}
	if !awaiting {
		return nil, &Refusal{Code: "revision_not_awaiting_decision"}
	}
	if _, err := tx.Exec(ctx, `UPDATE note_revisions SET awaiting_decision_since = NULL WHERE id = $1`, revisionID); err != nil {
		return nil, err
	}
	cursor, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, &sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	active := revisionID
	if note.Active != nil {
		active = *note.Active
	}
	return &PointerResult{NoteID: noteID, RevisionID: revisionID, ActiveRevisionID: active, SyncCursor: cursor}, nil
}

// TakeawayResult is the Takeaway after a write.
type TakeawayResult struct {
	Text          string
	UpdatedCursor int64
	UpdatedAt     *time.Time
	SyncCursor    int64
}

// SetTakeaway is the compare-and-set on the Owner's own text. A write that
// repeats the stored text is idempotent, which is what a replay after a
// lost acknowledgement looks like.
func (s *Service) SetTakeaway(ctx context.Context, userID, sessionID, noteID uuid.UUID, text string, baseCursor int64) (*TakeawayResult, error) {
	if len([]rune(text)) > 20000 {
		return nil, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "text"}}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if note.Trashed {
		return nil, &Refusal{Code: "note_trashed"}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO note_takeaways (note_id, user_id) VALUES ($1, $2) ON CONFLICT (note_id) DO NOTHING`, noteID, userID); err != nil {
		return nil, err
	}
	var current string
	var cursor int64
	var updatedAt *time.Time
	if err := tx.QueryRow(ctx, `SELECT text, updated_cursor, updated_at FROM note_takeaways WHERE note_id = $1 FOR UPDATE`, noteID).Scan(&current, &cursor, &updatedAt); err != nil {
		return nil, err
	}
	if current == text {
		return &TakeawayResult{Text: current, UpdatedCursor: cursor, UpdatedAt: updatedAt, SyncCursor: cursor}, nil
	}
	if cursor != baseCursor {
		return nil, &Refusal{Code: "takeaway_stale", Details: map[string]any{"server_text": current, "server_cursor": cursor}}
	}
	next, err := vsync.Append(ctx, tx, userID, vsync.EntityTakeaway, noteID, &noteID, vsync.ChangeUpdated, &sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.QueryRow(ctx, `UPDATE note_takeaways SET text = $2, updated_cursor = $3 WHERE note_id = $1 RETURNING updated_at`, noteID, text, next).Scan(&updatedAt); err != nil {
		return nil, err
	}
	if err := library.RebuildTakeawayChild(ctx, tx, userID, noteID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &TakeawayResult{Text: text, UpdatedCursor: next, UpdatedAt: updatedAt, SyncCursor: next}, nil
}

// Ref names a Topic or Tag.
type Ref struct {
	ID   uuid.UUID
	Name string
}

// SetPrimaryTopic files the Note under a Topic by id or by name, creating
// the name if needed. Last write wins.
func (s *Service) SetPrimaryTopic(ctx context.Context, userID, sessionID, noteID uuid.UUID, topicID *uuid.UUID, name string) (*Ref, int64, error) {
	name = strings.TrimSpace(name)
	if (topicID == nil) == (name == "") {
		return nil, 0, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "topic_id"}}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, 0, err
	}
	if note.Trashed {
		return nil, 0, &Refusal{Code: "note_trashed"}
	}
	var ref Ref
	if topicID != nil {
		if err := tx.QueryRow(ctx, `SELECT id, name FROM topics WHERE id = $1 AND user_id = $2`, *topicID, userID).Scan(&ref.ID, &ref.Name); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, 0, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "topic_id"}}
			}
			return nil, 0, err
		}
	} else {
		id, err := library.UpsertNamed(ctx, tx, "topics", userID, name)
		if err != nil {
			return nil, 0, err
		}
		if err := tx.QueryRow(ctx, `SELECT id, name FROM topics WHERE id = $1`, id).Scan(&ref.ID, &ref.Name); err != nil {
			return nil, 0, err
		}
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET primary_topic_id = $2 WHERE id = $1`, noteID, ref.ID); err != nil {
		return nil, 0, err
	}
	if err := library.RebuildMetadataChild(ctx, tx, userID, noteID); err != nil {
		return nil, 0, err
	}
	cursor, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, &sessionID)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return &ref, cursor, nil
}

// SetTags replaces the Note's Tags by name; unknown names create Tags.
func (s *Service) SetTags(ctx context.Context, userID, sessionID, noteID uuid.UUID, names []string) ([]Ref, int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, 0, err
	}
	if note.Trashed {
		return nil, 0, &Refusal{Code: "note_trashed"}
	}
	seen := map[string]bool{}
	refs := []Ref{}
	keep := []uuid.UUID{}
	for _, raw := range names {
		name := strings.TrimSpace(raw)
		key := library.NormalizeName(name)
		if name == "" || seen[key] {
			continue
		}
		seen[key] = true
		id, err := library.UpsertNamed(ctx, tx, "tags", userID, name)
		if err != nil {
			return nil, 0, err
		}
		var stored string
		if err := tx.QueryRow(ctx, `SELECT name FROM tags WHERE id = $1`, id).Scan(&stored); err != nil {
			return nil, 0, err
		}
		refs = append(refs, Ref{ID: id, Name: stored})
		keep = append(keep, id)
		if _, err := tx.Exec(ctx, `INSERT INTO note_tags (note_id, tag_id, user_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, noteID, id, userID); err != nil {
			return nil, 0, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM note_tags WHERE note_id = $1 AND NOT (tag_id = ANY($2::uuid[]))`, noteID, keep); err != nil {
		return nil, 0, err
	}
	if err := library.RebuildMetadataChild(ctx, tx, userID, noteID); err != nil {
		return nil, 0, err
	}
	cursor, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, noteID, nil, vsync.ChangeUpdated, &sessionID)
	if err != nil {
		return nil, 0, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, 0, err
	}
	return refs, cursor, nil
}

// Layout is the merged position map after a write.
type Layout struct {
	Positions  map[string]generation.Position
	UpdatedAt  time.Time
	SyncCursor int64
}

// UpdateLayout merges positions into the Note's map: last write wins, never
// a history entry, never a conflict (ADR 0004, ADR 0011).
func (s *Service) UpdateLayout(ctx context.Context, userID, sessionID, noteID uuid.UUID, positions map[string]generation.Position) (*Layout, error) {
	for id, p := range positions {
		if !isFinite(p.X) || !isFinite(p.Y) || !isFinite(p.W) || !isFinite(p.H) || p.W < 0 || p.H < 0 {
			return nil, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "positions." + id}}
		}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	note, err := s.lockNote(ctx, tx, userID, noteID)
	if err != nil {
		return nil, err
	}
	if note.Trashed {
		return nil, &Refusal{Code: "note_trashed"}
	}
	data, err := json.Marshal(positions)
	if err != nil {
		return nil, err
	}
	var raw []byte
	var updatedAt time.Time
	if err := tx.QueryRow(ctx, `
		INSERT INTO note_diagram_layouts (note_id, user_id, positions) VALUES ($1, $2, $3)
		ON CONFLICT (note_id) DO UPDATE SET positions = note_diagram_layouts.positions || EXCLUDED.positions, updated_at = now()
		RETURNING positions, updated_at`, noteID, userID, data).Scan(&raw, &updatedAt); err != nil {
		return nil, err
	}
	merged := map[string]generation.Position{}
	_ = json.Unmarshal(raw, &merged)
	cursor, err := vsync.Append(ctx, tx, userID, vsync.EntityDiagramLayout, noteID, &noteID, vsync.ChangeUpdated, &sessionID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &Layout{Positions: merged, UpdatedAt: updatedAt, SyncCursor: cursor}, nil
}

func isFinite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// Named is a Topic or Tag with its live Note count.
type Named struct {
	ID         uuid.UUID
	Name       string
	Normalized string
	NoteCount  int
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

const topicCount = `(SELECT count(*) FROM notes n WHERE n.primary_topic_id = t.id AND n.processing_state = 'ready' AND n.trashed_at IS NULL)`
const tagCount = `(SELECT count(*) FROM note_tags nt JOIN notes n ON n.id = nt.note_id WHERE nt.tag_id = t.id AND n.processing_state = 'ready' AND n.trashed_at IS NULL)`

// ListTopics is every Topic with its ready, non-Trash Note count.
func (s *Service) ListTopics(ctx context.Context, userID uuid.UUID) ([]Named, error) {
	return s.listNamed(ctx, userID, "topics", topicCount)
}

// ListTags is every Tag with its ready, non-Trash Note count.
func (s *Service) ListTags(ctx context.Context, userID uuid.UUID) ([]Named, error) {
	return s.listNamed(ctx, userID, "tags", tagCount)
}

func (s *Service) listNamed(ctx context.Context, userID uuid.UUID, table, count string) ([]Named, error) {
	rows, err := s.pool.Query(ctx, `SELECT t.id, t.name, t.normalized_name, `+count+`, t.created_at, t.updated_at FROM `+table+` t WHERE t.user_id = $1 ORDER BY t.normalized_name`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Named{}
	for rows.Next() {
		var n Named
		if err := rows.Scan(&n.ID, &n.Name, &n.Normalized, &n.NoteCount, &n.CreatedAt, &n.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *Service) getNamed(ctx context.Context, q interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}, userID, id uuid.UUID, table, count string) (*Named, error) {
	var n Named
	err := q.QueryRow(ctx, `SELECT t.id, t.name, t.normalized_name, `+count+`, t.created_at, t.updated_at FROM `+table+` t WHERE t.id = $1 AND t.user_id = $2`, id, userID).
		Scan(&n.ID, &n.Name, &n.Normalized, &n.NoteCount, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &n, nil
}

// CreateTopic creates a Topic; an existing normalized name is refused with
// the existing id in the details.
func (s *Service) CreateTopic(ctx context.Context, userID, sessionID uuid.UUID, name string) (*Named, error) {
	return s.createNamed(ctx, userID, sessionID, "topics", topicCount, vsync.EntityTopic, "topic_exists", "topic_id", name)
}

func (s *Service) createNamed(ctx context.Context, userID, sessionID uuid.UUID, table, count, entity, existsCode, idKey, name string) (*Named, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return nil, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "name"}}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	var existing uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM `+table+` WHERE user_id = $1 AND normalized_name = $2`, userID, library.NormalizeName(name)).Scan(&existing)
	if err == nil {
		return nil, &Refusal{Code: existsCode, Details: map[string]any{idKey: existing.String()}}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	id, err := library.UpsertNamed(ctx, tx, table, userID, name)
	if err != nil {
		return nil, err
	}
	if _, err := vsync.Append(ctx, tx, userID, entity, id, nil, vsync.ChangeCreated, &sessionID); err != nil {
		return nil, err
	}
	n, err := s.getNamed(ctx, tx, userID, id, table, count)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return n, nil
}

// RenameTopic renames a Topic; the new normalized name must be free.
func (s *Service) RenameTopic(ctx context.Context, userID, sessionID, id uuid.UUID, name string) (*Named, error) {
	return s.renameNamed(ctx, userID, sessionID, id, "topics", topicCount, vsync.EntityTopic, "topic_exists", "topic_id", name)
}

// RenameTag renames a Tag.
func (s *Service) RenameTag(ctx context.Context, userID, sessionID, id uuid.UUID, name string) (*Named, error) {
	return s.renameNamed(ctx, userID, sessionID, id, "tags", tagCount, vsync.EntityTag, "tag_exists", "tag_id", name)
}

func (s *Service) renameNamed(ctx context.Context, userID, sessionID, id uuid.UUID, table, count, entity, existsCode, idKey, name string) (*Named, error) {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return nil, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "name"}}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := s.getNamed(ctx, tx, userID, id, table, count); err != nil {
		return nil, err
	}
	var other uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM `+table+` WHERE user_id = $1 AND normalized_name = $2 AND id <> $3`, userID, library.NormalizeName(name), id).Scan(&other)
	if err == nil {
		return nil, &Refusal{Code: existsCode, Details: map[string]any{idKey: other.String()}}
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `UPDATE `+table+` SET name = $2, normalized_name = $3, updated_at = now() WHERE id = $1`, id, name, library.NormalizeName(name)); err != nil {
		return nil, err
	}
	if err := s.rebuildMetadataFor(ctx, tx, userID, table, id); err != nil {
		return nil, err
	}
	if _, err := vsync.Append(ctx, tx, userID, entity, id, nil, vsync.ChangeUpdated, &sessionID); err != nil {
		return nil, err
	}
	n, err := s.getNamed(ctx, tx, userID, id, table, count)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return n, nil
}

// rebuildMetadataFor rewrites the metadata child of every Note a Topic or
// Tag names, since the name is part of that child.
func (s *Service) rebuildMetadataFor(ctx context.Context, tx pgx.Tx, userID uuid.UUID, table string, id uuid.UUID) error {
	query := `SELECT id FROM notes WHERE primary_topic_id = $1`
	if table == "tags" {
		query = `SELECT note_id FROM note_tags WHERE tag_id = $1`
	}
	rows, err := tx.Query(ctx, query, id)
	if err != nil {
		return err
	}
	var notes []uuid.UUID
	for rows.Next() {
		var n uuid.UUID
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		notes = append(notes, n)
	}
	rows.Close()
	for _, n := range notes {
		if err := library.RebuildMetadataChild(ctx, tx, userID, n); err != nil {
			return err
		}
	}
	return nil
}

// DeleteTopic deletes an unused Topic.
func (s *Service) DeleteTopic(ctx context.Context, userID, sessionID, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := s.getNamed(ctx, tx, userID, id, "topics", topicCount); err != nil {
		return err
	}
	var inUse int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM notes WHERE primary_topic_id = $1`, id).Scan(&inUse); err != nil {
		return err
	}
	if inUse > 0 {
		return &Refusal{Code: "topic_in_use", Details: map[string]any{"note_count": inUse}}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM topics WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityTopic, id, nil, vsync.ChangeDeleted, &sessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// MergeTopic moves every Note under from to into, emits a Sync event per
// moved Note, and deletes from.
func (s *Service) MergeTopic(ctx context.Context, userID, sessionID, from, into uuid.UUID) (*Named, error) {
	if from == into {
		return nil, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "into_topic_id"}}
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := s.getNamed(ctx, tx, userID, from, "topics", topicCount); err != nil {
		return nil, err
	}
	if _, err := s.getNamed(ctx, tx, userID, into, "topics", topicCount); err != nil {
		if errors.Is(err, ErrNotFound) {
			return nil, &Refusal{Code: "invalid_request", Details: map[string]any{"field": "into_topic_id"}}
		}
		return nil, err
	}
	rows, err := tx.Query(ctx, `UPDATE notes SET primary_topic_id = $2 WHERE primary_topic_id = $1 RETURNING id`, from, into)
	if err != nil {
		return nil, err
	}
	var moved []uuid.UUID
	for rows.Next() {
		var n uuid.UUID
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return nil, err
		}
		moved = append(moved, n)
	}
	rows.Close()
	for _, n := range moved {
		if err := library.RebuildMetadataChild(ctx, tx, userID, n); err != nil {
			return nil, err
		}
		if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, n, nil, vsync.ChangeUpdated, &sessionID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM topics WHERE id = $1`, from); err != nil {
		return nil, err
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityTopic, from, nil, vsync.ChangeDeleted, &sessionID); err != nil {
		return nil, err
	}
	survivor, err := s.getNamed(ctx, tx, userID, into, "topics", topicCount)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return survivor, nil
}

// DeleteTag removes a Tag from every Note and deletes it.
func (s *Service) DeleteTag(ctx context.Context, userID, sessionID, id uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := s.getNamed(ctx, tx, userID, id, "tags", tagCount); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, `DELETE FROM note_tags WHERE tag_id = $1 RETURNING note_id`, id)
	if err != nil {
		return err
	}
	var affected []uuid.UUID
	for rows.Next() {
		var n uuid.UUID
		if err := rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		affected = append(affected, n)
	}
	rows.Close()
	for _, n := range affected {
		if err := library.RebuildMetadataChild(ctx, tx, userID, n); err != nil {
			return err
		}
		if _, err := vsync.Append(ctx, tx, userID, vsync.EntityNote, n, nil, vsync.ChangeUpdated, &sessionID); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM tags WHERE id = $1`, id); err != nil {
		return err
	}
	if _, err := vsync.Append(ctx, tx, userID, vsync.EntityTag, id, nil, vsync.ChangeDeleted, &sessionID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
