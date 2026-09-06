// Package audit writes Security events: the retained, credential-free record
// of rejected and administrative authentication activity (ADR 0003). Rows are
// append-only and carry a typed event, an optional Owner and Device session,
// the source address, the extension id, and a safe reason code -- never a
// credential, verifier, password, request body, query string or user agent.
package audit

import (
	"context"
	"log/slog"
	"net/netip"
	"regexp"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EventType is a value of the auth_security_event_type enum.
type EventType string

// The enum, spelled as the migrations spell it.
const (
	LoginFailed     EventType = "login_failed"
	RefreshReuse    EventType = "refresh_reuse"
	OriginRejected  EventType = "origin_rejected"
	PairingDenied   EventType = "pairing_denied"
	PairingReplay   EventType = "pairing_replay"
	PasswordChanged EventType = "password_changed"
	PasswordReset   EventType = "password_reset"
	SessionRevoked  EventType = "session_revoked"
	AIRoleChanged   EventType = "ai_role_changed"
	RateLimited     EventType = "rate_limited"
)

// Event is one row to append.
type Event struct {
	Type            EventType
	UserID          *uuid.UUID
	DeviceSessionID *uuid.UUID
	SourceIP        string
	ReasonCode      string
	ExtensionID     string
}

var reasonCodeShape = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)

// Recorder appends events. A nil pool records nothing and logs instead, which
// is what tests and the health endpoint want.
type Recorder struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

// New builds a recorder.
func New(pool *pgxpool.Pool, log *slog.Logger) *Recorder {
	return &Recorder{pool: pool, log: log}
}

// Record appends the event. Failures are logged and never returned: an audit
// write must not fail the request that caused it.
func (r *Recorder) Record(ctx context.Context, ev Event) {
	var reason *string
	if reasonCodeShape.MatchString(ev.ReasonCode) {
		reason = &ev.ReasonCode
	}
	var extension *string
	if ev.ExtensionID != "" {
		extension = &ev.ExtensionID
	}
	var source *netip.Addr
	if addr, err := netip.ParseAddr(ev.SourceIP); err == nil {
		source = &addr
	}
	if r == nil || r.pool == nil {
		if r != nil && r.log != nil {
			r.log.Info("security event (not persisted)", "type", string(ev.Type), "reason", ev.ReasonCode)
		}
		return
	}
	_, err := r.pool.Exec(ctx, `
		INSERT INTO auth_security_events (event_type, user_id, device_session_id, source_ip, reason_code, extension_id)
		VALUES ($1, $2, $3, $4, $5, $6)`,
		string(ev.Type), ev.UserID, ev.DeviceSessionID, source, reason, extension)
	if err != nil && r.log != nil {
		r.log.Error("record security event", "type", string(ev.Type), "err", err)
	}
}
