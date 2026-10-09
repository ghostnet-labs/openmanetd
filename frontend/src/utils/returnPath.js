// =============================================================================
// returnPath.js — Validate the post-login return path (`/login?next=...`)
// =============================================================================
//
// When the OpenMANET session ends (sign-out, expiry, a 401 from the API) the
// SPA sends the operator to /login and carries the page they were on in
// `next`, so signing in again lands them back on the same deep link.
//
// `next` comes from the URL, so it is untrusted: anything that is not a
// same-origin, root-relative SPA route collapses to '/'. That blocks open
// redirects (`//evil.example`, `/\evil.example`, `https://…`, `javascript:`)
// and keeps the router away from paths the SPA does not own (the LuCI
// handoff, the API proxies, the login page itself).

const DEFAULT_RETURN_PATH = '/';
const MAX_RETURN_PATH_LENGTH = 2048;

// Throwaway origin used only to resolve and normalise the candidate path.
const PROBE_ORIGIN = 'http://openmanet.invalid';

// Path prefixes that are not SPA routes. A full page load of the LuCI paths
// is the Advanced handoff, never a post-login destination.
const NON_SPA_PREFIXES = ['/login', '/api', '/rpc', '/auth', '/ws', '/whisper', '/cgi-bin', '/luci-static', '/ubus'];

// ASCII control characters and backslashes are never part of a route; a
// browser may strip or reinterpret them, which is how `/\t/evil` or
// `/\evil` turns into a protocol-relative URL.
function hasUnsafeChar(s) {
  for (let i = 0; i < s.length; i++) {
    const c = s.charCodeAt(i);
    if (c < 0x20 || c === 0x7f || c === 0x5c) return true;
  }
  return false;
}

function isNonSpaPath(pathname) {
  const lower = pathname.toLowerCase();
  return NON_SPA_PREFIXES.some(
    (prefix) => lower === prefix || lower.startsWith(prefix + '/'),
  );
}

// safeReturnPath returns `raw` normalised when it is a same-origin SPA path,
// and '/' otherwise.
export function safeReturnPath(raw) {
  if (typeof raw !== 'string' || raw.length === 0 || raw.length > MAX_RETURN_PATH_LENGTH) {
    return DEFAULT_RETURN_PATH;
  }
  if (raw[0] !== '/' || raw[1] === '/' || hasUnsafeChar(raw)) {
    return DEFAULT_RETURN_PATH;
  }

  let url;
  try {
    url = new URL(raw, PROBE_ORIGIN);
  } catch {
    return DEFAULT_RETURN_PATH;
  }
  if (url.origin !== PROBE_ORIGIN || isNonSpaPath(url.pathname)) {
    return DEFAULT_RETURN_PATH;
  }

  return url.pathname + url.search + url.hash;
}

// loginPathFor builds the /login URL that returns to `current` (a
// pathname + search + hash) after sign-in. The dashboard needs no `next`.
export function loginPathFor(current) {
  const next = safeReturnPath(current);
  if (next === DEFAULT_RETURN_PATH) return '/login';
  return `/login?next=${encodeURIComponent(next)}`;
}
