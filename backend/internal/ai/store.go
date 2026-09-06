package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/keys"
)

// querier is what both a pool and a transaction offer.
type querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Role is one row of ai_role_configs as Settings reads it: never the key.
type Role struct {
	Role              string
	Provider          string
	Model             string
	Endpoint          *string
	RequestOptions    map[string]any
	KeyConfigured     bool
	KeyHint           *string
	VerifiedAt        *time.Time
	VerificationError *string
	UpdatedAt         time.Time
}

// Default is one row of ai_role_defaults, the recommended starting point.
type Default struct {
	Role           string
	Provider       string
	Model          string
	RequestOptions map[string]any
	SourceNote     *string
}

// Write is what PUT /settings/ai/{role} carries. A nil RequestOptions keeps
// the stored options; a nil APIKey keeps the stored key.
type Write struct {
	Provider       string
	Model          string
	Endpoint       *string
	RequestOptions map[string]any
	APIKey         *string
}

// Credentials is what a Stage needs to call a provider: read fresh at the
// start of every call, never cached (ADR 0010).
type Credentials struct {
	Role           string
	Provider       string
	Model          string
	BaseURL        string
	RequestOptions map[string]any
	Key            string
	KeyVersion     int
}

// ErrNoKey is returned when the role holds no key.
var ErrNoKey = errors.New("no provider key is stored for this role")

// The changed-field names an update reports, which become ai_role_changed
// reason codes as <role>_<field>.
const (
	FieldProvider = "provider"
	FieldModel    = "model"
	FieldEndpoint = "endpoint"
	FieldKey      = "key"
	FieldOptions  = "request_options"
)

// Store reads and writes role configuration.
type Store struct {
	pool *pgxpool.Pool
	ring *keys.Ring
}

// NewStore builds a store over the pool and the master key ring.
func NewStore(pool *pgxpool.Pool, ring *keys.Ring) *Store {
	return &Store{pool: pool, ring: ring}
}

const roleColumns = `role::text, provider, model, endpoint, request_options, key_ciphertext IS NOT NULL, key_hint, verified_at, verification_error, updated_at`

func scanRole(row pgx.Row) (*Role, error) {
	var r Role
	if err := row.Scan(&r.Role, &r.Provider, &r.Model, &r.Endpoint, &r.RequestOptions, &r.KeyConfigured, &r.KeyHint, &r.VerifiedAt, &r.VerificationError, &r.UpdatedAt); err != nil {
		return nil, err
	}
	if r.RequestOptions == nil {
		r.RequestOptions = map[string]any{}
	}
	return &r, nil
}

// List returns the Owner's configured roles in Settings order.
func (s *Store) List(ctx context.Context, userID uuid.UUID) ([]Role, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+roleColumns+` FROM ai_role_configs WHERE user_id = $1
		 ORDER BY array_position($2::text[], role::text)`, userID, Roles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Role
	for rows.Next() {
		r, err := scanRole(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// Get reads one role; pgx.ErrNoRows when it has no row.
func (s *Store) Get(ctx context.Context, userID uuid.UUID, role string) (*Role, error) {
	return scanRole(s.pool.QueryRow(ctx, `SELECT `+roleColumns+` FROM ai_role_configs WHERE user_id = $1 AND role = $2`, userID, role))
}

// Defaults reads the recommended configuration.
func (s *Store) Defaults(ctx context.Context) ([]Default, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT role::text, provider, model, request_options, source_note FROM ai_role_defaults
		 ORDER BY array_position($1::text[], role::text)`, Roles)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Default
	for rows.Next() {
		var d Default
		if err := rows.Scan(&d.Role, &d.Provider, &d.Model, &d.RequestOptions, &d.SourceNote); err != nil {
			return nil, err
		}
		if d.RequestOptions == nil {
			d.RequestOptions = map[string]any{}
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// Blocking lists the roles that must hold a key before a Submission is
// accepted, from the one view every gate reads (ADR 0010).
func (s *Store) Blocking(ctx context.Context, userID uuid.UUID) ([]string, error) {
	rows, err := s.pool.Query(ctx, `SELECT role::text FROM ai_roles_blocking_submission WHERE user_id = $1 ORDER BY role`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var role string
		if err := rows.Scan(&role); err != nil {
			return nil, err
		}
		out = append(out, role)
	}
	return out, rows.Err()
}

// EmbeddingDimension is the dimension the search index stores.
func (s *Store) EmbeddingDimension(ctx context.Context) (int, error) {
	var dim int
	err := s.pool.QueryRow(ctx, `SELECT embedding_dimension FROM search_index_state`).Scan(&dim)
	return dim, err
}

// Update writes a role and reports which of provider, model, endpoint, key
// and request_options changed. Any change to the first four clears the
// verification, since a verification is about a combination, not a row.
func (s *Store) Update(ctx context.Context, userID uuid.UUID, role string, w Write) (*Role, []string, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, nil, err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rollback after commit is a no-op

	existing, err := scanRole(tx.QueryRow(ctx, `SELECT `+roleColumns+` FROM ai_role_configs WHERE user_id = $1 AND role = $2 FOR UPDATE`, userID, role))
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, nil, err
	}
	var changed []string
	options := w.RequestOptions
	if existing == nil {
		changed = append(changed, FieldProvider, FieldModel)
		if w.Endpoint != nil {
			changed = append(changed, FieldEndpoint)
		}
		if options == nil {
			options = map[string]any{}
		}
		changed = append(changed, FieldOptions)
	} else {
		if w.Provider != existing.Provider {
			changed = append(changed, FieldProvider)
		}
		if w.Model != existing.Model {
			changed = append(changed, FieldModel)
		}
		if !equalStrings(w.Endpoint, existing.Endpoint) {
			changed = append(changed, FieldEndpoint)
		}
		if options == nil {
			options = existing.RequestOptions
		} else if !equalJSON(options, existing.RequestOptions) {
			changed = append(changed, FieldOptions)
		}
	}
	var sealed *Sealed
	if w.APIKey != nil {
		sl, err := Seal(s.ring, userID, role, *w.APIKey)
		if err != nil {
			return nil, nil, err
		}
		sealed = &sl
		changed = append(changed, FieldKey)
	}
	clearVerification := false
	for _, f := range changed {
		if f != FieldOptions {
			clearVerification = true
		}
	}
	optionsJSON, err := json.Marshal(options)
	if err != nil {
		return nil, nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO ai_role_configs (user_id, role, provider, model, endpoint, request_options)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (user_id, role) DO UPDATE
		   SET provider = EXCLUDED.provider, model = EXCLUDED.model, endpoint = EXCLUDED.endpoint,
		       request_options = EXCLUDED.request_options, updated_at = now()`,
		userID, role, w.Provider, w.Model, w.Endpoint, optionsJSON); err != nil {
		return nil, nil, fmt.Errorf("write role: %w", err)
	}
	if sealed != nil {
		if _, err := tx.Exec(ctx, `
			UPDATE ai_role_configs SET key_ciphertext = $3, key_version = $4, key_hint = $5, updated_at = now()
			 WHERE user_id = $1 AND role = $2`, userID, role, sealed.Ciphertext, sealed.Version, sealed.Hint); err != nil {
			return nil, nil, fmt.Errorf("write key: %w", err)
		}
	}
	if clearVerification {
		if _, err := tx.Exec(ctx, `
			UPDATE ai_role_configs SET verified_at = NULL, verification_error = NULL WHERE user_id = $1 AND role = $2`, userID, role); err != nil {
			return nil, nil, err
		}
	}
	row, err := scanRole(tx.QueryRow(ctx, `SELECT `+roleColumns+` FROM ai_role_configs WHERE user_id = $1 AND role = $2`, userID, role))
	if err != nil {
		return nil, nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, nil, err
	}
	return row, changed, nil
}

// ClearKey removes the stored key, leaving the role unconfigured.
func (s *Store) ClearKey(ctx context.Context, userID uuid.UUID, role string) (*Role, error) {
	return scanRole(s.pool.QueryRow(ctx, `
		UPDATE ai_role_configs
		   SET key_ciphertext = NULL, key_version = NULL, key_hint = NULL,
		       verified_at = NULL, verification_error = NULL, updated_at = now()
		 WHERE user_id = $1 AND role = $2
		 RETURNING `+roleColumns, userID, role))
}

// MarkVerified records a successful check.
func (s *Store) MarkVerified(ctx context.Context, q querier, userID uuid.UUID, role string, at time.Time) error {
	_, err := q.Exec(ctx, `
		UPDATE ai_role_configs SET verified_at = $3, verification_error = NULL
		 WHERE user_id = $1 AND role = $2`, userID, role, at)
	return err
}

// MarkVerificationError records a failed check, or a 401/403 met later by
// any call, so a key revoked upstream shows as revoked in Settings.
func (s *Store) MarkVerificationError(ctx context.Context, q querier, userID uuid.UUID, role, message string) error {
	if len(message) > 500 {
		message = message[:500]
	}
	_, err := q.Exec(ctx, `
		UPDATE ai_role_configs SET verified_at = NULL, verification_error = $3
		 WHERE user_id = $1 AND role = $2`, userID, role, message)
	return err
}

// Credentials reads and unseals what a call needs. ErrNoKey when the role
// holds none.
func (s *Store) Credentials(ctx context.Context, q querier, userID uuid.UUID, role string) (*Credentials, error) {
	var (
		c          Credentials
		endpoint   *string
		ciphertext []byte
		version    *int
	)
	err := q.QueryRow(ctx, `
		SELECT role::text, provider, model, endpoint, request_options, key_ciphertext, key_version
		  FROM ai_role_configs WHERE user_id = $1 AND role = $2`, userID, role).
		Scan(&c.Role, &c.Provider, &c.Model, &endpoint, &c.RequestOptions, &ciphertext, &version)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNoKey
		}
		return nil, err
	}
	if ciphertext == nil || version == nil {
		return nil, ErrNoKey
	}
	key, err := Open(s.ring, userID, role, *version, ciphertext)
	if err != nil {
		return nil, err
	}
	c.Key = key
	c.KeyVersion = *version
	if c.RequestOptions == nil {
		c.RequestOptions = map[string]any{}
	}
	c.BaseURL = BaseURL(c.Provider, endpoint)
	return &c, nil
}

// BaseURL is the endpoint override when set, else the provider's canonical
// base, which lives here rather than in the database.
func BaseURL(provider string, endpoint *string) string {
	if endpoint != nil && *endpoint != "" {
		return *endpoint
	}
	if provider == ProviderOpenAI {
		return "https://api.openai.com/v1"
	}
	return ""
}

func equalStrings(a, b *string) bool {
	switch {
	case a == nil && b == nil:
		return true
	case a == nil || b == nil:
		return false
	default:
		return *a == *b
	}
}

func equalJSON(a, b map[string]any) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}
