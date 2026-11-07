package frontend

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/openmanet/openmanetd/internal/config"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// luciRequest records one request the fake LuCI upstream received.
type luciRequest struct {
	method string
	uri    string // path + raw query exactly as received
	host   string
	cookie string
	body   string
}

// fakeLuCI is an httptest upstream standing in for uhttpd + LuCI.
type fakeLuCI struct {
	srv *httptest.Server

	mu       sync.Mutex // protects the fields below
	requests []luciRequest
	header   http.Header
	status   int
}

func newFakeLuCI(t *testing.T) *fakeLuCI {
	t.Helper()

	f := &fakeLuCI{status: http.StatusOK}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)

		f.mu.Lock()
		f.requests = append(f.requests, luciRequest{
			method: r.Method,
			uri:    r.RequestURI,
			host:   r.Host,
			cookie: r.Header.Get("Cookie"),
			body:   string(body),
		})
		header := f.header
		status := f.status
		f.mu.Unlock()

		for k, vs := range header {
			for _, v := range vs {
				w.Header().Add(k, v)
			}
		}

		w.WriteHeader(status)
		_, _ = w.Write([]byte("luci"))
	}))
	t.Cleanup(f.srv.Close)

	return f
}

func (f *fakeLuCI) respondWith(status int, header http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.status = status
	f.header = header
}

func (f *fakeLuCI) received() []luciRequest {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]luciRequest(nil), f.requests...)
}

// newLuCIConfig returns a config with the LuCI proxy keys set.
func newLuCIConfig(t *testing.T, enable bool, upstream string) *config.Config {
	t.Helper()

	v := viper.New()
	v.Set("frontend.luciProxy.enable", enable)

	if upstream != "" {
		v.Set("frontend.luciProxy.upstream", upstream)
	}

	return config.NewWithoutWatch(v)
}

// newLuCIFrontend serves the full frontend handler chain with the given
// LuCI proxy settings.
func newLuCIFrontend(t *testing.T, enable bool, upstream string) *httptest.Server {
	t.Helper()

	srv := newTestServer(func(s *Server) {
		s.cfg = newLuCIConfig(t, enable, upstream)
		s.luci = &luciProxy{}
	})

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	return ts
}

// noRedirectClient returns redirects to the test instead of following them.
func noRedirectClient() *http.Client {
	return &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
}

func doRequest(t *testing.T, client *http.Client, method, target string, body io.Reader, header http.Header) (*http.Response, string) {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), method, target, body)
	require.NoError(t, err)

	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	resp, err := client.Do(req)
	require.NoError(t, err)

	t.Cleanup(func() { resp.Body.Close() })

	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	return resp, string(data)
}

func TestIsLuCIPath(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/cgi-bin/luci", true},
		{"/cgi-bin/luci/", true},
		{"/cgi-bin/luci/admin/network/wireless", true},
		{"/cgi-bin/cgi-upload", true},
		{"/cgi-bin/cgi-download", true},
		{"/cgi-bin/cgi-exec", true},
		{"/luci-static/resources/luci.js", true},
		{"/luci-static/", true},
		{"/ubus", true},
		{"/ubus/", true},
		{"/", false},
		{"/settings", false},
		{"/api/system/info", false},
		{"/rpc/foo.v1.Service/Method", false},
		{"/auth/login", false},
		{"/cgi-bin", true},
		{"/cgi-bin/", true},
		{"/cgi-bin/cgi-backup", true},
		{"/cgi-bin/luci/admin/ubus", true},
		{"/cgi-binX", false},
		{"/cgi-bin-luci", false},
		{"/ubusfoo", false},
		{"/luci-staticX/a.js", false},
		{"/luci-static/../api/system/info", false},
		{"/cgi-bin/luci/./admin", false},
		{"/cgi-bin/luci//admin", false},
	}

	for _, tc := range tests {
		t.Run(tc.path, func(t *testing.T) {
			assert.Equal(t, tc.want, isLuCIPath(tc.path))
		})
	}
}

func TestParseLuCIUpstream(t *testing.T) {
	tests := []struct {
		raw     string
		wantErr bool
	}{
		{"http://127.0.0.1:80", false},
		{"http://127.0.0.1", false},
		{"http://127.0.0.1:80/", false},
		{"https://127.0.0.1:443", false},
		{"", true},
		{"127.0.0.1:80", true},
		{"ftp://127.0.0.1", true},
		{"http://", true},
		{"http://127.0.0.1/luci", true},
		{"http://127.0.0.1/?x=1", true},
		{"http://root:pw@127.0.0.1", true},
		{"http://[::1", true},
	}

	for _, tc := range tests {
		t.Run(tc.raw, func(t *testing.T) {
			_, err := parseLuCIUpstream(tc.raw)
			if tc.wantErr {
				assert.Error(t, err)

				return
			}

			assert.NoError(t, err)
		})
	}
}

// Every LuCI path, query string and method reaches the upstream unchanged.
func TestLuCIProxy_ForwardsPathsUnchanged(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	targets := []string{
		"/cgi-bin/luci",
		"/cgi-bin/luci/",
		"/cgi-bin/luci/admin/status/overview?foo=bar&x=%2F",
		"/cgi-bin/cgi-upload",
		"/cgi-bin/cgi-download",
		"/cgi-bin/cgi-exec",
		"/cgi-bin/cgi-backup",
		"/cgi-bin/luci/admin/ubus",
		"/luci-static/resources/luci.js?v=25.1",
		"/ubus/",
	}

	for _, target := range targets {
		resp, body := doRequest(t, noRedirectClient(), http.MethodGet, ts.URL+target, nil, nil)
		assert.Equal(t, http.StatusOK, resp.StatusCode, target)
		assert.Equal(t, "luci", body, target)
	}

	got := luci.received()
	require.Len(t, got, len(targets))

	for i, target := range targets {
		assert.Equal(t, target, got[i].uri)
		assert.Equal(t, http.MethodGet, got[i].method)
	}
}

func TestLuCIProxy_ForwardsPOSTBodyAndBrowserHost(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	payload := `{"jsonrpc":"2.0","method":"call","params":[]}`
	resp, _ := doRequest(t, http.DefaultClient, http.MethodPost, ts.URL+"/ubus/", strings.NewReader(payload),
		http.Header{"Content-Type": []string{"application/json"}})
	require.Equal(t, http.StatusOK, resp.StatusCode)

	got := luci.received()
	require.Len(t, got, 1)
	assert.Equal(t, http.MethodPost, got[0].method)
	assert.Equal(t, payload, got[0].body)

	frontURL, err := url.Parse(ts.URL)
	require.NoError(t, err)
	assert.Equal(t, frontURL.Host, got[0].host, "LuCI must see the browser's Host so absolute URLs stay on the proxied origin")
}

// LuCI's sysauth cookie and its redirects reach the browser unchanged.
func TestLuCIProxy_PreservesSetCookieAndLocation(t *testing.T) {
	luci := newFakeLuCI(t)
	luci.respondWith(http.StatusFound, http.Header{
		"Location":   []string{"/cgi-bin/luci/admin/status/overview"},
		"Set-Cookie": []string{"sysauth_http=0123abcd; path=/cgi-bin/luci/; SameSite=Strict; HttpOnly"},
	})

	ts := newLuCIFrontend(t, true, luci.srv.URL)

	resp, _ := doRequest(t, noRedirectClient(), http.MethodPost, ts.URL+"/cgi-bin/luci/",
		strings.NewReader("luci_username=root&luci_password=x"),
		http.Header{"Content-Type": []string{"application/x-www-form-urlencoded"}})

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, "/cgi-bin/luci/admin/status/overview", resp.Header.Get("Location"))
	assert.Equal(t, []string{"sysauth_http=0123abcd; path=/cgi-bin/luci/; SameSite=Strict; HttpOnly"},
		resp.Header.Values("Set-Cookie"))
}

func TestLuCIProxy_LocationHandling(t *testing.T) {
	luci := newFakeLuCI(t)
	upstream, err := url.Parse(luci.srv.URL)
	require.NoError(t, err)

	tests := []struct {
		name     string
		location string
		want     string
	}{
		{"path-only kept", "/cgi-bin/luci/admin", "/cgi-bin/luci/admin"},
		{"relative kept", "admin/network", "admin/network"},
		{"other host kept", "https://openwrt.org/docs", "https://openwrt.org/docs"},
		{"upstream absolute made path-only", "http://" + upstream.Host + "/cgi-bin/luci/?a=1", "/cgi-bin/luci/?a=1"},
	}

	ts := newLuCIFrontend(t, true, luci.srv.URL)

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			luci.respondWith(http.StatusFound, http.Header{"Location": []string{tc.location}})

			resp, _ := doRequest(t, noRedirectClient(), http.MethodGet, ts.URL+"/cgi-bin/luci/", nil, nil)
			assert.Equal(t, tc.want, resp.Header.Get("Location"))
		})
	}
}

func TestRewriteUpstreamLocation_DefaultPort(t *testing.T) {
	h := http.Header{"Location": []string{"http://127.0.0.1/cgi-bin/luci/"}}
	rewriteUpstreamLocation(h, canonicalHostPort("http", "127.0.0.1:80"))
	assert.Equal(t, "/cgi-bin/luci/", h.Get("Location"))
}

// The OpenMANET session cookie is never handed to LuCI; LuCI's own cookies
// still are.
func TestLuCIProxy_StripsOpenMANETSessionCookie(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/", nil,
		http.Header{"Cookie": []string{"session=omtoken; sysauth_http=0123abcd"}})
	doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/", nil,
		http.Header{"Cookie": []string{"sysauth_http=0123abcd; other=1"}})

	got := luci.received()
	require.Len(t, got, 2)
	assert.Equal(t, "sysauth_http=0123abcd", got[0].cookie)
	assert.Equal(t, "sysauth_http=0123abcd; other=1", got[1].cookie, "a header without the session cookie passes unchanged")
}

// LuCI responses must not pick up the SPA's cross-origin-isolation headers.
func TestLuCIProxy_SkipsCOIHeaders(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	resp, _ := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/", nil, nil)
	assert.Empty(t, resp.Header.Get("Cross-Origin-Embedder-Policy"))
	assert.Empty(t, resp.Header.Get("Cross-Origin-Opener-Policy"))

	resp, _ = doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/", nil, nil)
	assert.Equal(t, "require-corp", resp.Header.Get("Cross-Origin-Embedder-Policy"), "SPA keeps its COI headers")
}

func TestLuCIProxy_StreamingFlushesIncrementally(t *testing.T) {
	chunkSent := make(chan struct{})

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flusher", http.StatusInternalServerError)

			return
		}

		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("first\n"))

		flusher.Flush()

		<-chunkSent

		_, _ = w.Write([]byte("second\n"))
	}))
	t.Cleanup(upstream.Close)

	ts := newLuCIFrontend(t, true, upstream.URL)

	resp, err := http.Get(ts.URL + "/cgi-bin/cgi-download")
	require.NoError(t, err)

	defer resp.Body.Close()

	buf := make([]byte, 6)
	_, err = io.ReadFull(resp.Body, buf)
	require.NoError(t, err, "first chunk must arrive before the upstream finishes")
	assert.Equal(t, "first\n", string(buf))

	close(chunkSent)

	rest, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	assert.Equal(t, "second\n", string(rest))
}

// With the flag off, LuCI paths get exactly today's response and the
// upstream is never contacted.
func TestLuCIProxy_FlagOffKeepsCurrentBehavior(t *testing.T) {
	luci := newFakeLuCI(t)
	off := newLuCIFrontend(t, false, luci.srv.URL)

	baseline := httptest.NewServer(newTestServer().handler())
	t.Cleanup(baseline.Close)

	for _, target := range []string{"/cgi-bin/luci/", "/luci-static/resources/luci.js", "/ubus", "/cgi-bin/cgi-upload"} {
		gotResp, gotBody := doRequest(t, noRedirectClient(), http.MethodGet, off.URL+target, nil, nil)
		wantResp, wantBody := doRequest(t, noRedirectClient(), http.MethodGet, baseline.URL+target, nil, nil)

		assert.Equal(t, wantResp.StatusCode, gotResp.StatusCode, target)
		assert.Equal(t, wantBody, gotBody, target)
		assert.Equal(t, wantResp.Header.Get("Cross-Origin-Embedder-Policy"),
			gotResp.Header.Get("Cross-Origin-Embedder-Policy"), target)
	}

	assert.Empty(t, luci.received(), "upstream must not be contacted while the proxy is off")
}

// Non-LuCI paths keep their existing handlers when the proxy is on.
func TestLuCIProxy_NonLuCIPathsUntouched(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	resp, body := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "SPA")

	resp, body = doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/assets/app.js", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, "console.log('app')", body)

	resp, body = doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/settings", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "SPA", "client-side routes still get the SPA shell")

	resp, body = doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-binX", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "SPA", "lookalike prefixes are not proxied")

	assert.Empty(t, luci.received())
}

func TestLuCIProxy_InvalidUpstreamFallsThrough(t *testing.T) {
	srv := newTestServer(func(s *Server) {
		s.cfg = newLuCIConfig(t, true, "not a url")
		s.luci = &luciProxy{}
	})

	assert.False(t, srv.luciProxyActive())

	ts := httptest.NewServer(srv.handler())
	t.Cleanup(ts.Close)

	resp, body := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/", nil, nil)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Contains(t, body, "SPA")
}

func TestLuCIProxy_UpstreamDownReturnsBadGateway(t *testing.T) {
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()

	ts := newLuCIFrontend(t, true, deadURL)

	resp, body := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/", nil, nil)
	assert.Equal(t, http.StatusBadGateway, resp.StatusCode)
	assert.Contains(t, body, "/cgi-bin/luci/")
}

func TestLuCIProxyCache_RebuildsOnUpstreamChange(t *testing.T) {
	srv := newTestServer()
	l := &luciProxy{}

	first := l.handlerFor("http://127.0.0.1:80", srv.log)
	require.NotNil(t, first)
	assert.Same(t, first, l.handlerFor("http://127.0.0.1:80", srv.log), "same upstream reuses the proxy")

	second := l.handlerFor("http://127.0.0.1:8000", srv.log)
	require.NotNil(t, second)
	assert.NotSame(t, first, second)

	assert.Nil(t, l.handlerFor("bogus", srv.log))
}

func TestLuCIProxy_NilConfigOrCacheIsOff(t *testing.T) {
	srv := newTestServer()
	assert.False(t, srv.luciProxyActive(), "a server without a LuCI cache never proxies")

	srv.luci = &luciProxy{}
	assert.False(t, srv.luciProxyActive(), "default config leaves the proxy off")
}

func TestSystemInfo_ReportsLuCIProxy(t *testing.T) {
	tests := []struct {
		name     string
		enable   bool
		upstream string
		want     bool
	}{
		{"off by default", false, "", false},
		{"on with valid upstream", true, "http://127.0.0.1:80", true},
		{"on with invalid upstream", true, "bogus", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := newTestServer(func(s *Server) {
				s.cfg = newLuCIConfig(t, tc.enable, tc.upstream)
				s.luci = &luciProxy{}
				s.cpu = newCPUSampler()
			})

			w := httptest.NewRecorder()
			srv.handleSystemInfo(w, httptest.NewRequest(http.MethodGet, "/api/system/info", nil))

			var info map[string]any
			decodeJSON(t, w.Body, &info)
			assert.Equal(t, tc.want, info["luci_proxy_enabled"])
		})
	}
}

// The SPA handler answers any unknown path with index.html; with the proxy
// on, LuCI paths must win over that fallback.
func TestLuCIProxy_TakesPrecedenceOverSPAFallback(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	for _, target := range []string{"/cgi-bin/luci/", "/luci-static/resources/cbi.js", "/ubus/"} {
		_, body := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+target, nil, nil)
		assert.Equal(t, "luci", body, target)
		assert.NotContains(t, body, "SPA", target)
	}

	require.Len(t, luci.received(), 3)
}

// Over the HTTPS origin, LuCI (behind plain-HTTP uhttpd) may issue either
// sysauth cookie; both must reach the browser byte-for-byte, and the
// upstream is told the browser's scheme via X-Forwarded-Proto.
func TestLuCIProxy_HTTPSOriginSysauthCookies(t *testing.T) {
	var (
		mu    sync.Mutex
		proto string
	)

	cookies := []string{
		"sysauth_http=0123abcd; path=/cgi-bin/luci/; SameSite=strict; HttpOnly",
		"sysauth_https=4567ef01; path=/cgi-bin/luci/; SameSite=strict; HttpOnly; secure",
	}

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		proto = r.Header.Get("X-Forwarded-Proto")
		mu.Unlock()

		for _, c := range cookies {
			w.Header().Add("Set-Cookie", c)
		}

		w.Header().Set("Location", "/cgi-bin/luci/")
		w.WriteHeader(http.StatusFound)
	}))
	t.Cleanup(upstream.Close)

	srv := newTestServer(func(s *Server) {
		s.cfg = newLuCIConfig(t, true, upstream.URL)
		s.luci = &luciProxy{}
	})

	front := httptest.NewTLSServer(srv.handler())
	t.Cleanup(front.Close)

	client := front.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	resp, _ := doRequest(t, client, http.MethodPost, front.URL+"/cgi-bin/luci/", strings.NewReader("luci_username=root"), nil)

	assert.Equal(t, http.StatusFound, resp.StatusCode)
	assert.Equal(t, cookies, resp.Header.Values("Set-Cookie"))
	assert.Equal(t, "/cgi-bin/luci/", resp.Header.Get("Location"))

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, "https", proto)
}

// The OpenMANET session cookie set by /auth/login is not touched by the
// LuCI proxy: it is neither rewritten nor cleared on LuCI responses.
func TestLuCIProxy_DoesNotTouchOpenMANETSetCookie(t *testing.T) {
	luci := newFakeLuCI(t)
	ts := newLuCIFrontend(t, true, luci.srv.URL)

	resp, _ := doRequest(t, http.DefaultClient, http.MethodGet, ts.URL+"/cgi-bin/luci/", nil,
		http.Header{"Cookie": []string{"session=omtoken"}})
	assert.Empty(t, resp.Header.Values("Set-Cookie"), "the proxy adds no cookies of its own")
}
