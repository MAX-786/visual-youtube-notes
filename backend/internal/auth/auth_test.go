package auth

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/testdb"
)

func TestPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=1$") {
		t.Fatalf("unexpected encoding %s", hash)
	}
	ok, rehash, err := VerifyPassword(hash, "correct horse battery staple")
	if err != nil || !ok || rehash {
		t.Fatalf("verify: ok=%v rehash=%v err=%v", ok, rehash, err)
	}
	ok, _, err = VerifyPassword(hash, "wrong")
	if err != nil || ok {
		t.Fatalf("wrong password verified")
	}
	weak := "$argon2id$v=19$m=1024,t=1,p=1$" + hash[strings.LastIndex(hash[:len(hash)-10], "$")-22:]
	if _, _, err := VerifyPassword(weak, "x"); err != nil {
		// A malformed weak hash is fine to reject; the point is no panic.
		t.Log(err)
	}
}

func testService(t *testing.T) (*Service, context.Context) {
	t.Helper()
	pool := testdb.Pool(t)
	testdb.Reset(t, pool)
	ring, err := keys.Parse(strings.NewReader("1:" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{9}, 32))))
	if err != nil {
		t.Fatal(err)
	}
	svc := New(pool, NewKeyring(ring), audit.New(pool, nil))
	return svc, context.Background()
}

func countEvents(t *testing.T, svc *Service, eventType string) int {
	t.Helper()
	var n int
	if err := svc.pool.QueryRow(context.Background(), `SELECT count(*) FROM auth_security_events WHERE event_type = $1`, eventType).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestOwnerLifecycle(t *testing.T) {
	svc, ctx := testService(t)

	account, err := svc.CreateOwner(ctx, "owner", "The Owner", "", "correct horse battery staple")
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	if _, err := svc.CreateOwner(ctx, "second", "Second", "", "another long password"); !errors.Is(err, ErrOwnerExists) {
		t.Fatalf("second owner: %v", err)
	}
	var roles int
	if err := svc.pool.QueryRow(ctx, `SELECT count(*) FROM ai_role_configs WHERE user_id = $1 AND key_ciphertext IS NULL`, account.ID).Scan(&roles); err != nil || roles != 3 {
		t.Fatalf("seeded roles %d err %v", roles, err)
	}

	if _, err := svc.Login(ctx, "owner", "wrong password here", "", "203.0.113.5"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong password: %v", err)
	}
	if _, err := svc.Login(ctx, "nobody", "wrong password here", "", "203.0.113.5"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("unknown user: %v", err)
	}
	if n := countEvents(t, svc, "login_failed"); n != 2 {
		t.Fatalf("login_failed events %d", n)
	}

	grant, err := svc.Login(ctx, "Owner ", "correct horse battery staple", "Tablet", "203.0.113.5")
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if grant.Session.Kind != "pwa" || grant.AccessToken == "" || grant.RefreshToken == "" || grant.CSRFToken == "" {
		t.Fatalf("grant %+v", grant)
	}

	session := svc.Authenticate(ctx, grant.AccessToken)
	if session == nil || session.ID != grant.Session.ID || session.UserID != account.ID {
		t.Fatalf("authenticate: %+v", session)
	}
	if svc.Authenticate(ctx, "not-a-token") != nil {
		t.Fatal("garbage authenticated")
	}
	csrf, err := CSRFHashOf(grant.CSRFToken)
	if err != nil || !bytes.Equal(csrf, session.CSRFHash) {
		t.Fatal("csrf token does not match the session")
	}

	// Refresh rotates: the old access token dies, the new pair works.
	rotated, err := svc.Refresh(ctx, grant.RefreshToken, "203.0.113.5")
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if rotated.Session.ID != grant.Session.ID {
		t.Fatal("refresh changed the session")
	}
	if svc.Authenticate(ctx, grant.AccessToken) != nil {
		t.Fatal("old access token still works after refresh")
	}
	if svc.Authenticate(ctx, rotated.AccessToken) == nil {
		t.Fatal("new access token does not work")
	}

	// Reuse of the consumed refresh token revokes the whole family.
	if _, err := svc.Refresh(ctx, grant.RefreshToken, "203.0.113.9"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("reuse: %v", err)
	}
	if svc.Authenticate(ctx, rotated.AccessToken) != nil {
		t.Fatal("session survived refresh reuse")
	}
	if _, err := svc.Refresh(ctx, rotated.RefreshToken, "203.0.113.9"); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("the rotated refresh token still works after reuse revocation")
	}
	if n := countEvents(t, svc, "refresh_reuse"); n != 1 {
		t.Fatalf("refresh_reuse events %d", n)
	}
	var reason string
	if err := svc.pool.QueryRow(ctx, `SELECT revocation_reason::text FROM device_sessions WHERE id = $1`, grant.Session.ID).Scan(&reason); err != nil || reason != "refresh_reuse" {
		t.Fatalf("revocation reason %q err %v", reason, err)
	}
}

func TestPasswordChangeKeepsCallerAndRevokesOthers(t *testing.T) {
	svc, ctx := testService(t)
	if _, err := svc.CreateOwner(ctx, "owner", "The Owner", "", "correct horse battery staple"); err != nil {
		t.Fatal(err)
	}
	desktop, err := svc.Login(ctx, "owner", "correct horse battery staple", "Desktop", "")
	if err != nil {
		t.Fatal(err)
	}
	tablet, err := svc.Login(ctx, "owner", "correct horse battery staple", "Tablet", "")
	if err != nil {
		t.Fatal(err)
	}
	sessions, err := svc.ListSessions(ctx, desktop.Session.UserID)
	if err != nil || len(sessions) != 2 {
		t.Fatalf("sessions %d err %v", len(sessions), err)
	}

	if _, err := svc.ChangePassword(ctx, desktop.Session, "not the current one", "a brand new long password", ""); !errors.Is(err, ErrUnauthenticated) {
		t.Fatalf("wrong current password: %v", err)
	}
	changed, err := svc.ChangePassword(ctx, desktop.Session, "correct horse battery staple", "a brand new long password", "")
	if err != nil {
		t.Fatalf("change password: %v", err)
	}
	if svc.Authenticate(ctx, tablet.AccessToken) != nil {
		t.Fatal("the other session survived a password change")
	}
	if svc.Authenticate(ctx, desktop.AccessToken) != nil {
		t.Fatal("the caller's old access token survived the rotation")
	}
	if svc.Authenticate(ctx, changed.AccessToken) == nil {
		t.Fatal("the caller's rotated access token does not work")
	}
	if _, err := svc.Login(ctx, "owner", "a brand new long password", "", ""); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}

	// Sign out all devices, the caller included.
	if err := svc.RevokeAll(ctx, desktop.Session.UserID); err != nil {
		t.Fatal(err)
	}
	if svc.Authenticate(ctx, changed.AccessToken) != nil {
		t.Fatal("revoke-all left the caller signed in")
	}

	// The server CLI reset revokes everything and rejects pending pairings.
	if err := svc.ResetPassword(ctx, "yet another long password"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Login(ctx, "owner", "yet another long password", "", ""); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(t, svc, "password_reset"); n != 1 {
		t.Fatalf("password_reset events %d", n)
	}

	events, err := svc.ListSecurityEvents(ctx, desktop.Session.UserID, 0, 50)
	if err != nil || len(events) == 0 {
		t.Fatalf("events %d err %v", len(events), err)
	}
	if events[0].ID <= events[len(events)-1].ID {
		t.Fatal("events are not newest first")
	}
	_ = uuid.Nil
}
