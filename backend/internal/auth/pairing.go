package auth

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// Pairing (ADR 0003, ADR 0013): the extension keeps a 256-bit verifier and
// registers its SHA-256; the Owner approves the named browser in the PWA;
// the extension exchanges the verifier for the only copy of a Token
// family. Every failure of the exchange looks the same from outside.

// The refusals of the Pairing operations.
var (
	ErrPairingNotFound    = errors.New("no such pairing request")
	ErrPairingDecided     = errors.New("the pairing request was already decided")
	ErrPairingExpired     = errors.New("the pairing request expired")
	ErrPairingNotApproved = errors.New("the pairing request is not approved yet")
	ErrPairingDenied      = errors.New("the pairing request was denied")
	ErrPairingRejected    = errors.New("the pairing exchange was rejected")
)

// PairingRequest is one request as the approval page sees it.
type PairingRequest struct {
	RequestID   string
	ExtensionID string
	DisplayName string
	Status      string
	CreatedAt   time.Time
	ExpiresAt   time.Time
}

// PairingRequestTTL is the contract's five minutes, enforced by the schema.
const PairingRequestTTL = 5 * time.Minute

// CreatePairingRequest stores the challenge and mints the public request id.
func (s *Service) CreatePairingRequest(ctx context.Context, challenge, extensionID, displayName string) (*PairingRequest, error) {
	digest, err := decode256(challenge)
	if err != nil {
		return nil, fmt.Errorf("challenge: %w", err)
	}
	_, publicID, err := mintToken()
	if err != nil {
		return nil, err
	}
	if len(displayName) > 80 {
		displayName = displayName[:80]
	}
	var out PairingRequest
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO pairing_requests (public_request_id, verifier_challenge, extension_id, display_name)
		VALUES ($1, $2, $3, $4)
		RETURNING public_request_id, extension_id, display_name, created_at, expires_at`,
		publicID, digest, extensionID, displayName).
		Scan(&out.RequestID, &out.ExtensionID, &out.DisplayName, &out.CreatedAt, &out.ExpiresAt); err != nil {
		return nil, fmt.Errorf("create pairing request: %w", err)
	}
	out.Status = "pending"
	return &out, nil
}

// GetPairingRequest reads a request for the approval page, by public id.
func (s *Service) GetPairingRequest(ctx context.Context, requestID string) (*PairingRequest, error) {
	return readPairingRequest(ctx, s.pool, requestID)
}

// rowQuerier is the one method the reads need, satisfied by the pool and a
// transaction alike.
type rowQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

func readPairingRequest(ctx context.Context, q rowQuerier, requestID string) (*PairingRequest, error) {
	var out PairingRequest
	var approved, denied, consumed *time.Time
	err := q.QueryRow(ctx, `
		SELECT public_request_id, extension_id, display_name, created_at, expires_at, approved_at, denied_at, consumed_at
		  FROM pairing_requests WHERE public_request_id = $1`, requestID).
		Scan(&out.RequestID, &out.ExtensionID, &out.DisplayName, &out.CreatedAt, &out.ExpiresAt, &approved, &denied, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPairingNotFound
	}
	if err != nil {
		return nil, err
	}
	switch {
	case consumed != nil:
		out.Status = "consumed"
	case denied != nil:
		out.Status = "denied"
	case time.Now().After(out.ExpiresAt):
		out.Status = "expired"
	case approved != nil:
		out.Status = "approved"
	default:
		out.Status = "pending"
	}
	return &out, nil
}

// DecidePairingRequest approves or denies a pending request as the Owner.
func (s *Service) DecidePairingRequest(ctx context.Context, userID uuid.UUID, requestID string, approve bool, sourceIP string) (*PairingRequest, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	current, err := readPairingRequest(ctx, tx, requestID)
	if err != nil {
		return nil, err
	}
	switch current.Status {
	case "expired":
		return nil, ErrPairingExpired
	case "pending":
	default:
		return nil, ErrPairingDecided
	}
	if approve {
		if _, err := tx.Exec(ctx, `UPDATE pairing_requests SET approved_at = now(), approved_by_user_id = $2 WHERE public_request_id = $1`, requestID, userID); err != nil {
			return nil, err
		}
	} else {
		if _, err := tx.Exec(ctx, `UPDATE pairing_requests SET denied_at = now() WHERE public_request_id = $1`, requestID); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_security_events (event_type, user_id, source_ip, reason_code)
			VALUES ('pairing_denied', $1, nullif($2, '')::inet, 'owner_denied')`, userID, sourceIP); err != nil {
			return nil, err
		}
	}
	decided, err := readPairingRequest(ctx, tx, requestID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return decided, nil
}

// ExchangePairing consumes an approved request for a Token family. The
// request row is locked so a replay and the first exchange cannot both
// succeed; a wrong verifier, an unknown id, a consumed request and an
// extension-id mismatch all answer ErrPairingRejected, and a replay is
// recorded as a security event.
func (s *Service) ExchangePairing(ctx context.Context, requestID, verifier, extensionID, sourceIP string) (Grant, error) {
	raw, err := decode256(verifier)
	if err != nil {
		return Grant{}, ErrPairingRejected
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	var (
		challenge                  []byte
		storedExtension, name      string
		expiresAt                  time.Time
		approved, denied, consumed *time.Time
		approvedBy                 *uuid.UUID
	)
	err = tx.QueryRow(ctx, `
		SELECT verifier_challenge, extension_id, display_name, expires_at, approved_at, approved_by_user_id, denied_at, consumed_at
		  FROM pairing_requests WHERE public_request_id = $1 FOR UPDATE`, requestID).
		Scan(&challenge, &storedExtension, &name, &expiresAt, &approved, &approvedBy, &denied, &consumed)
	if errors.Is(err, pgx.ErrNoRows) {
		return Grant{}, ErrPairingRejected
	}
	if err != nil {
		return Grant{}, err
	}
	digest := sha256.Sum256(raw)
	if subtle.ConstantTimeCompare(digest[:], challenge) != 1 || storedExtension != extensionID {
		return Grant{}, ErrPairingRejected
	}
	if consumed != nil {
		// A second exchange with the right verifier is a replay of a
		// credential that has already been handed out (ADR 0003).
		_, _ = tx.Exec(ctx, `
			INSERT INTO auth_security_events (event_type, user_id, source_ip, reason_code)
			VALUES ('pairing_replay', $1, nullif($2, '')::inet, 'exchange_replayed')`, approvedBy, sourceIP)
		_ = tx.Commit(ctx)
		return Grant{}, ErrPairingRejected
	}
	switch {
	case denied != nil:
		return Grant{}, ErrPairingDenied
	case time.Now().After(expiresAt):
		return Grant{}, ErrPairingExpired
	case approved == nil || approvedBy == nil:
		return Grant{}, ErrPairingNotApproved
	}
	grant, err := s.CreateExtensionSession(ctx, tx, *approvedBy, name, extensionID)
	if err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE pairing_requests SET consumed_at = now(), issued_device_session_id = $2 WHERE public_request_id = $1`, requestID, grant.Session.ID); err != nil {
		return Grant{}, fmt.Errorf("consume pairing request: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

// SessionRow reads one Device session as the sessions list shows it.
func (s *Service) SessionRow(ctx context.Context, sessionID uuid.UUID) (DeviceSession, error) {
	var d DeviceSession
	err := s.pool.QueryRow(ctx, `
		SELECT s.id, s.kind::text, s.display_name, s.extension_id, s.created_at, s.last_seen_at, s.last_refreshed_at, s.inactive_expires_at,
		       (SELECT count(*) FROM push_subscriptions p WHERE p.device_session_id = s.id)
		  FROM device_sessions s WHERE s.id = $1`, sessionID).
		Scan(&d.ID, &d.Kind, &d.DisplayName, &d.ExtensionID, &d.CreatedAt, &d.LastSeenAt, &d.LastRefreshedAt, &d.InactiveExpiresAt, &d.PushSubscriptions)
	return d, err
}

// PurgeExpiredPairingRequests removes requests a day past their expiry
// (ADR 0003 retention).
func (s *Service) PurgeExpiredPairingRequests(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM pairing_requests WHERE expires_at < now() - interval '24 hours'`)
	return err
}

// decode256 accepts the contract's Base64Url256: 43 unpadded characters
// carrying exactly 32 bytes.
func decode256(s string) ([]byte, error) {
	raw, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil || len(raw) != 32 {
		return nil, errors.New("not 32 base64url bytes")
	}
	return raw, nil
}
