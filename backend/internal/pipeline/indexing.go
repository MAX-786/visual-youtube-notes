package pipeline

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
)

// pendingChild is one search child without a current vector.
type pendingChild struct {
	ID      uuid.UUID
	Content string
	Hash    []byte
	Model   string
}

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
	creds, err := d.AI.Credentials(ctx, d.Pool, note.UserID, ai.RoleEmbeddings)
	if err != nil {
		return nil, credentialsError(ai.RoleEmbeddings, err)
	}
	if creds.Model != pending[0].Model {
		return nil, Fail(Permanent, "embedding_model_mismatch",
			fmt.Sprintf("The embeddings role names %s but search indexes %s. Set the role back in Settings → AI, or change the index through the admin reindex.", creds.Model, pending[0].Model), nil)
	}
	var dimension int
	if err := d.Pool.QueryRow(ctx, `SELECT embedding_dimension FROM search_index_state`).Scan(&dimension); err != nil {
		return nil, err
	}
	inputs := make([]string, 0, len(pending))
	estimate := 0
	for _, p := range pending {
		inputs = append(inputs, p.Content)
		estimate += EstimateTokens(p.Content)
	}
	usageID, err := openCall(ctx, d, note, sr, creds, ai.UnitMap{"embedding_tokens": float64(estimate)})
	if err != nil {
		return nil, err
	}
	res, err := ai.NewClient(*creds, d.AITransport).Embed(ctx, inputs)
	if err != nil {
		closeCall(ctx, d, usageID, nil, "", err)
		return nil, providerFailure(ctx, d, note.UserID, creds.Role, err)
	}
	closeCall(ctx, d, usageID, res.Units, res.RequestID, nil)
	literals := make([]string, len(res.Vectors))
	for i, v := range res.Vectors {
		if len(v) != dimension {
			return nil, Fail(Permanent, "embedding_dimension_mismatch",
				fmt.Sprintf("The provider returned %d-dimensional vectors but the index stores %d. Set dimensions to %d in Settings → AI.", len(v), dimension, dimension), nil)
		}
		literals[i] = vectorLiteral(v)
	}
	hash := sha256.New()
	for _, p := range pending {
		hash.Write(p.Hash)
	}
	return &result{
		providerRequestID: res.RequestID,
		fingerprint:       hash.Sum(nil),
		size:              int64(len(pending)),
		terminal:          true,
		apply: func(ctx context.Context, tx pgx.Tx, _ *NoteRow) error {
			for i, p := range pending {
				if _, err := tx.Exec(ctx, `
					INSERT INTO search_embeddings (child_id, embedding_model, embedding, content_hash)
					VALUES ($1, $2, $3::vector, $4)
					ON CONFLICT (child_id, embedding_model) DO UPDATE
					   SET embedding = EXCLUDED.embedding, content_hash = EXCLUDED.content_hash, embedded_at = now()`,
					p.ID, p.Model, literals[i], p.Hash); err != nil {
					return fmt.Errorf("store embedding: %w", err)
				}
			}
			return nil
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
