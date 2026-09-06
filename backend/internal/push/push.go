// Package push is Web Push (ADR 0014): one VAPID key stored and the other
// derived, subscriptions bound to PWA Device sessions, the copy of
// docs/design/notification-copy.md, and a sender that reads RFC 8030's
// answers the way it means them.
package push

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Keys is the VAPID pair: the private scalar from the mounted PKCS#8 file
// and the public point derived from it at boot, so the two cannot drift.
type Keys struct {
	// Public is the uncompressed P-256 point, base64url, 87 characters: what
	// the browser subscribes with.
	Public  string
	private string
}

// LoadKeys reads the PEM file the runbook writes with openssl.
func LoadKeys(path string) (*Keys, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	return ParseKeys(raw)
}

// ParseKeys accepts a PKCS#8 or SEC1 PEM holding a P-256 private key.
func ParseKeys(raw []byte) (*Keys, error) {
	block, _ := pem.Decode(raw)
	if block == nil {
		return nil, errors.New("vapid private key: not PEM")
	}
	var key *ecdsa.PrivateKey
	switch block.Type {
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("vapid private key: %w", err)
		}
		ec, ok := parsed.(*ecdsa.PrivateKey)
		if !ok {
			return nil, errors.New("vapid private key: not an EC key")
		}
		key = ec
	case "EC PRIVATE KEY":
		ec, err := x509.ParseECPrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("vapid private key: %w", err)
		}
		key = ec
	default:
		return nil, fmt.Errorf("vapid private key: unexpected PEM type %q", block.Type)
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("vapid private key: not P-256")
	}
	d, err := key.Bytes()
	if err != nil {
		return nil, fmt.Errorf("vapid private key: %w", err)
	}
	point, err := key.PublicKey.Bytes()
	if err != nil {
		return nil, fmt.Errorf("vapid public key: %w", err)
	}
	return &Keys{
		Public:  base64.RawURLEncoding.EncodeToString(point),
		private: base64.RawURLEncoding.EncodeToString(d),
	}, nil
}

// Subscription is one browser's push resource with its two switches.
type Subscription struct {
	ID                  uuid.UUID
	UserID              uuid.UUID
	DeviceSessionID     uuid.UUID
	DeviceDisplayName   string
	Endpoint            string
	P256dh, Auth        string
	ExpirationTime      *time.Time
	NotesEnabled        bool
	OpsEnabled          bool
	CreatedAt           time.Time
	LastSuccessAt       *time.Time
	LastFailureAt       *time.Time
	ConsecutiveFailures int
}

// Write is what the PWA registers.
type Write struct {
	Endpoint         string
	P256dh, Auth     string
	ExpirationTime   *time.Time
	NotesEnabled     bool
	OpsEnabled       bool
	ReplacesEndpoint string
}

// ErrNotFound is a subscription the Owner does not have.
var ErrNotFound = errors.New("no such subscription")

const subscriptionColumns = `
	s.id, s.user_id, s.device_session_id, d.display_name, s.endpoint, s.p256dh, s.auth, s.expiration_time,
	s.notes_enabled, s.ops_enabled, s.created_at, s.last_success_at, s.last_failure_at, s.consecutive_failures`

func scan(row pgx.Row) (*Subscription, error) {
	var s Subscription
	if err := row.Scan(&s.ID, &s.UserID, &s.DeviceSessionID, &s.DeviceDisplayName, &s.Endpoint, &s.P256dh, &s.Auth, &s.ExpirationTime,
		&s.NotesEnabled, &s.OpsEnabled, &s.CreatedAt, &s.LastSuccessAt, &s.LastFailureAt, &s.ConsecutiveFailures); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, err
	}
	return &s, nil
}

// Upsert registers or replaces the session's subscription, keyed by
// endpoint; a `replaces_endpoint` swaps the row a pushsubscriptionchange
// retired. An extension session is refused by the schema.
func Upsert(ctx context.Context, q querier, userID, sessionID uuid.UUID, w Write) (*Subscription, error) {
	if w.ReplacesEndpoint != "" && w.ReplacesEndpoint != w.Endpoint {
		if _, err := q.Exec(ctx, `DELETE FROM push_subscriptions WHERE user_id = $1 AND endpoint = $2`, userID, w.ReplacesEndpoint); err != nil {
			return nil, err
		}
	}
	var id uuid.UUID
	if err := q.QueryRow(ctx, `
		INSERT INTO push_subscriptions (user_id, device_session_id, endpoint, p256dh, auth, expiration_time, notes_enabled, ops_enabled)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (endpoint) DO UPDATE
		   SET user_id = EXCLUDED.user_id, device_session_id = EXCLUDED.device_session_id,
		       p256dh = EXCLUDED.p256dh, auth = EXCLUDED.auth, expiration_time = EXCLUDED.expiration_time,
		       notes_enabled = EXCLUDED.notes_enabled, ops_enabled = EXCLUDED.ops_enabled,
		       consecutive_failures = 0
		RETURNING id`, userID, sessionID, w.Endpoint, w.P256dh, w.Auth, w.ExpirationTime, w.NotesEnabled, w.OpsEnabled).Scan(&id); err != nil {
		return nil, err
	}
	return Get(ctx, q, userID, id)
}

// Get reads one of the Owner's subscriptions.
func Get(ctx context.Context, q querier, userID, id uuid.UUID) (*Subscription, error) {
	return scan(q.QueryRow(ctx, `SELECT `+subscriptionColumns+` FROM push_subscriptions s JOIN device_sessions d ON d.id = s.device_session_id WHERE s.user_id = $1 AND s.id = $2`, userID, id))
}

// List is every subscription the Owner has, newest first.
func List(ctx context.Context, q querier, userID uuid.UUID) ([]Subscription, error) {
	rows, err := q.Query(ctx, `SELECT `+subscriptionColumns+` FROM push_subscriptions s JOIN device_sessions d ON d.id = s.device_session_id WHERE s.user_id = $1 ORDER BY s.created_at DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// Update flips the switches; nil leaves one alone.
func Update(ctx context.Context, q querier, userID, id uuid.UUID, notes, ops *bool) (*Subscription, error) {
	tag, err := q.Exec(ctx, `
		UPDATE push_subscriptions SET notes_enabled = coalesce($3, notes_enabled), ops_enabled = coalesce($4, ops_enabled)
		 WHERE user_id = $1 AND id = $2`, userID, id, notes, ops)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrNotFound
	}
	return Get(ctx, q, userID, id)
}

// Delete removes a subscription.
func Delete(ctx context.Context, q querier, userID, id uuid.UUID) error {
	tag, err := q.Exec(ctx, `DELETE FROM push_subscriptions WHERE user_id = $1 AND id = $2`, userID, id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// Category is one of the two switches.
type Category string

// The categories.
const (
	CategoryNotes Category = "notes"
	CategoryOps   Category = "ops"
)

// Recipients is every subscription with the category switched on.
func Recipients(ctx context.Context, q querier, userID uuid.UUID, category Category) ([]Subscription, error) {
	column := "notes_enabled"
	if category == CategoryOps {
		column = "ops_enabled"
	}
	rows, err := q.Query(ctx, `SELECT `+subscriptionColumns+` FROM push_subscriptions s JOIN device_sessions d ON d.id = s.device_session_id WHERE s.user_id = $1 AND s.`+column+` ORDER BY s.created_at`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		s, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// Message is what the service worker shows: the copy, where a tap goes,
// and the tag that collapses a pending message with the same one.
type Message struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url"`
	Tag   string `json:"tag"`
	Kind  string `json:"kind"`
	// NoteID is present on note notifications so the service worker can
	// route without a fetch.
	NoteID string `json:"noteId,omitempty"`
}

// NoteKind is the terminal state a note notification carries.
type NoteKind string

// The note notification kinds; cancelled sends nothing.
const (
	Ready           NoteKind = "ready"
	Failed          NoteKind = "failed"
	FailedExtractor NoteKind = "failed_extractor"
	Held            NoteKind = "held"
)

// NoteTopic is the Note id with its hyphens stripped: exactly RFC 8030's
// 32-character Topic ceiling, so a later failed replaces a pending ready.
func NoteTopic(noteID uuid.UUID) string {
	return strings.ReplaceAll(noteID.String(), "-", "")
}

// NoteMessage is the copy of docs/design/notification-copy.md.
func NoteMessage(kind NoteKind, noteID uuid.UUID, title, reason string) Message {
	if strings.TrimSpace(title) == "" {
		title = "Your video"
	}
	m := Message{URL: "/notes/" + noteID.String(), Tag: NoteTopic(noteID), Kind: string(kind), NoteID: noteID.String()}
	switch kind {
	case Ready:
		m.Title, m.Body = title, "Visual note ready — tap to open."
	case Failed:
		if reason == "" {
			reason = "the video could not be processed"
		}
		m.Title, m.Body = title, "Couldn't create notes: "+strings.TrimRight(reason, ".")+". Open to retry or delete."
	case FailedExtractor:
		m.Title, m.Body = "YouTube extraction is blocked", "The video is fine — yt-dlp needs updating. Tap for the steps."
		m.URL = "/settings/health"
	case Held:
		m.Title, m.Body = "Monthly AI cap reached", title+" is waiting for you to confirm the spend."
		m.URL = "/settings/usage"
	}
	return m
}

// OpsMessage is the copy for one system_health check in alarm.
func OpsMessage(check string, observed map[string]any) Message {
	m := Message{URL: "/settings/health", Tag: check, Kind: "ops"}
	num := func(key string) string {
		if v, ok := observed[key]; ok {
			return strings.TrimSuffix(fmt.Sprintf("%.1f", toFloat(v)), ".0")
		}
		return "?"
	}
	switch check {
	case "backup_local":
		m.Title, m.Body = "Local backup is failing", "No fresh snapshot in "+num("days")+" days. Tap to check."
	case "backup_offsite":
		m.Title, m.Body = "Off-server copy is stale", "Last copy "+num("days")+" days ago. The laptop may be off."
	case "disk_free":
		m.Title, m.Body = "Disk is nearly full", num("gib_free")+" GiB free. Clear space before jobs start failing."
	case "extractor_breakage":
		m.Title, m.Body = "yt-dlp needs updating", "New notes are blocked until it is updated."
	case "worker_stale":
		m.Title, m.Body = "Processing has stopped", "No health check in "+num("minutes")+" minutes. Nothing is being processed."
	case "river_leader_lost":
		m.Title, m.Body = "Job scheduler lost its lease", "Scheduled work is not running. Tap to check."
	default:
		m.Title, m.Body = "Health check in alarm", check+" needs attention. Tap to check."
	}
	return m
}

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	default:
		return 0
	}
}

// Sender delivers messages and keeps the rows honest about what happened.
type Sender struct {
	keys       *Keys
	subscriber string
	client     *http.Client
	log        *slog.Logger
}

// NewSender takes the keys and the contact the VAPID token names.
func NewSender(keys *Keys, subscriber string, log *slog.Logger) *Sender {
	return &Sender{keys: keys, subscriber: subscriber, client: &http.Client{Timeout: 20 * time.Second}, log: log}
}

// Keys exposes the public key for the API to serve.
func (s *Sender) Keys() *Keys { return s.keys }

// WithHTTPClient swaps the transport, for tests against a local push service.
func (s *Sender) WithHTTPClient(client *http.Client) *Sender {
	s.client = client
	return s
}

// Outcome is what one delivery came to.
type Outcome string

// The outcomes: delivered; the subscription is gone (row deleted);
// transient (retry later); permanent (recorded, not retried).
const (
	Delivered Outcome = "delivered"
	Gone      Outcome = "gone"
	Transient Outcome = "transient"
	Permanent Outcome = "permanent"
)

// Urgency maps onto RFC 8030's header.
type Urgency = webpush.Urgency

// The urgencies used.
const (
	UrgencyNormal = webpush.UrgencyNormal
	UrgencyHigh   = webpush.UrgencyHigh
)

// Send pushes one message to one subscription. A 404 or 410 deletes the
// row: the subscription is gone, not unwell. 429 and 5xx are transient.
func (s *Sender) Send(ctx context.Context, q querier, sub *Subscription, m Message, ttl time.Duration, urgency Urgency) (Outcome, error) {
	payload, err := json.Marshal(m)
	if err != nil {
		return Permanent, err
	}
	resp, err := webpush.SendNotificationWithContext(ctx, payload, &webpush.Subscription{
		Endpoint: sub.Endpoint,
		Keys:     webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth},
	}, &webpush.Options{
		HTTPClient:      s.client,
		Subscriber:      s.subscriber,
		Topic:           m.Tag,
		TTL:             int(ttl.Seconds()),
		Urgency:         urgency,
		VAPIDPublicKey:  s.keys.Public,
		VAPIDPrivateKey: s.keys.private,
	})
	if err != nil {
		s.recordFailure(ctx, q, sub.ID)
		return Transient, err
	}
	defer resp.Body.Close() //nolint:errcheck // nothing to do with a close error
	_, _ = io.Copy(io.Discard, resp.Body)
	switch {
	case resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted:
		_, _ = q.Exec(ctx, `UPDATE push_subscriptions SET last_success_at = now(), consecutive_failures = 0 WHERE id = $1`, sub.ID)
		return Delivered, nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		_, _ = q.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1`, sub.ID)
		s.log.Info("push subscription gone; removed", "subscription", sub.ID, "status", resp.StatusCode)
		return Gone, nil
	case resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500:
		s.recordFailure(ctx, q, sub.ID)
		return Transient, fmt.Errorf("push service answered %d", resp.StatusCode)
	default:
		s.recordFailure(ctx, q, sub.ID)
		return Permanent, fmt.Errorf("push service refused with %d", resp.StatusCode)
	}
}

func (s *Sender) recordFailure(ctx context.Context, q querier, id uuid.UUID) {
	_, _ = q.Exec(ctx, `UPDATE push_subscriptions SET last_failure_at = now(), consecutive_failures = consecutive_failures + 1 WHERE id = $1`, id)
}

// Fanout sends one message to every recipient of a category. It returns
// an error only when a delivery was transient, so a job can retry; a
// permanent refusal is recorded on the row and not retried.
func (s *Sender) Fanout(ctx context.Context, q querier, userID uuid.UUID, category Category, m Message, ttl time.Duration, urgency Urgency) (int, error) {
	recipients, err := Recipients(ctx, q, userID, category)
	if err != nil {
		return 0, err
	}
	delivered := 0
	var transient error
	for i := range recipients {
		outcome, err := s.Send(ctx, q, &recipients[i], m, ttl, urgency)
		switch outcome {
		case Delivered:
			delivered++
		case Transient:
			transient = errors.Join(transient, err)
		case Permanent:
			s.log.Warn("push refused", "subscription", recipients[i].ID, "err", err)
		case Gone:
		}
	}
	return delivered, transient
}
