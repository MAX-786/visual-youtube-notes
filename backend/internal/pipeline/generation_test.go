package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

var updateFixtures = flag.Bool("update", false, "rewrite docs/design/generation/fixtures/en-neural-network.stored.json from the pipeline's output")

const fixtureDir = "../../../docs/design/generation/fixtures"

// fakeOpenAI is the no-network double beneath the provider client: the
// Responses API answers with canned notes in turn, the embeddings API with
// one-hot vectors, and every request is recorded.
type fakeOpenAI struct {
	mu        sync.Mutex
	notes     []string
	dimension int
	responses int
	paths     []string
	// Speech to text: the piece texts served in order (the last repeats),
	// an optional gate every call waits on, a started signal per call, and
	// the most calls ever in flight at once.
	transcriptions []string
	sttCalls       int
	sttGate        chan struct{}
	sttStarted     chan struct{}
	sttInFlight    int
	sttMaxInFlight int
	// sttStatus, when set, is the HTTP status every transcription call fails with.
	sttStatus int
}

func (f *fakeOpenAI) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, req.URL.Path)
	var body any
	switch req.URL.Path {
	case "/v1/responses":
		i := min(f.responses, len(f.notes)-1)
		f.responses++
		body = map[string]any{
			"id": fmt.Sprintf("resp_%d", f.responses), "status": "completed",
			"output": []any{map[string]any{"type": "message", "content": []any{map[string]any{"type": "output_text", "text": f.notes[i]}}}},
			"usage":  map[string]any{"input_tokens": 5000, "input_tokens_details": map[string]any{"cached_tokens": 1000}, "output_tokens": 2000},
		}
	case "/v1/audio/transcriptions":
		f.sttInFlight++
		f.sttMaxInFlight = max(f.sttMaxInFlight, f.sttInFlight)
		i := min(f.sttCalls, len(f.transcriptions)-1)
		f.sttCalls++
		gate, started := f.sttGate, f.sttStarted
		f.mu.Unlock()
		if started != nil {
			select {
			case started <- struct{}{}:
			default:
			}
		}
		var gateErr error
		if gate != nil {
			select {
			case <-gate:
			case <-req.Context().Done():
				gateErr = req.Context().Err()
			}
		}
		f.mu.Lock()
		f.sttInFlight--
		if gateErr != nil {
			return nil, gateErr
		}
		if f.sttStatus != 0 {
			return &http.Response{StatusCode: f.sttStatus, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"Unsupported file format","type":"invalid_request_error"}}`)), Header: http.Header{}, Request: req}, nil
		}
		text := ""
		if i >= 0 {
			text = f.transcriptions[i]
		}
		body = map[string]any{"text": text, "usage": map[string]any{"type": "duration", "seconds": 60}}
	case "/v1/embeddings":
		var in struct {
			Input []string `json:"input"`
		}
		raw, _ := io.ReadAll(req.Body)
		_ = json.Unmarshal(raw, &in)
		data := make([]any, len(in.Input))
		for i := range in.Input {
			vec := make([]float32, f.dimension)
			vec[i%f.dimension] = 1
			data[i] = map[string]any{"index": i, "embedding": vec}
		}
		body = map[string]any{"data": data, "usage": map[string]any{"total_tokens": 1234}}
	default:
		return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"no such route"}}`)), Header: http.Header{}, Request: req}, nil
	}
	raw, _ := json.Marshal(body)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(bytes.NewReader(raw)), Header: http.Header{"Content-Type": {"application/json"}}, Request: req}, nil
}

type fixtureTranscript struct {
	Chunks []struct {
		ChunkID string `json:"chunk_id"`
		Index   int    `json:"index"`
		Text    string `json:"text"`
	} `json:"chunks"`
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(fixtureDir, name))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// sealKeys stores a real sealed key for the roles, replacing setup's fake
// ciphertext that only admits Submissions.
func sealKeys(t *testing.T, pool *pgxpool.Pool, ring *keys.Ring, userID uuid.UUID, roles ...string) {
	t.Helper()
	for _, role := range roles {
		sealed, err := ai.Seal(ring, userID, role, "sk-test-"+role+"-0000wxyz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(context.Background(), `UPDATE ai_role_configs SET key_ciphertext = $3, key_version = $4, key_hint = $5 WHERE user_id = $1 AND role = $2`,
			userID, role, sealed.Ciphertext, sealed.Version, sealed.Hint); err != nil {
			t.Fatal(err)
		}
	}
}

// seedTranscribedNote creates a Note whose free Stages are done and whose
// Transcript is the design fixture, chunk ids included, then enqueues the
// generating Stage.
func seedTranscribedNote(t *testing.T, deps *Deps, userID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var fixture fixtureTranscript
	if err := json.Unmarshal(readFixture(t, "en-neural-network.transcript.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	var sourceID, noteID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO sources (user_id, video_id, canonical_url, title, channel_name, duration_seconds, metadata_fetched_at)
		VALUES ($1, 'aircAruvnKk', 'https://www.youtube.com/watch?v=aircAruvnKk', 'But what is a neural network?', '3Blue1Brown', 1120, now()) RETURNING id`, userID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO notes (user_id, source_id) VALUES ($1, $2) RETURNING id`, userID, sourceID).Scan(&noteID); err != nil {
		t.Fatal(err)
	}
	run := Run{NoteID: noteID, ProcessingVersion: 1}
	if err := createStagesTx(ctx, tx, userID, run); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE note_stages SET status = 'ready', started_at = now(), finished_at = now() WHERE note_id = $1 AND stage IN ('metadata', 'captions')`, noteID); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE note_stages SET status = 'skipped', finished_at = now() WHERE note_id = $1 AND stage = 'transcribing'`, noteID); err != nil {
		t.Fatal(err)
	}
	texts := make([]string, 0, len(fixture.Chunks))
	for _, c := range fixture.Chunks {
		texts = append(texts, c.Text)
	}
	var transcriptID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO transcripts (user_id, note_id, source_kind, language_code, normalized_text)
		VALUES ($1, $2, 'caption_human', 'en', $3) RETURNING id`, userID, noteID, strings.Join(texts, " ")).Scan(&transcriptID); err != nil {
		t.Fatal(err)
	}
	offset := 0
	for _, c := range fixture.Chunks {
		length := utf8.RuneCountInString(c.Text)
		if _, err := tx.Exec(ctx, `
			INSERT INTO transcript_chunks (id, transcript_id, ordinal, char_start, char_end, token_count, chunk_text)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`, uuid.MustParse(c.ChunkID), transcriptID, c.Index, offset, offset+length, max(EstimateTokens(c.Text), 1), c.Text); err != nil {
			t.Fatal(err)
		}
		offset += length + 1
	}
	if err := enqueueStage(ctx, deps.Client, tx, run, StageGenerating, time.Time{}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	return noteID
}

func noteStatus(t *testing.T, pool *pgxpool.Pool, noteID uuid.UUID) (state string, failureStage *string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `SELECT processing_state::text, failure_stage FROM notes WHERE id = $1`, noteID).Scan(&state, &failureStage); err != nil {
		t.Fatal(err)
	}
	return state, failureStage
}

func TestGenerationMakesTheFixtureAReadyNote(t *testing.T) {
	fake := &fakeOpenAI{notes: []string{string(readFixture(t, "en-neural-network.note-valid.json"))}, dimension: 1024}
	var ring *keys.Ring
	deps, _, _, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleNoteGeneration, ai.RoleEmbeddings)
	noteID := seedTranscribedNote(t, deps, userID)

	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, noteID)
		return state == "ready" || state == "failed"
	})
	if state, stage := noteStatus(t, deps.Pool, noteID); state != "ready" {
		var code, message string
		_ = deps.Pool.QueryRow(ctx, `SELECT coalesce(a.error_code, ''), coalesce(a.error_message, '') FROM stage_attempts a JOIN note_stages s ON s.id = a.stage_id WHERE s.note_id = $1 ORDER BY a.started_at DESC LIMIT 1`, noteID).Scan(&code, &message)
		t.Fatalf("note is %s at %v: %s %s", state, stage, code, message)
	}

	// The Revision is active, generated from the Transcript, and its payload
	// carries resolved grounding and the stamps the model never wrote.
	var payload []byte
	var origin string
	var number int
	if err := deps.Pool.QueryRow(ctx, `
		SELECT r.payload, r.origin::text, r.revision_number FROM note_revisions r JOIN notes n ON n.active_revision_id = r.id WHERE n.id = $1`, noteID).
		Scan(&payload, &origin, &number); err != nil {
		t.Fatal(err)
	}
	if origin != "generated" || number != 1 {
		t.Fatalf("revision %s #%d", origin, number)
	}
	var stored generation.StoredPayload
	if err := json.Unmarshal(payload, &stored); err != nil {
		t.Fatal(err)
	}
	if stored.SchemaVersion != 1 || stored.Summary.Provenance != "ai" || len(stored.Summary.SourceRefs) == 0 || stored.Diagram.SchemaVersion != 1 {
		t.Fatalf("payload stamps: %+v", stored.Summary)
	}
	for _, refs := range [][]generation.SourceRef{stored.Summary.SourceRefs, stored.KeyPoints[0].SourceRefs} {
		for _, ref := range refs {
			var exists bool
			if err := deps.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM transcript_chunks c JOIN transcripts t ON t.id = c.transcript_id WHERE c.id = $1 AND t.note_id = $2)`, ref.ChunkID, noteID).Scan(&exists); err != nil || !exists {
				t.Fatalf("source ref %v does not resolve to this note's transcript", ref)
			}
		}
	}

	// Topic and tags the generator proposed became the Note's.
	var topic string
	var tags int
	if err := deps.Pool.QueryRow(ctx, `SELECT t.name, (SELECT count(*) FROM note_tags nt WHERE nt.note_id = n.id) FROM notes n JOIN topics t ON t.id = n.primary_topic_id WHERE n.id = $1`, noteID).Scan(&topic, &tags); err != nil {
		t.Fatal(err)
	}
	if topic != "Neural networks" || tags != 4 {
		t.Fatalf("topic %q, %d tags", topic, tags)
	}

	// Every element has a server-seeded box.
	var positions map[string]generation.Position
	var raw []byte
	if err := deps.Pool.QueryRow(ctx, `SELECT positions FROM note_diagram_layouts WHERE note_id = $1`, noteID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	_ = json.Unmarshal(raw, &positions)
	if want := len(stored.Diagram.Elements()); stored.Diagram.Type != "comparison" && len(positions) != want {
		t.Fatalf("%d positions for %d elements", len(positions), want)
	}

	// Search children of every kind, each with a vector under the active model.
	counts := map[string]int{}
	rows, err := deps.Pool.Query(ctx, `SELECT kind::text, count(*) FROM search_children WHERE note_id = $1 GROUP BY kind`, noteID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var kind string
		var n int
		_ = rows.Scan(&kind, &n)
		counts[kind] = n
	}
	rows.Close()
	if counts["metadata"] != 1 || counts["summary"] != 1 || counts["diagram"] != 1 || counts["key_point"] != len(stored.KeyPoints) || counts["transcript_chunk"] == 0 {
		t.Fatalf("children %v", counts)
	}
	var children, embedded int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*), count(e.child_id) FROM search_children c LEFT JOIN search_embeddings e ON e.child_id = c.id AND e.embedding_model = 'text-embedding-3-large' WHERE c.note_id = $1`, noteID).Scan(&children, &embedded)
	if children == 0 || embedded != children {
		t.Fatalf("%d of %d children embedded", embedded, children)
	}
	var stale int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM search_children_needing_embedding WHERE note_id = $1`, noteID).Scan(&stale)
	if stale != 0 {
		t.Fatalf("%d children still need embedding", stale)
	}

	// One billed ledger row per call, with the provider's units and a
	// snapshotted charge at the seeded prices.
	type call struct {
		role, outcome, charge string
		reported              map[string]float64
	}
	var calls []call
	ledger, err := deps.Pool.Query(ctx, `SELECT role::text, outcome::text, charge_usd::text, reported_units FROM ai_usage_events WHERE note_id = $1 ORDER BY started_at`, noteID)
	if err != nil {
		t.Fatal(err)
	}
	for ledger.Next() {
		var c call
		var reported []byte
		if err := ledger.Scan(&c.role, &c.outcome, &c.charge, &reported); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(reported, &c.reported)
		calls = append(calls, c)
	}
	ledger.Close()
	if len(calls) != 2 || calls[0].role != "note_generation" || calls[1].role != "embeddings" {
		t.Fatalf("ledger %+v", calls)
	}
	// 4000 uncached at 0.000002, 1000 cached at 0.0000002, 2000 out at 0.000012.
	if calls[0].outcome != "billed" || calls[0].charge != "0.0322000000" || calls[0].reported["input_tokens"] != 4000 || calls[0].reported["cached_input_tokens"] != 1000 {
		t.Fatalf("generation row %+v", calls[0])
	}
	if calls[1].outcome != "billed" || calls[1].charge != "0.0001604200" || calls[1].reported["embedding_tokens"] != 1234 {
		t.Fatalf("embedding row %+v", calls[1])
	}
	if got := strings.Count(strings.Join(fake.paths, ","), "/v1/responses"); got != 1 {
		t.Fatalf("%d model calls", got)
	}

	if *updateFixtures {
		pretty, _ := json.MarshalIndent(stored, "", "  ")
		if err := os.WriteFile(filepath.Join(fixtureDir, "en-neural-network.stored.json"), append(pretty, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestHallucinatedQuoteIsReAskedOnceThenFailsPermanently(t *testing.T) {
	var note map[string]any
	if err := json.Unmarshal(readFixture(t, "en-neural-network.note-valid.json"), &note); err != nil {
		t.Fatal(err)
	}
	summary := note["summary"].(map[string]any)
	summary["source_refs"].([]any)[0].(map[string]any)["quote"] = "the moon is made of cheese"
	bad, _ := json.Marshal(note)
	fake := &fakeOpenAI{notes: []string{string(bad), string(bad)}, dimension: 1024}
	var ring *keys.Ring
	deps, _, _, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleNoteGeneration, ai.RoleEmbeddings)
	noteID := seedTranscribedNote(t, deps, userID)

	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, noteID)
		return state == "ready" || state == "failed"
	})
	state, stage := noteStatus(t, deps.Pool, noteID)
	if state != "failed" || stage == nil || *stage != StageGenerating {
		t.Fatalf("note %s at %v", state, stage)
	}
	var code, message string
	var permanent bool
	var attempts int
	if err := deps.Pool.QueryRow(ctx, `
		SELECT a.error_code, a.error_message, a.is_permanent, (SELECT count(*) FROM stage_attempts b WHERE b.stage_id = s.id)
		  FROM stage_attempts a JOIN note_stages s ON s.id = a.stage_id
		 WHERE s.note_id = $1 AND s.stage = 'generating' ORDER BY a.attempt_no DESC LIMIT 1`, noteID).Scan(&code, &message, &permanent, &attempts); err != nil {
		t.Fatal(err)
	}
	if code != "validation_failed" || !permanent || attempts != 1 || !strings.Contains(message, "quote not present in c2") {
		t.Fatalf("attempt: %s permanent=%v attempts=%d: %s", code, permanent, attempts, message)
	}
	if got := strings.Count(strings.Join(fake.paths, ","), "/v1/responses"); got != 2 {
		t.Fatalf("%d model calls, expected the first ask and one re-ask", got)
	}
	var billed int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_events WHERE note_id = $1 AND outcome = 'billed' AND role = 'note_generation'`, noteID).Scan(&billed)
	if billed != 2 {
		t.Fatalf("%d billed rows for two asks", billed)
	}
	var revisions int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM note_revisions WHERE note_id = $1`, noteID).Scan(&revisions)
	if revisions != 0 {
		t.Fatal("a failed generation must not write a Revision")
	}
}
