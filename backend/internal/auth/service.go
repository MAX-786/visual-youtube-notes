package auth

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
)

// ErrUnauthenticated is every credential failure. It is deliberately one
// error: invalid, expired, revoked and reused credentials look the same from
// outside, and the difference is written to Security events instead.
var ErrUnauthenticated = errors.New("unauthenticated")

// ErrOwnerExists is returned by CreateOwner when the single Owner already
// exists.
var ErrOwnerExists = errors.New("owner already exists")

// ErrNoOwner is returned when no Owner has been created yet.
var ErrNoOwner = errors.New("no owner exists")

// Service is the authentication use cases over the pool.
type Service struct {
	pool   *pgxpool.Pool
	keys   *Keyring
	audit  *audit.Recorder
	now    func() time.Time
	dummy  string
	argon2 func(password string) (string, error)
}

// New builds the service.
func New(pool *pgxpool.Pool, ring *Keyring, recorder *audit.Recorder) *Service {
	return &Service{pool: pool, keys: ring, audit: recorder, now: time.Now, dummy: dummyHash, argon2: HashPassword}
}

// Session is an authenticated Device session with what the request layer
// needs from it.
type Session struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Kind            string
	ExtensionID     string
	CSRFHash        []byte
	AccessExpiresAt time.Time
}

// Grant is what a login, refresh or pairing exchange hands out: the only copy
// of the raw credentials.
type Grant struct {
	Session         Session
	AccessToken     string
	RefreshToken    string
	CSRFToken       string
	AccessExpiresAt time.Time
}

// Account is the Owner row without its password hash.
type Account struct {
	ID                uuid.UUID
	Username          string
	DisplayName       string
	Timezone          string
	CreatedAt         time.Time
	PasswordChangedAt time.Time
}

// DeviceSession is one row of the sessions list.
type DeviceSession struct {
	ID                uuid.UUID
	Kind              string
	DisplayName       string
	ExtensionID       *string
	CreatedAt         time.Time
	LastSeenAt        time.Time
	LastRefreshedAt   time.Time
	InactiveExpiresAt time.Time
	PushSubscriptions int
}

// SecurityEvent is one audit row.
type SecurityEvent struct {
	ID              int64
	Type            string
	DeviceSessionID *uuid.UUID
	SourceIP        *string
	ReasonCode      *string
	ExtensionID     *string
	OccurredAt      time.Time
}

// CreateOwner seeds the single Owner (ADR 0003) and copies the recommended
// role defaults into their configuration with no key (ADR 0010).
func (s *Service) CreateOwner(ctx context.Context, username, displayName, timezone, password string) (Account, error) {
	if strings.TrimSpace(username) == "" || strings.TrimSpace(displayName) == "" {
		return Account{}, errors.New("username and display name are required")
	}
	if err := checkPassword(password); err != nil {
		return Account{}, err
	}
	hash, err := s.argon2(password)
	if err != nil {
		return Account{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Account{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var exists bool
	if err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM users)`).Scan(&exists); err != nil {
		return Account{}, err
	}
	if exists {
		return Account{}, ErrOwnerExists
	}
	if timezone == "" {
		timezone = "Asia/Calcutta"
	}
	var account Account
	err = tx.QueryRow(ctx, `
		INSERT INTO users (username, display_name, timezone, password_hash)
		VALUES ($1, $2, $3, $4)
		RETURNING id, username, display_name, timezone, created_at, password_changed_at`,
		strings.TrimSpace(username), strings.TrimSpace(displayName), timezone, hash,
	).Scan(&account.ID, &account.Username, &account.DisplayName, &account.Timezone, &account.CreatedAt, &account.PasswordChangedAt)
	if err != nil {
		return Account{}, fmt.Errorf("insert owner: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ai_role_configs (user_id, role, provider, model, request_options)
		SELECT $1, role, provider, model, request_options FROM ai_role_defaults`, account.ID); err != nil {
		return Account{}, fmt.Errorf("seed role configuration: %w", err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO ai_cost_settings (user_id) VALUES ($1)`, account.ID); err != nil {
		return Account{}, fmt.Errorf("seed cost settings: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Account{}, err
	}
	return account, nil
}

// ResetPassword replaces the sole Owner's password from the server CLI. There
// is no initiating session, so every Device session is revoked and every
// unconsumed Pairing request is rejected (ADR 0003).
func (s *Service) ResetPassword(ctx context.Context, password string) error {
	if err := checkPassword(password); err != nil {
		return err
	}
	hash, err := s.argon2(password)
	if err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var userID uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT id FROM users LIMIT 1`).Scan(&userID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNoOwner
		}
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, password_changed_at = now() WHERE id = $1`, userID, hash); err != nil {
		return err
	}
	if err := revokeSessionsTx(ctx, tx, userID, nil, "password_reset"); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE pairing_requests SET denied_at = now()
		 WHERE consumed_at IS NULL AND denied_at IS NULL AND expires_at > now()`); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_security_events (event_type, user_id, reason_code) VALUES ('password_reset', $1, 'server_cli')`, userID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// Login verifies the password and creates a fresh PWA Device session. A
// missing Owner costs the same time as a wrong password.
func (s *Service) Login(ctx context.Context, username, password, deviceName, sourceIP string) (Grant, error) {
	var userID uuid.UUID
	var hash string
	err := s.pool.QueryRow(ctx, `SELECT id, password_hash FROM users WHERE lower(btrim(username)) = lower(btrim($1))`, username).Scan(&userID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		hash = s.dummy
	} else if err != nil {
		return Grant{}, err
	}
	ok, needsRehash, verr := VerifyPassword(hash, password)
	if verr != nil || !ok || userID == uuid.Nil {
		s.audit.Record(ctx, audit.Event{Type: audit.LoginFailed, SourceIP: sourceIP, ReasonCode: "bad_credentials"})
		return Grant{}, ErrUnauthenticated
	}
	if needsRehash {
		if rehashed, err := s.argon2(password); err == nil {
			_, _ = s.pool.Exec(ctx, `UPDATE users SET password_hash = $2 WHERE id = $1 AND password_hash = $3`, userID, rehashed, hash)
		}
	}
	if strings.TrimSpace(deviceName) == "" {
		deviceName = "Browser"
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	grant, err := s.createSessionTx(ctx, tx, userID, "pwa", deviceName, nil)
	if err != nil {
		return Grant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	return grant, nil
}

// CreateExtensionSession issues a Token family to a paired extension.
func (s *Service) CreateExtensionSession(ctx context.Context, tx pgx.Tx, userID uuid.UUID, displayName, extensionID string) (Grant, error) {
	return s.createSessionTx(ctx, tx, userID, "extension", displayName, &extensionID)
}

func (s *Service) createSessionTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, kind, displayName string, extensionID *string) (Grant, error) {
	csrfRaw, csrfToken, err := mintToken()
	if err != nil {
		return Grant{}, err
	}
	if len(displayName) > 80 {
		displayName = displayName[:80]
	}
	var sessionID uuid.UUID
	err = tx.QueryRow(ctx, `
		INSERT INTO device_sessions (user_id, kind, display_name, extension_id, csrf_token_hash)
		VALUES ($1, $2, $3, $4, $5) RETURNING id`,
		userID, kind, displayName, extensionID, csrfHash(csrfRaw)).Scan(&sessionID)
	if err != nil {
		return Grant{}, fmt.Errorf("create device session: %w", err)
	}
	access, refresh, expires, err := s.issueTokensTx(ctx, tx, sessionID, nil)
	if err != nil {
		return Grant{}, err
	}
	ext := ""
	if extensionID != nil {
		ext = *extensionID
	}
	return Grant{
		Session:         Session{ID: sessionID, UserID: userID, Kind: kind, ExtensionID: ext, CSRFHash: csrfHash(csrfRaw), AccessExpiresAt: expires},
		AccessToken:     access,
		RefreshToken:    refresh,
		CSRFToken:       csrfToken,
		AccessExpiresAt: expires,
	}, nil
}

// issueTokensTx inserts a fresh access row and a refresh row for a session.
func (s *Service) issueTokensTx(ctx context.Context, tx pgx.Tx, sessionID uuid.UUID, parentRefresh *uuid.UUID) (access, refresh string, expires time.Time, err error) {
	accessRaw, accessToken, err := mintToken()
	if err != nil {
		return "", "", time.Time{}, err
	}
	refreshRaw, refreshToken, err := mintToken()
	if err != nil {
		return "", "", time.Time{}, err
	}
	ad := s.keys.Current(accessRaw)
	rd := s.keys.Current(refreshRaw)
	if err := tx.QueryRow(ctx, `
		INSERT INTO session_access_tokens (device_session_id, token_hash, hash_key_id)
		VALUES ($1, $2, $3) RETURNING expires_at`, sessionID, ad.Hash, ad.KeyID).Scan(&expires); err != nil {
		return "", "", time.Time{}, fmt.Errorf("issue access token: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO session_refresh_tokens (device_session_id, parent_refresh_token_id, token_hash, hash_key_id)
		VALUES ($1, $2, $3, $4)`, sessionID, parentRefresh, rd.Hash, rd.KeyID); err != nil {
		return "", "", time.Time{}, fmt.Errorf("issue refresh token: %w", err)
	}
	return accessToken, refreshToken, expires, nil
}

// Authenticate resolves an access token to its live Device session, or nil.
// Ordinary activity bumps last_seen_at at most every five minutes and never
// extends the inactivity deadline (ADR 0003).
func (s *Service) Authenticate(ctx context.Context, presented string) *Session {
	raw, err := decodeToken(presented)
	if err != nil {
		return nil
	}
	for _, d := range s.keys.Candidates(raw) {
		var sess Session
		var ext *string
		err := s.pool.QueryRow(ctx, `
			SELECT s.id, s.user_id, s.kind::text, s.extension_id, s.csrf_token_hash, a.expires_at
			  FROM session_access_tokens a
			  JOIN device_sessions s ON s.id = a.device_session_id
			 WHERE a.hash_key_id = $1 AND a.token_hash = $2
			   AND a.revoked_at IS NULL AND a.expires_at > now()
			   AND s.revoked_at IS NULL AND s.inactive_expires_at > now()`,
			d.KeyID, d.Hash).Scan(&sess.ID, &sess.UserID, &sess.Kind, &ext, &sess.CSRFHash, &sess.AccessExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil
		}
		if ext != nil {
			sess.ExtensionID = *ext
		}
		_, _ = s.pool.Exec(ctx, `UPDATE device_sessions SET last_seen_at = now() WHERE id = $1 AND last_seen_at < now() - interval '5 minutes'`, sess.ID)
		return &sess
	}
	return nil
}

// SessionAlive reports whether a Device session is still live, for the event
// stream's heartbeat re-check (ADR 0012).
func (s *Service) SessionAlive(ctx context.Context, sessionID uuid.UUID) bool {
	var alive bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM device_sessions WHERE id = $1 AND revoked_at IS NULL AND inactive_expires_at > now())`, sessionID).Scan(&alive)
	return err == nil && alive
}

// Refresh consumes a refresh token and rotates the whole family. Presenting
// an already-consumed token revokes the Device session with no grace window.
func (s *Service) Refresh(ctx context.Context, presented, sourceIP string) (Grant, error) {
	raw, err := decodeToken(presented)
	if err != nil {
		return Grant{}, ErrUnauthenticated
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	var (
		tokenID, sessionID, userID uuid.UUID
		kind                       string
		ext                        *string
		consumedAt, revokedAt      *time.Time
		sessionRevokedAt           *time.Time
		inactiveExpiresAt          time.Time
	)
	found := false
	for _, d := range s.keys.Candidates(raw) {
		err := tx.QueryRow(ctx, `
			SELECT r.id, r.consumed_at, r.revoked_at, s.id, s.user_id, s.kind::text, s.extension_id, s.revoked_at, s.inactive_expires_at
			  FROM session_refresh_tokens r
			  JOIN device_sessions s ON s.id = r.device_session_id
			 WHERE r.hash_key_id = $1 AND r.token_hash = $2
			 FOR UPDATE OF r, s`, d.KeyID, d.Hash,
		).Scan(&tokenID, &consumedAt, &revokedAt, &sessionID, &userID, &kind, &ext, &sessionRevokedAt, &inactiveExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return Grant{}, err
		}
		found = true
		break
	}
	if !found {
		return Grant{}, ErrUnauthenticated
	}
	if sessionRevokedAt != nil || revokedAt != nil || !inactiveExpiresAt.After(s.now()) {
		return Grant{}, ErrUnauthenticated
	}
	if consumedAt != nil {
		// Reuse. Revoke the family and record it; the reused token is marked.
		if _, err := tx.Exec(ctx, `UPDATE session_refresh_tokens SET reuse_detected_at = now() WHERE id = $1`, tokenID); err != nil {
			return Grant{}, err
		}
		if err := revokeSessionsTx(ctx, tx, userID, &sessionID, "refresh_reuse"); err != nil {
			return Grant{}, err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_security_events (event_type, user_id, device_session_id, source_ip, reason_code)
			VALUES ('refresh_reuse', $1, $2, $3, 'consumed_token_presented')`, userID, sessionID, nullIP(sourceIP)); err != nil {
			return Grant{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return Grant{}, err
		}
		return Grant{}, ErrUnauthenticated
	}

	if _, err := tx.Exec(ctx, `UPDATE session_refresh_tokens SET consumed_at = now() WHERE id = $1`, tokenID); err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE session_access_tokens SET revoked_at = now() WHERE device_session_id = $1 AND revoked_at IS NULL`, sessionID); err != nil {
		return Grant{}, err
	}
	csrfRaw, csrfToken, err := mintToken()
	if err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE device_sessions
		   SET last_refreshed_at = now(), inactive_expires_at = now() + interval '30 days',
		       last_seen_at = now(), csrf_token_hash = $2
		 WHERE id = $1`, sessionID, csrfHash(csrfRaw)); err != nil {
		return Grant{}, err
	}
	access, refresh, expires, err := s.issueTokensTx(ctx, tx, sessionID, &tokenID)
	if err != nil {
		return Grant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	extID := ""
	if ext != nil {
		extID = *ext
	}
	return Grant{
		Session:         Session{ID: sessionID, UserID: userID, Kind: kind, ExtensionID: extID, CSRFHash: csrfHash(csrfRaw), AccessExpiresAt: expires},
		AccessToken:     access,
		RefreshToken:    refresh,
		CSRFToken:       csrfToken,
		AccessExpiresAt: expires,
	}, nil
}

// RotateCSRF mints a new CSRF token for a session whose refresh cookie is
// live, for the no-store recovery path after a reload (ADR 0003).
func (s *Service) RotateCSRF(ctx context.Context, presentedRefresh string) (string, error) {
	sessionID, err := s.liveSessionForRefresh(ctx, presentedRefresh)
	if err != nil {
		return "", err
	}
	csrfRaw, csrfToken, err := mintToken()
	if err != nil {
		return "", err
	}
	if _, err := s.pool.Exec(ctx, `UPDATE device_sessions SET csrf_token_hash = $2 WHERE id = $1`, sessionID, csrfHash(csrfRaw)); err != nil {
		return "", err
	}
	return csrfToken, nil
}

// LogoutViaRefresh revokes the session behind a live refresh cookie.
func (s *Service) LogoutViaRefresh(ctx context.Context, presentedRefresh string) error {
	sessionID, err := s.liveSessionForRefresh(ctx, presentedRefresh)
	if err != nil {
		return err
	}
	return s.RevokeSession(ctx, uuid.Nil, sessionID, "signed_out")
}

func (s *Service) liveSessionForRefresh(ctx context.Context, presented string) (uuid.UUID, error) {
	raw, err := decodeToken(presented)
	if err != nil {
		return uuid.Nil, ErrUnauthenticated
	}
	for _, d := range s.keys.Candidates(raw) {
		var sessionID uuid.UUID
		err := s.pool.QueryRow(ctx, `
			SELECT s.id FROM session_refresh_tokens r
			  JOIN device_sessions s ON s.id = r.device_session_id
			 WHERE r.hash_key_id = $1 AND r.token_hash = $2
			   AND r.consumed_at IS NULL AND r.revoked_at IS NULL
			   AND s.revoked_at IS NULL AND s.inactive_expires_at > now()`, d.KeyID, d.Hash).Scan(&sessionID)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return uuid.Nil, err
		}
		return sessionID, nil
	}
	return uuid.Nil, ErrUnauthenticated
}

// RevokeSession revokes one Device session with a reason. userID scopes the
// revocation to the Owner when known; uuid.Nil skips the scope, for flows
// that already proved the session through its own credential.
func (s *Service) RevokeSession(ctx context.Context, userID, sessionID uuid.UUID, reason string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	var owner uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT user_id FROM device_sessions WHERE id = $1 AND revoked_at IS NULL`, sessionID).Scan(&owner); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return pgx.ErrNoRows
		}
		return err
	}
	if userID != uuid.Nil && owner != userID {
		return pgx.ErrNoRows
	}
	if err := revokeSessionsTx(ctx, tx, owner, &sessionID, reason); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// RevokeAll revokes every Device session of the Owner, the caller's included.
func (s *Service) RevokeAll(ctx context.Context, userID uuid.UUID) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if err := revokeSessionsTx(ctx, tx, userID, nil, "all_devices_revoked"); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ChangePassword verifies the current password, stores the new one, rotates
// the calling session's credentials and revokes every other session.
func (s *Service) ChangePassword(ctx context.Context, session Session, current, next, sourceIP string) (Grant, error) {
	if err := checkPassword(next); err != nil {
		return Grant{}, err
	}
	var hash string
	if err := s.pool.QueryRow(ctx, `SELECT password_hash FROM users WHERE id = $1`, session.UserID).Scan(&hash); err != nil {
		return Grant{}, err
	}
	ok, _, err := VerifyPassword(hash, current)
	if err != nil || !ok {
		s.audit.Record(ctx, audit.Event{Type: audit.LoginFailed, UserID: &session.UserID, DeviceSessionID: &session.ID, SourceIP: sourceIP, ReasonCode: "password_change_bad_current"})
		return Grant{}, ErrUnauthenticated
	}
	newHash, err := s.argon2(next)
	if err != nil {
		return Grant{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Grant{}, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op
	if _, err := tx.Exec(ctx, `UPDATE users SET password_hash = $2, password_changed_at = now() WHERE id = $1`, session.UserID, newHash); err != nil {
		return Grant{}, err
	}
	// Every other session goes; this one keeps its identity and rotates.
	if _, err := tx.Exec(ctx, `
		UPDATE device_sessions SET revoked_at = now(), revocation_reason = 'password_changed'
		 WHERE user_id = $1 AND id <> $2 AND revoked_at IS NULL`, session.UserID, session.ID); err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session_access_tokens a SET revoked_at = now()
		  FROM device_sessions s WHERE a.device_session_id = s.id AND s.user_id = $1 AND a.revoked_at IS NULL`, session.UserID); err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE session_refresh_tokens r SET revoked_at = now()
		  FROM device_sessions s WHERE r.device_session_id = s.id AND s.user_id = $1
		   AND r.consumed_at IS NULL AND r.revoked_at IS NULL`, session.UserID); err != nil {
		return Grant{}, err
	}
	csrfRaw, csrfToken, err := mintToken()
	if err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `UPDATE device_sessions SET csrf_token_hash = $2, last_seen_at = now() WHERE id = $1`, session.ID, csrfHash(csrfRaw)); err != nil {
		return Grant{}, err
	}
	access, refresh, expires, err := s.issueTokensTx(ctx, tx, session.ID, nil)
	if err != nil {
		return Grant{}, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO auth_security_events (event_type, user_id, device_session_id, source_ip, reason_code)
		VALUES ('password_changed', $1, $2, $3, 'owner_changed_password')`, session.UserID, session.ID, nullIP(sourceIP)); err != nil {
		return Grant{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Grant{}, err
	}
	session.CSRFHash = csrfHash(csrfRaw)
	session.AccessExpiresAt = expires
	return Grant{Session: session, AccessToken: access, RefreshToken: refresh, CSRFToken: csrfToken, AccessExpiresAt: expires}, nil
}

// GetAccount reads the Owner.
func (s *Service) GetAccount(ctx context.Context, userID uuid.UUID) (Account, error) {
	var a Account
	err := s.pool.QueryRow(ctx, `SELECT id, username, display_name, timezone, created_at, password_changed_at FROM users WHERE id = $1`, userID).
		Scan(&a.ID, &a.Username, &a.DisplayName, &a.Timezone, &a.CreatedAt, &a.PasswordChangedAt)
	return a, err
}

// UpdateAccount changes the display name and timezone. A nil pointer leaves
// the field alone.
func (s *Service) UpdateAccount(ctx context.Context, userID uuid.UUID, displayName, timezone *string) (Account, error) {
	if timezone != nil {
		if _, err := time.LoadLocation(*timezone); err != nil {
			return Account{}, fmt.Errorf("unknown timezone %q", *timezone)
		}
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE users SET display_name = coalesce($2, display_name), timezone = coalesce($3, timezone) WHERE id = $1`,
		userID, displayName, timezone)
	if err != nil {
		return Account{}, err
	}
	return s.GetAccount(ctx, userID)
}

// SyncCursor is the Owner's current cursor, 0 when nothing was ever written.
func (s *Service) SyncCursor(ctx context.Context, userID uuid.UUID) (int64, error) {
	var cursor int64
	err := s.pool.QueryRow(ctx, `SELECT coalesce((SELECT next_cursor - 1 FROM sync_cursors WHERE user_id = $1), 0)`, userID).Scan(&cursor)
	return cursor, err
}

// ListSessions lists the Owner's live Device sessions, most recently seen first.
func (s *Service) ListSessions(ctx context.Context, userID uuid.UUID) ([]DeviceSession, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT s.id, s.kind::text, s.display_name, s.extension_id, s.created_at, s.last_seen_at,
		       s.last_refreshed_at, s.inactive_expires_at,
		       (SELECT count(*) FROM push_subscriptions p WHERE p.device_session_id = s.id)
		  FROM device_sessions s
		 WHERE s.user_id = $1 AND s.revoked_at IS NULL AND s.inactive_expires_at > now()
		 ORDER BY s.last_seen_at DESC, s.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceSession
	for rows.Next() {
		var d DeviceSession
		if err := rows.Scan(&d.ID, &d.Kind, &d.DisplayName, &d.ExtensionID, &d.CreatedAt, &d.LastSeenAt, &d.LastRefreshedAt, &d.InactiveExpiresAt, &d.PushSubscriptions); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ListSecurityEvents pages the audit table newest first. before is an
// exclusive upper bound on id; 0 means the newest page.
func (s *Service) ListSecurityEvents(ctx context.Context, userID uuid.UUID, before int64, limit int) ([]SecurityEvent, error) {
	if before <= 0 {
		before = 1<<62 - 1
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, event_type::text, device_session_id, host(source_ip), reason_code, extension_id, occurred_at
		  FROM auth_security_events
		 WHERE (user_id = $1 OR user_id IS NULL) AND id < $2
		 ORDER BY id DESC LIMIT $3`, userID, before, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SecurityEvent
	for rows.Next() {
		var e SecurityEvent
		if err := rows.Scan(&e.ID, &e.Type, &e.DeviceSessionID, &e.SourceIP, &e.ReasonCode, &e.ExtensionID, &e.OccurredAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// revokeSessionsTx revokes one session (when only is set) or every live
// session of the Owner, together with their live credentials, and records
// one session_revoked event per session.
func revokeSessionsTx(ctx context.Context, tx pgx.Tx, userID uuid.UUID, only *uuid.UUID, reason string) error {
	rows, err := tx.Query(ctx, `
		UPDATE device_sessions SET revoked_at = now(), revocation_reason = $3
		 WHERE user_id = $1 AND revoked_at IS NULL AND ($2::uuid IS NULL OR id = $2)
		 RETURNING id`, userID, only, reason)
	if err != nil {
		return fmt.Errorf("revoke sessions: %w", err)
	}
	var revoked []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		revoked = append(revoked, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, id := range revoked {
		if _, err := tx.Exec(ctx, `UPDATE session_access_tokens SET revoked_at = now() WHERE device_session_id = $1 AND revoked_at IS NULL`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `UPDATE session_refresh_tokens SET revoked_at = now() WHERE device_session_id = $1 AND consumed_at IS NULL AND revoked_at IS NULL`, id); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO auth_security_events (event_type, user_id, device_session_id, reason_code)
			VALUES ('session_revoked', $1, $2, $3)`, userID, id, reason); err != nil {
			return err
		}
	}
	return nil
}

func checkPassword(password string) error {
	if len(password) < 12 {
		return errors.New("password must be at least 12 characters")
	}
	if len(password) > 1024 {
		return errors.New("password is too long")
	}
	return nil
}

func nullIP(ip string) *string {
	if ip == "" {
		return nil
	}
	return &ip
}
