package frontend

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/rs/zerolog"
)

// LuCI reverse proxy (Unified UI, GHO-69 / GHO-70).
//
// When frontend.luciProxy.enable is true the frontend server forwards
// LuCI's absolute paths (/cgi-bin/, /luci-static/, /ubus/) to the
// configured upstream (uhttpd on loopback by
// default), so the browser reaches LuCI at https://<node>:8081/cgi-bin/luci/
// on the same origin as the OpenMANET UI. Paths, query strings, cookies and
// redirects pass through unchanged; LuCI keeps its own sysauth login and is
// not gated by the OpenMANET session (the two sign-ins stay separate).
//
// HTTPS: uhttpd only ever sees plain HTTP from loopback, so LuCI runs with
// HTTPS unset and issues its sysauth_http cookie (path=/cgi-bin/luci/,
// SameSite=strict, no Secure flag). The browser stores and returns it on the
// https://<node>:8081 origin like any other cookie, so the session works.
// X-Forwarded-Proto carries the browser's scheme for upstreams that honor
// it; uhttpd does not, and the proxy never rewrites Set-Cookie.
//
// With the flag off, none of this runs: requests for these paths reach the
// existing handler chain exactly as before.

// errLuCIUpstream is returned for an unusable frontend.luciProxy.upstream.
var errLuCIUpstream = errors.New("frontend.luciProxy.upstream must be an http(s) URL with a host and no path, query or credentials")

// isLuCIPath reports whether p is one of the URL paths LuCI serves from the
// web server root. Each base matches exactly or as a directory prefix
// ("/ubus" and "/ubus/..." match, "/ubusfoo" does not). Paths that are not
// in canonical form (dot segments, doubled slashes) never match, so they
// keep today's handling instead of being forwarded.
func isLuCIPath(p string) bool {
	// The whole /cgi-bin tree belongs to uhttpd: luci, cgi-upload,
	// cgi-download, cgi-backup and cgi-exec all live there, and LuCI's
	// ubus fallback is /cgi-bin/luci/admin/ubus.
	bases := [...]string{
		"/cgi-bin",
		"/luci-static",
		"/ubus",
	}

	matched := false

	for _, base := range bases {
		if p == base || (len(p) > len(base) && p[len(base)] == '/' && strings.HasPrefix(p, base)) {
			matched = true

			break
		}
	}

	if !matched {
		return false
	}

	clean := path.Clean(p)

	return clean == p || clean+"/" == p
}

// parseLuCIUpstream validates the configured upstream base URL.
func parseLuCIUpstream(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parse frontend.luciProxy.upstream: %w", err)
	}

	if (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		(u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.User != nil {
		return nil, errLuCIUpstream
	}

	return u, nil
}

// canonicalHostPort returns host:port for a URL authority, filling in the
// scheme's default port so "127.0.0.1" and "127.0.0.1:80" compare equal.
func canonicalHostPort(scheme, host string) string {
	if _, _, err := net.SplitHostPort(host); err == nil {
		return strings.ToLower(host)
	}

	port := "80"
	if scheme == "https" {
		port = "443"
	}

	return strings.ToLower(net.JoinHostPort(strings.Trim(host, "[]"), port))
}

// newLuCIReverseProxy builds the reverse proxy for one upstream. It returns
// the transport too so the caller can release idle connections when the
// upstream changes.
func newLuCIReverseProxy(upstream *url.URL, log zerolog.Logger) (*httputil.ReverseProxy, *http.Transport) {
	transport := &http.Transport{
		DialContext: (&net.Dialer{
			Timeout: 10 * time.Second,
		}).DialContext,
		IdleConnTimeout: 90 * time.Second,
	}

	upstreamHostPort := canonicalHostPort(upstream.Scheme, upstream.Host)

	proxy := &httputil.ReverseProxy{
		// Rewrite (not the deprecated Director) drops inbound
		// X-Forwarded-* headers; SetXForwarded stamps the real client.
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.SetXForwarded()
			pr.Out.URL.Scheme = upstream.Scheme
			pr.Out.URL.Host = upstream.Host
			// Keep the browser's Host so any absolute URL LuCI builds
			// names the proxied origin, not the loopback upstream.
			pr.Out.Host = pr.In.Host

			stripCookie(pr.Out, auth.SessionCookieName)
		},
		ModifyResponse: func(resp *http.Response) error {
			rewriteUpstreamLocation(resp.Header, upstreamHostPort)

			return nil
		},
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			log.Warn().Err(err).Str("path", r.URL.Path).Msg("LuCI proxy: upstream request failed")
			http.Error(w, "LuCI is not reachable through OpenMANET right now. "+
				"Open http://<node>/cgi-bin/luci/ directly to reach it.", http.StatusBadGateway)
		},
		Transport: transport,
		// Flush every write so streamed responses (backup downloads,
		// cgi-exec output, long-polling ubus calls) are not buffered.
		FlushInterval: -1,
	}

	return proxy, transport
}

// stripCookie removes the named cookie from an outbound request so the
// OpenMANET session token is never handed to LuCI. The Cookie header is
// left byte-for-byte untouched when the cookie is absent.
func stripCookie(r *http.Request, name string) {
	cookies := r.Cookies()

	found := false

	for _, c := range cookies {
		if c.Name == name {
			found = true

			break
		}
	}

	if !found {
		return
	}

	r.Header.Del("Cookie")

	for _, c := range cookies {
		if c.Name != name {
			r.AddCookie(c)
		}
	}
}

// rewriteUpstreamLocation turns an absolute Location that points at the
// upstream itself (for example http://127.0.0.1/cgi-bin/luci/) into a
// path-only reference so the browser stays on the proxied origin. Relative
// and path-only Locations, and absolute ones naming any other host, are
// preserved unchanged.
func rewriteUpstreamLocation(h http.Header, upstreamHostPort string) {
	loc := h.Get("Location")
	if loc == "" {
		return
	}

	u, err := url.Parse(loc)
	if err != nil || !u.IsAbs() || u.Host == "" {
		return
	}

	if canonicalHostPort(u.Scheme, u.Host) != upstreamHostPort {
		return
	}

	rel := u.EscapedPath()
	if rel == "" {
		rel = "/"
	}

	if u.RawQuery != "" {
		rel += "?" + u.RawQuery
	}

	if u.Fragment != "" {
		rel += "#" + u.EscapedFragment()
	}

	h.Set("Location", rel)
}

// luciProxy caches the reverse proxy built for the current upstream so a
// config reload that changes frontend.luciProxy.upstream takes effect
// without a restart, while steady-state requests reuse one transport.
// Field order follows govet fieldalignment; mu guards every other field.
type luciProxy struct {
	handler   http.Handler // nil when upstream is invalid
	transport *http.Transport
	upstream  string
	mu        sync.Mutex // protects handler, transport, upstream and built
	built     bool
}

// handlerFor returns the proxy for upstream, building it on first use or
// when the upstream changed. It returns nil when upstream is unusable.
func (l *luciProxy) handlerFor(upstream string, log zerolog.Logger) http.Handler {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.built && l.upstream == upstream {
		return l.handler
	}

	if l.transport != nil {
		l.transport.CloseIdleConnections()
	}

	l.upstream = upstream
	l.built = true
	l.handler = nil
	l.transport = nil

	u, err := parseLuCIUpstream(upstream)
	if err != nil {
		log.Error().Err(err).Str("upstream", upstream).Msg("LuCI proxy disabled: invalid upstream")

		return nil
	}

	proxy, transport := newLuCIReverseProxy(u, log)
	l.handler = proxy
	l.transport = transport

	log.Info().Str("upstream", u.String()).Msg("LuCI proxy enabled")

	return l.handler
}

// luciHandler returns the LuCI proxy when the feature is enabled and the
// upstream is valid, or nil when requests must take the normal path.
func (s *Server) luciHandler() http.Handler {
	if s.cfg == nil || s.luci == nil || !s.cfg.GetFrontendLuCIProxyEnable() {
		return nil
	}

	return s.luci.handlerFor(s.cfg.GetFrontendLuCIProxyUpstream(), s.log)
}

// luciProxyActive reports whether the SPA should offer the Advanced
// (LuCI) entry: the flag is on and the upstream is usable.
func (s *Server) luciProxyActive() bool {
	return s.luciHandler() != nil
}

// withLuCIProxy routes LuCI paths to the proxy ahead of every other
// handler — including the COI and frontend-auth middleware, whose headers
// and session check belong to the OpenMANET UI, not to LuCI. Everything
// else, and every request while the proxy is off, goes to next unchanged.
func (s *Server) withLuCIProxy(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isLuCIPath(r.URL.Path) {
			if h := s.luciHandler(); h != nil {
				h.ServeHTTP(w, r)

				return
			}
		}

		next.ServeHTTP(w, r)
	})
}
