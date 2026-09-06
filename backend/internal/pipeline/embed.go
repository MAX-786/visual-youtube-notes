package pipeline

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/rivertype"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
)

// pendingChild is a search child the index has no current vector for.
type pendingChild struct {
	ID      uuid.UUID
	UserID  uuid.UUID
	NoteID  uuid.UUID
	Content string
	Hash    []byte
	Model   string
}

// embedBatch makes one embeddings call for the children, with a ledger row
// opened before and closed after, and returns the vectors as pgvector
// literals. The role's model must be the model asked for.
func embedBatch(ctx context.Context, d *Deps, userID uuid.UUID, noteID *uuid.UUID, sr *StageRun, model string, pending []pendingChild) ([]string, string, error) {
	creds, err := d.AI.Credentials(ctx, d.Pool, userID, ai.RoleEmbeddings)
	if err != nil {
		return nil, "", credentialsError(ai.RoleEmbeddings, err)
	}
	if creds.Model != model {
		return nil, "", Fail(Permanent, "embedding_model_mismatch",
			fmt.Sprintf("The embeddings role names %s but search indexes %s. Set the role back in Settings → AI, or change the index through the admin reindex.", creds.Model, model), nil)
	}
	var dimension int
	if err := d.Pool.QueryRow(ctx, `SELECT embedding_dimension FROM search_index_state`).Scan(&dimension); err != nil {
		return nil, "", err
	}
	inputs := make([]string, 0, len(pending))
	estimate := 0
	for _, p := range pending {
		inputs = append(inputs, p.Content)
		estimate += EstimateTokens(p.Content)
	}
	call := ai.Call{UserID: userID, NoteID: noteID, Role: creds.Role, Provider: creds.Provider, Model: creds.Model, Estimated: ai.UnitMap{"embedding_tokens": float64(estimate)}}
	if sr != nil {
		version := sr.Run.ProcessingVersion
		call.AttemptID, call.ProcessingVersion = &sr.AttemptID, &version
	}
	usageID, err := ai.OpenUsage(ctx, d.Pool, call)
	if err != nil {
		return nil, "", err
	}
	res, err := ai.NewClient(*creds, d.AITransport).Embed(ctx, inputs)
	if err != nil {
		closeCall(ctx, d, usageID, nil, "", err)
		return nil, "", providerFailure(ctx, d, userID, creds.Role, err)
	}
	closeCall(ctx, d, usageID, res.Units, res.RequestID, nil)
	if len(res.Vectors) != len(pending) {
		return nil, "", Fail(Transient, "embedding_count_mismatch", "The provider returned a different number of vectors than inputs; it will be retried.", nil)
	}
	literals := make([]string, len(res.Vectors))
	for i, v := range res.Vectors {
		if len(v) != dimension {
			return nil, "", Fail(Permanent, "embedding_dimension_mismatch",
				fmt.Sprintf("The provider returned %d-dimensional vectors but the index stores %d. Set dimensions to %d in Settings → AI.", len(v), dimension, dimension), nil)
		}
		literals[i] = vectorLiteral(v)
	}
	return literals, res.RequestID, nil
}

type execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// storeVectors upserts one model's vectors for the children.
func storeVectors(ctx context.Context, q execer, model string, pending []pendingChild, literals []string) error {
	for i, p := range pending {
		if _, err := q.Exec(ctx, `
			INSERT INTO search_embeddings (child_id, embedding_model, embedding, content_hash)
			VALUES ($1, $2, $3::vector, $4)
			ON CONFLICT (child_id, embedding_model) DO UPDATE
			   SET embedding = EXCLUDED.embedding, content_hash = EXCLUDED.content_hash, embedded_at = now()`,
			p.ID, model, literals[i], p.Hash); err != nil {
			return fmt.Errorf("store embedding: %w", err)
		}
	}
	return nil
}

// A batch is what one provider call carries.
const embedBatchSize = 64

// ReindexArgs embeds every child under an incoming model (ADR 0005,
// docs/design/search-ranking.md "Changing the embedding model"). Search keeps
// reading the active model throughout; the flip is the Owner's, gated on
// completeness.
type ReindexArgs struct {
	Model string `json:"model"`
}

// Kind implements river.JobArgs.
func (ReindexArgs) Kind() string { return "search_reindex" }

// InsertOpts implements river.JobArgsWithInsertOpts: one job per model at a time.
func (ReindexArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 4, UniqueOpts: river.UniqueOpts{ByArgs: true, ByState: []rivertype.JobState{rivertype.JobStateAvailable, rivertype.JobStatePending, rivertype.JobStateRetryable, rivertype.JobStateRunning, rivertype.JobStateScheduled}}}
}

// EmbedSweepArgs embeds children the active model has no current vector
// for: edits rebuild children in the write transaction, and this is what
// gives them vectors afterwards (hourly, and at worker start).
type EmbedSweepArgs struct{}

// Kind implements river.JobArgs.
func (EmbedSweepArgs) Kind() string { return "embed_sweep" }

// InsertOpts implements river.JobArgsWithInsertOpts.
func (EmbedSweepArgs) InsertOpts() river.InsertOpts {
	return river.InsertOpts{Queue: QueueDefault, MaxAttempts: 1, UniqueOpts: river.UniqueOpts{ByArgs: true, ByPeriod: 10 * time.Minute}}
}

// StartReindex enqueues the reindex job for a model.
func (s *Service) StartReindex(ctx context.Context, model string) error {
	_, err := s.client.Insert(ctx, ReindexArgs{Model: model}, nil)
	return err
}

type reindexWorker struct {
	river.WorkerDefaults[ReindexArgs]
	deps *Deps
}

// Timeout gives a large library room; the job resumes where it stopped.
func (w *reindexWorker) Timeout(*river.Job[ReindexArgs]) time.Duration { return 6 * time.Hour }

// Work embeds batch after batch until every child has a current vector
// under the model, stopping quietly if the reindex was abandoned meanwhile.
func (w *reindexWorker) Work(ctx context.Context, job *river.Job[ReindexArgs]) error {
	d := w.deps
	if d.AI == nil {
		return river.JobCancel(errors.New("no AI configuration in this worker"))
	}
	for {
		var into *string
		if err := d.Pool.QueryRow(ctx, `SELECT reindexing_into_model FROM search_index_state`).Scan(&into); err != nil {
			return err
		}
		if into == nil || *into != job.Args.Model {
			d.Logger.Info("reindex stopped: no longer in progress", "model", job.Args.Model)
			return nil
		}
		batch, err := pendingForModel(ctx, d, job.Args.Model)
		if err != nil {
			return err
		}
		if len(batch) == 0 {
			d.Logger.Info("reindex complete; awaiting activation", "model", job.Args.Model)
			return nil
		}
		if err := embedAndStore(ctx, d, job.Args.Model, batch); err != nil {
			return err
		}
	}
}

// pendingForModel is one batch of children lacking a current vector under
// the model, all of one user so one key covers them.
func pendingForModel(ctx context.Context, d *Deps, model string) ([]pendingChild, error) {
	rows, err := d.Pool.Query(ctx, `
		SELECT c.id, c.user_id, c.note_id, c.content, c.content_hash
		  FROM search_children c
		  JOIN notes n ON n.id = c.note_id
		 WHERE n.trashed_at IS NULL
		   AND c.user_id = (SELECT c2.user_id FROM search_children c2
		                     WHERE NOT EXISTS (SELECT 1 FROM search_embeddings e WHERE e.child_id = c2.id AND e.embedding_model = $1 AND e.content_hash = c2.content_hash)
		                     ORDER BY c2.user_id LIMIT 1)
		   AND NOT EXISTS (SELECT 1 FROM search_embeddings e WHERE e.child_id = c.id AND e.embedding_model = $1 AND e.content_hash = c.content_hash)
		 ORDER BY c.note_id, c.id
		 LIMIT $2`, model, embedBatchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []pendingChild
	for rows.Next() {
		p := pendingChild{Model: model}
		if err := rows.Scan(&p.ID, &p.UserID, &p.NoteID, &p.Content, &p.Hash); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

func embedAndStore(ctx context.Context, d *Deps, model string, batch []pendingChild) error {
	var noteID *uuid.UUID
	if id := batch[0].NoteID; allSameNote(batch) {
		noteID = &id
	}
	literals, _, err := embedBatch(ctx, d, batch[0].UserID, noteID, nil, model, batch)
	if err != nil {
		var se *StageError
		if errors.As(err, &se) && se.Verdict != Transient {
			d.Logger.Warn("embedding stopped permanently", "code", se.Code, "err", err)
			return river.JobCancel(err)
		}
		return err
	}
	return storeVectors(ctx, d.Pool, model, batch, literals)
}

func allSameNote(batch []pendingChild) bool {
	for _, p := range batch[1:] {
		if p.NoteID != batch[0].NoteID {
			return false
		}
	}
	return true
}

type embedSweepWorker struct {
	river.WorkerDefaults[EmbedSweepArgs]
	deps *Deps
}

// Timeout bounds one sweep; the next one continues.
func (w *embedSweepWorker) Timeout(*river.Job[EmbedSweepArgs]) time.Duration { return 30 * time.Minute }

// Work embeds, under the active model, the children of ready Notes that
// have no current vector, a batch at a time.
func (w *embedSweepWorker) Work(ctx context.Context, _ *river.Job[EmbedSweepArgs]) error {
	d := w.deps
	if d.AI == nil {
		return nil
	}
	for {
		rows, err := d.Pool.Query(ctx, `
			SELECT v.child_id, v.user_id, v.note_id, v.content, v.content_hash, v.embedding_model
			  FROM search_children_needing_embedding v
			  JOIN notes n ON n.id = v.note_id
			 WHERE n.processing_state = 'ready' AND n.trashed_at IS NULL
			   AND v.user_id = (SELECT v2.user_id FROM search_children_needing_embedding v2 JOIN notes n2 ON n2.id = v2.note_id
			                     WHERE n2.processing_state = 'ready' AND n2.trashed_at IS NULL ORDER BY v2.user_id LIMIT 1)
			 ORDER BY v.note_id, v.child_id
			 LIMIT $1`, embedBatchSize)
		if err != nil {
			return err
		}
		var batch []pendingChild
		for rows.Next() {
			var p pendingChild
			if err := rows.Scan(&p.ID, &p.UserID, &p.NoteID, &p.Content, &p.Hash, &p.Model); err != nil {
				rows.Close()
				return err
			}
			batch = append(batch, p)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if len(batch) == 0 {
			return nil
		}
		if err := embedAndStore(ctx, d, batch[0].Model, batch); err != nil {
			return err
		}
	}
}

// ContentHash is how children are fingerprinted, shared with the library.
func ContentHash(text string) []byte {
	sum := sha256.Sum256([]byte(text))
	return sum[:]
}
