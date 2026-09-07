package health

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/push"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

const argonHash = "$argon2id$v=19$m=65536,t=3,p=4$c29tZXNhbHQ$dGVzdA"

// sidecar is a fake backup sidecar: /status answers with snapshot ages the
// test dials, or with an error.
type sidecar struct {
	mu        sync.Mutex
	now       time.Time
	localAge  time.Duration
	offAge    time.Duration
	offsite   bool
	dropTag   string
	lastError string
	broken    bool
}

func (s *sidecar) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		if s.broken {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":"no reading of the local repository yet"}`))
			return
		}
		repo := func(age time.Duration) map[string]any {
			latest := map[string]any{}
			for _, tag := range []string{"db", "globals", "keys"} {
				if tag == s.dropTag {
					continue
				}
				latest[tag] = map[string]any{"time": s.now.Add(-age).Format(time.RFC3339Nano), "snapshot": "ab12cd34"}
			}
			out := map[string]any{"repository": "x", "as_of": s.now.Format(time.RFC3339), "latest": latest, "snapshot_count": 21, "last_error": nil, "last_error_at": nil}
			if s.lastError != "" {
				out["last_error"] = s.lastError
			}
			return out
		}
		doc := map[string]any{"now": s.now.Format(time.RFC3339), "retention": map[string]int{"keep_daily": 7, "keep_weekly": 4},
			"repositories": map[string]any{"local": repo(s.localAge), "offsite": nil}}
		if s.offsite {
			doc["repositories"].(map[string]any)["offsite"] = repo(s.offAge)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(doc)
	})
}

func setup(t *testing.T) (*pgxpool.Pool, *sidecar, *Worker, time.Time) {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	now := time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)
	side := &sidecar{now: now, localAge: time.Hour, offAge: 2 * 24 * time.Hour, offsite: true}
	server := httptest.NewServer(side.handler())
	t.Cleanup(server.Close)
	w := &Worker{
		Pool: pool, Client: server.Client(), StatusURL: server.URL + "/status",
		StaleLocal: 48 * time.Hour, StaleOffsite: 240 * time.Hour,
		DiskMinFreeBytes: 1, DiskMinFreePercent: 0, DiskPaths: []string{t.TempDir()},
		Cadence: 15 * time.Minute, Log: slog.Default(), Now: func() time.Time { return now },
	}
	return pool, side, w, now
}

func rowsByName(t *testing.T, pool *pgxpool.Pool, now time.Time) map[string]Check {
	t.Helper()
	checks, err := Read(context.Background(), pool, now)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Check{}
	for _, c := range checks {
		out[c.Name] = c
	}
	return out
}

func seedOwner(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO users (username, display_name, password_hash) VALUES ('owner', 'Owner', $1) RETURNING id`, argonHash).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedBlockedNote is a Note whose current run failed on extractor breakage,
// which is what notes_blocked_by_tooling selects (ADR 0009).
func seedBlockedNote(t *testing.T, pool *pgxpool.Pool, userID uuid.UUID) uuid.UUID {
	t.Helper()
	ctx := context.Background()
	var sourceID, noteID, stageID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO sources (user_id, video_id, canonical_url) VALUES ($1, 'brokenxtrac', 'https://www.youtube.com/watch?v=brokenxtrac') RETURNING id`, userID).Scan(&sourceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO notes (user_id, source_id, saved_at, processing_state, failure_stage) VALUES ($1, $2, now(), 'failed', 'captions') RETURNING id`, userID, sourceID).Scan(&noteID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO note_stages (user_id, note_id, processing_version, stage, status, started_at, finished_at) VALUES ($1, $2, 1, 'captions', 'failed', now(), now()) RETURNING id`, userID, noteID).Scan(&stageID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO stage_attempts (stage_id, attempt_no, finished_at, error_code, error_message, is_permanent) VALUES ($1, 1, now(), 'extractor_breakage', 'yt-dlp: unable to extract', true)`, stageID); err != nil {
		t.Fatal(err)
	}
	return noteID
}

func TestWorkerReadsBackupsDiskAndTheExtractor(t *testing.T) {
	pool, side, w, now := setup(t)
	ctx := context.Background()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	rows := rowsByName(t, pool, now)
	for _, name := range []string{CheckBackupLocal, CheckBackupOffsite, CheckDiskFree, CheckExtractorBreakage} {
		c, ok := rows[name]
		if !ok || c.Status != StatusOK || c.Cadence != 15*time.Minute || c.LastOkAt == nil || c.FirstAlarmAt != nil {
			t.Fatalf("%s after a healthy pass: %+v", name, c)
		}
	}
	if d := rows[CheckBackupOffsite].Observed["days"]; d != 2.0 {
		t.Fatalf("offsite age %v", d)
	}
	if rows[CheckBackupLocal].Observed["snapshot"] != "ab12cd34" || rows[CheckDiskFree].Observed["gib_free"].(float64) <= 0 {
		t.Fatalf("observed: %v %v", rows[CheckBackupLocal].Observed, rows[CheckDiskFree].Observed)
	}

	// Stale snapshots: local past 48 hours, off-server past ten days, each
	// alarm carrying the age in days the notification copy reads.
	side.mu.Lock()
	side.localAge, side.offAge, side.lastError = 3*24*time.Hour, 11*24*time.Hour, "off-server repository unreachable"
	side.mu.Unlock()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	rows = rowsByName(t, pool, now)
	local, offsite := rows[CheckBackupLocal], rows[CheckBackupOffsite]
	if local.Status != StatusAlarm || local.Observed["days"] != 3.0 || local.FirstAlarmAt == nil || !local.FirstAlarmAt.Equal(now) {
		t.Fatalf("stale local: %+v", local)
	}
	if offsite.Status != StatusAlarm || offsite.Observed["days"] != 11.0 || !strings.Contains(*offsite.Message, "unreachable") {
		t.Fatalf("stale offsite: %+v", offsite)
	}

	// A missing snapshot kind is stale however fresh the others are.
	side.mu.Lock()
	side.localAge, side.dropTag = time.Hour, "keys"
	side.mu.Unlock()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if local := rowsByName(t, pool, now)[CheckBackupLocal]; local.Status != StatusAlarm || !strings.Contains(*local.Message, "no keys snapshot") {
		t.Fatalf("missing keys: %+v", local)
	}

	// An unreadable sidecar counts as stale for both, never as a skipped round.
	side.mu.Lock()
	side.dropTag, side.broken = "", true
	side.mu.Unlock()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	rows = rowsByName(t, pool, now)
	if rows[CheckBackupLocal].Status != StatusAlarm || rows[CheckBackupOffsite].Status != StatusAlarm || !strings.Contains(*rows[CheckBackupLocal].Message, "503") {
		t.Fatalf("broken sidecar: %+v / %+v", rows[CheckBackupLocal], rows[CheckBackupOffsite])
	}

	// No reading of the off-server repository at all is an alarm too.
	side.mu.Lock()
	side.broken, side.offsite = false, false
	side.mu.Unlock()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if offsite := rowsByName(t, pool, now)[CheckBackupOffsite]; offsite.Status != StatusAlarm || !strings.Contains(*offsite.Message, "no reading") {
		t.Fatalf("no offsite reading: %+v", offsite)
	}

	// Disk: an impossible floor alarms, with the free space observed.
	w.DiskMinFreeBytes = 1 << 62
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if disk := rowsByName(t, pool, now)[CheckDiskFree]; disk.Status != StatusAlarm || disk.Observed["gib_free"].(float64) <= 0 || !strings.Contains(*disk.Message, "below the") {
		t.Fatalf("disk alarm: %+v", disk)
	}
	w.DiskMinFreeBytes = 1

	// Extractor breakage: one blocked Note raises the alarm and is counted.
	owner := seedOwner(t, pool)
	seedBlockedNote(t, pool, owner)
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if ext := rowsByName(t, pool, now)[CheckExtractorBreakage]; ext.Status != StatusAlarm || ext.Observed["notes"] != 1.0 || *ext.Message != "1 note is blocked until yt-dlp is updated." {
		t.Fatalf("extractor: %+v", ext)
	}
	if n, err := BlockedByTooling(ctx, pool, owner); err != nil || n != 1 {
		t.Fatalf("blocked count %d %v", n, err)
	}

	// Recovery clears the alarm onset and moves last_ok_at.
	side.mu.Lock()
	side.offsite, side.lastError = true, ""
	side.mu.Unlock()
	later := now.Add(10 * time.Minute)
	w.Now = func() time.Time { return later }
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if local := rowsByName(t, pool, later)[CheckBackupLocal]; local.Status != StatusOK || local.FirstAlarmAt != nil || local.LastOkAt == nil || !local.LastOkAt.Equal(later) {
		t.Fatalf("recovered local: %+v", local)
	}
}

func TestReportComputesUnknownAndAlternatesTheDrill(t *testing.T) {
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()
	owner := seedOwner(t, pool)
	now := time.Date(2026, 9, 7, 6, 0, 0, 0, time.UTC)
	for _, name := range []string{CheckBackupLocal, CheckDiskFree} {
		if err := Upsert(ctx, pool, name, StatusOK, "fine", nil, 15*time.Minute, now); err != nil {
			t.Fatal(err)
		}
	}
	if rows := rowsByName(t, pool, now.Add(30*time.Minute)); rows[CheckBackupLocal].Status != StatusOK {
		t.Fatalf("two cadences later the row should still read ok: %+v", rows[CheckBackupLocal])
	}
	report, err := BuildReport(ctx, pool, owner, now.Add(46*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range report.Checks {
		if c.Status != StatusUnknown {
			t.Fatalf("%s after three cadences: %s", c.Name, c.Status)
		}
	}
	if report.NextDrillRepository != RepositoryLocal || len(report.Drills) != 0 || report.BlockedByTooling != 0 {
		t.Fatalf("empty report: %+v", report)
	}

	first, err := RecordDrill(ctx, pool, Drill{Repository: RepositoryLocal, SnapshotID: "ab12cd34", RestoreSeconds: 754, KeyRecoveryVerified: true, PostgresVersion: "17.6", PgvectorVersion: "0.8.0"}, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := RecordDrill(ctx, pool, Drill{Repository: "laptop", SnapshotID: "x", RestoreSeconds: 1, PostgresVersion: "17", PgvectorVersion: "0.8"}, now); err == nil {
		t.Fatal("an unknown repository was recorded")
	}
	notes := "The laptop needed waking."
	second, err := RecordDrill(ctx, pool, Drill{Repository: RepositoryOffsite, SnapshotID: "ef56ab78", RestoreSeconds: 901, KeyRecoveryVerified: true, PostgresVersion: "17.6", PgvectorVersion: "0.8.0", Notes: &notes}, now.Add(90*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	report, err = BuildReport(ctx, pool, owner, now.Add(91*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Drills) != 2 || report.Drills[0].ID != second.ID || report.Drills[1].ID != first.ID || report.NextDrillRepository != RepositoryLocal || *report.Drills[0].Notes != notes {
		t.Fatalf("drills: %+v next %s", report.Drills, report.NextDrillRepository)
	}
}

// The acceptance walk of ADR 0009: a stopped worker turns every check
// unknown within three cadences and raises worker_stale exactly once.
func TestAStoppedWorkerTurnsUnknownAndRaisesWorkerStaleOnce(t *testing.T) {
	pool, _, w, now := setup(t)
	ctx := context.Background()
	if err := w.Run(ctx); err != nil {
		t.Fatal(err)
	}
	ops := push.NewOps(pool, nil, slog.Default())
	clock := now
	ops.Now = func() time.Time { return clock }
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if rows := rowsByName(t, pool, clock); rows[push.CheckWorkerStale].Status != StatusOK {
		t.Fatalf("a live worker reads as stale: %+v", rows[push.CheckWorkerStale])
	}

	// The worker stops; the API keeps evaluating.
	clock = now.Add(46 * time.Minute)
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	rows := rowsByName(t, pool, clock)
	for _, name := range []string{CheckBackupLocal, CheckBackupOffsite, CheckDiskFree, CheckExtractorBreakage} {
		if rows[name].Status != StatusUnknown {
			t.Fatalf("%s with the worker stopped: %s", name, rows[name].Status)
		}
	}
	stale := rows[push.CheckWorkerStale]
	if stale.Status != StatusAlarm || stale.FirstAlarmAt == nil || !stale.FirstAlarmAt.Equal(clock) || stale.LastNotifiedAt == nil {
		t.Fatalf("worker_stale: %+v", stale)
	}
	notified := *stale.LastNotifiedAt

	clock = now.Add(56 * time.Minute)
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	again := rowsByName(t, pool, clock)[push.CheckWorkerStale]
	if again.Status != StatusAlarm || !again.FirstAlarmAt.Equal(now.Add(46*time.Minute)) || !again.LastNotifiedAt.Equal(notified) {
		t.Fatalf("worker_stale raised again within the day: %+v", again)
	}
}
