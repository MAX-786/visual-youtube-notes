// Package httpapi is the HTTP surface of the API: the generated strict server
// contract, the middleware chain every /api request passes through, and the
// handlers that implement the operations.
package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/ai"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/audit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/contract"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/edit"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/notes"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/pipeline"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/ratelimit"
	vsync "github.com/MAX-786/visual-youtube-notes/backend/internal/sync"
)

// Deps is everything the router needs. Pool, Audit and Auth may be nil in
// tests that stay in front of the handlers.
type Deps struct {
	Config   *config.Config
	Pool     *pgxpool.Pool
	Contract *contract.Contract
	Limiter  *ratelimit.Limiter
	Logger   *slog.Logger
	Audit    *audit.Recorder
	Auth     *auth.Service
	Sync     *vsync.Broker
	// Submissions and Notes are the pipeline's write and read sides.
	Submissions *pipeline.Service
	Notes       *notes.Reader
	// AI is the Processing role configuration; AITransport, when set, is the
	// no-network double the tests inject beneath the provider clients.
	AI          *ai.Store
	AITransport http.RoundTripper
	// Edit is the write side of a ready Note (ADR 0004).
	Edit *edit.Service
	// Authenticate identifies the Device session behind a request, or returns
	// nil when the presented credential is absent or invalid. Every credential
	// failure looks the same to the client (ADR 0003).
	Authenticate func(ctx context.Context, transport Transport, credential string) *Session
}

// Server implements the generated StrictServerInterface. Operations that no
// slice has implemented yet live in handlers_stub.go, which go generate keeps
// in step with the interface.
type Server struct {
	deps Deps
}

// AccessCookie is the PWA's access cookie (ADR 0003).
const AccessCookie = "__Host-vyn_access"

// concurrencyWait is how long a request waits for a concurrency slot before
// it is refused. Argon2id verifications and PDF renders take well under it.
const concurrencyWait = 5 * time.Second

// NewRouter assembles the whole HTTP surface.
func NewRouter(deps Deps) http.Handler {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(requestLogger(deps.Logger))
	r.Use(recoverer(deps.Logger))
	r.Get("/healthz", healthz(deps))

	r.Route("/api", func(api chi.Router) {
		api.Use(corsMiddleware(deps))
		api.Use(resolveOperation(deps))
		api.Use(identifySession(deps))
		api.Use(rateLimitMiddleware(deps))
		api.Use(enforceSecurity(deps))
		api.Use(validateRequest(deps))
		api.NotFound(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(apierr.NotFound, "There is no such operation. Check the path against the contract."))
		})
		api.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
			apierr.Write(w, apierr.New(apierr.NotFound, "That method is not defined for this path."))
		})

		server := &Server{deps: deps}
		// The event stream is in the contract but outside code generation
		// (ADR 0015); it is mounted by hand behind the same middleware chain.
		if deps.Sync != nil {
			api.Get("/events", server.streamEvents)
		}
		strict := NewStrictHandlerWithOptions(server, []StrictMiddlewareFunc{exposeHTTP}, StrictHTTPServerOptions{
			RequestErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				apierr.Write(w, apierr.New(apierr.InvalidRequest, "The request could not be read. Send valid JSON with the documented fields.").With("reason", err.Error()))
			},
			ResponseErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
				e := apierr.From(err)
				if e.Code == apierr.InternalError {
					deps.Logger.Error("handler failed", "path", r.URL.Path, "err", err, "request_id", middleware.GetReqID(r.Context()))
				}
				apierr.Write(w, e)
			},
		})
		HandlerWithOptions(strict, ChiServerOptions{
			BaseRouter: api,
			ErrorHandlerFunc: func(w http.ResponseWriter, _ *http.Request, err error) {
				apierr.Write(w, apierr.New(apierr.InvalidRequest, "A path or query parameter is malformed.").With("reason", err.Error()))
			},
		})
	})
	return r
}

func healthz(deps Deps) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		if deps.Pool == nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"no database"}`))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := deps.Pool.Ping(ctx); err != nil {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"status":"database unreachable"}`))
			return
		}
		_, _ = w.Write([]byte(`{"status":"ok"}`))
	}
}

// requestLogger logs one line per request. Never the query string: spec 15
// keeps shared URLs and query parameters out of logs.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			log.Info("request",
				"method", r.Method,
				"path", r.URL.Path,
				"status", ww.Status(),
				"bytes", ww.BytesWritten(),
				"duration_ms", time.Since(start).Milliseconds(),
				"request_id", middleware.GetReqID(r.Context()),
			)
		})
	}
}

func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if rec := recover(); rec != nil {
					if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
						panic(rec)
					}
					log.Error("panic", "path", r.URL.Path, "panic", rec, "request_id", middleware.GetReqID(r.Context()))
					apierr.Write(w, apierr.From(errors.New("panic")))
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// corsMiddleware admits exactly the operations marked x-extension-callable
// from exactly the allowlisted extension origin (ADR 0003, ADR 0015). Every
// other origin gets no CORS headers at all, which the browser turns into a
// refusal; the extension origin on a non-callable operation is refused
// outright and recorded.
func corsMiddleware(deps Deps) func(http.Handler) http.Handler {
	extensionOrigin := deps.Config.ExtensionOrigin()
	allow := func(w http.ResponseWriter) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", extensionOrigin)
		h.Set("Access-Control-Allow-Methods", "GET, POST, DELETE")
		h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		h.Set("Access-Control-Max-Age", "600")
		h.Add("Vary", "Origin")
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				// Preflight. Resolve the operation the browser is asking about.
				probe := r.Clone(r.Context())
				probe.Method = r.Header.Get("Access-Control-Request-Method")
				if route, _, err := deps.Contract.Resolve(probe); err == nil {
					if op := deps.Contract.OperationFor(route); op != nil && op.ExtensionCallable && origin == extensionOrigin {
						allow(w)
					}
				}
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if origin == extensionOrigin {
				route, _, err := deps.Contract.Resolve(r)
				op := deps.Contract.OperationFor(route)
				if err != nil || op == nil || !op.ExtensionCallable {
					deps.Audit.Record(r.Context(), audit.Event{Type: audit.OriginRejected, SourceIP: clientIP(r), ReasonCode: "extension_origin_not_allowed"})
					apierr.Write(w, apierr.New(apierr.OriginRejected, "The extension may not call this operation."))
					return
				}
				allow(w)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// resolveOperation matches the request against the contract once and keeps
// the result for the middlewares behind it.
func resolveOperation(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			route, params, err := deps.Contract.Resolve(r)
			if err != nil {
				switch {
				case contract.IsNotFound(err):
					apierr.Write(w, apierr.New(apierr.NotFound, "There is no such operation. Check the path against the contract."))
				case contract.IsMethodNotAllowed(err):
					apierr.Write(w, apierr.New(apierr.NotFound, "That method is not defined for this path."))
				default:
					apierr.Write(w, apierr.Internal(err))
				}
				return
			}
			op := deps.Contract.OperationFor(route)
			if op == nil {
				apierr.Write(w, apierr.Internal(errors.New("route without operation")))
				return
			}
			next.ServeHTTP(w, r.WithContext(withResolved(r.Context(), &resolved{route: route, params: params, op: op})))
		})
	}
}

// identifySession reads the credential the request carries, refuses mixed
// transports, and attaches the Device session when the credential verifies.
// It never rejects an absent or invalid credential; enforceSecurity does that
// after the rate limiter has had its say, so unauthenticated hammering of an
// authenticated operation is still limited.
func identifySession(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			var cookieValue, bearer string
			if c, err := r.Cookie(AccessCookie); err == nil && c.Value != "" {
				cookieValue = c.Value
			}
			if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
				bearer = strings.TrimSpace(auth[len("bearer "):])
			}
			ctx := r.Context()
			switch {
			case cookieValue != "" && bearer != "":
				apierr.Write(w, apierr.New(apierr.MixedTransport, "Send either the session cookie or a bearer token, never both."))
				return
			case cookieValue != "":
				ctx = withTransport(ctx, TransportCookie)
				if deps.Authenticate != nil {
					if s := deps.Authenticate(ctx, TransportCookie, cookieValue); s != nil {
						s.Transport = TransportCookie
						ctx = WithSession(ctx, s)
					}
				}
			case bearer != "":
				ctx = withTransport(ctx, TransportBearer)
				if deps.Authenticate != nil {
					if s := deps.Authenticate(ctx, TransportBearer, bearer); s != nil {
						s.Transport = TransportBearer
						ctx = WithSession(ctx, s)
					}
				}
			}
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// rateLimitMiddleware applies the operation's class. Session-keyed classes
// fall back to the client address when no session is present, so an
// unauthenticated request to an authenticated operation still draws on a
// bucket.
func rateLimitMiddleware(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res := resolvedFrom(r.Context())
			if res == nil || deps.Limiter == nil {
				next.ServeHTTP(w, r)
				return
			}
			class, ok := deps.Limiter.Class(res.op.RateLimitClass)
			if !ok {
				next.ServeHTTP(w, r)
				return
			}
			ip := clientIP(r)
			kind, key, sessionID := "ip", ip, ""
			if class.Key == "session" {
				if s := SessionFrom(r.Context()); s != nil {
					kind, key, sessionID = "session", s.ID.String(), s.ID.String()
				}
			}
			if retryAfter, admitted := deps.Limiter.Check(r.Context(), class, kind, key, ip, sessionID); !admitted {
				writeRateLimited(w, class.Name, retryAfter)
				return
			}
			release, admitted := deps.Limiter.Acquire(r.Context(), class, kind, key, ip, sessionID, concurrencyWait)
			if !admitted {
				writeRateLimited(w, class.Name, concurrencyWait)
				return
			}
			defer release()
			next.ServeHTTP(w, r)
		})
	}
}

func writeRateLimited(w http.ResponseWriter, class string, retryAfter time.Duration) {
	seconds := ratelimit.RetryAfterSeconds(retryAfter)
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	apierr.Write(w, apierr.Newf(apierr.RateLimited, "Too many requests for now. Wait %d seconds and try again.", seconds).
		With("class", class).
		With("retry_after_seconds", seconds))
}

// enforceSecurity turns the operation's security requirement into a decision:
// public operations pass, and everything else needs a verified session on a
// transport the operation admits.
func enforceSecurity(deps Deps) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res := resolvedFrom(r.Context())
			if res == nil || res.op.Public {
				next.ServeHTTP(w, r)
				return
			}
			session := SessionFrom(r.Context())
			switch {
			case session == nil:
				apierr.Write(w, apierr.New(apierr.Unauthenticated, "Sign in to continue."))
				return
			case session.Transport == TransportCookie && !res.op.AcceptsCookie,
				session.Transport == TransportBearer && !res.op.AcceptsBearer:
				apierr.Write(w, apierr.New(apierr.Unauthenticated, "This operation is not available to this kind of session."))
				return
			}
			if session.Transport == TransportCookie && r.Method != http.MethodGet && r.Method != http.MethodHead {
				if err := checkCSRF(deps, r, session); err != nil {
					deps.Audit.Record(r.Context(), audit.Event{Type: audit.OriginRejected, UserID: &session.UserID, DeviceSessionID: &session.ID, SourceIP: clientIP(r), ReasonCode: string(err.Code)})
					apierr.Write(w, err)
					return
				}
				session.CSRFOK = true
			}
			next.ServeHTTP(w, r)
		})
	}
}

// checkCSRF is the cookie transport's mutation guard (ADR 0003): an exact
// source-origin match, a non-simple JSON content type, and the session-bound
// token in X-CSRF-Token. The token comparison itself is the session store's
// job; VerifyCSRF is wired by the auth slice and a nil hook fails closed.
func checkCSRF(deps Deps, r *http.Request, session *Session) *apierr.Error {
	origin := r.Header.Get("Origin")
	if origin == "" {
		if referer := r.Header.Get("Referer"); referer != "" {
			if idx := strings.Index(referer[strings.Index(referer, "://")+3:], "/"); idx >= 0 {
				origin = referer[:strings.Index(referer, "://")+3+idx]
			} else {
				origin = referer
			}
		}
	}
	if origin != deps.Config.PublicOrigin {
		return apierr.New(apierr.OriginRejected, "Requests must come from the app itself.")
	}
	contentType := r.Header.Get("Content-Type")
	if r.ContentLength != 0 && !strings.HasPrefix(strings.ToLower(contentType), "application/json") {
		return apierr.New(apierr.CSRFRejected, "Send JSON.")
	}
	token := r.Header.Get("X-CSRF-Token")
	presented, err := auth.CSRFHashOf(token)
	if err != nil || len(session.CSRFHash) == 0 || subtle.ConstantTimeCompare(presented, session.CSRFHash) != 1 {
		return apierr.New(apierr.CSRFRejected, "The request is missing its CSRF token. Reload the app and try again.")
	}
	return nil
}

// validateRequest checks parameters and body against the contract before any
// handler runs (ADR 0015). Security is not validated here: enforceSecurity
// already decided it, and the validator's own notion would need the same
// session lookup a second time.
func validateRequest(deps Deps) func(http.Handler) http.Handler {
	_ = deps
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			res := resolvedFrom(r.Context())
			if res == nil {
				next.ServeHTTP(w, r)
				return
			}
			input := &openapi3filter.RequestValidationInput{
				Request:    r,
				PathParams: res.params,
				Route:      res.route,
				Options: &openapi3filter.Options{
					AuthenticationFunc: openapi3filter.NoopAuthenticationFunc,
				},
			}
			if err := openapi3filter.ValidateRequest(r.Context(), input); err != nil {
				apierr.Write(w, validationError(err))
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func validationError(err error) *apierr.Error {
	e := apierr.New(apierr.InvalidRequest, "The request does not match the contract. Fix the field named in details.")
	var reqErr *openapi3filter.RequestError
	if errors.As(err, &reqErr) {
		field := ""
		if reqErr.Parameter != nil {
			field = reqErr.Parameter.Name
		}
		var schemaErr *openapi3.SchemaError
		switch {
		case errors.As(reqErr.Err, &schemaErr):
			if pointer := schemaErr.JSONPointer(); len(pointer) > 0 {
				field = strings.Join(pointer, ".")
			}
			e = e.With("reason", schemaErr.Reason)
		case reqErr.Err != nil:
			e = e.With("reason", reqErr.Err.Error())
		default:
			e = e.With("reason", reqErr.Reason)
		}
		if field != "" {
			e = e.With("field", field)
		}
		return e
	}
	return e.With("reason", err.Error())
}

// notImplemented is what an operation answers before its slice lands.
func notImplemented(operation string) error {
	return apierr.Newf(apierr.InternalError, "%s is not implemented yet.", operation)
}
