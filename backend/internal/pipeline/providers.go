package pipeline

import (
	"context"
	"errors"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
)

// roleLabel is how a role is named in a sentence for the Owner.
func roleLabel(role string) string {
	switch role {
	case ai.RoleNoteGeneration:
		return "note generation"
	case ai.RoleEmbeddings:
		return "embeddings"
	case ai.RoleTranscription:
		return "transcription"
	default:
		return role
	}
}

// credentialsError turns a failure to read a role's key into a permanent,
// explained Stage failure: an unconfigured role names Settings → AI (spec 15).
func credentialsError(role string, err error) error {
	switch {
	case errors.Is(err, ai.ErrNoKey):
		return Fail(Permanent, "roles_unconfigured", capitalize(roleLabel(role))+" is not configured. Add a provider key in Settings → AI.", err)
	case errors.Is(err, ai.ErrCiphertextInvalid), errors.Is(err, ai.ErrKeyVersionMissing):
		return Fail(Permanent, "key_unreadable", "The stored "+roleLabel(role)+" key could not be unsealed. Enter it again in Settings → AI.", err)
	default:
		return err
	}
}

// providerFailure classifies a provider call's error. A 401 or 403 clears
// the role's verification so a key revoked upstream shows as revoked in
// Settings (ADR 0010); rate limits and outages ride the retry ladder; any
// other rejection is permanent and carries the provider's own message.
func providerFailure(ctx context.Context, d *Deps, userID uuid.UUID, role string, err error) error {
	var pe *ai.ProviderError
	if !errors.As(err, &pe) {
		return Fail(Transient, "provider_error", "The model provider call failed. It will be retried.", err)
	}
	switch {
	case pe.Unauthorized():
		if d.AI != nil {
			if markErr := d.AI.MarkVerificationError(context.WithoutCancel(ctx), d.Pool, userID, role, pe.Error()); markErr != nil {
				d.Logger.Warn("could not record the rejected key", "role", role, "err", markErr)
			}
		}
		return Fail(Permanent, "provider_unauthorized", "The provider rejected the "+roleLabel(role)+" key. Check it in Settings → AI.", err)
	case pe.Transient():
		return Fail(Transient, "provider_unavailable", "The model provider is unavailable or rate-limiting. It will be retried.", err)
	default:
		return Fail(Permanent, "provider_rejected", "The provider rejected the "+roleLabel(role)+" request: "+pe.Message, err)
	}
}

// openCall writes the ledger row before a provider call (ADR 0007).
func openCall(ctx context.Context, d *Deps, note *NoteRow, sr *StageRun, creds *ai.Credentials, estimate ai.UnitMap) (uuid.UUID, error) {
	version := sr.Run.ProcessingVersion
	return ai.OpenUsage(ctx, d.Pool, ai.Call{
		UserID:            note.UserID,
		NoteID:            &note.ID,
		AttemptID:         &sr.AttemptID,
		ProcessingVersion: &version,
		Role:              creds.Role,
		Provider:          creds.Provider,
		Model:             creds.Model,
		Estimated:         estimate,
	})
}

// closeCall completes the ledger row after the call, whatever happened; a
// failure to record is logged, never allowed to fail the Stage twice.
func closeCall(ctx context.Context, d *Deps, usageID uuid.UUID, reported ai.UnitMap, requestID string, callErr error) {
	outcome := ai.Outcome{Reported: reported, ProviderRequestID: requestID}
	if callErr != nil {
		outcome.Reported = nil
		outcome.ErrorCode = "provider_error"
		var pe *ai.ProviderError
		if errors.As(callErr, &pe) {
			outcome.ErrorCode = providerErrorCode(pe)
			if outcome.ProviderRequestID == "" {
				outcome.ProviderRequestID = pe.RequestID
			}
		}
	}
	if err := ai.CloseUsage(context.WithoutCancel(ctx), d.Pool, usageID, outcome); err != nil {
		d.Logger.Error("could not close the usage row", "usage", usageID, "err", err)
	}
}

func providerErrorCode(pe *ai.ProviderError) string {
	switch {
	case pe.Status == 0:
		return "provider_unreachable"
	case pe.Unauthorized():
		return "provider_unauthorized"
	case pe.Status == 429:
		return "provider_rate_limited"
	case pe.Status >= 500:
		return "provider_unavailable"
	default:
		return "provider_rejected"
	}
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return string([]rune(s)[0]-32) + s[1:]
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
