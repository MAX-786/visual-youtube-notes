package httpapi

import (
	"net/http"
	"time"

	"github.com/MAX-786/visual-youtube-notes/backend/internal/auth"
)

// RefreshCookie is the PWA's refresh cookie, scoped to the refresh path so it
// is never sent anywhere else (ADR 0003).
const RefreshCookie = "__Secure-vyn_refresh"

// refreshCookiePath is the real mount of the refresh operations: the contract
// is served under /api (ADR 0008), so the cookie's path must include it.
const refreshCookiePath = "/api/auth/refresh"

const (
	accessLifetime  = 15 * time.Minute
	refreshLifetime = 30 * 24 * time.Hour
)

// setSessionCookies writes both credentials. The __Host- and __Secure-
// prefixes require Secure, and every modern browser treats localhost as a
// secure context, so the attribute is never relaxed.
func setSessionCookies(w http.ResponseWriter, g auth.Grant) {
	http.SetCookie(w, &http.Cookie{
		Name:     AccessCookie,
		Value:    g.AccessToken,
		Path:     "/",
		MaxAge:   int(accessLifetime.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
	http.SetCookie(w, &http.Cookie{
		Name:     RefreshCookie,
		Value:    g.RefreshToken,
		Path:     refreshCookiePath,
		MaxAge:   int(refreshLifetime.Seconds()),
		Secure:   true,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// clearSessionCookies expires both credentials.
func clearSessionCookies(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: AccessCookie, Value: "", Path: "/", MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	http.SetCookie(w, &http.Cookie{Name: RefreshCookie, Value: "", Path: refreshCookiePath, MaxAge: -1, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
}

func refreshCookieValue(r *http.Request) string {
	if r == nil {
		return ""
	}
	c, err := r.Cookie(RefreshCookie)
	if err != nil {
		return ""
	}
	return c.Value
}
