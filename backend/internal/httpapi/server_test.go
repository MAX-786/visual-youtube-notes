package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/apierr"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/config"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/contract"
	"github.com/MAX-786/visual-youtube-notes/backend/internal/ratelimit"
)

func testRouter(t *testing.T) http.Handler {
	t.Helper()
	c, err := contract.Load()
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	cfg := &config.Config{
		PublicOrigin: "https://yt-track.mkhismkh.com",
		ExtensionID:  "fjodmlmkfifedoamachdbgigfpaldicm",
	}
	return NewRouter(Deps{
		Config:   cfg,
		Contract: c,
		Limiter:  ratelimit.New(c.Classes, nil),
	})
}

type envelope struct {
	Error struct {
		Code    apierr.Code    `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

func decode(t *testing.T, rec *httptest.ResponseRecorder) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("response is not an error envelope: %s", rec.Body.String())
	}
	return env
}

func TestUnauthenticatedEnvelope(t *testing.T) {
	router := testRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	if env := decode(t, rec); env.Error.Code != apierr.Unauthenticated {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func TestUnknownPathIsTypedNotFound(t *testing.T) {
	router := testRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/nothing-here", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d", rec.Code)
	}
	if env := decode(t, rec); env.Error.Code != apierr.NotFound {
		t.Fatalf("code %q", env.Error.Code)
	}
}

func TestMalformedBodyIsRejectedBeforeHandlers(t *testing.T) {
	router := testRouter(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"username":"owner"}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, body %s", rec.Code, rec.Body.String())
	}
	env := decode(t, rec)
	if env.Error.Code != apierr.InvalidRequest {
		t.Fatalf("code %q", env.Error.Code)
	}
	if env.Error.Details["reason"] == nil {
		t.Fatalf("no reason in details: %v", env.Error.Details)
	}
}

func TestMixedTransportIsRefused(t *testing.T) {
	router := testRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req.AddCookie(&http.Cookie{Name: AccessCookie, Value: "x"})
	req.Header.Set("Authorization", "Bearer y")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if env := decode(t, rec); env.Error.Code != apierr.MixedTransport {
		t.Fatalf("code %q (status %d)", env.Error.Code, rec.Code)
	}
}

func TestLoginClassLimitsByAddress(t *testing.T) {
	router := testRouter(t)
	body := `{"username":"owner","password":"correct horse battery staple"}`
	var last *httptest.ResponseRecorder
	for i := 0; i < 11; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = "203.0.113.7:4444"
		last = httptest.NewRecorder()
		router.ServeHTTP(last, req)
		if i < 10 && last.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was limited inside the burst", i+1)
		}
	}
	if last.Code != http.StatusTooManyRequests {
		t.Fatalf("11th login was not limited: status %d body %s", last.Code, last.Body.String())
	}
	if last.Header().Get("Retry-After") == "" {
		t.Fatal("no Retry-After header")
	}
	env := decode(t, last)
	if env.Error.Code != apierr.RateLimited || env.Error.Details["class"] != "login" {
		t.Fatalf("envelope %+v", env.Error)
	}

	// Another address has its own bucket.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.RemoteAddr = "198.51.100.9:4444"
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	if rec.Code == http.StatusTooManyRequests {
		t.Fatal("a different address shared the bucket")
	}
}

func TestExtensionCORS(t *testing.T) {
	router := testRouter(t)
	origin := "chrome-extension://fjodmlmkfifedoamachdbgigfpaldicm"

	preflight := httptest.NewRequest(http.MethodOptions, "/api/submissions", nil)
	preflight.Header.Set("Origin", origin)
	preflight.Header.Set("Access-Control-Request-Method", "POST")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, preflight)
	if rec.Code != http.StatusNoContent || rec.Header().Get("Access-Control-Allow-Origin") != origin {
		t.Fatalf("preflight on a callable operation: status %d, headers %v", rec.Code, rec.Header())
	}

	preflight = httptest.NewRequest(http.MethodOptions, "/api/notes", nil)
	preflight.Header.Set("Origin", origin)
	preflight.Header.Set("Access-Control-Request-Method", "GET")
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, preflight)
	if rec.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("preflight on a non-callable operation was allowed")
	}

	actual := httptest.NewRequest(http.MethodGet, "/api/notes", nil)
	actual.Header.Set("Origin", origin)
	rec = httptest.NewRecorder()
	router.ServeHTTP(rec, actual)
	if env := decode(t, rec); env.Error.Code != apierr.OriginRejected {
		t.Fatalf("extension origin on a non-callable operation: %q", env.Error.Code)
	}
}

func TestEveryOperationHasAClass(t *testing.T) {
	c, err := contract.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Classes) != 14 {
		t.Fatalf("%d classes", len(c.Classes))
	}
	for id, op := range c.Operations {
		if _, ok := c.Classes[op.RateLimitClass]; !ok {
			t.Errorf("%s has no class", id)
		}
	}
	req := httptest.NewRequest(http.MethodGet, "/api/exports/notes/6f1a1b2c-3d4e-4f60-8a9b-0c1d2e3f4a5b.pdf", nil)
	route, params, err := c.Resolve(req)
	if err != nil {
		t.Fatalf("resolve export path: %v", err)
	}
	if route.Operation.OperationID != "exportNotePdf" || params["noteId"] == "" {
		t.Fatalf("resolved %s with %v", route.Operation.OperationID, params)
	}
}
