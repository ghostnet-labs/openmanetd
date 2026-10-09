package auth_test

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newLoginRequest builds a POST /auth/login. scheme selects how the
// request looks to the API: "http" (plain), "tls" (this server terminated
// TLS) or "xfp" (the frontend terminated TLS and set X-Forwarded-Proto).
func newLoginRequest(t *testing.T, scheme string) *http.Request {
	t.Helper()

	req := httptest.NewRequest(http.MethodPost, "/auth/login",
		strings.NewReader(`{"username":"alice","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")

	switch scheme {
	case "http":
	case "tls":
		req.TLS = &tls.ConnectionState{}
	case "xfp":
		req.Header.Set("X-Forwarded-Proto", "https")
	default:
		t.Fatalf("unknown scheme %q", scheme)
	}

	return req
}

// The session cookie carries HttpOnly, SameSite=Lax and Path=/ on both
// schemes, and Secure (under the __Host- name) only over HTTPS.
func TestHandleLogin_CookieScopeByScheme(t *testing.T) {
	cases := []struct {
		name       string
		scheme     string
		wantName   string
		wantSecure bool
	}{
		{name: "plain HTTP", scheme: "http", wantName: auth.SessionCookieName, wantSecure: false},
		{name: "TLS terminated here", scheme: "tls", wantName: auth.SecureSessionCookieName, wantSecure: true},
		{name: "TLS terminated by frontend", scheme: "xfp", wantName: auth.SecureSessionCookieName, wantSecure: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newTestAuthHandler(t, nil)
			rr := httptest.NewRecorder()

			h.HandleLogin(rr, newLoginRequest(t, tc.scheme))
			require.Equal(t, http.StatusOK, rr.Code)

			cookies := rr.Result().Cookies()
			require.Len(t, cookies, 1)

			c := cookies[0]
			assert.Equal(t, tc.wantName, c.Name)
			assert.Equal(t, tc.wantSecure, c.Secure)
			assert.Equal(t, "/", c.Path)
			assert.Empty(t, c.Domain, "a __Host- cookie must not carry Domain; neither cookie needs one")
			assert.True(t, c.HttpOnly)
			assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
			assert.Zero(t, c.MaxAge, "a browser-session cookie; the server enforces the lifetime")
		})
	}
}

func TestRequestIsTLS(t *testing.T) {
	cases := []struct {
		name string
		xfp  string
		tls  bool
		want bool
	}{
		{name: "plain", want: false},
		{name: "tls", tls: true, want: true},
		{name: "xfp https", xfp: "https", want: true},
		{name: "xfp upper case", xfp: "HTTPS", want: true},
		{name: "xfp list uses first hop", xfp: "https, http", want: true},
		{name: "xfp http", xfp: "http", want: false},
		{name: "xfp junk", xfp: "httpsx", want: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tc.tls {
				req.TLS = &tls.ConnectionState{}
			}

			if tc.xfp != "" {
				req.Header.Set("X-Forwarded-Proto", tc.xfp)
			}

			assert.Equal(t, tc.want, auth.RequestIsTLS(req))
		})
	}
}

func TestIsSessionCookieName(t *testing.T) {
	for _, name := range []string{"session", "Session", "__Host-session", "__host-SESSION"} {
		assert.True(t, auth.IsSessionCookieName(name), name)
	}

	for _, name := range []string{"", "sysauth_http", "sysauth_https", "sysauth", "sessionx", "__Secure-session"} {
		assert.False(t, auth.IsSessionCookieName(name), name)
	}
}

// The secure cookie authenticates like the plain one.
func TestAPIAuthMiddleware_SecureCookie(t *testing.T) {
	store := auth.NewSessionStore(time.Hour, 16)
	token := store.Create("alice")

	var gotUser string

	handler := auth.NewAPIAuthMiddleware(store, true)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUser = auth.UsernameFromContext(r.Context())

		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/openmanet.foo.v1.FooService/Method", nil)
	req.AddCookie(&http.Cookie{Name: auth.SecureSessionCookieName, Value: token, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
	assert.Equal(t, "alice", gotUser)
}

// A stale cookie of one scheme does not hide a live cookie of the other.
func TestAPIAuthMiddleware_StaleSecureCookieFallsBackToPlain(t *testing.T) {
	store := auth.NewSessionStore(time.Hour, 16)
	live := store.Create("bob")

	handler := auth.NewAPIAuthMiddleware(store, true)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/openmanet.foo.v1.FooService/Method", nil)
	req.AddCookie(&http.Cookie{Name: auth.SecureSessionCookieName, Value: "revoked", Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: live}) //nolint:gosec // test fixture for the plain-HTTP cookie

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	assert.Equal(t, http.StatusOK, rr.Code)
}

// Expired and revoked sessions are rejected with 401 on both cookie names
// and as a bearer token.
func TestAPIAuthMiddleware_ExpiredOrRevokedSession(t *testing.T) {
	// A negative lifetime makes every session expired on its first check
	// without sleeping or a fake clock.
	expired := auth.NewSessionStore(-time.Second, 16)
	revoked := auth.NewSessionStore(time.Hour, 16)

	revokedToken := revoked.Create("alice")
	revoked.Delete(revokedToken)

	cases := []struct {
		store *auth.SessionStore
		token string
		name  string
		how   string
	}{
		{name: "expired plain cookie", store: expired, token: expired.Create("alice"), how: auth.SessionCookieName},
		{name: "expired secure cookie", store: expired, token: expired.Create("alice"), how: auth.SecureSessionCookieName},
		{name: "expired bearer", store: expired, token: expired.Create("alice"), how: "bearer"},
		{name: "revoked plain cookie", store: revoked, token: revokedToken, how: auth.SessionCookieName},
		{name: "revoked bearer", store: revoked, token: revokedToken, how: "bearer"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			called := false
			handler := auth.NewAPIAuthMiddleware(tc.store, true)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				called = true

				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/openmanet.foo.v1.FooService/Method", nil)
			if tc.how == "bearer" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			} else {
				req.AddCookie(&http.Cookie{Name: tc.how, Value: tc.token}) //nolint:gosec // test fixture
			}

			rr := httptest.NewRecorder()
			handler.ServeHTTP(rr, req)

			assert.Equal(t, http.StatusUnauthorized, rr.Code)
			assert.JSONEq(t, `{"error":"unauthorized"}`, rr.Body.String())
			assert.False(t, called, "the protected handler must not run")
		})
	}
}

// Logout over HTTPS revokes every token the browser sent and clears both
// cookies; over plain HTTP it can only clear the plain cookie.
func TestHandleLogout_ClearsBothSchemes(t *testing.T) {
	cases := []struct {
		name      string
		scheme    string
		wantClear []string
	}{
		{name: "https", scheme: "xfp", wantClear: []string{auth.SecureSessionCookieName, auth.SessionCookieName}},
		{name: "http", scheme: "http", wantClear: []string{auth.SessionCookieName}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := auth.NewSessionStore(time.Hour, 16)
			h := &auth.AuthHandler{Log: zerolog.Nop(), Authenticator: &fakeAuthenticator{}, Store: store}

			secureTok := store.Create("alice")
			plainTok := store.Create("alice")

			req := httptest.NewRequest(http.MethodPost, "/auth/logout", nil)
			if tc.scheme == "xfp" {
				req.Header.Set("X-Forwarded-Proto", "https")
			}

			req.AddCookie(&http.Cookie{Name: auth.SecureSessionCookieName, Value: secureTok, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
			req.AddCookie(&http.Cookie{Name: auth.SessionCookieName, Value: plainTok}) //nolint:gosec // test fixture

			rr := httptest.NewRecorder()
			h.HandleLogout(rr, req)
			require.Equal(t, http.StatusNoContent, rr.Code)

			_, ok := store.Validate(secureTok)
			assert.False(t, ok, "secure token must be revoked")
			_, ok = store.Validate(plainTok)
			assert.False(t, ok, "plain token must be revoked")

			cookies := rr.Result().Cookies()
			got := make([]string, 0, len(cookies))

			for _, c := range cookies {
				got = append(got, c.Name)
				assert.Negative(t, c.MaxAge, "%s must be a deletion cookie", c.Name)
				assert.Equal(t, c.Name == auth.SecureSessionCookieName, c.Secure, c.Name)
			}

			assert.ElementsMatch(t, tc.wantClear, got)
		})
	}
}
