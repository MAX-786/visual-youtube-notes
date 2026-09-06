package httpapi

import (
	"context"
	"errors"
	"strings"

	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/push"
)

func toPushSubscription(s *push.Subscription) PushSubscription {
	return PushSubscription{
		Id: s.ID, DeviceSessionId: s.DeviceSessionID, DeviceDisplayName: s.DeviceDisplayName, Endpoint: s.Endpoint,
		ExpirationTime: s.ExpirationTime, NotesEnabled: s.NotesEnabled, OpsEnabled: s.OpsEnabled, CreatedAt: s.CreatedAt,
		LastSuccessAt: s.LastSuccessAt, LastFailureAt: s.LastFailureAt, ConsecutiveFailures: s.ConsecutiveFailures,
	}
}

// GetVapidPublicKey is GET /push/vapid-public-key: derived at boot from the
// mounted private key (ADR 0014).
func (s *Server) GetVapidPublicKey(_ context.Context, _ GetVapidPublicKeyRequestObject) (GetVapidPublicKeyResponseObject, error) {
	if s.deps.Push == nil {
		return nil, apierr.New(apierr.NotFound, "Web Push is not configured on this server.")
	}
	return GetVapidPublicKey200JSONResponse(VapidPublicKey{PublicKey: s.deps.Push.Keys().Public}), nil
}

// ListPushSubscriptions is GET /push-subscriptions.
func (s *Server) ListPushSubscriptions(ctx context.Context, _ ListPushSubscriptionsRequestObject) (ListPushSubscriptionsResponseObject, error) {
	session := SessionFrom(ctx)
	subs, err := push.List(ctx, s.deps.Pool, session.UserID)
	if err != nil {
		return nil, apierr.Internal(err)
	}
	out := PushSubscriptionList{Subscriptions: make([]PushSubscription, 0, len(subs))}
	for i := range subs {
		out.Subscriptions = append(out.Subscriptions, toPushSubscription(&subs[i]))
	}
	return ListPushSubscriptions200JSONResponse(out), nil
}

// UpsertPushSubscription is PUT /push-subscriptions: bound to the calling
// PWA session; an extension session is refused.
func (s *Server) UpsertPushSubscription(ctx context.Context, req UpsertPushSubscriptionRequestObject) (UpsertPushSubscriptionResponseObject, error) {
	session := SessionFrom(ctx)
	if session.Kind != "pwa" {
		return nil, apierr.New(apierr.InvalidRequest, "Only the app can hold a push subscription.")
	}
	w := push.Write{
		Endpoint:       strings.TrimSpace(req.Body.Endpoint),
		P256dh:         req.Body.Keys.P256dh,
		Auth:           req.Body.Keys.Auth,
		ExpirationTime: req.Body.ExpirationTime,
		NotesEnabled:   true,
		OpsEnabled:     true,
	}
	if req.Body.NotesEnabled != nil {
		w.NotesEnabled = *req.Body.NotesEnabled
	}
	if req.Body.OpsEnabled != nil {
		w.OpsEnabled = *req.Body.OpsEnabled
	}
	if req.Body.ReplacesEndpoint != nil {
		w.ReplacesEndpoint = strings.TrimSpace(*req.Body.ReplacesEndpoint)
	}
	sub, err := push.Upsert(ctx, s.deps.Pool, session.UserID, session.ID, w)
	if err != nil {
		if strings.Contains(err.Error(), "push_subscriptions_") {
			return nil, apierr.New(apierr.InvalidRequest, "The subscription's keys are not in the shape a browser produces.")
		}
		return nil, apierr.Internal(err)
	}
	return UpsertPushSubscription200JSONResponse(toPushSubscription(sub)), nil
}

// UpdatePushSubscription is PATCH /push-subscriptions/{subscriptionId}.
func (s *Server) UpdatePushSubscription(ctx context.Context, req UpdatePushSubscriptionRequestObject) (UpdatePushSubscriptionResponseObject, error) {
	session := SessionFrom(ctx)
	if req.Body.NotesEnabled == nil && req.Body.OpsEnabled == nil {
		return nil, apierr.New(apierr.InvalidRequest, "Say which switch to change.")
	}
	sub, err := push.Update(ctx, s.deps.Pool, session.UserID, uuid.UUID(req.SubscriptionId), req.Body.NotesEnabled, req.Body.OpsEnabled)
	if errors.Is(err, push.ErrNotFound) {
		return nil, apierr.New(apierr.NotFound, "No such subscription.")
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return UpdatePushSubscription200JSONResponse(toPushSubscription(sub)), nil
}

// DeletePushSubscription is DELETE /push-subscriptions/{subscriptionId}.
func (s *Server) DeletePushSubscription(ctx context.Context, req DeletePushSubscriptionRequestObject) (DeletePushSubscriptionResponseObject, error) {
	session := SessionFrom(ctx)
	err := push.Delete(ctx, s.deps.Pool, session.UserID, uuid.UUID(req.SubscriptionId))
	if errors.Is(err, push.ErrNotFound) {
		return nil, apierr.New(apierr.NotFound, "No such subscription.")
	}
	if err != nil {
		return nil, apierr.Internal(err)
	}
	return DeletePushSubscription204Response{}, nil
}
