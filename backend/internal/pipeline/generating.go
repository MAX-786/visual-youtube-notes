package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
)

// expectedOutputTokens is the measured size of a note (cost-tracking.md).
const expectedOutputTokens = 2000

// transcriptRows is the stored Transcript as the generator needs it.
type transcriptRows struct {
	ID     uuid.UUID
	Chunks []generation.Chunk
}

func loadTranscript(ctx context.Context, q querier, noteID uuid.UUID) (*transcriptRows, error) {
	var tr transcriptRows
	if err := q.QueryRow(ctx, `SELECT id FROM transcripts WHERE note_id = $1`, noteID).Scan(&tr.ID); err != nil {
		return nil, err
	}
	rows, err := q.Query(ctx, `SELECT id, ordinal, chunk_text FROM transcript_chunks WHERE transcript_id = $1 ORDER BY ordinal`, tr.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var c generation.Chunk
		if err := rows.Scan(&c.ID, &c.Ordinal, &c.Text); err != nil {
			return nil, err
		}
		tr.Chunks = append(tr.Chunks, c)
	}
	return &tr, rows.Err()
}

// generatingWork is the generating Stage (spec 8.3): one structured-output
// call, validation, free repairs, at most one re-ask, then the Revision
// with its layout and search children in the completion transaction.
func generatingWork(ctx context.Context, d *Deps, note *NoteRow, sr *StageRun) (*result, error) {
	tr, err := loadTranscript(ctx, d.Pool, note.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, Fail(Permanent, "transcript_missing", "No Transcript is stored for this video, so there is nothing to make a note from.", err)
		}
		return nil, err
	}
	if len(tr.Chunks) == 0 {
		return nil, Fail(Permanent, "transcript_missing", "The stored Transcript has no text.", nil)
	}
	creds, err := d.AI.Credentials(ctx, d.Pool, note.UserID, ai.RoleNoteGeneration)
	if err != nil {
		return nil, credentialsError(ai.RoleNoteGeneration, err)
	}
	duration := 0
	if note.DurationSeconds != nil {
		duration = *note.DurationSeconds
	}
	meta := generation.Meta{Title: deref(note.Title), Channel: deref(note.ChannelName), Duration: duration}
	input := generation.RenderInput(tr.Chunks, meta, "")
	schema := generation.RequestSchema()
	client := ai.NewClient(*creds, d.AITransport)
	logger := d.Logger.With("note", note.ID, "stage", StageGenerating)

	var previous string
	var feedback []string
	for reask := 0; reask <= 1; reask++ {
		in := input
		if reask > 0 {
			in = generation.RenderReAsk(input, previous, feedback)
		}
		estimate := ai.UnitMap{
			"input_tokens":  float64(EstimateTokens(generation.SystemPrompt + string(schema) + in)),
			"output_tokens": expectedOutputTokens,
		}
		usageID, err := openCall(ctx, d, note, sr, creds, estimate)
		if err != nil {
			return nil, err
		}
		res, err := client.Respond(ctx, ai.ResponsesRequest{Instructions: generation.SystemPrompt, Input: in, SchemaName: generation.SchemaName, Schema: schema})
		if err != nil {
			closeCall(ctx, d, usageID, nil, "", err)
			return nil, providerFailure(ctx, d, note.UserID, creds.Role, err)
		}
		closeCall(ctx, d, usageID, res.Units, res.RequestID, nil)
		if res.Refusal != "" {
			return nil, Fail(Permanent, "model_refused", "The model declined to write this note: "+truncate(res.Refusal, 200), nil)
		}
		if res.Incomplete != "" {
			return nil, Fail(Permanent, "output_incomplete", "The model stopped before finishing the note ("+res.Incomplete+"). Raise max_output_tokens in Settings → AI or choose a different model.", nil)
		}
		var parsed generation.Note
		if err := json.Unmarshal([]byte(res.Text), &parsed); err != nil {
			if reask == 0 {
				previous = res.Text
				feedback = []string{"note: the answer was not one valid JSON object (" + err.Error() + ")"}
				logger.Info("re-asking after an unparsable answer")
				continue
			}
			return nil, Fail(Permanent, "validation_failed", "The model's answer was not valid JSON, twice.", err)
		}
		cycle := generation.Cycle(&parsed, tr.Chunks, reask, 1)
		if len(cycle.Applied) > 0 {
			logger.Info("repaired the model's answer", "applied", cycle.Applied)
		}
		switch cycle.Outcome {
		case generation.Ready:
			return buildRevision(cycle.Note, tr, res.RequestID, note.ActiveRevisionID)
		case generation.ReAsk:
			previous = res.Text
			feedback = cycle.Feedback
			logger.Info("re-asking the model", "findings", len(cycle.Findings))
		default:
			return nil, Fail(Permanent, "validation_failed", "The note failed validation after a repair and one re-ask: "+generation.Summarize(cycle.Findings), nil)
		}
	}
	return nil, Fail(Permanent, "validation_failed", "The note failed validation.", nil)
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

// buildRevision prepares the completion: the stored payload, its fingerprint,
// and the writes that make it the Note's Active Revision.
func buildRevision(n *generation.Note, tr *transcriptRows, requestID string, base *uuid.UUID) (*result, error) {
	stored := generation.ToStored(n, tr.Chunks)
	payload, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(payload)
	return &result{
		providerRequestID: requestID,
		fingerprint:       sum[:],
		size:              int64(len(payload)),
		next:              StageIndexing,
		apply: func(ctx context.Context, tx pgx.Tx, locked *NoteRow) error {
			return storeRevision(ctx, tx, locked, tr.ID, stored, payload, base)
		},
	}, nil
}

// storeRevision writes the generated Revision and, when the Note still
// points at the base this run recorded, activates it: the compare-and-set
// of ADR 0004. Activation seeds the topic and tags the generator proposed,
// the layout (ADR 0011) and the search children (ADR 0005).
func storeRevision(ctx context.Context, tx pgx.Tx, note *NoteRow, transcriptID uuid.UUID, stored *generation.StoredPayload, payload []byte, base *uuid.UUID) error {
	var number int
	if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision_number), 0) + 1 FROM note_revisions WHERE note_id = $1`, note.ID).Scan(&number); err != nil {
		return err
	}
	parent := note.ActiveRevisionID
	if number > 1 && parent == nil {
		var latest uuid.UUID
		if err := tx.QueryRow(ctx, `SELECT id FROM note_revisions WHERE note_id = $1 ORDER BY revision_number DESC LIMIT 1`, note.ID).Scan(&latest); err != nil {
			return err
		}
		parent = &latest
	}
	var revisionID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO note_revisions (user_id, note_id, revision_number, origin, parent_revision_id, generated_from_transcript_id, payload)
		VALUES ($1, $2, $3, 'generated', $4, $5, $6) RETURNING id`,
		note.UserID, note.ID, number, parent, transcriptID, payload).Scan(&revisionID); err != nil {
		return fmt.Errorf("insert revision: %w", err)
	}
	if !equalUUID(note.ActiveRevisionID, base) {
		// The Active Revision moved while the model was thinking: the result
		// awaits the Owner's decision rather than overwriting their work.
		if _, err := tx.Exec(ctx, `UPDATE note_revisions SET awaiting_decision_since = now() WHERE id = $1`, revisionID); err != nil {
			return err
		}
		return appendNoteUpdated(ctx, tx, note)
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET active_revision_id = $2 WHERE id = $1`, note.ID, revisionID); err != nil {
		return err
	}
	first := note.ActiveRevisionID == nil

	topicName := strings.TrimSpace(stored.PrimaryTopic)
	if topicName == "" {
		topicName = "Uncategorized"
	}
	topicID, err := upsertNamed(ctx, tx, "topics", note.UserID, topicName)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE notes SET primary_topic_id = coalesce(primary_topic_id, $2) WHERE id = $1`, note.ID, topicID); err != nil {
		return err
	}
	if first {
		seen := map[string]bool{}
		for _, tag := range stored.Tags {
			name := strings.TrimSpace(tag)
			key := NormalizeName(name)
			if name == "" || seen[key] {
				continue
			}
			seen[key] = true
			tagID, err := upsertNamed(ctx, tx, "tags", note.UserID, name)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `INSERT INTO note_tags (note_id, tag_id, user_id) VALUES ($1, $2, $3) ON CONFLICT DO NOTHING`, note.ID, tagID, note.UserID); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(ctx, `INSERT INTO note_takeaways (note_id, user_id) VALUES ($1, $2) ON CONFLICT (note_id) DO NOTHING`, note.ID, note.UserID); err != nil {
		return err
	}
	if err := seedLayout(ctx, tx, note, &stored.Diagram); err != nil {
		return err
	}
	if err := rebuildChildren(ctx, tx, note, stored, transcriptID); err != nil {
		return err
	}
	return appendNoteUpdated(ctx, tx, note)
}

// NormalizeName is the identity of a Topic or Tag: case-folded, single
// spaces, trimmed.
func NormalizeName(name string) string {
	return strings.ToLower(strings.Join(strings.Fields(name), " "))
}

// upsertNamed finds or creates a topic or tag by normalized name, keeping the
// Owner's spelling when one exists.
func upsertNamed(ctx context.Context, tx pgx.Tx, table string, userID uuid.UUID, name string) (uuid.UUID, error) {
	var id uuid.UUID
	err := tx.QueryRow(ctx, `
		INSERT INTO `+table+` (user_id, name, normalized_name) VALUES ($1, $2, $3)
		ON CONFLICT (user_id, normalized_name) DO UPDATE SET name = `+table+`.name
		RETURNING id`, userID, name, NormalizeName(name)).Scan(&id)
	if err != nil {
		return uuid.Nil, fmt.Errorf("upsert %s: %w", table, err)
	}
	return id, nil
}

// seedLayout gives every element a position, computing only the missing
// ones and merging into the map (ADR 0011).
func seedLayout(ctx context.Context, tx pgx.Tx, note *NoteRow, diagram *generation.StoredDiagram) error {
	existing := map[string]generation.Position{}
	var raw []byte
	if err := tx.QueryRow(ctx, `SELECT positions FROM note_diagram_layouts WHERE note_id = $1`, note.ID).Scan(&raw); err != nil && !errors.Is(err, pgx.ErrNoRows) {
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
		note.ID, note.UserID, data)
	return err
}

// rebuildChildren replaces the Note's section children and makes sure every
// Transcript chunk has one (search-ranking.md). tsv is generated, so
// lexical search is correct the moment this commits; embeddings follow in
// the indexing Stage.
func rebuildChildren(ctx context.Context, tx pgx.Tx, note *NoteRow, stored *generation.StoredPayload, transcriptID uuid.UUID) error {
	if _, err := tx.Exec(ctx, `DELETE FROM search_children WHERE note_id = $1 AND kind <> 'transcript_chunk'`, note.ID); err != nil {
		return err
	}
	insert := func(kind, weight string, ordinal int, elementID string, content string) error {
		content = strings.TrimSpace(content)
		if content == "" {
			return nil
		}
		sum := sha256.Sum256([]byte(content))
		var element *string
		if elementID != "" {
			element = &elementID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO search_children (user_id, note_id, kind, weight, ordinal, element_id, content, content_hash)
			VALUES ($1, $2, $3::search_child_kind, $4::"char", $5, $6, $7, $8)`,
			note.UserID, note.ID, kind, weight, ordinal, element, content, sum[:])
		return err
	}
	var topic, tags string
	if err := tx.QueryRow(ctx, `
		SELECT coalesce((SELECT t.name FROM topics t JOIN notes n ON n.primary_topic_id = t.id WHERE n.id = $1), ''),
		       coalesce((SELECT string_agg(g.name, ' ' ORDER BY g.name) FROM note_tags nt JOIN tags g ON g.id = nt.tag_id WHERE nt.note_id = $1), '')`, note.ID).Scan(&topic, &tags); err != nil {
		return err
	}
	if err := insert("metadata", "A", 0, "", strings.Join([]string{deref(note.Title), deref(note.ChannelName), topic, tags}, " ")); err != nil {
		return err
	}
	if err := insert("summary", "B", 0, stored.Summary.ID, stored.Summary.Text); err != nil {
		return err
	}
	for i, b := range stored.KeyPoints {
		if err := insert("key_point", "B", i, b.ID, b.Text); err != nil {
			return err
		}
	}
	if err := insert("diagram", "B", 0, "", generation.TextAlternative(&stored.Diagram)); err != nil {
		return err
	}
	var takeaway string
	if err := tx.QueryRow(ctx, `SELECT text FROM note_takeaways WHERE note_id = $1`, note.ID).Scan(&takeaway); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := insert("takeaway", "B", 0, "", takeaway); err != nil {
		return err
	}
	for i, b := range stored.ImportantDetails {
		if err := insert("important_detail", "C", i, b.ID, b.Text); err != nil {
			return err
		}
	}
	for i, b := range stored.PracticalActions {
		if err := insert("practical_action", "C", i, b.ID, b.Text); err != nil {
			return err
		}
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO search_children (user_id, note_id, kind, weight, ordinal, transcript_chunk_id, content, content_hash)
		SELECT $1, $2, 'transcript_chunk', 'D', c.ordinal, c.id, c.chunk_text, sha256(convert_to(c.chunk_text, 'UTF8'))
		  FROM transcript_chunks c WHERE c.transcript_id = $3
		ON CONFLICT (transcript_chunk_id) WHERE transcript_chunk_id IS NOT NULL DO NOTHING`, note.UserID, note.ID, transcriptID)
	return err
}

func equalUUID(a, b *uuid.UUID) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}
