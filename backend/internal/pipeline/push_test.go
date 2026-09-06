package pipeline

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/cost"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/push"
)

// pushService is the fake push service: it records every delivery.
type pushService struct {
	mu       sync.Mutex
	requests []*http.Request
}

func (p *pushService) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.mu.Lock()
		p.requests = append(p.requests, r.Clone(context.Background()))
		p.mu.Unlock()
		w.WriteHeader(http.StatusCreated)
	})
}

func (p *pushService) topics() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.requests))
	for _, r := range p.requests {
		out = append(out, r.Header.Get("Topic"))
	}
	return out
}

func vapidPEM(t *testing.T) []byte {
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

// pushSetup is the search harness with a sender pointed at the fake push
// service and one tablet subscription for the Owner.
func pushSetup(t *testing.T) (*Deps, *Service, *fakeExtractor, uuid.UUID, context.Context, *pushService) {
	t.Helper()
	service := &pushService{}
	server := httptest.NewTLSServer(service.handler())
	t.Cleanup(server.Close)
	k, err := push.ParseKeys(vapidPEM(t))
	if err != nil {
		t.Fatal(err)
	}
	fake := &fakeOpenAI{dimension: 1024}
	var ring *keys.Ring
	deps, svc, extractor, userID, ctx := setupWith(t, func(d *Deps) {
		ring, _ = keys.Parse(strings.NewReader(testMasterKey))
		d.AI = ai.NewStore(d.Pool, ring)
		d.AITransport = fake
		d.Hooks.BeforePaidStage = CostGate
		d.Hooks.Generating = sectionsFromTranscript
		d.Push = push.NewSender(k, "https://example.test", d.Logger).WithHTTPClient(server.Client())
	})
	sealKeys(t, deps.Pool, ring, userID, ai.RoleNoteGeneration, ai.RoleEmbeddings, ai.RoleTranscription)
	var sessionID uuid.UUID
	if err := deps.Pool.QueryRow(ctx, `INSERT INTO device_sessions (user_id, kind, display_name, csrf_token_hash) VALUES ($1, 'pwa', 'Tablet', decode(repeat('ab', 32), 'hex')) RETURNING id`, userID).Scan(&sessionID); err != nil {
		t.Fatal(err)
	}
	client, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	point, _ := client.PublicKey.Bytes()
	secret := make([]byte, 16)
	_, _ = rand.Read(secret)
	if _, err := push.Upsert(ctx, deps.Pool, userID, sessionID, push.Write{
		Endpoint: server.URL + "/tablet", P256dh: base64.RawURLEncoding.EncodeToString(point), Auth: base64.RawURLEncoding.EncodeToString(secret),
		NotesEnabled: true, OpsEnabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	return deps, svc, extractor, userID, ctx, service
}

func TestReadyFailedAndHeldNotifyTheTabletAndNothingElseDoes(t *testing.T) {
	deps, svc, _, userID, ctx, service := pushSetup(t)

	// Ready: the captioned Note runs to the end and the tablet is told once,
	// under the Note's own topic. Intermediate Stages send nothing.
	sub, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, sub.NoteID)
		return state == "ready" || state == "failed"
	})
	waitFor(t, 20*time.Second, func() bool { return len(service.topics()) == 1 })
	if topics := service.topics(); topics[0] != push.NoteTopic(sub.NoteID) {
		t.Fatalf("ready topic %v", topics)
	}
	var events []string
	rows, _ := deps.Pool.Query(ctx, `SELECT args->>'event' FROM river_job WHERE kind = 'push_note' AND args->>'note_id' = $1 ORDER BY id`, sub.NoteID.String())
	for rows.Next() {
		var e string
		_ = rows.Scan(&e)
		events = append(events, e)
	}
	rows.Close()
	if strings.Join(events, ",") != "ready" {
		t.Fatalf("push jobs for the ready note: %v", events)
	}

	// Failed: a permanent input failure notifies with the failed copy;
	// extractor breakage notifies with the tool's fault, not the video's.
	private, err := svc.Submit(ctx, userID, "liveliveliv", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return noteFailed(t, deps, private.NoteID) })
	broken, err := svc.Submit(ctx, userID, "brokenxtrac", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool { return noteFailed(t, deps, broken.NoteID) })
	waitFor(t, 20*time.Second, func() bool { return len(service.topics()) == 3 })
	var kinds = map[string]string{}
	rows, _ = deps.Pool.Query(ctx, `SELECT args->>'note_id', args->>'event' FROM river_job WHERE kind = 'push_note'`)
	for rows.Next() {
		var id, e string
		_ = rows.Scan(&id, &e)
		kinds[id] = e
	}
	rows.Close()
	if kinds[private.NoteID.String()] != "failed" || kinds[broken.NoteID.String()] != "failed_extractor" {
		t.Fatalf("failure events %v", kinds)
	}

	// Held: a run stopped by the cap tells the tablet to confirm the spend.
	if _, err := cost.SetSoftCap(ctx, deps.Pool, userID, "0"); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(ctx, `DELETE FROM notes WHERE id = $1`, sub.NoteID); err != nil {
		t.Fatal(err)
	}
	held, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, func() bool {
		var since *time.Time
		_ = deps.Pool.QueryRow(ctx, `SELECT cost_hold_since FROM notes WHERE id = $1`, held.NoteID).Scan(&since)
		return since != nil
	})
	waitFor(t, 20*time.Second, func() bool { return len(service.topics()) == 4 })
	var heldEvent string
	_ = deps.Pool.QueryRow(ctx, `SELECT args->>'event' FROM river_job WHERE kind = 'push_note' AND args->>'note_id' = $1`, held.NoteID.String()).Scan(&heldEvent)
	if heldEvent != "held" {
		t.Fatalf("held event %q", heldEvent)
	}

	// Cancelled sends nothing: a Delete mid-run ends without a push job.
	if _, err := cost.SetSoftCap(ctx, deps.Pool, userID, "100"); err != nil {
		t.Fatal(err)
	}
	if _, err := deps.Pool.Exec(ctx, `DELETE FROM notes WHERE id = $1`, held.NoteID); err != nil {
		t.Fatal(err)
	}
	cancelled, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Undo(ctx, userID, cancelled.NoteID); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	var cancelledJobs int
	_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'push_note' AND args->>'note_id' = $1`, cancelled.NoteID.String()).Scan(&cancelledJobs)
	if cancelledJobs != 0 {
		t.Fatalf("a cancelled run queued %d push jobs", cancelledJobs)
	}
	if len(service.topics()) != 4 {
		t.Fatalf("deliveries: %v", service.topics())
	}
}

func TestNotesSwitchOffStopsNoteNotifications(t *testing.T) {
	deps, svc, _, userID, ctx, service := pushSetup(t)
	if _, err := deps.Pool.Exec(ctx, `UPDATE push_subscriptions SET notes_enabled = false`); err != nil {
		t.Fatal(err)
	}
	sub, err := svc.Submit(ctx, userID, "aircAruvnKk", nil)
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 40*time.Second, func() bool {
		state, _ := noteStatus(t, deps.Pool, sub.NoteID)
		return state == "ready" || state == "failed"
	})
	waitFor(t, 10*time.Second, func() bool {
		var done int
		_ = deps.Pool.QueryRow(ctx, `SELECT count(*) FROM river_job WHERE kind = 'push_note' AND state = 'completed'`).Scan(&done)
		return done == 1
	})
	if len(service.topics()) != 0 {
		t.Fatalf("note notification sent with the switch off: %v", service.topics())
	}
}
