package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

// testPEM is what the runbook's openssl line produces: PKCS#8, P-256.
func testPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

func TestKeysDeriveThePublicPoint(t *testing.T) {
	k, err := ParseKeys(testPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(k.Public) != 87 {
		t.Fatalf("public key is %d characters, want 87", len(k.Public))
	}
	point, err := base64.RawURLEncoding.DecodeString(k.Public)
	if err != nil || len(point) != 65 || point[0] != 4 {
		t.Fatalf("public key is not an uncompressed P-256 point: %v", err)
	}
	if _, err := ParseKeys([]byte("not pem")); err == nil {
		t.Fatal("garbage parsed as a key")
	}
	dir := t.TempDir()
	path := dir + "/vapid"
	if err := os.WriteFile(path, testPEM(t), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadKeys(path); err != nil {
		t.Fatal(err)
	}
}

func TestNoteMessagesFollowTheCopy(t *testing.T) {
	id := uuid.MustParse("2f5a1d7e-6c4b-4f0e-9a3c-1b2c3d4e5f60")
	ready := NoteMessage(Ready, id, "Spaced recall, explained", "")
	if ready.Title != "Spaced recall, explained" || ready.Body != "Visual note ready — tap to open." || ready.URL != "/notes/"+id.String() {
		t.Fatalf("ready %+v", ready)
	}
	if ready.Tag != "2f5a1d7e6c4b4f0e9a3c1b2c3d4e5f60" || len(ready.Tag) != 32 {
		t.Fatalf("topic %q", ready.Tag)
	}
	failed := NoteMessage(Failed, id, "Spaced recall, explained", "This video is private.")
	if failed.Body != "Couldn't create notes: This video is private. Open to retry or delete." || failed.Tag != ready.Tag {
		t.Fatalf("failed %+v", failed)
	}
	extractor := NoteMessage(FailedExtractor, id, "Spaced recall, explained", "please report this issue")
	if extractor.Title != "YouTube extraction is blocked" || !strings.Contains(extractor.Body, "The video is fine") {
		t.Fatalf("extractor %+v", extractor)
	}
	held := NoteMessage(Held, id, "Spaced recall, explained", "")
	if held.Title != "Monthly AI cap reached" || held.Body != "Spaced recall, explained is waiting for you to confirm the spend." {
		t.Fatalf("held %+v", held)
	}
	ops := OpsMessage("worker_stale", map[string]any{"minutes": 47})
	if ops.Title != "Processing has stopped" || ops.Body != "No health check in 47 minutes. Nothing is being processed." || ops.Tag != "worker_stale" || ops.URL != "/settings/health" {
		t.Fatalf("ops %+v", ops)
	}
	disk := OpsMessage("disk_free", map[string]any{"gib_free": 1.5})
	if disk.Body != "1.5 GiB free. Clear space before jobs start failing." {
		t.Fatalf("disk %+v", disk)
	}
}

// fakePushService records what a push service would receive and answers
// with the status the test chose.
type fakePushService struct {
	mu       sync.Mutex
	status   int
	requests []*http.Request
}

func (f *fakePushService) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Clone(context.Background()))
		w.WriteHeader(f.status)
	})
}

func owner(t *testing.T) (context.Context, *pgxpool.Pool, uuid.UUID, uuid.UUID) {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ctx := context.Background()
	ring, _ := keys.Parse(strings.NewReader("1:AwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwMDAwM="))
	account, err := auth.New(pool, auth.NewKeyring(ring), audit.New(pool, nil)).CreateOwner(ctx, "owner", "Owner", "", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	var sessionID uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO device_sessions (user_id, kind, display_name, csrf_token_hash) VALUES ($1, 'pwa', 'Tablet', decode(repeat('ab', 32), 'hex')) RETURNING id`, account.ID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	return ctx, pool, account.ID, sessionID
}

// browserKeys is a client key pair in the shape a browser produces.
func browserKeys(t *testing.T) (string, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, _ := key.PublicKey.Bytes()
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	return base64.RawURLEncoding.EncodeToString(point), base64.RawURLEncoding.EncodeToString(secret)
}

func TestSendRecordsDeliveryAndRemovesGoneSubscriptions(t *testing.T) {
	ctx, pool, userID, sessionID := owner(t)
	service := &fakePushService{status: http.StatusCreated}
	server := httptest.NewTLSServer(service.handler())
	defer server.Close()
	k, _ := ParseKeys(testPEM(t))
	sender := NewSender(k, "https://example.test", slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))).WithHTTPClient(server.Client())
	p256dh, secret := browserKeys(t)

	sub, err := Upsert(ctx, pool, userID, sessionID, Write{Endpoint: server.URL + "/push/one", P256dh: p256dh, Auth: secret, NotesEnabled: true, OpsEnabled: false})
	if err != nil {
		t.Fatal(err)
	}
	// Re-registering the same endpoint is the same row.
	again, err := Upsert(ctx, pool, userID, sessionID, Write{Endpoint: server.URL + "/push/one", P256dh: p256dh, Auth: secret, NotesEnabled: true, OpsEnabled: true})
	if err != nil || again.ID != sub.ID || !again.OpsEnabled {
		t.Fatalf("upsert by endpoint: %+v %v", again, err)
	}
	noteID := uuid.New()
	outcome, err := sender.Send(ctx, pool, again, NoteMessage(Ready, noteID, "A title", ""), time.Hour, UrgencyNormal)
	if err != nil || outcome != Delivered {
		t.Fatalf("send: %v %v", outcome, err)
	}
	service.mu.Lock()
	req := service.requests[0]
	service.mu.Unlock()
	if req.Header.Get("Topic") != NoteTopic(noteID) || req.Header.Get("TTL") != "3600" || req.Header.Get("Content-Encoding") != "aes128gcm" || !strings.HasPrefix(req.Header.Get("Authorization"), "vapid ") {
		t.Fatalf("request headers %v", req.Header)
	}
	stored, _ := Get(ctx, pool, userID, sub.ID)
	if stored.LastSuccessAt == nil || stored.ConsecutiveFailures != 0 {
		t.Fatalf("after delivery %+v", stored)
	}

	// A later failed for the same Note collapses onto the same Topic.
	_, _ = sender.Send(ctx, pool, again, NoteMessage(Failed, noteID, "A title", "private"), time.Hour, UrgencyNormal)
	service.mu.Lock()
	sameTopic := service.requests[1].Header.Get("Topic") == service.requests[0].Header.Get("Topic")
	service.mu.Unlock()
	if !sameTopic {
		t.Fatal("failed did not reuse the ready's topic")
	}

	// The category switch is honoured by the fanout.
	if _, err := Update(ctx, pool, userID, sub.ID, boolPtr(false), nil); err != nil {
		t.Fatal(err)
	}
	if n, err := sender.Fanout(ctx, pool, userID, CategoryNotes, NoteMessage(Ready, noteID, "A title", ""), time.Hour, UrgencyNormal); err != nil || n != 0 {
		t.Fatalf("notes switched off still delivered %d (%v)", n, err)
	}
	if n, err := sender.Fanout(ctx, pool, userID, CategoryOps, OpsMessage("disk_free", nil), time.Hour, UrgencyHigh); err != nil || n != 1 {
		t.Fatalf("ops fanout %d %v", n, err)
	}

	// 5xx is transient and counted; 410 removes the row.
	service.mu.Lock()
	service.status = http.StatusServiceUnavailable
	service.mu.Unlock()
	if outcome, err := sender.Send(ctx, pool, again, OpsMessage("disk_free", nil), time.Hour, UrgencyHigh); outcome != Transient || err == nil {
		t.Fatalf("5xx: %v %v", outcome, err)
	}
	stored, _ = Get(ctx, pool, userID, sub.ID)
	if stored.ConsecutiveFailures != 1 || stored.LastFailureAt == nil {
		t.Fatalf("after a transient failure %+v", stored)
	}
	service.mu.Lock()
	service.status = http.StatusGone
	service.mu.Unlock()
	if outcome, _ := sender.Send(ctx, pool, again, OpsMessage("disk_free", nil), time.Hour, UrgencyHigh); outcome != Gone {
		t.Fatalf("410: %v", outcome)
	}
	if _, err := Get(ctx, pool, userID, sub.ID); err == nil {
		t.Fatal("a gone subscription was kept")
	}
}

func TestRevokingTheSessionRemovesItsSubscription(t *testing.T) {
	ctx, pool, userID, sessionID := owner(t)
	p256dh, secret := browserKeys(t)
	if _, err := Upsert(ctx, pool, userID, sessionID, Write{Endpoint: "https://push.example/one", P256dh: p256dh, Auth: secret, NotesEnabled: true, OpsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	// A swap on pushsubscriptionchange replaces the row rather than adding one.
	swapped, err := Upsert(ctx, pool, userID, sessionID, Write{Endpoint: "https://push.example/two", P256dh: p256dh, Auth: secret, NotesEnabled: true, OpsEnabled: true, ReplacesEndpoint: "https://push.example/one"})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := List(ctx, pool, userID)
	if len(list) != 1 || list[0].ID != swapped.ID || list[0].DeviceDisplayName != "Tablet" {
		t.Fatalf("after the swap %+v", list)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM device_sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatal(err)
	}
	list, _ = List(ctx, pool, userID)
	if len(list) != 0 {
		t.Fatal("the revoked session's subscription survived")
	}
	// An extension session cannot hold one.
	var ext uuid.UUID
	if err := pool.QueryRow(ctx, `INSERT INTO device_sessions (user_id, kind, display_name, extension_id, csrf_token_hash) VALUES ($1, 'extension', 'Edge', 'fjodmlmkfifedoamachdbgigfpaldicm', decode(repeat('cd', 32), 'hex')) RETURNING id`, userID).Scan(&ext); err != nil {
		t.Fatal(err)
	}
	if _, err := Upsert(ctx, pool, userID, ext, Write{Endpoint: "https://push.example/three", P256dh: p256dh, Auth: secret}); err == nil {
		t.Fatal("an extension session held a push subscription")
	}
}

func TestOpsAlertsFireOnTransitionAndOnceADay(t *testing.T) {
	ctx, pool, userID, sessionID := owner(t)
	service := &fakePushService{status: http.StatusCreated}
	server := httptest.NewTLSServer(service.handler())
	defer server.Close()
	k, _ := ParseKeys(testPEM(t))
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	sender := NewSender(k, "https://example.test", logger).WithHTTPClient(server.Client())
	p256dh, secret := browserKeys(t)
	if _, err := Upsert(ctx, pool, userID, sessionID, Write{Endpoint: server.URL + "/push/ops", P256dh: p256dh, Auth: secret, NotesEnabled: true, OpsEnabled: true}); err != nil {
		t.Fatal(err)
	}
	// The worker wrote its checks an hour ago at a fifteen-minute cadence.
	if _, err := pool.Exec(ctx, `
		INSERT INTO system_health (check_name, status, message, cadence_seconds, checked_at, last_ok_at)
		VALUES ('backup_local', 'ok', 'fresh', 900, now() - interval '1 hour', now() - interval '1 hour')
		ON CONFLICT (check_name) DO UPDATE SET checked_at = EXCLUDED.checked_at, status = 'ok', first_alarm_at = NULL, last_notified_at = NULL`); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(pool, sender, logger)
	now := time.Now()
	ops.Now = func() time.Time { return now }
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	var firstAlarm, notified *time.Time
	if err := pool.QueryRow(ctx, `SELECT status::text, first_alarm_at, last_notified_at FROM system_health WHERE check_name = 'worker_stale'`).Scan(&status, &firstAlarm, &notified); err != nil {
		t.Fatal(err)
	}
	if status != "alarm" || firstAlarm == nil || notified == nil {
		t.Fatalf("worker_stale after the first pass: %s %v %v", status, firstAlarm, notified)
	}
	sent := func() int {
		service.mu.Lock()
		defer service.mu.Unlock()
		return len(service.requests)
	}
	if sent() != 1 {
		t.Fatalf("expected one alert, got %d", sent())
	}
	service.mu.Lock()
	if topic := service.requests[0].Header.Get("Topic"); topic != "worker_stale" || service.requests[0].Header.Get("Urgency") != "high" {
		t.Fatalf("alert headers %v", service.requests[0].Header)
	}
	service.mu.Unlock()

	// Ten minutes later: still in alarm, nothing new sent.
	ops.Now = func() time.Time { return now.Add(10 * time.Minute) }
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if sent() != 1 {
		t.Fatalf("alert repeated inside a day: %d", sent())
	}
	// A day later it repeats once.
	ops.Now = func() time.Time { return now.Add(25 * time.Hour) }
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if sent() != 2 {
		t.Fatalf("alert did not repeat after a day: %d", sent())
	}
	// The worker comes back: the row recovers and the suppression clears.
	if _, err := pool.Exec(ctx, `UPDATE system_health SET checked_at = $1 WHERE check_name = 'backup_local'`, now.Add(25*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT status::text, first_alarm_at, last_notified_at FROM system_health WHERE check_name = 'worker_stale'`).Scan(&status, &firstAlarm, &notified); err != nil {
		t.Fatal(err)
	}
	if status != "ok" || firstAlarm != nil || notified != nil {
		t.Fatalf("worker_stale after recovery: %s %v %v", status, firstAlarm, notified)
	}
	// Ops switched off: the next alarm sends nothing.
	if _, err := pool.Exec(ctx, `UPDATE push_subscriptions SET ops_enabled = false`); err != nil {
		t.Fatal(err)
	}
	ops.Now = func() time.Time { return now.Add(30 * time.Hour) }
	if err := ops.Evaluate(ctx); err != nil {
		t.Fatal(err)
	}
	if sent() != 2 {
		t.Fatalf("ops alert sent with the switch off: %d", sent())
	}
}

func boolPtr(b bool) *bool { return &b }
