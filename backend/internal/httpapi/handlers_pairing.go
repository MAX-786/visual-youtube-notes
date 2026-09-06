package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
)

func toPairingRequest(p *auth.PairingRequest) PairingRequest {
	return PairingRequest{
		RequestId: p.RequestID, ExtensionId: p.ExtensionID, DisplayName: p.DisplayName,
		Status: PairingRequestStatus(p.Status), CreatedAt: p.CreatedAt, ExpiresAt: p.ExpiresAt,
	}
}

func pairingError(err error) error {
	switch {
	case errors.Is(err, auth.ErrPairingNotFound):
		return apierr.New(apierr.NotFound, "No such pairing request. It may have expired.")
	case errors.Is(err, auth.ErrPairingDecided):
		return apierr.New(apierr.PairingDecided, "That pairing request was already decided.")
	case errors.Is(err, auth.ErrPairingExpired):
		return apierr.New(apierr.PairingExpired, "That pairing request expired. Start pairing again from the extension.")
	case errors.Is(err, auth.ErrPairingNotApproved):
		return apierr.New(apierr.PairingNotApproved, "Not approved yet.")
	case errors.Is(err, auth.ErrPairingDenied):
		return apierr.New(apierr.PairingDenied, "The pairing was denied.")
	case errors.Is(err, auth.ErrPairingRejected):
		return apierr.New(apierr.PairingRejected, "The pairing exchange was rejected.")
	default:
		return apierr.Internal(err)
	}
}

// CreatePairingRequest is POST /pairing/requests, unauthenticated and
// CORS-enabled for the extension origin only (the server's CORS layer
// enforces that). The extension id must be the one the server allowlists.
func (s *Server) CreatePairingRequest(ctx context.Context, req CreatePairingRequestRequestObject) (CreatePairingRequestResponseObject, error) {
	if req.Body.ExtensionId != s.deps.Config.ExtensionID {
		return nil, apierr.New(apierr.OriginRejected, "That extension is not the one this server pairs with.")
	}
	name := strings.TrimSpace(req.Body.DisplayName)
	if name == "" {
		name = "Chromium browser"
	}
	created, err := s.deps.Auth.CreatePairingRequest(ctx, req.Body.Challenge, req.Body.ExtensionId, name)
	if err != nil {
		if strings.HasPrefix(err.Error(), "challenge:") {
			return nil, apierr.New(apierr.InvalidRequest, "The challenge must be 32 bytes, base64url.").With("field", "challenge")
		}
		return nil, apierr.Internal(err)
	}
	return CreatePairingRequest201JSONResponse(PairingRequestCreated{
		RequestId:   created.RequestID,
		ExpiresAt:   created.ExpiresAt,
		ApprovalUrl: strings.TrimRight(s.deps.Config.PublicOrigin, "/") + "/pair/" + created.RequestID,
	}), nil
}

// GetPairingRequest is GET /pairing/requests/{requestId}.
func (s *Server) GetPairingRequest(ctx context.Context, req GetPairingRequestRequestObject) (GetPairingRequestResponseObject, error) {
	p, err := s.deps.Auth.GetPairingRequest(ctx, req.RequestId)
	if err != nil {
		return nil, pairingError(err)
	}
	return GetPairingRequest200JSONResponse(toPairingRequest(p)), nil
}

// ApprovePairingRequest is POST /pairing/requests/{requestId}/approve.
func (s *Server) ApprovePairingRequest(ctx context.Context, req ApprovePairingRequestRequestObject) (ApprovePairingRequestResponseObject, error) {
	session := SessionFrom(ctx)
	p, err := s.deps.Auth.DecidePairingRequest(ctx, session.UserID, req.RequestId, true, clientIP(requestFrom(ctx)))
	if err != nil {
		return nil, pairingError(err)
	}
	return ApprovePairingRequest200JSONResponse(toPairingRequest(p)), nil
}

// DenyPairingRequest is POST /pairing/requests/{requestId}/deny.
func (s *Server) DenyPairingRequest(ctx context.Context, req DenyPairingRequestRequestObject) (DenyPairingRequestResponseObject, error) {
	session := SessionFrom(ctx)
	p, err := s.deps.Auth.DecidePairingRequest(ctx, session.UserID, req.RequestId, false, clientIP(requestFrom(ctx)))
	if err != nil {
		return nil, pairingError(err)
	}
	return DenyPairingRequest200JSONResponse(toPairingRequest(p)), nil
}

func (s *Server) tokenGrant(ctx context.Context, g auth.Grant) (TokenGrant, error) {
	row, err := s.deps.Auth.SessionRow(ctx, g.Session.ID)
	if err != nil {
		return TokenGrant{}, apierr.Internal(err)
	}
	return TokenGrant{
		AccessToken:     g.AccessToken,
		RefreshToken:    g.RefreshToken,
		AccessExpiresAt: g.AccessExpiresAt,
		Session:         toDeviceSession(row, g.Session.ID),
	}, nil
}

// ExchangePairingVerifier is POST /pairing/exchange: polled by the extension
// until the Owner decides; success returns the only copy of the tokens.
func (s *Server) ExchangePairingVerifier(ctx context.Context, req ExchangePairingVerifierRequestObject) (ExchangePairingVerifierResponseObject, error) {
	grant, err := s.deps.Auth.ExchangePairing(ctx, req.Body.RequestId, req.Body.Verifier, s.deps.Config.ExtensionID, clientIP(requestFrom(ctx)))
	if err != nil {
		return nil, pairingError(err)
	}
	out, err := s.tokenGrant(ctx, grant)
	if err != nil {
		return nil, err
	}
	return ExchangePairingVerifier200JSONResponse(out), nil
}

// RefreshExtensionTokens is POST /pairing/refresh: the bearer refresh.
func (s *Server) RefreshExtensionTokens(ctx context.Context, req RefreshExtensionTokensRequestObject) (RefreshExtensionTokensResponseObject, error) {
	grant, err := s.deps.Auth.Refresh(ctx, req.Body.RefreshToken, clientIP(requestFrom(ctx)))
	if err != nil {
		return nil, apierr.New(apierr.Unauthenticated, "Pair this browser again.")
	}
	out, err := s.tokenGrant(ctx, grant)
	if err != nil {
		return nil, err
	}
	return RefreshExtensionTokens200JSONResponse(out), nil
}

// LogoutExtension is POST /pairing/logout: revokes the calling session.
func (s *Server) LogoutExtension(ctx context.Context, _ LogoutExtensionRequestObject) (LogoutExtensionResponseObject, error) {
	session := SessionFrom(ctx)
	if err := s.deps.Auth.RevokeSession(ctx, session.UserID, session.ID, "signed_out"); err != nil {
		return nil, apierr.Internal(err)
	}
	return LogoutExtension204Response{}, nil
}
