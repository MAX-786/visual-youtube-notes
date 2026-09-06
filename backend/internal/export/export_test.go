package export

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/generation"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

// A password hash in the only shape the users table admits; nothing here
// ever verifies it.
const argonHash = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$dGVzdA"

var exportedAt = time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)

var (
	firstSummary  = "Spacing reviews over days beats one long session for durable recall."
	secondSummary = "Spaced reviews beat cramming."
)

type fixture struct {
	pool                *pgxpool.Pool
	userID, otherUserID uuid.UUID
	mindMap, comparison uuid.UUID
	processing          uuid.UUID
	revision1           uuid.UUID
	revision2           uuid.UUID
}

func designPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", "..", "docs", "design"}, parts...)...)
}

func loadDiagram(t *testing.T, name string) generation.StoredDiagram {
	t.Helper()
	raw, err := os.ReadFile(designPath("diagram-examples", "valid", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var d generation.StoredDiagram
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	return d
}

func block(id, text string, claim bool) generation.StoredBlock {
	return generation.StoredBlock{ID: id, Text: text, Provenance: "ai", SpeakerClaim: claim, SourceRefs: []generation.SourceRef{}}
}

func payloadFor(t *testing.T, diagram generation.StoredDiagram, summary string) []byte {
	t.Helper()
	p := generation.StoredPayload{
		SchemaVersion:    1,
		DetectedLanguage: "en",
		PrimaryTopic:     "Learning techniques",
		Tags:             []string{"memory", "study habits"},
		Summary:          block("s1", summary, false),
		KeyPoints: []generation.StoredBlock{
			block("k1", "Recall is stronger when reviews are separated in time.", false),
			block("k2", "Effortful retrieval is what consolidates a memory.", true),
		},
		ImportantDetails: []generation.StoredBlock{block("d1", "The total minutes matter less than their spacing.", false)},
		PracticalActions: []generation.StoredBlock{block("a1", "Review tomorrow, then next Sunday.", false)},
		Diagram:          diagram,
	}
	raw, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

type noteSpec struct {
	video, title, channel string
	duration              int
	published             *time.Time
	savedAt               time.Time
	transcript            []string
}

func seedLibrary(t *testing.T) *fixture {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()
	f := &fixture{pool: pool}
	insertUser := func(username, timezone string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, display_name, timezone, password_hash) VALUES ($1, $2, $3, $4) RETURNING id`,
			username, "Owner", timezone, argonHash).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.userID = insertUser("owner", "Asia/Kolkata")
	// The users table admits exactly one Owner; another owner is an id that
	// does not exist, which the loaders must treat like any stranger.
	f.otherUserID = uuid.New()

	insertNamed := func(table, name string) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `INSERT INTO `+table+` (user_id, name, normalized_name) VALUES ($1, $2, lower($2)) RETURNING id`, f.userID, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	topic := insertNamed("topics", "Learning techniques")
	tagA := insertNamed("tags", "memory")
	tagB := insertNamed("tags", "study habits")

	insertNote := func(n noteSpec) (uuid.UUID, uuid.UUID) {
		var sourceID, noteID, transcriptID uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO sources (user_id, video_id, canonical_url, title, channel_id, channel_name, duration_seconds, published_at, metadata_fetched_at)
			VALUES ($1, $2, 'https://www.youtube.com/watch?v=' || $2, nullif($3, ''), 'UC1', nullif($4, ''), nullif($5, 0), $6, now()) RETURNING id`,
			f.userID, n.video, n.title, n.channel, n.duration, n.published).Scan(&sourceID); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `INSERT INTO notes (user_id, source_id, saved_at) VALUES ($1, $2, $3) RETURNING id`, f.userID, sourceID, n.savedAt).Scan(&noteID); err != nil {
			t.Fatal(err)
		}
		if len(n.transcript) == 0 {
			return noteID, uuid.Nil
		}
		if err := pool.QueryRow(ctx, `
			INSERT INTO transcripts (user_id, note_id, source_kind, language_code, normalized_text)
			VALUES ($1, $2, 'caption_human', 'en', $3) RETURNING id`, f.userID, noteID, strings.Join(n.transcript, " ")).Scan(&transcriptID); err != nil {
			t.Fatal(err)
		}
		offset := 0
		for i, text := range n.transcript {
			length := utf8.RuneCountInString(text)
			if _, err := pool.Exec(ctx, `
				INSERT INTO transcript_chunks (transcript_id, ordinal, char_start, char_end, token_count, chunk_text)
				VALUES ($1, $2, $3, $4, $5, $6)`, transcriptID, i, offset, offset+length, 12, text); err != nil {
				t.Fatal(err)
			}
			offset += length + 1
		}
		return noteID, transcriptID
	}
	insertRevision := func(noteID uuid.UUID, number int, origin string, parent, from *uuid.UUID, payload []byte) uuid.UUID {
		var id uuid.UUID
		if err := pool.QueryRow(ctx, `
			INSERT INTO note_revisions (user_id, note_id, revision_number, origin, parent_revision_id, generated_from_transcript_id, payload)
			VALUES ($1, $2, $3, $4::revision_origin, $5, $6, $7) RETURNING id`, f.userID, noteID, number, origin, parent, from, payload).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	activate := func(noteID, revisionID uuid.UUID) {
		if _, err := pool.Exec(ctx, `UPDATE notes SET processing_state = 'ready', active_revision_id = $2, primary_topic_id = $3 WHERE id = $1`, noteID, revisionID, topic); err != nil {
			t.Fatal(err)
		}
	}

	base := time.Date(2026, 8, 2, 9, 12, 44, 0, time.UTC)
	published := time.Date(2026, 7, 19, 0, 0, 0, 0, time.UTC)
	mindMap := loadDiagram(t, "mind-map")
	var transcriptID uuid.UUID
	f.mindMap, transcriptID = insertNote(noteSpec{
		video: "dQw4w9WgXcQ", title: "How spaced repetition actually works", channel: "Study Lab", duration: 1873, published: &published, savedAt: base,
		transcript: []string{
			"Most people revise by rereading, which feels fluent and teaches very little.",
			"Spacing the same total minutes across several days forces retrieval, and it is the effort of retrieving that consolidates the memory.",
		},
	})
	f.revision1 = insertRevision(f.mindMap, 1, "generated", nil, &transcriptID, payloadFor(t, mindMap, firstSummary))
	f.revision2 = insertRevision(f.mindMap, 2, "manual", &f.revision1, nil, payloadFor(t, mindMap, secondSummary))
	activate(f.mindMap, f.revision2)
	for _, tag := range []uuid.UUID{tagA, tagB} {
		if _, err := pool.Exec(ctx, `INSERT INTO note_tags (note_id, tag_id, user_id) VALUES ($1, $2, $3)`, f.mindMap, tag, f.userID); err != nil {
			t.Fatal(err)
		}
	}
	positions, err := json.Marshal(generation.Layout(&mindMap, nil))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO note_diagram_layouts (note_id, user_id, positions) VALUES ($1, $2, $3)`, f.mindMap, f.userID, positions); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO note_takeaways (note_id, user_id, text, updated_cursor) VALUES ($1, $2, $3, 0)`, f.mindMap, f.userID, "Try this on the statistics deck: one review tomorrow, one next Sunday."); err != nil {
		t.Fatal(err)
	}
	for _, e := range []struct {
		role, charge string
		unpriced     *string
	}{
		{"note_generation", "0.0312", nil},
		{"embeddings", "0.0072", ptr(`{"input_tokens": 500}`)},
	} {
		if _, err := pool.Exec(ctx, `
			INSERT INTO ai_usage_events (user_id, note_id, role, provider, model, billing_mode, started_at, finished_at, billing_month,
			                             estimated_units, reported_units, unpriced_units, charge_usd, outcome)
			VALUES ($1, $2, $3::ai_processing_role, 'openai', 'gpt-5', 'sync', now(), now(), date_trunc('month', now())::date,
			        '{"input_tokens": 1000}', '{"input_tokens": 1000}', $4::jsonb, $5::numeric, 'billed')`,
			f.userID, f.mindMap, e.role, e.unpriced, e.charge); err != nil {
			t.Fatal(err)
		}
	}

	comparison := loadDiagram(t, "comparison")
	var tr2 uuid.UUID
	f.comparison, tr2 = insertNote(noteSpec{video: "aircAruvnKk", title: "Anki vs paper flashcards", channel: "Study Lab", duration: 600, savedAt: base.Add(time.Hour),
		transcript: []string{"Paper cards are cheap and Anki schedules the reviews for you."}})
	rev := insertRevision(f.comparison, 1, "generated", nil, &tr2, payloadFor(t, comparison, "Paper is cheaper; Anki schedules for you."))
	activate(f.comparison, rev)
	if _, err := pool.Exec(ctx, `UPDATE notes SET trashed_at = now(), purge_after = now() + interval '30 days' WHERE id = $1`, f.comparison); err != nil {
		t.Fatal(err)
	}

	f.processing, _ = insertNote(noteSpec{video: "liveliveliv", savedAt: base.Add(2 * time.Hour)})
	return f
}

func ptr[T any](v T) *T { return &v }

func zipEntries(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		_ = rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		out[f.Name] = string(b)
	}
	return out
}

func mustContain(t *testing.T, what, s string, wants ...string) {
	t.Helper()
	for _, w := range wants {
		if !strings.Contains(s, w) {
			t.Fatalf("%s lacks %q:\n%s", what, w, s)
		}
	}
}

func mustNotContain(t *testing.T, what, s string, banned ...string) {
	t.Helper()
	for _, b := range banned {
		if strings.Contains(s, b) {
			t.Fatalf("%s carries %q", what, b)
		}
	}
}

func TestNoteExportsRenderTheChosenRevision(t *testing.T) {
	f := seedLibrary(t)
	ctx := context.Background()

	doc, err := Load(ctx, f.pool, f.userID, f.mindMap, nil, false, exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Revision.ID != f.revision2 || doc.Revision.Number != 2 || doc.Payload.Summary.Text != secondSummary {
		t.Fatalf("the default export is not the Active Revision: %+v", doc.Revision)
	}
	if doc.PrimaryTopic != "Learning techniques" || strings.Join(doc.Tags, ",") != "memory,study habits" || doc.Channel != "Study Lab" {
		t.Fatalf("metadata: topic %q tags %v channel %q", doc.PrimaryTopic, doc.Tags, doc.Channel)
	}
	if doc.Transcript != nil {
		t.Fatal("the Transcript was loaded without being asked for")
	}

	// The SVG: every node boxed, labels as selectable text, hidden members
	// included, nothing outside the static subset.
	svg, ok := DiagramSVG(&doc.Payload.Diagram, doc.Positions, true)
	if !ok {
		t.Fatal("a mind map did not render as SVG")
	}
	mustContain(t, "svg", svg, `<svg xmlns="http://www.w3.org/2000/svg"`, "<text ", "<tspan", "Spaced recall", "After one day", `data-element-id="root"`, "prefers-color-scheme", `font-family:"Noto Sans"`)
	mustNotContain(t, "svg", svg, "foreignObject", "<script", "<filter", "<use", "xlink:href")
	if got, want := strings.Count(svg, "<rect "), len(doc.Payload.Diagram.Nodes); got != want {
		t.Fatalf("%d boxes for %d nodes", got, want)
	}
	// A box the layout never stored is computed the way the server would.
	delete(doc.Positions, "day-one")
	again, _ := DiagramSVG(&doc.Payload.Diagram, doc.Positions, false)
	if strings.Count(again, "<rect ") != len(doc.Payload.Diagram.Nodes) || strings.Contains(again, "prefers-color-scheme") {
		t.Fatal("a missing box was not filled in, or the print variant carries the dark rule")
	}

	// The print document: A4 portrait, the footer from the margin box, the
	// diagram inline, light-only, no appendix unless asked.
	html := string(PrintHTML(doc, false))
	mustContain(t, "print html", html, "@page { size: A4 portrait", "@bottom-center", "counter(page)", "counter(pages)", "<svg", "<h2>Summary</h2>",
		secondSummary, "<h2>Key points</h2>", `<span class="badge">speaker claim</span>`, "<h2>What I can use</h2>", "<h2>My takeaway</h2>", "color-scheme: light only")
	mustNotContain(t, "print html", html, "prefers-color-scheme", `class="transcript"`, "<script")

	withTranscript, err := Load(ctx, f.pool, f.userID, f.mindMap, nil, true, exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if withTranscript.Transcript == nil || len(withTranscript.Transcript.Chunks) != 2 {
		t.Fatalf("transcript: %+v", withTranscript.Transcript)
	}
	html = string(PrintHTML(withTranscript, true))
	mustContain(t, "print html with transcript", html, `<section class="transcript" lang="en">`, "<h2>Transcript</h2>", "Most people revise by rereading", "forces retrieval")

	// The Markdown ZIP: note.md with front matter and both diagram forms,
	// diagram.svg beside it, transcript.md only when asked for.
	var buf bytes.Buffer
	if err := WriteMarkdownZip(&buf, withTranscript, true); err != nil {
		t.Fatal(err)
	}
	entries := zipEntries(t, buf.Bytes())
	for _, name := range []string{"note.md", "diagram.svg", "transcript.md"} {
		if _, ok := entries[name]; !ok {
			t.Fatalf("zip lacks %s: %v", name, keys(entries))
		}
	}
	mustContain(t, "note.md", entries["note.md"],
		"---\ntitle: \"How spaced repetition actually works\"", "channel: \"Study Lab\"", "video_id: \"dQw4w9WgXcQ\"", "duration_seconds: 1873",
		"published_at: 2026-07-19", "primary_topic: \"Learning techniques\"", "tags: [\"memory\", \"study habits\"]", "detected_language: \"en\"",
		"revision_id: "+f.revision2.String(), "revision_number: 2", "exported_at: 2026-09-07T06:00:00Z",
		"# How spaced repetition actually works", "Study Lab · 31 min · Saved 2 Aug 2026 · Published 19 Jul 2026 · [Watch on YouTube](https://www.youtube.com/watch?v=dQw4w9WgXcQ)",
		"## Summary\n\n"+secondSummary, "## Mind map\n\n![Diagram](diagram.svg)\n\n```text\nSpaced recall", "## Key points\n\n- Recall is stronger",
		"*(speaker claim)*", "## What I can use", "## My takeaway\n\nTry this on the statistics deck", "## Transcript\n\nThe transcript is in [transcript.md](transcript.md).")
	mustContain(t, "transcript.md", entries["transcript.md"], "# Transcript: How spaced repetition actually works", "Human captions · language en", "Most people revise by rereading", "forces retrieval")
	mustContain(t, "diagram.svg", entries["diagram.svg"], "<svg xmlns", "Spaced recall")
	if !strings.HasPrefix(entries["diagram.svg"], "<svg") {
		t.Fatal("diagram.svg does not start with the svg element")
	}

	buf.Reset()
	if err := WriteMarkdownZip(&buf, doc, false); err != nil {
		t.Fatal(err)
	}
	entries = zipEntries(t, buf.Bytes())
	if _, ok := entries["transcript.md"]; ok || strings.Contains(entries["note.md"], "## Transcript") {
		t.Fatal("the transcript travelled without being asked for")
	}

	// The revision parameter renders that Revision's own payload.
	first, err := Load(ctx, f.pool, f.userID, f.mindMap, &f.revision1, false, exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if first.Revision.Number != 1 || first.Payload.Summary.Text != firstSummary {
		t.Fatalf("revision 1 rendered %q", first.Payload.Summary.Text)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestComparisonExportsAsATableAndATrashedNoteExports(t *testing.T) {
	f := seedLibrary(t)
	doc, err := Load(context.Background(), f.pool, f.userID, f.comparison, nil, true, exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	if doc.TrashedAt == nil {
		t.Fatal("the comparison note should be in the Trash")
	}
	if _, ok := DiagramSVG(&doc.Payload.Diagram, doc.Positions, true); ok {
		t.Fatal("a comparison rendered as SVG")
	}
	html := string(PrintHTML(doc, false))
	mustContain(t, "print html", html, "<h2>Comparison</h2>", "<table>", `<th scope="col">Criterion</th>`, `<th scope="row"`)
	mustNotContain(t, "print html", html, "<svg")
	var buf bytes.Buffer
	if err := WriteMarkdownZip(&buf, doc, true); err != nil {
		t.Fatal(err)
	}
	entries := zipEntries(t, buf.Bytes())
	if _, ok := entries["diagram.svg"]; ok {
		t.Fatal("a comparison shipped a diagram.svg")
	}
	mustContain(t, "note.md", entries["note.md"], "## Comparison\n\n| Criterion |", "\n|---|", "## Transcript")
	mustNotContain(t, "note.md", entries["note.md"], "![Diagram]")
}

func TestExportRefusesWhatItCannotRender(t *testing.T) {
	f := seedLibrary(t)
	ctx := context.Background()
	for _, tc := range []struct {
		name     string
		user     uuid.UUID
		note     uuid.UUID
		revision *uuid.UUID
		want     error
	}{
		{"processing note", f.userID, f.processing, nil, ErrNoActiveRevision},
		{"another note's revision", f.userID, f.comparison, &f.revision1, ErrRevisionNotOfNote},
		{"another owner's note", f.otherUserID, f.mindMap, nil, ErrNotFound},
		{"no such note", f.userID, uuid.New(), nil, ErrNotFound},
	} {
		if _, err := Load(ctx, f.pool, tc.user, tc.note, tc.revision, false, exportedAt); !errors.Is(err, tc.want) {
			t.Fatalf("%s: %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := LoadTranscript(ctx, f.pool, f.userID, f.processing); !errors.Is(err, ErrNotFound) {
		t.Fatalf("transcript of a note without one: %v", err)
	}
	if _, err := LoadTranscript(ctx, f.pool, f.otherUserID, f.mindMap); !errors.Is(err, ErrNotFound) {
		t.Fatalf("another owner's transcript: %v", err)
	}
	tr, err := LoadTranscript(ctx, f.pool, f.userID, f.mindMap)
	if err != nil {
		t.Fatal(err)
	}
	if tr.SourceKind != "caption_human" || len(tr.Chunks) != 2 || tr.Chunks[1].Ordinal != 1 || tr.Chunks[1].CharStart <= tr.Chunks[0].CharEnd-1 {
		t.Fatalf("transcript shape: %+v", tr)
	}
	if paras := tr.Paragraphs(); len(paras) != 2 || !strings.HasPrefix(paras[1], "Spacing the same") {
		t.Fatalf("paragraphs: %q", paras)
	}
}

func TestGotenbergClientSpeaksTheChromiumRoute(t *testing.T) {
	type call struct {
		path, file, body string
		fields           map[string]string
	}
	var got call
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		file, header, err := r.FormFile("files")
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		data, _ := io.ReadAll(file)
		got = call{path: r.URL.Path, file: header.Filename, body: string(data), fields: map[string]string{}}
		for k, v := range r.MultipartForm.Value {
			got.fields[k] = v[0]
		}
		if strings.Contains(got.body, "fail") {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		_, _ = w.Write([]byte("%PDF-1.7 fake"))
	}))
	g := NewGotenberg(server.URL+"/", server.Client())
	pdf, size, err := g.RenderPDF(context.Background(), []byte("<html>ok</html>"))
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(pdf)
	_ = pdf.Close()
	if string(body) != "%PDF-1.7 fake" || size != int64(len(body)) {
		t.Fatalf("pdf %q size %d", body, size)
	}
	if got.path != "/forms/chromium/convert/html" || got.file != "index.html" || got.body != "<html>ok</html>" {
		t.Fatalf("request: %+v", got)
	}
	for k, v := range map[string]string{"preferCssPageSize": "true", "printBackground": "true", "emulatedMediaType": "print"} {
		if got.fields[k] != v {
			t.Fatalf("field %s = %q, want %q", k, got.fields[k], v)
		}
	}
	if _, _, err := g.RenderPDF(context.Background(), []byte("fail")); !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("a 500 is %v", err)
	}
	server.Close()
	if _, _, err := g.RenderPDF(context.Background(), []byte("<html>ok</html>")); !errors.Is(err, ErrRendererUnavailable) {
		t.Fatalf("an unreachable renderer is %v", err)
	}
}

// TestGotenbergRendersSelectableTextAndAFooter runs against a real Gotenberg
// named by VYN_TEST_GOTENBERG_URL and, when VYN_TEST_PDF_OUT names a file,
// leaves the PDF there for pdftotext to confirm the labels and the footer.
func TestGotenbergRendersSelectableTextAndAFooter(t *testing.T) {
	gotenberg := os.Getenv("VYN_TEST_GOTENBERG_URL")
	if gotenberg == "" {
		t.Skip("VYN_TEST_GOTENBERG_URL not set")
	}
	f := seedLibrary(t)
	doc, err := Load(context.Background(), f.pool, f.userID, f.mindMap, nil, true, exportedAt)
	if err != nil {
		t.Fatal(err)
	}
	pdf, _, err := NewGotenberg(gotenberg, nil).RenderPDF(context.Background(), PrintHTML(doc, true))
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(pdf)
	_ = pdf.Close()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(data, []byte("%PDF-")) || len(data) < 4096 {
		t.Fatalf("not a PDF: %d bytes", len(data))
	}
	if out := os.Getenv("VYN_TEST_PDF_OUT"); out != "" {
		if err := os.WriteFile(out, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func TestArchiveVerifiesWithTheReferenceChecker(t *testing.T) {
	f := seedLibrary(t)
	ctx := context.Background()
	var buf bytes.Buffer
	if err := WriteArchive(ctx, f.pool, &buf, f.userID, exportedAt); err != nil {
		t.Fatal(err)
	}
	raw := buf.Bytes()
	var archive struct {
		ArchiveVersion int            `json:"archive_version"`
		ExportedAt     string         `json:"exported_at"`
		Account        map[string]any `json:"account"`
		Topics         []map[string]any
		Tags           []map[string]any
		Notes          []map[string]any
		Integrity      struct {
			Algorithm        string
			Canonicalization string
			Counts           map[string]int
			Digest           string
		}
	}
	if err := json.Unmarshal(raw, &archive); err != nil {
		t.Fatalf("archive is not JSON: %v", err)
	}
	if archive.ArchiveVersion != 1 || archive.ExportedAt != "2026-09-07T06:00:00Z" || archive.Account["timezone"] != "Asia/Kolkata" || archive.Account["user_id"] != f.userID.String() {
		t.Fatalf("header: %d %s %v", archive.ArchiveVersion, archive.ExportedAt, archive.Account)
	}
	if c := archive.Integrity.Counts; c["topics"] != 1 || c["tags"] != 2 || c["notes"] != 3 || c["revisions"] != 3 {
		t.Fatalf("counts %v", c)
	}
	if archive.Integrity.Algorithm != "sha256" || archive.Integrity.Canonicalization != "RFC8785" || !strings.HasPrefix(archive.Integrity.Digest, "sha256:") {
		t.Fatalf("integrity %+v", archive.Integrity)
	}
	// Notes in Save order; the mind map carries both Revisions verbatim, the
	// cost summary, the Transcript and the takeaway; the processing note has
	// its nulls; the trashed note travels with its trashed_at.
	first := archive.Notes[0]
	revisions := first["revisions"].([]any)
	if first["id"] != f.mindMap.String() || len(revisions) != 2 || first["active_revision_id"] != f.revision2.String() {
		t.Fatalf("first note: %v", first["id"])
	}
	payload := revisions[0].(map[string]any)["payload"].(map[string]any)
	if payload["summary"].(map[string]any)["text"] != firstSummary || payload["diagram"].(map[string]any)["type"] != "mind_map" {
		t.Fatalf("payload is not verbatim: %v", payload["summary"])
	}
	cost := first["ai_cost"].(map[string]any)
	byRole := cost["by_role"].(map[string]any)
	if cost["total_micro_usd"].(float64) != 38400 || byRole["note_generation"].(float64) != 31200 || byRole["embeddings"].(float64) != 7200 || cost["unpriced_events"].(float64) != 1 {
		t.Fatalf("cost %v", cost)
	}
	if first["transcript"].(map[string]any)["source_kind"] != "caption_human" || first["takeaway"].(map[string]any)["text"] == "" || len(first["tag_ids"].([]any)) != 2 {
		t.Fatalf("transcript/takeaway/tags: %v %v %v", first["transcript"], first["takeaway"], first["tag_ids"])
	}
	if trashed := archive.Notes[1]; trashed["trashed_at"] == nil || trashed["purge_after"] == nil {
		t.Fatalf("trashed note: %v", trashed["trashed_at"])
	}
	if processing := archive.Notes[2]; processing["active_revision_id"] != nil || processing["transcript"] != nil || processing["takeaway"] != nil || processing["primary_topic_id"] != nil || processing["processing_state"] != "queued" {
		t.Fatalf("processing note: %v", processing)
	}
	// Nothing that could authenticate travels.
	text := string(raw)
	mustNotContain(t, "archive", text, "argon2id", f.otherUserID.String(), "password", "device_sessions")
	if bytes.Count(raw, []byte("\n")) != 1 {
		t.Fatalf("the archive should be one line and a newline, found %d newlines", bytes.Count(raw, []byte("\n")))
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH; the reference checker cannot run")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "archive.json")
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	verifier, _ := filepath.Abs(designPath("verify-archive.mjs"))
	if out, err := exec.Command(node, verifier, path).CombinedOutput(); err != nil {
		t.Fatalf("verify-archive.mjs refused the archive: %v\n%s", err, out)
	}
	// The schema, through the same Ajv the design tests use.
	root, _ := filepath.Abs(filepath.Join("..", "..", ".."))
	schema, _ := filepath.Abs(designPath("library-archive.schema.json"))
	script := filepath.Join(dir, "validate.mjs")
	if err := os.WriteFile(script, []byte(validateScript), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, script, root, schema, path).CombinedOutput(); err != nil {
		t.Fatalf("the archive does not satisfy library-archive.schema.json: %v\n%s", err, out)
	}
	// A tampered record fails the checker.
	tampered := bytes.Replace(raw, []byte(secondSummary), []byte("Spaced reviews beat cramming!"), 1)
	if bytes.Equal(tampered, raw) {
		t.Fatal("the tamper target was not found")
	}
	if err := os.WriteFile(path, tampered, 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(node, verifier, path).CombinedOutput(); err == nil {
		t.Fatalf("verify-archive.mjs accepted a tampered archive:\n%s", out)
	}
}

const validateScript = `import { createRequire } from "node:module";
import { readFileSync } from "node:fs";
import { pathToFileURL } from "node:url";
const [root, schemaPath, archivePath] = process.argv.slice(2);
const require = createRequire(pathToFileURL(root + "/package.json"));
const Ajv2020 = require("ajv/dist/2020.js");
const ajv = new Ajv2020({ allErrors: true, strict: false });
const validate = ajv.compile(JSON.parse(readFileSync(schemaPath, "utf8")));
if (!validate(JSON.parse(readFileSync(archivePath, "utf8")))) {
  console.error(JSON.stringify(validate.errors, null, 1));
  process.exit(1);
}
console.log("schema ok");
`

func TestCanonicalFormFollowsRFC8785(t *testing.T) {
	var buf bytes.Buffer
	// Keys sort by UTF-16 code unit: a supplementary-plane character sorts
	// before U+FFFF even though its UTF-8 bytes are greater.
	if err := canonical(&buf, map[string]any{"\uffff": 2, "𝄞": 1, "b": []any{true, nil, json.Number("42")}, "a": "x"}, "$"); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), `{"a":"x","b":[true,null,42],"𝄞":1,"`+"\uffff"+`":2}`; got != want {
		t.Fatalf("canonical %s, want %s", got, want)
	}
	buf.Reset()
	// Strings escape only what ECMAScript escapes; U+2028 travels literally.
	if err := canonical(&buf, "a\"b\\c\n\u0001\u2028é", "$"); err != nil {
		t.Fatal(err)
	}
	if got, want := buf.String(), `"a\"b\\c\n\u0001`+"\u2028é\""; got != want {
		t.Fatalf("string %s, want %s", got, want)
	}
	for _, bad := range []any{json.Number("1.5"), json.Number("1e3"), 2.5, map[string]any{"x": []any{json.Number("0.0")}}} {
		buf.Reset()
		if err := canonical(&buf, bad, "$"); err == nil {
			t.Fatalf("%v was accepted; the archive admits no float", bad)
		}
	}
	buf.Reset()
	if err := canonical(&buf, map[string]any{"n": 2.0, "m": int64(7)}, "$"); err != nil || buf.String() != `{"m":7,"n":2}` {
		t.Fatalf("integral numbers: %s %v", buf.String(), err)
	}
}

func TestFileNamesAndDispositions(t *testing.T) {
	if got := FileName(`How: spaced/repetition? "works"`, "pdf"); got != "How spaced repetition works.pdf" {
		t.Fatalf("FileName %q", got)
	}
	if got := FileName("   ", "zip"); got != "note.zip" {
		t.Fatalf("empty title %q", got)
	}
	if got := Attachment("plain name.pdf"); got != `attachment; filename="plain name.pdf"` {
		t.Fatalf("ascii disposition %q", got)
	}
	got := Attachment("नोट्स.pdf")
	const prefix = `attachment; filename="note.pdf"; filename*=UTF-8''`
	if !strings.HasPrefix(got, prefix) {
		t.Fatalf("unicode disposition %q", got)
	}
	if decoded, err := url.PathUnescape(strings.TrimPrefix(got, prefix)); err != nil || decoded != "नोट्स.pdf" {
		t.Fatalf("filename* decodes to %q (%v)", decoded, err)
	}
	if got := Attachment(`Say "hi".zip`); !strings.HasPrefix(got, `attachment; filename="Say _hi_.zip"; filename*=UTF-8''Say%20%22hi%22.zip`) {
		t.Fatalf("quoted disposition %q", got)
	}
}
