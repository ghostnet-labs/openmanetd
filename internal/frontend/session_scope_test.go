package frontend

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/openmanet/openmanetd/internal/api/openmanet/gnss/v1/gnssv1connect"
	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"
)

// Negative and scope tests for the OpenMANET sign-in (GHO-72): what an
// unauthenticated or expired caller gets, which cookies cross the LuCI
// proxy in each direction, and that LuCI paths meet uhttpd's own auth.

// uhttpdRequest records what the fake uhttpd saw on one request.
type uhttpdRequest struct {
	path          string
	cookie        string
	authorization string
}

// fakeUhttpd stands in for uhttpd + LuCI + rpcd with their own
// authentication: a LuCI admin page needs a sysauth_http cookie naming the
// LuCI session, and a ubus call needs that session id in the JSON-RPC body.
// Anything else gets the same denial uhttpd and rpcd return.
type fakeUhttpd struct {
	srv *httptest.Server

	mu        sync.Mutex // protects the fields below
	requests  []uhttpdRequest
	setCookie []string
}

const fakeLuCISession = "0123456789abcdef0123456789abcdef"

func newFakeUhttpd(t *testing.T) *fakeUhttpd {
	t.Helper()

	f := &fakeUhttpd{}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeUhttpd) serve(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(r.Body)

	f.mu.Lock()
	f.requests = append(f.requests, uhttpdRequest{
		path:          r.URL.Path,
		cookie:        r.Header.Get("Cookie"),
		authorization: r.Header.Get("Authorization"),
	})
	setCookie := f.setCookie
	f.mu.Unlock()

	for _, v := range setCookie {
		w.Header().Add("Set-Cookie", v)
	}

	switch {
	case r.URL.Path == "/ubus" || strings.HasPrefix(r.URL.Path, "/ubus/"):
		var call struct {
			Params []json.RawMessage `json:"params"`
		}

		_ = json.Unmarshal(body, &call)

		sid := ""
		if len(call.Params) > 0 {
			_ = json.Unmarshal(call.Params[0], &sid)
		}

		w.Header().Set("Content-Type", "application/json")

		if sid != fakeLuCISession {
			// rpcd's answer to an unknown or anonymous session.
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"error":{"code":-32002,"message":"Access denied"}}`))

			return
		}

		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":[0,{}]}`))
	case strings.HasPrefix(r.URL.Path, "/cgi-bin/luci/admin"):
		if c, err := r.Cookie("sysauth_http"); err == nil && c.Value == fakeLuCISession {
			_, _ = w.Write([]byte("luci admin page"))

			return
		}

		// LuCI renders its login form with 403 when sysauth is missing.
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte("luci login form"))
	default:
		http.NotFound(w, r)
	}
}

func (f *fakeUhttpd) setResponseCookies(values ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.setCookie = values
}

func (f *fakeUhttpd) received() []uhttpdRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]uhttpdRequest(nil), f.requests...)
}

// newAuthFrontend serves the full frontend chain with auth enabled, the
// /rpc and /auth proxies pointed at api, and the LuCI proxy set as given.
// With useTLS the server is an httptest TLS server, as on :8081.
func newAuthFrontend(t *testing.T, api *httptest.Server, store *auth.SessionStore, luciEnable bool, luciUpstream string, useTLS bool) *httptest.Server {
	t.Helper()

	rpcProxy, authProxy := buildAPIProxies(api.URL, zerolog.Nop())
	require.NotNil(t, rpcProxy)
	require.NotNil(t, authProxy)

	srv := newTestServer(func(s *Server) {
		s.rpcProxy = rpcProxy
		s.authProxy = authProxy
		s.sessionStore = store
		s.authEnabled = true
		s.cfg = newLuCIConfig(t, luciEnable, luciUpstream)
		s.luci = &luciProxy{}
	})

	var ts *httptest.Server
	if useTLS {
		ts = httptest.NewTLSServer(srv.handler())
	} else {
		ts = httptest.NewServer(srv.handler())
	}

	t.Cleanup(ts.Close)

	return ts
}

// signIn logs in through the frontend and returns the session token.
func signIn(t *testing.T, client *http.Client, frontURL string) string {
	t.Helper()

	resp, body := doRequest(t, client, http.MethodPost, frontURL+"/auth/login",
		strings.NewReader(`{"username":"root","password":""}`), http.Header{"Content-Type": []string{"application/json"}})
	require.Equal(t, http.StatusOK, resp.StatusCode, "login body=%s", body)

	var out struct {
		Token string `json:"token"`
	}

	require.NoError(t, json.Unmarshal([]byte(body), &out))
	require.NotEmpty(t, out.Token)

	return out.Token
}

// jarCookie returns the named cookie from the client's jar, or nil.
func jarCookie(t *testing.T, client *http.Client, rawURL, name string) *http.Cookie {
	t.Helper()

	u, err := url.Parse(rawURL)
	require.NoError(t, err)

	for _, c := range client.Jar.Cookies(u) {
		if c.Name == name {
			return c
		}
	}

	return nil
}

// assertUnauthenticated checks a ConnectRPC error is CodeUnauthenticated.
func assertUnauthenticated(t *testing.T, err error) {
	t.Helper()

	var connectErr *connect.Error
	require.ErrorAs(t, err, &connectErr)
	assert.Equal(t, connect.CodeUnauthenticated, connectErr.Code())
}

// A ConnectRPC call through /rpc without any session is refused with
// HTTP 401, which a Connect client reports as CodeUnauthenticated.
func TestSessionScope_UnauthenticatedRPC(t *testing.T) {
	api, store := newAuthEnabledAPIServer(t)
	front := newAuthFrontend(t, api, store, false, "", true)
	client := httpsClient(t, front)

	gnss := gnssv1connect.NewGNSSServiceClient(client, front.URL+"/rpc")
	_, err := gnss.GetGNSSConfig(context.Background(), &emptypb.Empty{})
	assertUnauthenticated(t, err)

	resp, body := doRequest(t, client, http.MethodPost, front.URL+"/rpc/openmanet.foo.v1.FooService/Method",
		strings.NewReader(`{}`), http.Header{"Content-Type": []string{"application/json"}})
	assert.Equal(t, http.StatusUnauthorized, resp.StatusCode)
	assert.JSONEq(t, `{"error":"unauthorized"}`, body)
}

// A forged or garbage token is refused the same way as no token.
func TestSessionScope_ForgedTokenRPC(t *testing.T) {
	api, store := newAuthEnabledAPIServer(t)
	front := newAuthFrontend(t, api, store, false, "", true)
	client := httpsClient(t, front)

	for _, header := range []http.Header{
		{"Cookie": []string{auth.SecureSessionCookieName + "=forged"}},
		{"Cookie": []string{auth.SessionCookieName + "=forged"}},
		{"Authorization": []string{"Bearer forged"}},
	} {
		header.Set("Content-Type", "application/json")
		resp, _ := doRequest(t, client, http.MethodPost, front.URL+"/rpc/openmanet.foo.v1.FooService/Method",
			strings.NewReader(`{}`), header)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%v", header)
	}
}

// An expired session is refused on /rpc and reported as signed out by
// /auth/check, which is what sends the SPA back to its login page.
func TestSessionScope_ExpiredSession(t *testing.T) {
	api, store := newAuthEnabledAPIServerWithMaxAge(t, -time.Second)
	front := newAuthFrontend(t, api, store, false, "", true)
	client := httpsClient(t, front)

	signIn(t, client, front.URL)
	require.NotNil(t, jarCookie(t, client, front.URL, auth.SecureSessionCookieName), "login still issues a cookie")

	gnss := gnssv1connect.NewGNSSServiceClient(client, front.URL+"/rpc")
	_, err := gnss.GetGNSSConfig(context.Background(), &emptypb.Empty{})
	assertUnauthenticated(t, err)

	resp, body := doRequest(t, client, http.MethodGet, front.URL+"/auth/check", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, `"authenticated":false`)
}

// A session revoked by sign-out is refused afterwards even if a client
// replays the old token.
func TestSessionScope_RevokedSessionReplay(t *testing.T) {
	api, store := newAuthEnabledAPIServer(t)
	front := newAuthFrontend(t, api, store, false, "", true)
	client := httpsClient(t, front)

	token := signIn(t, client, front.URL)

	resp, _ := doRequest(t, client, http.MethodPost, front.URL+"/auth/logout", nil, nil)
	require.Equal(t, http.StatusNoContent, resp.StatusCode)

	for _, header := range []http.Header{
		{"Cookie": []string{auth.SecureSessionCookieName + "=" + token}},
		{"Authorization": []string{"Bearer " + token}},
	} {
		header.Set("Content-Type", "application/json")
		resp, _ = doRequest(t, &http.Client{Transport: client.Transport}, http.MethodPost,
			front.URL+"/rpc/openmanet.foo.v1.FooService/Method", strings.NewReader(`{}`), header)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%v", header)
	}
}

// The frontend's own protected paths (/api/*, /ws) refuse a caller with
// no session or an expired one.
func TestSessionScope_FrontendAPIRequiresSession(t *testing.T) {
	expired := auth.NewSessionStore(-time.Second, 4)
	expiredToken := expired.Create("root")

	srv := newTestServer(func(s *Server) {
		s.sessionStore = expired
		s.authEnabled = true
	})

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	for _, path := range []string{"/api/system/info", "/ws"} {
		resp, _ := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+path, nil, nil)
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s without a session", path)

		resp, _ = doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+path, nil,
			http.Header{"Cookie": []string{auth.SessionCookieName + "=" + expiredToken}})
		assert.Equal(t, http.StatusUnauthorized, resp.StatusCode, "%s with an expired session", path)
	}
}

// LuCI paths reach uhttpd's own authentication. The proxy adds no
// credentials: holding a valid OpenMANET session gets the caller nothing
// on /ubus or a LuCI admin page, and an anonymous caller is not stopped by
// the OpenMANET gate either; uhttpd/rpcd decide.
func TestSessionScope_LuCIPathsMeetUhttpdAuth(t *testing.T) {
	api, store := newAuthEnabledAPIServer(t)
	uhttpd := newFakeUhttpd(t)
	front := newAuthFrontend(t, api, store, true, uhttpd.srv.URL, true)
	client := httpsClient(t, front)

	token := signIn(t, client, front.URL)
	anon := &http.Client{Transport: client.Transport}

	ubusAnon := `{"jsonrpc":"2.0","id":1,"method":"call","params":["00000000000000000000000000000000","system","board",{}]}`
	ubusLuCI := `{"jsonrpc":"2.0","id":1,"method":"call","params":["` + fakeLuCISession + `","system","board",{}]}`

	cases := []struct {
		header   http.Header
		client   *http.Client
		name     string
		method   string
		path     string
		body     string
		wantCode int
		wantBody string
	}{
		{
			name: "ubus with only the OpenMANET session", client: client,
			method: http.MethodPost, path: "/ubus", body: ubusAnon,
			wantCode: http.StatusOK, wantBody: "Access denied",
		},
		{
			name: "ubus anonymous", client: anon,
			method: http.MethodPost, path: "/ubus", body: ubusAnon,
			wantCode: http.StatusOK, wantBody: "Access denied",
		},
		{
			name: "ubus with a LuCI session", client: client,
			method: http.MethodPost, path: "/ubus", body: ubusLuCI,
			wantCode: http.StatusOK, wantBody: `"result"`,
		},
		{
			name: "LuCI admin with only the OpenMANET session", client: client,
			method: http.MethodGet, path: "/cgi-bin/luci/admin/status/overview",
			wantCode: http.StatusForbidden, wantBody: "luci login form",
		},
		{
			name: "LuCI admin with the OpenMANET bearer token", client: anon,
			header: http.Header{"Authorization": []string{"Bearer " + token}},
			method: http.MethodGet, path: "/cgi-bin/luci/admin/status/overview",
			wantCode: http.StatusForbidden, wantBody: "luci login form",
		},
		{
			name: "LuCI admin anonymous", client: anon,
			method: http.MethodGet, path: "/cgi-bin/luci/admin/status/overview",
			wantCode: http.StatusForbidden, wantBody: "luci login form",
		},
		{
			name: "LuCI admin with a LuCI session", client: anon,
			header: http.Header{"Cookie": []string{"sysauth_http=" + fakeLuCISession}},
			method: http.MethodGet, path: "/cgi-bin/luci/admin/status/overview",
			wantCode: http.StatusOK, wantBody: "luci admin page",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var body io.Reader
			if tc.body != "" {
				body = strings.NewReader(tc.body)
			}

			resp, got := doRequest(t, tc.client, tc.method, front.URL+tc.path, body, tc.header)
			assert.Equal(t, tc.wantCode, resp.StatusCode)
			assert.Contains(t, got, tc.wantBody)
		})
	}

	reqs := uhttpd.received()
	require.Len(t, reqs, len(cases), "every LuCI request must reach uhttpd")

	for i, r := range reqs {
		assert.NotContains(t, r.cookie, token, "request %d (%s) carried the OpenMANET token in Cookie", i, r.path)
		assert.NotContains(t, strings.ToLower(r.cookie), "session=", "request %d (%s) carried a session cookie", i, r.path)

		if cases[i].header.Get("Authorization") == "" {
			assert.Empty(t, r.authorization, "request %d (%s): the proxy must not add credentials", i, r.path)
		}
	}
}

// A LuCI response cannot set, overwrite or clear the OpenMANET session
// cookie: Set-Cookie headers using its names are dropped, LuCI's own
// cookies pass, and the OpenMANET session keeps working afterwards.
func TestSessionScope_LuCICannotOverwriteOpenMANETSession(t *testing.T) {
	api, store := newAuthEnabledAPIServer(t)
	uhttpd := newFakeUhttpd(t)
	front := newAuthFrontend(t, api, store, true, uhttpd.srv.URL, true)
	client := httpsClient(t, front)

	token := signIn(t, client, front.URL)

	uhttpd.setResponseCookies(
		auth.SecureSessionCookieName+"=evil; Path=/; Secure; HttpOnly",
		auth.SessionCookieName+"=evil; Path=/",
		"SESSION=evil; Path=/",
		"__host-session=; Path=/; Secure; Max-Age=0",
		"sysauth_http="+fakeLuCISession+"; path=/cgi-bin/luci/; SameSite=strict; HttpOnly",
	)

	resp, _ := doRequest(t, client, http.MethodGet, front.URL+"/cgi-bin/luci/", nil, nil)
	require.Equal(t, http.StatusNotFound, resp.StatusCode, "the fake answers / with 404; status passes through")

	setCookies := resp.Header.Values("Set-Cookie")
	require.Len(t, setCookies, 1, "only LuCI's own cookie may pass: %v", setCookies)
	assert.True(t, strings.HasPrefix(setCookies[0], "sysauth_http="), setCookies[0])

	c := jarCookie(t, client, front.URL, auth.SecureSessionCookieName)
	require.NotNil(t, c)
	assert.Equal(t, token, c.Value, "the OpenMANET session cookie must be unchanged")

	resp, _ = doRequest(t, client, http.MethodPost, front.URL+"/rpc/openmanet.foo.v1.FooService/Method",
		strings.NewReader(`{}`), http.Header{"Content-Type": []string{"application/json"}})
	assert.Equal(t, http.StatusOK, resp.StatusCode, "the OpenMANET session still works after the LuCI visit")
}

// Both OpenMANET cookie names are stripped from requests to LuCI, in any
// case, and nothing else in the Cookie header changes.
func TestSessionScope_StripsBothSessionCookieNames(t *testing.T) {
	uhttpd := newFakeUhttpd(t)
	ts := newLuCIFrontend(t, true, uhttpd.srv.URL)

	doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/admin/", nil, http.Header{"Cookie": []string{
		"__Host-session=a; sysauth_http=" + fakeLuCISession + "; session=b; Session=c; other=1",
	}})

	reqs := uhttpd.received()
	require.Len(t, reqs, 1)
	assert.Equal(t, "sysauth_http="+fakeLuCISession+"; other=1", reqs[0].cookie)
}

// With the flag off, no LuCI path reaches uhttpd, whatever the method or
// credentials, and the OpenMANET gate is unchanged.
func TestSessionScope_FlagOffNeverReachesLuCI(t *testing.T) {
	api, store := newAuthEnabledAPIServer(t)
	uhttpd := newFakeUhttpd(t)
	front := newAuthFrontend(t, api, store, false, uhttpd.srv.URL, true)
	client := httpsClient(t, front)

	signIn(t, client, front.URL)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPost, "/ubus"},
		{http.MethodPost, "/ubus/"},
		{http.MethodGet, "/cgi-bin/luci/"},
		{http.MethodPost, "/cgi-bin/luci/admin/ubus"},
		{http.MethodPost, "/cgi-bin/cgi-upload"},
		{http.MethodGet, "/luci-static/resources/luci.js"},
	} {
		resp, body := doRequest(t, noRedirectClientWith(client), tc.method, front.URL+tc.path,
			strings.NewReader(`{}`), http.Header{"Cookie": []string{"sysauth_http=" + fakeLuCISession}})
		assert.NotContains(t, body, "luci", "%s %s", tc.method, tc.path)
		assert.NotEqual(t, http.StatusBadGateway, resp.StatusCode, "%s %s", tc.method, tc.path)
	}

	assert.Empty(t, uhttpd.received(), "uhttpd must not be contacted while the proxy is off")
}

// noRedirectClientWith keeps c's transport and jar but returns redirects.
func noRedirectClientWith(c *http.Client) *http.Client {
	return &http.Client{
		Transport: c.Transport,
		Jar:       c.Jar,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func TestDropSessionSetCookies(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "none", in: nil, want: nil},
		{name: "only LuCI", in: []string{"sysauth_http=x; path=/cgi-bin/luci/"}, want: []string{"sysauth_http=x; path=/cgi-bin/luci/"}},
		{
			name: "mixed",
			in:   []string{"session=x", "sysauth_https=y; Secure", " __Host-session =z; Secure", "sessionid=keep"},
			want: []string{"sysauth_https=y; Secure", "sessionid=keep"},
		},
		{name: "all dropped", in: []string{"session=", "__HOST-SESSION=x"}, want: nil},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := http.Header{}
			for _, v := range tc.in {
				h.Add("Set-Cookie", v)
			}

			dropSessionSetCookies(h)
			assert.Equal(t, tc.want, h.Values("Set-Cookie"))
		})
	}
}
