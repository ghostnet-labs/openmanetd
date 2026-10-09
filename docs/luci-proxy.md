# LuCI through the OpenMANET web server

The OpenMANET frontend server can reverse-proxy LuCI so a node has one web
origin: `https://<node>:8081` (and `http://<node>:8080`). With the proxy on,
LuCI is at `https://<node>:8081/cgi-bin/luci/` and the OpenMANET navigation
shows an **Advanced** entry ("Full router settings (LuCI)") that opens it.

This is the first routing step of the Unified UI project. The design record is
[`project/software/unified-ui-design.md`](https://github.com/ghostnet-labs/docs/blob/main/project/software/unified-ui-design.md)
in ghostnet-labs/docs (GHO-69). The implementation is GHO-70 and the Advanced
entry is GHO-71.

## Configuration

The proxy is off by default. With it off, the server behaves exactly as it
did before the feature existed.

```yaml
# /etc/openmanetd/config.yml
frontend:
  luciProxy:
    enable: true                   # default false
    upstream: http://127.0.0.1:80  # default; uhttpd on loopback
```

`upstream` must be an `http` or `https` URL with a host and no path, query or
credentials. An invalid value disables the proxy (logged at error level) and
hides the Advanced entry. Both keys are re-read on a config reload, so no
restart is needed. The SPA reads the flag once per page load, so reload the
page after you change it.

The proxy never changes listen addresses. It runs on the frontend server's
existing `:8080` and `:8081` listeners.

## What is forwarded

| Path on the OpenMANET origin | Goes to | Notes |
|---|---|---|
| `/cgi-bin` and `/cgi-bin/...` | upstream, same path | LuCI (`/cgi-bin/luci`), `cgi-upload`, `cgi-download`, `cgi-backup`, `cgi-exec`, and the ubus fallback `/cgi-bin/luci/admin/ubus` |
| `/luci-static/...` | upstream, same path | LuCI JS, CSS and themes |
| `/ubus` and `/ubus/...` | upstream, same path | LuCI's JSON-RPC ubus endpoint |
| anything else | OpenMANET handlers, as before | SPA, `/api/*`, `/rpc/*`, `/auth/*`, `/ws` |

LuCI routes are matched before every other handler, so the SPA's
`index.html` fallback cannot answer for them. Paths that are not in canonical
form (`..`, `.` or `//` segments) are never proxied.

Request handling:

- The path, query string, method and body are forwarded unchanged. Responses
  stream with no buffering (`FlushInterval: -1`), so backup downloads and
  `cgi-exec` output arrive as they are produced.
- The browser's `Host` header is forwarded, so absolute URLs that LuCI builds
  name the proxied origin. `X-Forwarded-For`, `-Host` and `-Proto` are set by
  the proxy, and inbound copies are discarded.
- The OpenMANET `session` cookie is removed from requests sent to LuCI. All
  other cookies are forwarded as sent.
- `Set-Cookie` from LuCI is never modified. `Location` is preserved, except
  that an absolute `Location` pointing at the upstream itself (for example
  `http://127.0.0.1/cgi-bin/luci/`) becomes path-only so the browser stays on
  the proxied origin.
- LuCI responses do not get the SPA's cross-origin isolation headers
  (`Cross-Origin-Opener-Policy`, `Cross-Origin-Embedder-Policy`,
  `Permissions-Policy`).
- If the upstream is unreachable the proxy returns `502` with a short text
  that points to the direct route.

## Sign-in

The two sign-ins stay separate in release 1:

- LuCI keeps its own rpcd `sysauth` login. The proxy does not require an
  OpenMANET session for LuCI paths and does not share one with LuCI.
- uhttpd sees plain HTTP from loopback, so LuCI runs with `HTTPS` unset and
  issues `sysauth_http` (`path=/cgi-bin/luci/`, `SameSite=strict`, no `Secure`
  flag), even when the browser is on `https://<node>:8081`. The browser stores
  and returns that cookie on the HTTPS origin, so sign-in works. Cookies are
  scoped by host and not by port, so the same `sysauth_http` cookie is also
  valid if you open LuCI directly on `http://<node>/`.
- LuCI's CSRF token is embedded in its own pages and checked by LuCI, so it
  works unchanged through the proxy.
- The OpenMANET `session` cookie (`Path=/`) is unchanged.

## Recovery

uhttpd keeps serving LuCI on `:80`. If openmanetd is down or the proxy is
misconfigured, open `http://<node>/cgi-bin/luci/` directly.

If uhttpd has `redirect_https` enabled, it may redirect proxied requests to its
own HTTPS listener and the browser leaves the OpenMANET origin. Keep the
upstream on plain HTTP on loopback; bench-check this on a real node.
