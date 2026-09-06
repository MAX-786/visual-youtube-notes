package library

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

// SeedLayout gives every element of a Diagram a position, computing only
// the missing ones and merging into the stored map (ADR 0011). Called
// whenever a Revision becomes Active.
func SeedLayout(ctx context.Context, tx pgx.Tx, userID, noteID uuid.UUID, diagram *generation.StoredDiagram) error {
	existing := map[string]generation.Position{}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT positions FROM note_diagram_layouts WHERE note_id = $1`, noteID).Scan(&raw); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &existing)
	}
	missing := generation.Layout(diagram, existing)
	if len(missing) == 0 {
		return nil
	}
	data, err := json.Marshal(missing)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO note_diagram_layouts (note_id, user_id, positions) VALUES ($1, $2, $3)
		ON CONFLICT (note_id) DO UPDATE SET positions = note_diagram_layouts.positions || EXCLUDED.positions, updated_at = now()`,
		noteID, userID, data)
	return err
}
