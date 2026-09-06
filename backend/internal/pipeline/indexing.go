package pipeline

import (
	"context"
	"crypto/sha256"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
)

// pendingChild is one search child without a current vector.
// indexingWork is the indexing Stage: embed every search child the active
// model has no current vector for, in one call, and mark the Note ready.
// Lexical search was correct the moment the Revision committed; this is
// the half that needs the network (ADR 0005).
func indexingWork(ctx context.Context, d *Deps, note *NoteRow, sr *StageRun) (*result, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT child_id, content, content_hash, embedding_model
		  FROM search_children_needing_embedding WHERE note_id = $1 ORDER BY child_id`, note.ID)
	if err != nil {
		return nil, err
	}
	var pending []pendingChild
	for rows.Next() {
		var p pendingChild
		if err := rows.Scan(&p.ID, &p.Content, &p.Hash, &p.Model); err != nil {
			rows.Close()
			return nil, err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(pending) == 0 {
		sum := sha256.Sum256([]byte("nothing to embed"))
		return &result{fingerprint: sum[:], terminal: true}, nil
	}
	literals, requestID, err := embedBatch(ctx, d, note.UserID, &note.ID, sr, pending[0].Model, pending)
	if err != nil {
		return nil, err
	}
	hash := sha256.New()
	for _, p := range pending {
		hash.Write(p.Hash)
	}
	return &result{
		providerRequestID: requestID,
		fingerprint:       hash.Sum(nil),
		size:              int64(len(pending)),
		terminal:          true,
		apply: func(ctx context.Context, tx pgx.Tx, _ *NoteRow) error {
			return storeVectors(ctx, tx, pending[0].Model, pending, literals)
		},
	}, nil
}

// vectorLiteral is pgvector's text form.
func vectorLiteral(v []float32) string {
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = strconv.FormatFloat(float64(f), 'g', -1, 32)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
