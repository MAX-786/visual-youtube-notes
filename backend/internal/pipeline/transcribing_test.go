package pipeline

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// fakeMedia stands in for FFmpeg: a split is N copies of the file.
type fakeMedia struct {
	pieces       int
	pieceSeconds float64
}

func (m *fakeMedia) DurationSeconds(_ context.Context, path string) (float64, error) {
	if strings.HasPrefix(filepath.Base(path), "chunk-") {
		return m.pieceSeconds, nil
	}
	return m.pieceSeconds * float64(m.pieces), nil
}

func (m *fakeMedia) Split(_ context.Context, path, dir string, _, _ time.Duration) ([]string, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []string
	for i := 0; i < m.pieces; i++ {
		p := filepath.Join(dir, "chunk-"+itoa(i)+filepath.Ext(path))
		if err := os.WriteFile(p, src, 0o600); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

// fixtureText joins a transcript fixture's chunks into the spoken text.
func fixtureText(t *testing.T, name string) string {
	t.Helper()
	var f fixtureTranscript
	if err := json.Unmarshal(readFixture(t, name), &f); err != nil {
		t.Fatal(err)
	}
	parts := make([]string, 0, len(f.Chunks))
	for _, c := range f.Chunks {
		parts = append(parts, c.Text)
	}
	return strings.Join(parts, " ")
}

// overlappingPieces cuts text into n pieces that each repeat the last
// `overlap` words of the piece before, the way overlapping audio chunks
// transcribe.
func overlappingPieces(text string, n, overlap int) []string {
	words := strings.Fields(text)
	per := (len(words) + n - 1) / n
	var pieces []string
	for i := 0; i < n; i++ {
		start := max(0, i*per-overlap)
		end := min(len(words), (i+1)*per)
		if start >= len(words) {
			break
		}
		pieces = append(pieces, strings.Join(words[start:end], " "))
	}
	return pieces
}

// quietGenerating stands in for the paid Stages after transcribing when a
// test is about the Transcript alone: it completes its Stage and enqueues
// nothing, so the run stops there without a Revision.
func quietGenerating(_ context.Context, _ *Deps, _ *NoteRow, _ *StageRun) (*result, error) {
	sum := sha256.Sum256([]byte("stub"))
	return &result{fingerprint: sum[:], skip: StageIndexing}, nil
}

// generated waits for the stubbed generating Stage, which follows transcribing.
func generated(t *testing.T, deps *Deps, noteID uuid.UUID) {
	t.Helper()
	waitFor(t, 60*time.Second, func() bool {
		status := stageStatus(t, deps, noteID, StageGenerating)
		return status == "ready" || status == "failed" || noteFailed(t, deps, noteID)
	})
}

func noteFailed(t *testing.T, deps *Deps, noteID uuid.UUID) bool {
	t.Helper()
	state, _ := noteStatus(t, deps.Pool, noteID)
	return state == "failed"
}

func sttSetup(t *testing.T, fake *fakeOpenAI, media *fakeMedia, configure func(*Deps)) (*Deps, *Service, *fakeExtractor, uuid.UUID, context.Context) {
	t.Helper()
	var ring *keys.Ring
	deps, svc, extractor, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
		d.Media = media
		d.MaxUploadBytes = 1 // every test file exceeds it, so every run splits
		if configure != nil {
			configure(d)
		}
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleTranscription, ai.RoleNoteGeneration, ai.RoleEmbeddings)
	extractor.audio = []byte("not really opus, but bytes the fakes accept")
	extractor.info["nocaptions2"] = &VideoInfo{ID: "nocaptions2", Title: "Silent too", Duration: 600, LiveStatus: "not_live", Availability: "public"}
	extractor.info["nocaptions3"] = &VideoInfo{ID: "nocaptions3", Title: "Silent three", Duration: 600, LiveStatus: "not_live", Availability: "public"}
	return deps, svc, extractor, userID, ctx
}

func attemptDirs(t *testing.T, root string) []string {
	t.Helper()
	var dirs []string
	_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err == nil && entry.IsDir() && countSeparators(strings.TrimPrefix(path, root)) == 4 {
			dirs = append(dirs, path)
		}
		return nil
	})
	return dirs
}

// The paid Stages after transcribing are stubbed here: the generation test
// covers them from a seeded Transcript, and the note fixture's Source refs
// are tied to that seeding's chunk boundaries.
func TestSpeechToTextTranscribesSplitAudioIntoOneTranscript(t *testing.T) {
	text := fixtureText(t, "en-neural-network.transcript.json")
	pieces := overlappingPieces(text, 3, 12)
	if merged := MergeTranscriptions(pieces); merged != strings.Join(strings.Fields(text), " ") {
		t.Fatal("the overlap removal does not reconstruct the fixture")
	}
	fake := &fakeOpenAI{transcriptions: pieces}
	deps, svc, _, userID, ctx := sttSetup(t, fake, &fakeMedia{pieces: 3, pieceSeconds: 380}, func(d *Deps) {
		d.Hooks.Generating = quietGenerating
	})

	sub, err := svc.Submit(ctx, userID, "nocaptions1", nil)
	if err != nil {
		t.Fatal(err)
	}
	generated(t, deps, sub.NoteID)
	if state, stage := noteStatus(t, deps.Pool, sub.NoteID); state == "failed" {
		var code, message string
		_ = deps.Pool.QueryRow(ctx, `SELECT coalesce(a.error_code, ''), coalesce(a.error_message, '') FROM stage_attempts a JOIN note_stages s ON s.id = a.stage_id WHERE s.note_id = $1 ORDER BY a.started_at DESC LIMIT 1`, sub.NoteID).Scan(&code, &message)
		t.Fatalf("note is %s at %v: %s %s", state, stage, code, message)
	}

	// The Transcript came from speech to text, whole, in one piece.
	var kind, stored string
	if err := deps.Pool.QueryRow(ctx, `SELECT source_kind::text, normalized_text FROM transcripts WHERE note_id = $1`, sub.NoteID).Scan(&kind, &stored); err != nil {
		t.Fatal(err)
	}
	if kind != "stt" || stored != strings.Join(strings.Fields(text), " ") {
		t.Fatalf("transcript %s: %d chars", kind, len(stored))
	}
	if status := stageStatus(t, deps, sub.NoteID, StageTranscribing); status != "ready" {
		t.Fatalf("transcribing stage %s", status)
	}
	// One ledger row per piece, each billed on the seconds the provider reported.
	var calls int
	var charged string
	if err := deps.Pool.QueryRow(ctx, `SELECT count(*), coalesce(sum(charge_usd), 0)::text FROM ai_usage_events WHERE note_id = $1 AND role = 'transcription' AND outcome = 'billed'`, sub.NoteID).Scan(&calls, &charged); err != nil {
		t.Fatal(err)
	}
	if calls != 3 || charged != "0.0135000000" {
		t.Fatalf("transcription ledger: %d calls, %s USD", calls, charged)
	}
	if dirs := attemptDirs(t, deps.TempDir); len(dirs) != 0 {
		t.Fatalf("temporary media left behind: %v", dirs)
	}
}

func TestSpeechToTextKeepsHindiAndHinglishScript(t *testing.T) {
	hindi := fixtureText(t, "hi-neural-network.transcript.json")
	hinglish := fixtureText(t, "hinglish-python-oops.transcript.json")
	// The fake serves each note's pieces in submission order.
	pieces := append(overlappingPieces(hindi, 2, 10), overlappingPieces(hinglish, 2, 10)...)
	fake := &fakeOpenAI{transcriptions: pieces}
	deps, svc, _, userID, ctx := sttSetup(t, fake, &fakeMedia{pieces: 2, pieceSeconds: 300}, func(d *Deps) {
		d.Hooks.Generating = quietGenerating
	})

	for _, video := range []string{"nocaptions1", "nocaptions2"} {
		sub, err := svc.Submit(ctx, userID, video, nil)
		if err != nil {
			t.Fatal(err)
		}
		generated(t, deps, sub.NoteID)
		var language, stored string
		if err := deps.Pool.QueryRow(ctx, `SELECT language_code, normalized_text FROM transcripts WHERE note_id = $1`, sub.NoteID).Scan(&language, &stored); err != nil {
			t.Fatalf("%s: %v", video, err)
		}
		want := hindi
		if video == "nocaptions2" {
			want = hinglish
		}
		if stored != strings.Join(strings.Fields(want), " ") {
			t.Fatalf("%s: the stored transcript differs from what was spoken", video)
		}
		if language != "hi" || !strings.ContainsRune(stored, 'क') {
			t.Fatalf("%s: language %s, script lost", video, language)
		}
	}
}

func TestCancellationMidTranscriptionRemovesMedia(t *testing.T) {
	fake := &fakeOpenAI{transcriptions: []string{"never delivered"}, sttGate: make(chan struct{}), sttStarted: make(chan struct{}, 4)}
	deps, svc, _, userID, ctx := sttSetup(t, fake, &fakeMedia{pieces: 2, pieceSeconds: 300}, func(d *Deps) {
		d.Hooks.Generating = quietGenerating
	})
	sub, err := svc.Submit(ctx, userID, "nocaptions1", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.sttStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("speech to text never started")
	}
	if dirs := attemptDirs(t, deps.TempDir); len(dirs) != 1 {
		t.Fatalf("expected the attempt's media directory while the call is in flight, got %v", dirs)
	}
	// Delete while the provider call is in flight: the job's context is
	// cancelled, the Stage ends cancelled, and the media is gone.
	if err := svc.Trash(ctx, userID, sub.NoteID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		return stageStatus(t, deps, sub.NoteID, StageTranscribing) == "cancelled"
	})
	waitFor(t, 10*time.Second, func() bool { return len(attemptDirs(t, deps.TempDir)) == 0 })
	var state string
	if err := deps.Pool.QueryRow(ctx, `SELECT processing_state::text FROM notes WHERE id = $1`, sub.NoteID).Scan(&state); err != nil || state != "cancelled" {
		t.Fatalf("note state %q %v", state, err)
	}
	close(fake.sttGate)
}

func TestProviderRejectionFailsTheRunAndRemovesMedia(t *testing.T) {
	fake := &fakeOpenAI{transcriptions: []string{"unused"}, sttStatus: 400}
	deps, svc, _, userID, ctx := sttSetup(t, fake, &fakeMedia{pieces: 2, pieceSeconds: 300}, nil)
	sub, err := svc.Submit(ctx, userID, "nocaptions1", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return noteFailed(t, deps, sub.NoteID) })
	var code string
	if err := deps.Pool.QueryRow(ctx, `SELECT coalesce(a.error_code, '') FROM stage_attempts a JOIN note_stages s ON s.id = a.stage_id WHERE s.note_id = $1 AND s.stage = 'transcribing' ORDER BY a.started_at DESC LIMIT 1`, sub.NoteID).Scan(&code); err != nil || code != "provider_rejected" {
		t.Fatalf("attempt error %q %v", code, err)
	}
	var noCharge int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM ai_usage_events WHERE note_id = $1 AND outcome = 'no_charge'`, sub.NoteID).Scan(&noCharge)
	if noCharge != 1 {
		t.Fatalf("expected the rejected call closed as no_charge, got %d rows", noCharge)
	}
	if dirs := attemptDirs(t, deps.TempDir); len(dirs) != 0 {
		t.Fatalf("temporary media left behind after a failure: %v", dirs)
	}
}

func TestMediaJobsSerializeWhileGeneralJobsRun(t *testing.T) {
	fake := &fakeOpenAI{transcriptions: []string{"spoken words for the first note", "spoken words for the second note"}, sttGate: make(chan struct{}), sttStarted: make(chan struct{}, 8)}
	deps, svc, _, userID, ctx := sttSetup(t, fake, &fakeMedia{pieces: 1, pieceSeconds: 600}, func(d *Deps) {
		d.Hooks.Generating = quietGenerating
	})
	first, err := svc.Submit(ctx, userID, "nocaptions1", nil)
	if err != nil {
		t.Fatal(err)
	}
	second, err := svc.Submit(ctx, userID, "nocaptions2", nil)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-fake.sttStarted:
	case <-time.After(30 * time.Second):
		t.Fatal("speech to text never started")
	}
	// With the media queue held by the first call, a captioned Note still
	// runs through the general queue to ready.
	captioned, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	generated(t, deps, captioned.NoteID)
	if stageStatus(t, deps, captioned.NoteID, StageGenerating) != "ready" {
		t.Fatal("the captioned note did not run beside the media job")
	}
	fake.mu.Lock()
	inFlight, started := fake.sttInFlight, fake.sttCalls
	fake.mu.Unlock()
	if inFlight != 1 || started != 1 {
		t.Fatalf("the second speech-to-text call started beside the first: in flight %d, started %d", inFlight, started)
	}
	close(fake.sttGate)
	for _, id := range []uuid.UUID{first.NoteID, second.NoteID} {
		generated(t, deps, id)
	}
	fake.mu.Lock()
	most := fake.sttMaxInFlight
	fake.mu.Unlock()
	if most != 1 {
		t.Fatalf("speech-to-text calls overlapped: %d in flight at once", most)
	}
	var kinds []string
	rows, err := deps.Pool.Query(ctx, `SELECT source_kind::text FROM transcripts WHERE note_id = ANY($1)`, []uuid.UUID{first.NoteID, second.NoteID})
	if err != nil {
		t.Fatal(err)
	}
	kinds, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil || len(kinds) != 2 {
		t.Fatalf("transcripts %v %v", kinds, err)
	}
}
