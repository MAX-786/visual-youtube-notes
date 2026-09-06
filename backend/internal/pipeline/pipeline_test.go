package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

// fakeExtractor stands in for yt-dlp: canned metadata per video, canned
// captions, and scripted failures.
type fakeExtractor struct {
	info     map[string]*VideoInfo
	vtt      map[string]string
	failWith map[string]error
	calls    int
	// audio, when set, is what Audio writes into the Attempt directory.
	audio []byte
}

func (f *fakeExtractor) Metadata(_ context.Context, videoID string) (*VideoInfo, error) {
	f.calls++
	if err, ok := f.failWith[videoID]; ok {
		return nil, err
	}
	info, ok := f.info[videoID]
	if !ok {
		return nil, Fail(Permanent, "unavailable", "This video is not available.", nil)
	}
	return info, nil
}

// FetchCaptions serves the canned track; fixtures list the Video ID as the
// track's URL.
func (f *fakeExtractor) FetchCaptions(_ context.Context, format CaptionFormat) ([]byte, error) {
	vtt, ok := f.vtt[strings.TrimPrefix(format.URL, "fake://")]
	if !ok {
		return nil, Fail(Transient, "captions_fetch", "no track", nil)
	}
	return []byte(vtt), nil
}

func (f *fakeExtractor) Audio(_ context.Context, _ string, dir string) (string, error) {
	if f.audio == nil {
		return "", Fail(Permanent, "audio_missing", "no audio in tests", nil)
	}
	path := filepath.Join(dir, "audio.webm")
	if err := os.WriteFile(path, f.audio, 0o600); err != nil {
		return "", err
	}
	return path, nil
}

// captionedVTT is a plausible track: every line distinct, since the 40%
// repeated-content rule would otherwise (correctly) call it degenerate.
func captionedVTT(sentences int) string {
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for i := 0; i < sentences; i++ {
		b.WriteString("00:00:00.000 --> 00:00:01.000\n")
		b.WriteString("Sentence ")
		b.WriteString(strings.Repeat("x", i%5+1))
		b.WriteString(" number ")
		b.WriteString(itoa(i))
		b.WriteString(" explains how neural networks learn weights from data.\n\n")
	}
	return b.String()
}

// testMasterKey is the ring every test boots with.
const testMasterKey = "1:AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM="

func setup(t *testing.T) (*Deps, *Service, *fakeExtractor, uuid.UUID, context.Context) {
	t.Helper()
	return setupWith(t, nil)
}

// setupWith lets a test configure Deps before the workers are registered,
// which is where the paid Stages decide between placeholder and real work.
func setupWith(t *testing.T, configure func(*Deps)) (*Deps, *Service, *fakeExtractor, uuid.UUID, context.Context) {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()

	ring, _ := keys.Parse(strings.NewReader(testMasterKey))
	authSvc := auth.New(pool, auth.NewKeyring(ring), audit.New(pool, nil))
	account, err := authSvc.CreateOwner(ctx, "owner", "Owner", "", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	// The required roles hold a (fake) key, so Submissions are admitted.
	if _, err := pool.Exec(ctx, `UPDATE ai_role_configs SET key_ciphertext = decode(repeat('ab', 40), 'hex'), key_version = 1, key_hint = 'wxyz' WHERE user_id = $1`, account.ID); err != nil {
		t.Fatal(err)
	}

	fake := &fakeExtractor{
		info: map[string]*VideoInfo{
			"aircAruvnKk": {ID: "aircAruvnKk", Title: "But what is a neural network?", ChannelID: "UCx", Channel: "3Blue1Brown", Duration: 1140, UploadDate: "20171005", LiveStatus: "not_live", Availability: "public",
				Subtitles: map[string][]CaptionFormat{"en": {{Ext: "vtt", URL: "fake://aircAruvnKk"}}}},
			"liveliveliv": {ID: "liveliveliv", Title: "Live", Duration: 0, LiveStatus: "is_live", Availability: "public"},
			"nocaptions1": {ID: "nocaptions1", Title: "Silent", Duration: 600, LiveStatus: "not_live", Availability: "public"},
		},
		vtt: map[string]string{"aircAruvnKk": captionedVTT(300)},
		failWith: map[string]error{
			"brokenxtrac": Fail(ExtractorBreakage, "extractor_breakage", "The video is fine; the extraction tool needs updating.", errors.New("please report this issue")),
		},
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	deps := &Deps{Pool: pool, Extractor: fake, TempDir: t.TempDir(), Logger: logger}
	if configure != nil {
		configure(deps)
	}
	workers := river.NewWorkers()
	RegisterWorkers(workers, deps)
	client, err := river.NewClient(riverpgxv5.New(pool), &river.Config{
		Queues:            map[string]river.QueueConfig{QueueDefault: {MaxWorkers: 2}, QueueMedia: {MaxWorkers: 1}},
		Workers:           workers,
		Logger:            logger,
		FetchCooldown:     50 * time.Millisecond,
		FetchPollInterval: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	deps.Client = client
	if err := client.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		stopCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Stop(stopCtx)
	})
	return deps, NewService(pool, client, logger), fake, account.ID, ctx
}

func waitFor(t *testing.T, timeout time.Duration, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("condition not met in time")
}

func stageStatus(t *testing.T, d *Deps, noteID uuid.UUID, stage string) string {
	t.Helper()
	var status string
	err := d.Pool.QueryRow(context.Background(), `SELECT status::text FROM note_stages WHERE note_id = $1 AND stage = $2 ORDER BY processing_version DESC LIMIT 1`, noteID, stage).Scan(&status)
	if err != nil {
		return "missing"
	}
	return status
}

func noteState(t *testing.T, d *Deps, noteID uuid.UUID) string {
	t.Helper()
	var state string
	if err := d.Pool.QueryRow(context.Background(), `SELECT processing_state::text FROM notes WHERE id = $1`, noteID).Scan(&state); err != nil {
		return "missing"
	}
	return state
}

func TestSubmissionRunsThroughCaptions(t *testing.T) {
	deps, svc, _, userID, ctx := setup(t)

	sub, err := svc.Submit(ctx, userID, "https://youtu.be/aircAruvnKk?si=x", nil)
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if sub.Outcome != OutcomeCreated || sub.UndoUntil.Before(time.Now()) {
		t.Fatalf("submission %+v", sub)
	}
	// A duplicate opens the existing Note and creates nothing.
	dup, err := svc.Submit(ctx, userID, "https://www.youtube.com/watch?v=aircAruvnKk", nil)
	if err != nil || dup.Outcome != OutcomeDuplicate || dup.NoteID != sub.NoteID {
		t.Fatalf("duplicate: %+v %v", dup, err)
	}
	var count int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM notes`).Scan(&count)
	if count != 1 {
		t.Fatalf("%d notes", count)
	}

	// The scheduled first Stage waits out the Undo window, then the chain
	// runs metadata and captions and stops at the (placeholder) generating
	// Stage, which fails permanently in this build.
	waitFor(t, 30*time.Second, func() bool { return stageStatus(t, deps, sub.NoteID, StageCaptions) == "ready" })
	if stageStatus(t, deps, sub.NoteID, StageMetadata) != "ready" {
		t.Fatal("metadata not ready")
	}
	if stageStatus(t, deps, sub.NoteID, StageTranscribing) != "skipped" {
		t.Fatalf("transcribing should be skipped, got %s", stageStatus(t, deps, sub.NoteID, StageTranscribing))
	}
	var title string
	var duration int
	if err := deps.Pool.QueryRow(ctx, `SELECT s.title, s.duration_seconds FROM sources s JOIN notes n ON n.source_id = s.id WHERE n.id = $1`, sub.NoteID).Scan(&title, &duration); err != nil || title == "" || duration != 1140 {
		t.Fatalf("source metadata %q %d %v", title, duration, err)
	}
	var chunks int
	var lang, kind string
	if err := deps.Pool.QueryRow(ctx, `SELECT count(c.*), t.language_code, t.source_kind::text FROM transcripts t JOIN transcript_chunks c ON c.transcript_id = t.id WHERE t.note_id = $1 GROUP BY t.id`, sub.NoteID).Scan(&chunks, &lang, &kind); err != nil {
		t.Fatalf("transcript: %v", err)
	}
	if chunks < 3 || lang != "en" || kind != "caption_human" {
		t.Fatalf("chunks %d lang %s kind %s", chunks, lang, kind)
	}
	waitFor(t, 20*time.Second, func() bool { return noteState(t, deps, sub.NoteID) == "failed" })
	var failureStage string
	_ = deps.Pool.QueryRow(ctx, `SELECT failure_stage FROM notes WHERE id = $1`, sub.NoteID).Scan(&failureStage)
	if failureStage != StageGenerating {
		t.Fatalf("failure stage %q", failureStage)
	}
	var events int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM sync_events WHERE note_id IS NULL AND entity_id = $1`, sub.NoteID).Scan(&events)
	if events < 2 {
		t.Fatalf("expected a created and a terminal sync event, got %d", events)
	}
}

func TestUndoBeforeWorkHardDeletes(t *testing.T) {
	deps, svc, _, userID, ctx := setup(t)
	sub, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	result, err := svc.Undo(ctx, userID, sub.NoteID)
	if err != nil || result != Undone {
		t.Fatalf("undo: %v %v", result, err)
	}
	if noteState(t, deps, sub.NoteID) != "missing" {
		t.Fatal("note survived an undo inside the window")
	}
	// Undone means a fresh submission is a new Note, not a duplicate.
	again, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil || again.Outcome != OutcomeCreated || again.NoteID == sub.NoteID {
		t.Fatalf("resubmit after undo: %+v %v", again, err)
	}
}

func TestIneligibleAndBrokenVideosFailWithReasons(t *testing.T) {
	deps, svc, _, userID, ctx := setup(t)
	live, err := svc.Submit(ctx, userID, "https://www.youtube.com/watch?v=liveliveliv", nil)
	if err != nil {
		t.Fatal(err)
	}
	broken, err := svc.Submit(ctx, userID, "https://www.youtube.com/watch?v=brokenxtrac", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		return noteState(t, deps, live.NoteID) == "failed" && noteState(t, deps, broken.NoteID) == "failed"
	})
	var code string
	var permanent bool
	if err := deps.Pool.QueryRow(ctx, `
		SELECT a.error_code, a.is_permanent FROM stage_attempts a JOIN note_stages s ON s.id = a.stage_id
		 WHERE s.note_id = $1 AND s.stage = 'metadata' ORDER BY a.attempt_no DESC LIMIT 1`, live.NoteID).Scan(&code, &permanent); err != nil {
		t.Fatal(err)
	}
	if code != "live_now" || !permanent {
		t.Fatalf("live stream: %s permanent=%v", code, permanent)
	}
	var blocked int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM notes_blocked_by_tooling WHERE note_id = $1`, broken.NoteID).Scan(&blocked)
	if blocked != 1 {
		t.Fatal("extractor breakage is not visible in notes_blocked_by_tooling")
	}
	// Retries were not spent on a permanent failure.
	var attempts int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM stage_attempts a JOIN note_stages s ON s.id = a.stage_id WHERE s.note_id = $1`, live.NoteID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("a permanent failure took %d attempts", attempts)
	}
}

func TestNoCaptionsHandsOffToTranscribing(t *testing.T) {
	deps, svc, _, userID, ctx := setup(t)
	sub, err := svc.Submit(ctx, userID, "nocaptions1", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return noteState(t, deps, sub.NoteID) == "failed" })
	if stageStatus(t, deps, sub.NoteID, StageCaptions) != "ready" {
		t.Fatal("captions stage should complete with no track")
	}
	var failureStage string
	_ = deps.Pool.QueryRow(ctx, `SELECT failure_stage FROM notes WHERE id = $1`, sub.NoteID).Scan(&failureStage)
	if failureStage != StageTranscribing {
		t.Fatalf("expected the placeholder transcribing Stage to fail, got %q", failureStage)
	}
}
