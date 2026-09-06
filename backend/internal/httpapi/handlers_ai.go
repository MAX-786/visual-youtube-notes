package httpapi

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
)

func toRoleConfig(r *ai.Role) AiRoleConfig {
	return AiRoleConfig{
		Role:              AiRole(r.Role),
		Provider:          r.Provider,
		Model:             r.Model,
		Endpoint:          r.Endpoint,
		RequestOptions:    r.RequestOptions,
		KeyConfigured:     r.KeyConfigured,
		KeyHint:           r.KeyHint,
		VerifiedAt:        r.VerifiedAt,
		VerificationError: r.VerificationError,
		UpdatedAt:         r.UpdatedAt,
	}
}

// GetAiSettings is GET /settings/ai: every role's configuration and key
// status, the roles blocking Submission, and the recommended defaults.
// Never a key.
func (s *Server) GetAiSettings(ctx context.Context, _ GetAiSettingsRequestObject) (GetAiSettingsResponseObject, error) {
	session := SessionFrom(ctx)
	roles, err := s.deps.AI.List(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	blocking, err := s.deps.AI.Blocking(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	defaults, err := s.deps.AI.Defaults(ctx)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := AiSettings{Roles: []AiRoleConfig{}, BlockingSubmission: []AiRole{}, Defaults: []AiRoleDefault{}}
	for i := range roles {
		out.Roles = append(out.Roles, toRoleConfig(&roles[i]))
	}
	for _, role := range blocking {
		out.BlockingSubmission = append(out.BlockingSubmission, AiRole(role))
	}
	for _, d := range defaults {
		out.Defaults = append(out.Defaults, AiRoleDefault{Role: AiRole(d.Role), Provider: d.Provider, Model: d.Model, RequestOptions: d.RequestOptions, SourceNote: d.SourceNote})
	}
	return GetAiSettings200JSONResponse(out), nil
}

// UpdateAiRole is PUT /settings/ai/{role}. The endpoint and the request
// options are validated here, with the Owner present to be told (ADR 0010);
// a new key is sealed, then checked against the provider without blocking
// the save.
func (s *Server) UpdateAiRole(ctx context.Context, req UpdateAiRoleRequestObject) (UpdateAiRoleResponseObject, error) {
	session := SessionFrom(ctx)
	role := string(req.Role)
	if !ai.IsRole(role) {
		return nil, apierr.New(apierr.NotFound, "No such Processing role.")
	}
	body := req.Body
	w := ai.Write{Provider: strings.ToLower(strings.TrimSpace(body.Provider)), Model: strings.TrimSpace(body.Model)}
	if w.Provider != ai.ProviderOpenAI {
		return nil, apierr.New(apierr.InvalidRequest, "This build speaks to OpenAI only. Set the provider to openai.").With("field", "provider")
	}
	if w.Model == "" {
		return nil, apierr.New(apierr.InvalidRequest, "Name the model to call.").With("field", "model")
	}
	if body.Endpoint != nil && strings.TrimSpace(*body.Endpoint) != "" {
		normalized, err := ai.ValidateEndpoint(ctx, *body.Endpoint)
		if err != nil {
			var ee *ai.EndpointError
			if errors.As(err, &ee) {
				return nil, apierr.New(apierr.EndpointNotAllowed, "The endpoint "+ee.Reason+". Use an https address that resolves to a public host, or leave it empty for the provider's own.").With("reason", ee.Reason)
			}
			return nil, apierr.Internal(err)
		}
		w.Endpoint = &normalized
	}
	if body.RequestOptions != nil {
		dimension, err := s.deps.AI.EmbeddingDimension(ctx)
		if err != nil {
			return nil, apierr.Internal(err)
		}
		if err := ai.ValidateOptions(w.Provider, role, *body.RequestOptions, dimension); err != nil {
			var oe *ai.OptionError
			if errors.As(err, &oe) {
				return nil, apierr.Newf(apierr.InvalidRequestOptions, "Request option %s %s.", oe.Key, oe.Reason).With("field", oe.Key).With("reason", oe.Reason)
			}
			return nil, apierr.Internal(err)
		}
		w.RequestOptions = *body.RequestOptions
	}
	if body.ApiKey != nil {
		key := strings.TrimSpace(*body.ApiKey)
		if len(key) < 8 {
			return nil, apierr.New(apierr.InvalidRequest, "That does not look like an API key. Paste the whole key.").With("field", "api_key")
		}
		w.APIKey = &key
	}
	row, changed, err := s.deps.AI.Update(ctx, session.UserID, role, w)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	for _, field := range changed {
		if field == ai.FieldOptions {
			continue
		}
		s.recordRoleChange(ctx, session, role, field)
	}
	// A key with no verdict for this combination is checked now; a failure
	// warns rather than blocks (ADR 0010).
	if row.KeyConfigured && row.VerifiedAt == nil && row.VerificationError == nil {
		if verified, err := s.verifyRole(ctx, session.UserID, role); err == nil {
			row = verified
		} else {
			s.deps.Logger.Warn("role verification failed to record", "role", role, "err", err)
		}
	}
	return UpdateAiRole200JSONResponse(toRoleConfig(row)), nil
}

// ClearAiRoleKey is DELETE /settings/ai/{role}/key.
func (s *Server) ClearAiRoleKey(ctx context.Context, req ClearAiRoleKeyRequestObject) (ClearAiRoleKeyResponseObject, error) {
	session := SessionFrom(ctx)
	role := string(req.Role)
	row, err := s.deps.AI.ClearKey(ctx, session.UserID, role)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, apierr.New(apierr.NotFound, "This role has no configuration yet, so there is no key to remove.")
		}
		return nil, apierr.Internal(err)
	}
	s.recordRoleChange(ctx, session, role, ai.FieldKey)
	return ClearAiRoleKey200JSONResponse(toRoleConfig(row)), nil
}

// VerifyAiRole is POST /settings/ai/{role}/verify: the cheapest
// authenticated read. On OpenAI it is free, so nothing is charged and no
// ledger row is written (ADR 0017).
func (s *Server) VerifyAiRole(ctx context.Context, req VerifyAiRoleRequestObject) (VerifyAiRoleResponseObject, error) {
	session := SessionFrom(ctx)
	role := string(req.Role)
	row, err := s.verifyRole(ctx, session.UserID, role)
	if err != nil {
		if errors.Is(err, ai.ErrNoKey) {
			return nil, apierr.New(apierr.NotFound, "No key is stored for this role. Add one in Settings → AI first.")
		}
		return nil, apierr.Internal(err)
	}
	return VerifyAiRole200JSONResponse(VerificationResult{Role: AiRole(role), VerifiedAt: row.VerifiedAt, VerificationError: row.VerificationError, Charged: false}), nil
}

// verifyRole checks the stored key against the provider and records the
// verdict on the row: verified_at on success, verification_error otherwise,
// never both.
func (s *Server) verifyRole(ctx context.Context, userID uuid.UUID, role string) (*ai.Role, error) {
	creds, err := s.deps.AI.Credentials(ctx, s.deps.Pool, userID, role)
	if err != nil {
		return nil, err
	}
	if err := ai.NewClient(*creds, s.deps.AITransport).Verify(ctx); err != nil {
		message := "The provider could not be reached. Check the endpoint and try again."
		var pe *ai.ProviderError
		if errors.As(err, &pe) && pe.Status != 0 {
			message = pe.Error()
		}
		if err := s.deps.AI.MarkVerificationError(ctx, s.deps.Pool, userID, role, message); err != nil {
			return nil, err
		}
	} else if err := s.deps.AI.MarkVerified(ctx, s.deps.Pool, userID, role, time.Now()); err != nil {
		return nil, err
	}
	return s.deps.AI.Get(ctx, userID, role)
}

// recordRoleChange writes one ai_role_changed Security event per changed
// field, carrying role and field in reason_code and never a value.
func (s *Server) recordRoleChange(ctx context.Context, session *Session, role, field string) {
	s.deps.Audit.Record(ctx, audit.Event{
		Type:            audit.AIRoleChanged,
		UserID:          &session.UserID,
		DeviceSessionID: &session.ID,
		SourceIP:        clientIP(requestFrom(ctx)),
		ReasonCode:      role + "_" + field,
	})
}
