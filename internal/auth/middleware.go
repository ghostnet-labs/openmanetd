package auth

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// ctxKey is an unexported type for context keys in this package.
type ctxKey int

const (
	ctxKeyUsername ctxKey = iota
)

// Session cookie names. The daemon serves the same UI over plain HTTP
// (:8080) and HTTPS (:8081) on one host, so the scheme picks the name:
//
//   - SessionCookieName is issued over plain HTTP. It cannot carry Secure,
//     or the browser would refuse to send it back.
//   - SecureSessionCookieName is issued over HTTPS. The __Host- prefix makes
//     the browser insist on Secure, Path=/ and no Domain, so a plain-HTTP
//     response or a sibling host can neither set nor overwrite it.
//
// Two names rather than one with a per-scheme Secure flag: browsers refuse
// to let a plain-HTTP response overwrite a Secure cookie of the same name,
// which would lock an operator out of HTTP sign-in after an HTTPS one.
const (
	SessionCookieName       = "session"
	SecureSessionCookieName = "__Host-session"
)

// IsSessionCookieName reports whether name is one of the OpenMANET session
// cookie names. The match ignores case so a proxy filter built on it cannot
// be sidestepped by a differently cased name.
func IsSessionCookieName(name string) bool {
	return strings.EqualFold(name, SessionCookieName) || strings.EqualFold(name, SecureSessionCookieName)
}

// RequestIsTLS reports whether the browser reached the daemon over HTTPS:
// either this server terminated TLS itself, or the frontend server did and
// said so in X-Forwarded-Proto. The frontend proxy discards any inbound
// X-Forwarded-* headers before stamping its own, so the browser cannot
// forge the value through it. A client talking to the API port directly
// can, but the only effect is a Secure cookie on its own response.
func RequestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}

	proto, _, _ := strings.Cut(r.Header.Get("X-Forwarded-Proto"), ",")

	return strings.EqualFold(strings.TrimSpace(proto), "https")
}

// NewAPIAuthMiddleware returns HTTP middleware that enforces session
// authentication for the ConnectRPC API server. When enabled is false the
// middleware is a no-op. The session token may be supplied as a session
// cookie (browser clients, see SessionCookieName) or an "Authorization: Bearer <token>" header
// (curl, scripts, ConnectRPC clients in other languages). When both are
// present the Authorization header wins. The following paths are always
// allowed without a valid session:
//   - POST /auth/login
//   - GET  /auth/check
//   - POST /openmanet.dashboard.v1.DashboardService/GetDashboardStatus
func NewAPIAuthMiddleware(store *SessionStore, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled || isAPISkipPath(r) {
				next.ServeHTTP(w, r)

				return
			}

			sess, ok := lookupSession(store, r)
			if !ok {
				writeUnauthorized(w)

				return
			}

			ctx := ContextWithUsername(r.Context(), sess.Username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// NewFrontendAuthMiddleware returns HTTP middleware that enforces session
// authentication for the frontend server. Only requests to /api/* and /ws
// require a valid session. All other paths (SPA shell, static assets) pass
// through. When enabled is false the middleware is a no-op. Accepts the
// session token via cookie or Authorization: Bearer header — same rules as
// NewAPIAuthMiddleware.
func NewFrontendAuthMiddleware(store *SessionStore, enabled bool) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !enabled || !isFrontendProtectedPath(r) {
				next.ServeHTTP(w, r)

				return
			}

			sess, ok := lookupSession(store, r)
			if !ok {
				writeUnauthorized(w)

				return
			}

			ctx := ContextWithUsername(r.Context(), sess.Username)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// sessionTokens returns the session tokens the request carries, in the
// order they are tried:
//  1. Authorization: Bearer <token> header. When present it is the only
//     candidate, so a stale browser cookie cannot mask an explicit header.
//  2. The __Host-session cookie (HTTPS sign-in).
//  3. The session cookie (plain-HTTP sign-in).
//
// Returns nil when the request carries none, or the header is malformed.
func sessionTokens(r *http.Request) []string {
	if h := r.Header.Get("Authorization"); h != "" {
		const prefix = "Bearer "
		if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
			if token := strings.TrimSpace(h[len(prefix):]); token != "" {
				return []string{token}
			}
		}
	}

	tokens := make([]string, 0, 2)

	for _, name := range [...]string{SecureSessionCookieName, SessionCookieName} {
		if cookie, err := r.Cookie(name); err == nil && cookie.Value != "" {
			tokens = append(tokens, cookie.Value)
		}
	}

	return tokens
}

// lookupSession returns the first valid session among the request's
// tokens. Trying each one means an expired HTTPS cookie does not hide a
// live plain-HTTP one on the same host, or the other way round.
func lookupSession(store *SessionStore, r *http.Request) (*Session, bool) {
	for _, token := range sessionTokens(r) {
		if sess, ok := store.Validate(token); ok {
			return sess, true
		}
	}

	return nil, false
}

// UsernameFromContext returns the authenticated username stored in ctx, or an
// empty string if no session is present (e.g. when auth is disabled).
func UsernameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUsername).(string)

	return v
}

// ContextWithUsername returns a copy of ctx with the authenticated username
// attached. Used by the auth middleware and by tests that exercise handlers
// requiring a session without running the full middleware chain.
func ContextWithUsername(ctx context.Context, username string) context.Context {
	return context.WithValue(ctx, ctxKeyUsername, username)
}

// isAPISkipPath returns true for paths that are always allowed on the API
// server without authentication. The setup wizard endpoints are listed
// here so the frontend can poll status and apply a profile before any
// admin password has been set; the SetupService handler enforces its own
// enabled/complete gate as defense-in-depth.
func isAPISkipPath(r *http.Request) bool {
	switch r.URL.Path {
	case "/auth/login", "/auth/check":
		return true
	case "/openmanet.dashboard.v1.DashboardService/GetDashboardStatus":
		return true
	case "/openmanet.setup.v1.SetupService/GetSetupStatus",
		"/openmanet.setup.v1.SetupService/ApplySetup":
		return true
	}

	return false
}

// isFrontendProtectedPath returns true for frontend server paths that require
// a valid session.
func isFrontendProtectedPath(r *http.Request) bool {
	p := r.URL.Path
	if len(p) >= 5 && p[:5] == "/api/" {
		return true
	}

	return p == "/ws"
}

// writeUnauthorized sends a 401 JSON response.
func writeUnauthorized(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusUnauthorized)
	_ = json.NewEncoder(w).Encode(map[string]string{errorKey: "unauthorized"})
}
