package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"testing"
)

const testExtension = "fjodmlmkfifedoamachdbgigfpaldicm"

func verifierAndChallenge(t *testing.T) (string, string) {
	t.Helper()
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	return base64.RawURLEncoding.EncodeToString(raw), base64.RawURLEncoding.EncodeToString(sum[:])
}

func pairingOwner(ctx context.Context, t *testing.T, svc *Service) Account {
	t.Helper()
	account, err := svc.CreateOwner(ctx, "owner", "Owner", "", "correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	return account
}

func TestPairingApprovedExchangeIssuesOneTokenFamily(t *testing.T) {
	svc, ctx := testService(t)
	owner := pairingOwner(ctx, t, svc)
	verifier, challenge := verifierAndChallenge(t)

	created, err := svc.CreatePairingRequest(ctx, challenge, testExtension, "Edge")
	if err != nil {
		t.Fatal(err)
	}
	if created.Status != "pending" || len(created.RequestID) != 43 || created.ExpiresAt.Sub(created.CreatedAt).Minutes() != 5 {
		t.Fatalf("created %+v", created)
	}
	// Polling before the decision: not approved, and nothing issued.
	if _, err := svc.ExchangePairing(ctx, created.RequestID, verifier, testExtension, "10.0.0.1"); !errors.Is(err, ErrPairingNotApproved) {
		t.Fatalf("exchange before approval: %v", err)
	}
	// The wrong verifier and the wrong extension are both plain rejections.
	other, _ := verifierAndChallenge(t)
	if _, err := svc.ExchangePairing(ctx, created.RequestID, other, testExtension, ""); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("wrong verifier: %v", err)
	}
	if _, err := svc.ExchangePairing(ctx, created.RequestID, verifier, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", ""); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("wrong extension: %v", err)
	}
	if _, err := svc.ExchangePairing(ctx, "Xk3vT9p1QmR4sZ7wL2nB8cD5fG0hJ6kM1oP3qS5tU7v", verifier, testExtension, ""); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("unknown request: %v", err)
	}

	approved, err := svc.DecidePairingRequest(ctx, owner.ID, created.RequestID, true, "10.0.0.2")
	if err != nil || approved.Status != "approved" {
		t.Fatalf("approve: %+v %v", approved, err)
	}
	if _, err := svc.DecidePairingRequest(ctx, owner.ID, created.RequestID, false, ""); !errors.Is(err, ErrPairingDecided) {
		t.Fatalf("a decided request cannot be decided again: %v", err)
	}
	grant, err := svc.ExchangePairing(ctx, created.RequestID, verifier, testExtension, "10.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if grant.Session.Kind != "extension" || grant.Session.ExtensionID != testExtension || grant.AccessToken == "" || grant.RefreshToken == "" {
		t.Fatalf("grant %+v", grant.Session)
	}
	row, err := svc.SessionRow(ctx, grant.Session.ID)
	if err != nil || row.DisplayName != "Edge" || row.ExtensionID == nil || *row.ExtensionID != testExtension {
		t.Fatalf("session row %+v %v", row, err)
	}
	if state, _ := svc.GetPairingRequest(ctx, created.RequestID); state.Status != "consumed" {
		t.Fatalf("status after exchange %s", state.Status)
	}
	// The access token authenticates; the refresh rotates the family.
	if sess := svc.Authenticate(ctx, grant.AccessToken); sess == nil || sess.ID != grant.Session.ID {
		t.Fatal("the issued access token does not authenticate")
	}
	rotated, err := svc.Refresh(ctx, grant.RefreshToken, "10.0.0.1")
	if err != nil || rotated.Session.ID != grant.Session.ID || rotated.AccessToken == grant.AccessToken {
		t.Fatalf("refresh: %v", err)
	}
	// A replay of the exchange is rejected and recorded.
	if _, err := svc.ExchangePairing(ctx, created.RequestID, verifier, testExtension, "10.0.0.9"); !errors.Is(err, ErrPairingRejected) {
		t.Fatalf("replayed exchange: %v", err)
	}
	if countEvents(t, svc, "pairing_replay") != 1 {
		t.Fatal("the replay was not recorded")
	}
	// Revoking from the PWA drops the extension: its tokens stop working.
	if err := svc.RevokeSession(ctx, owner.ID, grant.Session.ID, "owner_revoked"); err != nil {
		t.Fatal(err)
	}
	if sess := svc.Authenticate(ctx, rotated.AccessToken); sess != nil {
		t.Fatal("a revoked extension session still authenticates")
	}
	if _, err := svc.Refresh(ctx, rotated.RefreshToken, "10.0.0.1"); err == nil {
		t.Fatal("a revoked extension session still refreshes")
	}
}

func TestPairingDeniedAndExpired(t *testing.T) {
	svc, ctx := testService(t)
	owner := pairingOwner(ctx, t, svc)
	verifier, challenge := verifierAndChallenge(t)
	created, err := svc.CreatePairingRequest(ctx, challenge, testExtension, "Brave")
	if err != nil {
		t.Fatal(err)
	}
	denied, err := svc.DecidePairingRequest(ctx, owner.ID, created.RequestID, false, "10.0.0.2")
	if err != nil || denied.Status != "denied" {
		t.Fatalf("deny: %+v %v", denied, err)
	}
	if countEvents(t, svc, "pairing_denied") != 1 {
		t.Fatal("the denial was not recorded")
	}
	if _, err := svc.ExchangePairing(ctx, created.RequestID, verifier, testExtension, ""); !errors.Is(err, ErrPairingDenied) {
		t.Fatalf("exchange after denial: %v", err)
	}

	// Expiry: the schema pins expires_at to created_at + 5 minutes, so the
	// whole window is moved into the past.
	verifier2, challenge2 := verifierAndChallenge(t)
	stale, err := svc.CreatePairingRequest(ctx, challenge2, testExtension, "Chrome")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.pool.Exec(ctx, `UPDATE pairing_requests SET created_at = now() - interval '10 minutes', expires_at = now() - interval '5 minutes' WHERE public_request_id = $1`, stale.RequestID); err != nil {
		t.Fatal(err)
	}
	if state, _ := svc.GetPairingRequest(ctx, stale.RequestID); state.Status != "expired" {
		t.Fatalf("status %s", state.Status)
	}
	if _, err := svc.DecidePairingRequest(ctx, owner.ID, stale.RequestID, true, ""); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("approve after expiry: %v", err)
	}
	if _, err := svc.ExchangePairing(ctx, stale.RequestID, verifier2, testExtension, ""); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("exchange after expiry: %v", err)
	}
	if _, err := svc.CreatePairingRequest(ctx, "not-base64url!", testExtension, "x"); err == nil {
		t.Fatal("a malformed challenge was accepted")
	}
	if _, err := svc.GetPairingRequest(ctx, "Xk3vT9p1QmR4sZ7wL2nB8cD5fG0hJ6kM1oP3qS5tU7v"); !errors.Is(err, ErrPairingNotFound) {
		t.Fatalf("unknown request: %v", err)
	}
}
