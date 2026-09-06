package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/routers"
	"github.com/google/uuid"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/contract"
)

type contextKey int

const (
	routeKey contextKey = iota
	sessionKey
	transportKey
)

// Transport is how the request authenticated: the PWA's cookie, the
// extension's bearer token, or nothing.
type Transport string

// The transports.
const (
	TransportNone   Transport = ""
	TransportCookie Transport = "cookie"
	TransportBearer Transport = "bearer"
)

// Session is the authenticated Device session behind a request.
type Session struct {
	ID              uuid.UUID
	UserID          uuid.UUID
	Kind            string
	ExtensionID     string
	Transport       Transport
	CSRFHash        []byte
	AccessExpiresAt time.Time
	// CSRFOK is set once a cookie-transport mutation has passed the CSRF check.
	CSRFOK bool
}

type resolved struct {
	route  *routers.Route
	params map[string]string
	op     *contract.Operation
}

func withResolved(ctx context.Context, r *resolved) context.Context {
	return context.WithValue(ctx, routeKey, r)
}

func resolvedFrom(ctx context.Context) *resolved {
	r, _ := ctx.Value(routeKey).(*resolved)
	return r
}

// WithSession attaches a session to a context.
func WithSession(ctx context.Context, s *Session) context.Context {
	return context.WithValue(ctx, sessionKey, s)
}

// SessionFrom returns the session behind a context, or nil.
func SessionFrom(ctx context.Context) *Session {
	s, _ := ctx.Value(sessionKey).(*Session)
	return s
}

func withTransport(ctx context.Context, t Transport) context.Context {
	return context.WithValue(ctx, transportKey, t)
}

// TransportFrom returns how the request authenticated, whether or not the
// credential verified.
func TransportFrom(ctx context.Context) Transport {
	t, _ := ctx.Value(transportKey).(Transport)
	return t
}

// clientIP is the address a request came from. Cloudflare's header is taken
// when present; it is forgeable while the origin is reachable directly, which
// is why the per-address rate-limit bucket is only a partition and the global
// bucket is the guarantee (ADR 0016).
func clientIP(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get("CF-Connecting-IP")); v != "" {
		if ip := net.ParseIP(v); ip != nil {
			return ip.String()
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
