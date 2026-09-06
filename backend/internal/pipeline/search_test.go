package pipeline

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/riverqueue/river"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/library"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/search"
)

// seededNote describes a ready Note built without the network: its Source
// facts, and a Transcript whose text is also what the stubbed generation
// writes into the Note's sections, so children and vectors exist for both.
type seededNote struct {
	video, title, channel, channelID string
	duration                         int
	savedAt                          time.Time
	transcript                       []string
}

// seedReadyNote inserts the Note with free Stages done and its Transcript,
// runs the stubbed generating and the real indexing Stage, and waits.
func seedReadyNote(t *testing.T, deps *Deps, userID uuid.UUID, n seededNote) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	tx, err := deps.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	var sourceID, noteID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO sources (user_id, video_id, canonical_url, title, channel_id, channel_name, duration_seconds, metadata_fetched_at)
		VALUES ($1, $2, 'https://www.youtube.com/watch?v=' || $2, $3, $4, $5, $6, now()) RETURNING id`,
		userID, n.video, n.title, n.channelID, n.channel, n.duration).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRow(ctx, `INSERT INTO notes (user_id, source_id, saved_at) VALUES ($1, $2, $3) RETURNING id`, userID, sourceID, n.savedAt).Scan(&noteID); err != nil {
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
	var transcriptID uuid.UUID
	if err := tx.QueryRow(ctx, `
		INSERT INTO transcripts (user_id, note_id, source_kind, language_code, normalized_text)
		VALUES ($1, $2, 'caption_human', 'en', $3) RETURNING id`, userID, noteID, strings.Join(n.transcript, " ")).Scan(&transcriptID); err != nil {
		t.Fatal(err)
	}
	offset := 0
	for i, text := range n.transcript {
		length := utf8.RuneCountInString(text)
		if _, err := tx.Exec(ctx, `
			INSERT INTO transcript_chunks (transcript_id, ordinal, char_start, char_end, token_count, chunk_text)
			VALUES ($1, $2, $3, $4, $5, $6)`, transcriptID, i, offset, offset+length, max(EstimateTokens(text), 1), text); err != nil {
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
	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, noteID)
		return state == "ready" || state == "failed"
	})
	if state, _ := noteStatus(t, deps.Pool, noteID); state != "ready" {
		t.Fatalf("seeded note %s is %s", n.video, state)
	}
	return noteID
}

// sectionsFromTranscript is the stubbed generating Stage: a Revision whose
// summary is the first chunk and whose key points are the rest, so the
// Note's sections index content distinct from the Transcript's later chunks.
func sectionsFromTranscript(ctx context.Context, d *Deps, note *NoteRow, _ *StageRun) (*result, error) {
	rows, err := d.Pool.Query(ctx, `SELECT c.chunk_text FROM transcript_chunks c JOIN transcripts t ON t.id = c.transcript_id WHERE t.note_id = $1 ORDER BY c.ordinal`, note.ID)
	if err != nil {
		return nil, err
	}
	var chunks []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			rows.Close()
			return nil, err
		}
		chunks = append(chunks, s)
	}
	rows.Close()
	summary := chunks[0]
	keyPoints := ""
	for i, c := range chunks[1:min(3, len(chunks))] {
		if i > 0 {
			keyPoints += ","
		}
		keyPoints += `{"id":"kp` + string(rune('1'+i)) + `","text":` + jsonString(c) + `,"provenance":"ai","speaker_claim":false,"source_refs":[]}`
	}
	payload := `{"schema_version":1,"detected_language":"en","primary_topic":"Learning","tags":["memory"],
		"summary":{"id":"sum","text":` + jsonString(summary) + `,"provenance":"ai","speaker_claim":false,"source_refs":[]},
		"key_points":[` + keyPoints + `],"important_details":[],"practical_actions":[],
		"diagram":{"type":"mind_map","schema_version":1,"layout_engine":"radial","nodes":[{"id":"root","kind":"root","label":"Root","initially_visible":true,"speaker_claim":false,"source_refs":[]}],"edges":[],"collapsed_groups":[]}}`
	sum := ContentHash(payload)
	return &result{
		fingerprint: sum,
		next:        StageIndexing,
		apply: func(ctx context.Context, tx pgx.Tx, note *NoteRow) error {
			var revisionID, transcriptID uuid.UUID
			if err := tx.QueryRow(ctx, `SELECT id FROM transcripts WHERE note_id = $1`, note.ID).Scan(&transcriptID); err != nil {
				return err
			}
			var number int
			if err := tx.QueryRow(ctx, `SELECT coalesce(max(revision_number), 0) + 1 FROM note_revisions WHERE note_id = $1`, note.ID).Scan(&number); err != nil {
				return err
			}
			var parent *uuid.UUID
			if number > 1 {
				parent = note.ActiveRevisionID
			}
			if err := tx.QueryRow(ctx, `
				INSERT INTO note_revisions (user_id, note_id, revision_number, origin, parent_revision_id, generated_from_transcript_id, payload)
				VALUES ($1, $2, $3, 'generated', $4, $5, $6::jsonb) RETURNING id`, note.UserID, note.ID, number, parent, transcriptID, payload).Scan(&revisionID); err != nil {
				return err
			}
			topicID, err := library.UpsertNamed(ctx, tx, "topics", note.UserID, "Learning")
			if err != nil {
				return err
			}
			if _, err := tx.Exec(ctx, `UPDATE notes SET active_revision_id = $2, primary_topic_id = $3 WHERE id = $1`, note.ID, revisionID, topicID); err != nil {
				return err
			}
			var stored generation.StoredPayload
			if err := json.Unmarshal([]byte(payload), &stored); err != nil {
				return err
			}
			return library.RebuildChildren(ctx, tx, library.NoteHead{ID: note.ID, UserID: note.UserID, Title: deref(note.Title), Channel: deref(note.ChannelName), Payload: &stored, Transcript: &transcriptID})
		},
	}, nil
}

func jsonString(s string) string {
	b := strings.Builder{}
	b.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

func searchSetup(t *testing.T) (*Deps, uuid.UUID, context.Context, *fakeOpenAI) {
	t.Helper()
	fake := &fakeOpenAI{dimension: 1024}
	var ring *keys.Ring
	deps, _, _, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
		d.Hooks.Generating = sectionsFromTranscript
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleNoteGeneration, ai.RoleEmbeddings)
	return deps, userID, ctx, fake
}

func queryVector(t *testing.T, fake *fakeOpenAI, text string) string {
	t.Helper()
	return vectorLiteral(fakeEmbedding(text, fake.dimension))
}

var (
	spacedRecall = seededNote{video: "spacedrecal", title: "Spaced recall, explained", channel: "Learning Lab", channelID: "UClearn", duration: 1120,
		savedAt: time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
		transcript: []string{
			"Spacing reviews out beats cramming because each recall strengthens the memory trace.",
			"Recall after a day, then a week, then a month; effort is the point.",
			"Interleave subjects rather than blocking one topic for hours.",
			"The forgetting curve was measured by Ebbinghaus with nonsense syllables in the nineteenth century.",
		}}
	sourdough = seededNote{video: "sourdoughbk", title: "Sourdough for beginners", channel: "Kitchen Table", channelID: "UCcook", duration: 2400,
		savedAt: time.Date(2026, 9, 5, 10, 0, 0, 0, time.UTC),
		transcript: []string{
			"A sourdough starter is flour and water fermented by wild yeast and bacteria.",
			"Feed the starter daily until it doubles within six hours.",
			"Bake the loaf in a preheated dutch oven for forty minutes with the lid on.",
			"Score the top with a razor so the crust opens where you decide.",
		}}
)

func TestKeywordAndParaphraseBothFindTheNoteAndTranscriptMatchesCarryAnExcerpt(t *testing.T) {
	deps, userID, ctx, fake := searchSetup(t)
	recallID := seedReadyNote(t, deps, userID, spacedRecall)
	breadID := seedReadyNote(t, deps, userID, sourdough)

	// An exact keyword from a key point.
	page, err := search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: "cramming", Vector: queryVector(t, fake, "cramming")})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) == 0 || page.Hits[0].NoteID != recallID {
		t.Fatalf("keyword search: %+v", page.Hits)
	}
	if !contains(page.Hits[0].MatchedKinds, "summary") || len(page.Hits[0].Excerpts) != 0 {
		t.Fatalf("a section match shows no excerpt: %+v", page.Hits[0])
	}

	// A paraphrase with no lexical token in common with the Note's words
	// under the 'simple' configuration, but sharing stems the embedder sees.
	page, err = search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: "spaced recalling strengthens memories", Vector: queryVector(t, fake, "spaced recalling strengthens memories")})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) == 0 || page.Hits[0].NoteID != recallID {
		t.Fatalf("paraphrase search: %+v", page.Hits)
	}
	// The same query without a vector is lexical only and finds nothing.
	lexical, err := search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: "recalling strengthens memories"})
	if err != nil {
		t.Fatal(err)
	}
	if len(lexical.Hits) != 0 {
		t.Fatalf("lexical-only should miss the paraphrase, got %+v", lexical.Hits)
	}

	// A phrase that appears only in a later Transcript chunk: an excerpt
	// under 240 characters, and the bread Note stays out.
	page, err = search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: "Ebbinghaus nonsense syllables", Vector: queryVector(t, fake, "Ebbinghaus nonsense syllables")})
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Hits) == 0 || page.Hits[0].NoteID != recallID {
		t.Fatalf("transcript search: %+v", page.Hits)
	}
	hit := page.Hits[0]
	if len(hit.MatchedKinds) != 1 || hit.MatchedKinds[0] != "transcript_chunk" {
		t.Fatalf("matched kinds %v", hit.MatchedKinds)
	}
	if len(hit.Excerpts) != 1 || len(hit.Excerpts[0].Text) > 240 || !strings.Contains(hit.Excerpts[0].Text, "Ebbinghaus") {
		t.Fatalf("excerpt %+v", hit.Excerpts)
	}
	// The unrelated Note may trail (the semantic list always ranks
	// something) but never leads.
	if page.Hits[0].NoteID == breadID {
		t.Fatal("the unrelated note surfaced first")
	}
}

func TestFiltersComposeBeforeRanking(t *testing.T) {
	deps, userID, ctx, fake := searchSetup(t)
	recallID := seedReadyNote(t, deps, userID, spacedRecall)
	breadID := seedReadyNote(t, deps, userID, sourdough)
	var topicID uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `INSERT INTO topics (user_id, name, normalized_name) VALUES ($1, 'Baking', 'baking') RETURNING id`, userID).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(ctx, `UPDATE notes SET primary_topic_id = $2 WHERE id = $1`, breadID, topicID); err != nil {
		t.Fatal(err)
	}
	// "the" and common words appear in both; the vector is neutral enough
	// that both Notes rank, so the filters decide.
	query := "the"
	vec := queryVector(t, fake, "flour water yeast reviews memory")
	both, err := search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: query, Vector: vec})
	if err != nil || len(both.Hits) != 2 {
		t.Fatalf("unfiltered: %d hits %v", len(both.Hits), err)
	}
	want := func(name string, f search.Filters, id uuid.UUID) {
		t.Helper()
		page, err := search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: query, Vector: vec, Filters: f})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(page.Hits) != 1 || page.Hits[0].NoteID != id {
			t.Fatalf("%s: expected only %s, got %+v", name, id, page.Hits)
		}
	}
	from, to := "2026-09-04", "2026-09-06"
	want("date", search.Filters{From: &from, To: &to}, breadID)
	want("topic", search.Filters{TopicID: &topicID}, breadID)
	learn := "UClearn"
	want("channel", search.Filters{ChannelID: &learn}, recallID)
	short := 1500
	want("duration", search.Filters{MaxDuration: &short}, recallID)
	long := 2000
	want("date+duration", search.Filters{From: &from, MinDuration: &long}, breadID)
	early := "2026-08-01"
	page, err := search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: query, Vector: vec, Filters: search.Filters{From: &early, To: &early}})
	if err != nil || len(page.Hits) != 0 {
		t.Fatalf("a day with no Notes: %+v %v", page.Hits, err)
	}
	if !page.Exhausted || page.NextCursor != "" {
		t.Fatal("an empty page is exhausted")
	}
}

func resetIndexState(t *testing.T, deps *Deps) {
	t.Helper()
	if _, err := deps.Pool.Exec(context.Background(), `
		UPDATE search_index_state SET active_embedding_model = 'text-embedding-3-large', reindexing_into_model = NULL, reindex_started_at = NULL`); err != nil {
		t.Fatal(err)
	}
}

func TestReindexCannotFlipWhileAReadyNoteLacksVectors(t *testing.T) {
	deps, userID, ctx, _ := searchSetup(t)
	resetIndexState(t, deps)
	t.Cleanup(func() { resetIndexState(t, deps) })
	seedReadyNote(t, deps, userID, spacedRecall)
	seedReadyNote(t, deps, userID, sourdough)

	const incoming = "text-embedding-3-small"
	if _, err := search.StartReindex(ctx, deps.Pool, incoming); err != nil {
		t.Fatal(err)
	}
	state, err := search.ReadState(ctx, deps.Pool)
	if err != nil || state.ReindexingInto == nil || *state.ReindexingInto != incoming || state.IncompleteNotes != 2 {
		t.Fatalf("state after start: %+v %v", state, err)
	}
	_, err = search.ActivateReindex(ctx, deps.Pool)
	var incomplete *search.IncompleteError
	if !asIncomplete(err, &incomplete) || incomplete.Notes != 2 {
		t.Fatalf("flip must be refused while notes lack vectors: %v", err)
	}
	if _, err := search.StartReindex(ctx, deps.Pool, "another"); !errors.Is(err, search.ErrReindexInProgress) {
		t.Fatalf("second start: %v", err)
	}

	// Search still reads the active model meanwhile: nothing changed.
	page, err := search.Query(ctx, deps.Pool, search.Request{UserID: userID, Query: "cramming"})
	if err != nil || len(page.Hits) != 1 {
		t.Fatalf("search during reindex: %d hits %v", len(page.Hits), err)
	}

	// The role moves to the incoming model, the job embeds everything, and
	// only then does the flip go through; the old rows are gone after it.
	if _, err := deps.Pool.Exec(ctx, `UPDATE ai_role_configs SET model = $2 WHERE user_id = $1 AND role = 'embeddings'`, userID, incoming); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Client.Insert(ctx, ReindexArgs{Model: incoming}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 40*time.Second, func() bool {
		st, err := search.ReadState(ctx, deps.Pool)
		return err == nil && st.IncompleteNotes == 0
	})
	state, err = search.ActivateReindex(ctx, deps.Pool)
	if err != nil || state.ActiveModel != incoming || state.ReindexingInto != nil {
		t.Fatalf("flip: %+v %v", state, err)
	}
	var oldRows, newRows int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM search_embeddings WHERE embedding_model = 'text-embedding-3-large'`).Scan(&oldRows)
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM search_embeddings WHERE embedding_model = $1`, incoming).Scan(&newRows)
	if oldRows != 0 || newRows == 0 {
		t.Fatalf("after the flip: %d old rows, %d new rows", oldRows, newRows)
	}
	if _, err := search.AbandonReindex(ctx, deps.Pool); !errors.Is(err, search.ErrNoReindexInProgress) {
		t.Fatalf("abandon after flip: %v", err)
	}
	// The uniqueness of the job: a second insert while one is queued is a no-op.
	first, err := deps.Client.Insert(ctx, ReindexArgs{Model: "x"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := deps.Client.Insert(ctx, ReindexArgs{Model: "x"}, nil)
	if err != nil || !second.UniqueSkippedAsDuplicate || first.Job.ID != second.Job.ID {
		t.Fatalf("duplicate reindex jobs: %v %v", second, err)
	}
	_, _ = deps.Client.JobCancel(ctx, first.Job.ID)
}

func TestEmbedSweepGivesEditedChildrenVectors(t *testing.T) {
	deps, userID, ctx, _ := searchSetup(t)
	noteID := seedReadyNote(t, deps, userID, spacedRecall)
	// An edit rebuilds a child with new content; its vector is now stale.
	if _, err := deps.Pool.Exec(ctx, `
		UPDATE search_children SET content = 'Interleaving practice mixes problem types within one session.', content_hash = $2
		 WHERE note_id = $1 AND kind = 'summary'`, noteID, ContentHash("Interleaving practice mixes problem types within one session.")); err != nil {
		t.Fatal(err)
	}
	st, err := search.ReadState(ctx, deps.Pool)
	if err != nil || st.ChildrenNeedingEmbedding != 1 {
		t.Fatalf("needing embedding: %+v %v", st, err)
	}
	if _, err := deps.Client.Insert(ctx, EmbedSweepArgs{}, nil); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		st, err := search.ReadState(ctx, deps.Pool)
		return err == nil && st.ChildrenNeedingEmbedding == 0
	})
	var ledgerRows int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_events WHERE note_id = $1 AND role = 'embeddings' AND outcome = 'billed'`, noteID).Scan(&ledgerRows)
	if ledgerRows < 2 {
		t.Fatalf("the sweep's call should be billed against the note: %d rows", ledgerRows)
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func asIncomplete(err error, target **search.IncompleteError) bool {
	if err == nil {
		return false
	}
	e, ok := err.(*search.IncompleteError) //nolint:errorlint // the service returns it unwrapped
	if ok {
		*target = e
	}
	return ok
}

var _ = river.JobCancel
