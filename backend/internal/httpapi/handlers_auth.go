package httpapi

import (
	"context"
	"errors"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
)

const (
	securityEventsDefaultLimit = 50
	securityEventsMaxLimit     = 200
)

// Login is POST /auth/login.
func (s *Server) Login(ctx context.Context, req LoginRequestObject) (LoginResponseObject, error) {
	r := requestFrom(ctx)
	deviceName := ""
	if req.Body.DeviceName != nil {
		deviceName = *req.Body.DeviceName
	}
	grant, err := s.deps.Auth.Login(ctx, req.Body.Username, req.Body.Password, deviceName, clientIP(r))
	if err != nil {
		return nil, authError(err)
	}
	setSessionCookies(writerFrom(ctx), grant)
	sg, err := s.sessionGrant(ctx, grant)
	if err != nil {
		return nil, err
	}
	return Login200JSONResponse(sg), nil
}

// RefreshSession is POST /auth/refresh, cookie transport only.
func (s *Server) RefreshSession(ctx context.Context, _ RefreshSessionRequestObject) (RefreshSessionResponseObject, error) {
	r := requestFrom(ctx)
	presented := refreshCookieValue(r)
	if presented == "" {
		return nil, apierr.New(apierr.Unauthenticated, "Sign in to continue.")
	}
	grant, err := s.deps.Auth.Refresh(ctx, presented, clientIP(r))
	if err != nil {
		clearSessionCookies(writerFrom(ctx))
		return nil, authError(err)
	}
	setSessionCookies(writerFrom(ctx), grant)
	sg, err := s.sessionGrant(ctx, grant)
	if err != nil {
		return nil, err
	}
	return RefreshSession200JSONResponse(sg), nil
}

// RecoverCsrfToken is GET /auth/refresh/csrf.
func (s *Server) RecoverCsrfToken(ctx context.Context, _ RecoverCsrfTokenRequestObject) (RecoverCsrfTokenResponseObject, error) {
	presented := refreshCookieValue(requestFrom(ctx))
	if presented == "" {
		return nil, apierr.New(apierr.Unauthenticated, "Sign in to continue.")
	}
	token, err := s.deps.Auth.RotateCSRF(ctx, presented)
	if err != nil {
		return nil, authError(err)
	}
	noStore := "no-store"
	return RecoverCsrfToken200JSONResponse{
		Body:    CsrfToken{CsrfToken: token},
		Headers: RecoverCsrfToken200ResponseHeaders{CacheControl: &noStore},
	}, nil
}

// LogoutViaRefresh is POST /auth/refresh/logout.
func (s *Server) LogoutViaRefresh(ctx context.Context, _ LogoutViaRefreshRequestObject) (LogoutViaRefreshResponseObject, error) {
	presented := refreshCookieValue(requestFrom(ctx))
	clearSessionCookies(writerFrom(ctx))
	if presented == "" {
		return nil, apierr.New(apierr.Unauthenticated, "Sign in to continue.")
	}
	if err := s.deps.Auth.LogoutViaRefresh(ctx, presented); err != nil {
		return nil, authError(err)
	}
	return LogoutViaRefresh204Response{}, nil
}

// Logout is POST /auth/logout.
func (s *Server) Logout(ctx context.Context, _ LogoutRequestObject) (LogoutResponseObject, error) {
	session := SessionFrom(ctx)
	clearSessionCookies(writerFrom(ctx))
	if err := s.deps.Auth.RevokeSession(ctx, session.UserID, session.ID, "signed_out"); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.Internal(err)
	}
	return Logout204Response{}, nil
}

// GetSession is GET /auth/session, the foreground gate.
func (s *Server) GetSession(ctx context.Context, _ GetSessionRequestObject) (GetSessionResponseObject, error) {
	session := SessionFrom(ctx)
	account, err := s.deps.Auth.GetAccount(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	cursor, err := s.deps.Auth.SyncCursor(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	device, err := s.currentDevice(ctx, session)
	if err != nil {
		return nil, err
	}
	return GetSession200JSONResponse(SessionInfo{
		Session:         device,
		Account:         toAccount(account),
		AccessExpiresAt: session.AccessExpiresAt,
		SyncCursor:      cursor,
	}), nil
}

// GetAccount is GET /auth/account.
func (s *Server) GetAccount(ctx context.Context, _ GetAccountRequestObject) (GetAccountResponseObject, error) {
	account, err := s.deps.Auth.GetAccount(ctx, SessionFrom(ctx).UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return GetAccount200JSONResponse(toAccount(account)), nil
}

// UpdateAccount is PATCH /auth/account.
func (s *Server) UpdateAccount(ctx context.Context, req UpdateAccountRequestObject) (UpdateAccountResponseObject, error) {
	account, err := s.deps.Auth.UpdateAccount(ctx, SessionFrom(ctx).UserID, req.Body.DisplayName, req.Body.Timezone)
	if err != nil {
		if req.Body.Timezone != nil {
			return nil, apierr.New(apierr.InvalidRequest, "That is not an IANA timezone name, such as Asia/Kolkata.").With("field", "timezone")
		}
		return nil, apierr.Internal(err)
	}
	return UpdateAccount200JSONResponse(toAccount(account)), nil
}

// ChangePassword is POST /auth/account/password.
func (s *Server) ChangePassword(ctx context.Context, req ChangePasswordRequestObject) (ChangePasswordResponseObject, error) {
	session := SessionFrom(ctx)
	grant, err := s.deps.Auth.ChangePassword(ctx, auth.Session{ID: session.ID, UserID: session.UserID, Kind: session.Kind, ExtensionID: session.ExtensionID}, req.Body.CurrentPassword, req.Body.NewPassword, clientIP(requestFrom(ctx)))
	if err != nil {
		if errors.Is(err, auth.ErrUnauthenticated) {
			return nil, apierr.New(apierr.Unauthenticated, "The current password did not match.")
		}
		return nil, apierr.New(apierr.InvalidRequest, err.Error()).With("field", "new_password")
	}
	setSessionCookies(writerFrom(ctx), grant)
	sg, err := s.sessionGrant(ctx, grant)
	if err != nil {
		return nil, err
	}
	return ChangePassword200JSONResponse(sg), nil
}

// ListSessions is GET /auth/sessions.
func (s *Server) ListSessions(ctx context.Context, _ ListSessionsRequestObject) (ListSessionsResponseObject, error) {
	session := SessionFrom(ctx)
	rows, err := s.deps.Auth.ListSessions(ctx, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := DeviceSessionList{Sessions: make([]DeviceSession, 0, len(rows))}
	for _, d := range rows {
		out.Sessions = append(out.Sessions, toDeviceSession(d, session.ID))
	}
	return ListSessions200JSONResponse(out), nil
}

// RevokeSession is DELETE /auth/sessions/{sessionId}.
func (s *Server) RevokeSession(ctx context.Context, req RevokeSessionRequestObject) (RevokeSessionResponseObject, error) {
	session := SessionFrom(ctx)
	err := s.deps.Auth.RevokeSession(ctx, session.UserID, uuid.UUID(req.SessionId), "owner_revoked")
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, apierr.New(apierr.NotFound, "No such session. It may already be signed out.")
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	if uuid.UUID(req.SessionId) == session.ID {
		clearSessionCookies(writerFrom(ctx))
	}
	return RevokeSession204Response{}, nil
}

// RevokeAllSessions is POST /auth/sessions/revoke-all.
func (s *Server) RevokeAllSessions(ctx context.Context, _ RevokeAllSessionsRequestObject) (RevokeAllSessionsResponseObject, error) {
	if err := s.deps.Auth.RevokeAll(ctx, SessionFrom(ctx).UserID); err != nil {
		return nil, apierr.Internal(err)
	}
	clearSessionCookies(writerFrom(ctx))
	return RevokeAllSessions204Response{}, nil
}

// ListSecurityEvents is GET /auth/security-events.
func (s *Server) ListSecurityEvents(ctx context.Context, req ListSecurityEventsRequestObject) (ListSecurityEventsResponseObject, error) {
	limit := securityEventsDefaultLimit
	if req.Params.Limit != nil {
		limit = min(max(*req.Params.Limit, 1), securityEventsMaxLimit)
	}
	var before int64
	if req.Params.Cursor != nil && *req.Params.Cursor != "" {
		n, err := strconv.ParseInt(*req.Params.Cursor, 10, 64)
		if err != nil {
			return nil, apierr.New(apierr.InvalidRequest, "The cursor is not one this list issued.").With("field", "cursor")
		}
		before = n
	}
	rows, err := s.deps.Auth.ListSecurityEvents(ctx, SessionFrom(ctx).UserID, before, limit+1)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := SecurityEventList{Events: make([]SecurityEvent, 0, len(rows))}
	for i, e := range rows {
		if i == limit {
			next := strconv.FormatInt(rows[i-1].ID, 10)
			out.NextCursor = &next
			break
		}
		var sessionID *Uuid
		if e.DeviceSessionID != nil {
			id := Uuid(*e.DeviceSessionID)
			sessionID = &id
		}
		out.Events = append(out.Events, SecurityEvent{
			Id:              e.ID,
			EventType:       SecurityEventType(e.Type),
			DeviceSessionId: sessionID,
			SourceIp:        e.SourceIP,
			ReasonCode:      e.ReasonCode,
			ExtensionId:     e.ExtensionID,
			OccurredAt:      e.OccurredAt,
		})
	}
	return ListSecurityEvents200JSONResponse(out), nil
}

// sessionGrant assembles the response a login, refresh or password change
// returns.
func (s *Server) sessionGrant(ctx context.Context, grant auth.Grant) (SessionGrant, error) {
	account, err := s.deps.Auth.GetAccount(ctx, grant.Session.UserID)
	if err != nil {
		return SessionGrant{}, apierr.Internal(err)
	}
	cursor, err := s.deps.Auth.SyncCursor(ctx, grant.Session.UserID)
	if err != nil {
		return SessionGrant{}, apierr.Internal(err)
	}
	device, err := s.currentDevice(ctx, &Session{ID: grant.Session.ID, UserID: grant.Session.UserID})
	if err != nil {
		return SessionGrant{}, err
	}
	return SessionGrant{
		Session:         device,
		Account:         toAccount(account),
		CsrfToken:       grant.CSRFToken,
		AccessExpiresAt: grant.AccessExpiresAt,
		SyncCursor:      cursor,
	}, nil
}

func (s *Server) currentDevice(ctx context.Context, session *Session) (DeviceSession, error) {
	rows, err := s.deps.Auth.ListSessions(ctx, session.UserID)
	if err != nil {
		return DeviceSession{}, apierr.Internal(err)
	}
	for _, d := range rows {
		if d.ID == session.ID {
			return toDeviceSession(d, session.ID), nil
		}
	}
	return DeviceSession{}, apierr.New(apierr.Unauthenticated, "Sign in to continue.")
}

func toAccount(a auth.Account) Account {
	return Account{
		Id:                Uuid(a.ID),
		Username:          a.Username,
		DisplayName:       a.DisplayName,
		Timezone:          a.Timezone,
		CreatedAt:         a.CreatedAt,
		PasswordChangedAt: a.PasswordChangedAt,
	}
}

func toDeviceSession(d auth.DeviceSession, current uuid.UUID) DeviceSession {
	return DeviceSession{
		Id:                Uuid(d.ID),
		Kind:              DeviceSessionKind(d.Kind),
		DisplayName:       d.DisplayName,
		ExtensionId:       d.ExtensionID,
		CreatedAt:         d.CreatedAt,
		LastSeenAt:        d.LastSeenAt,
		LastRefreshedAt:   d.LastRefreshedAt,
		InactiveExpiresAt: d.InactiveExpiresAt,
		IsCurrent:         d.ID == current,
		PushSubscriptions: d.PushSubscriptions,
	}
}

// authError maps service failures onto the envelope. Every credential failure
// is the same unauthenticated answer (ADR 0003).
func authError(err error) error {
	if errors.Is(err, auth.ErrUnauthenticated) {
		return apierr.New(apierr.Unauthenticated, "That username and password did not match.")
	}
	return apierr.Internal(err)
}
