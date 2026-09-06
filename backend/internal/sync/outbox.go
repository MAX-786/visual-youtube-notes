package sync

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Entity kinds and changes, spelled as the enums spell them.
const (
	EntityNote          = "note"
	EntityTakeaway      = "takeaway"
	EntityTopic         = "topic"
	EntityTag           = "tag"
	EntityDiagramLayout = "diagram_layout"

	ChangeCreated = "created"
	ChangeUpdated = "updated"
	ChangeDeleted = "deleted"
)

// Append allocates a Sync cursor and writes one outbox row inside the
// caller's transaction (ADR 0004). The allocator's row lock is held until the
// transaction commits, so cursors become visible in allocation order. The
// stream is nudged in the same transaction, so the nudge lands with the
// commit.
func Append(ctx context.Context, tx pgx.Tx, userID uuid.UUID, entityKind string, entityID uuid.UUID, noteID *uuid.UUID, change string, originSession *uuid.UUID) (int64, error) {
	var cursor int64
	if err := tx.QueryRow(ctx, `SELECT allocate_sync_cursor($1)`, userID).Scan(&cursor); err != nil {
		return 0, fmt.Errorf("allocate sync cursor: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO sync_events (cursor, user_id, entity_kind, entity_id, note_id, change, origin_device_session_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		cursor, userID, entityKind, entityID, noteID, change, originSession); err != nil {
		return 0, fmt.Errorf("append sync event: %w", err)
	}
	if err := Notify(ctx, tx, userID); err != nil {
		return 0, err
	}
	return cursor, nil
}
