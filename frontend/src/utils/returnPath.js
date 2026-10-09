// =============================================================================
// returnPath.js — Where the login page sends the operator afterwards
// =============================================================================
//
// ProtectedRoute redirects to /login with `state.from` set to the location
// the operator was on (a deep link, or the page they were using when the
// session expired). After a successful login the page returns there instead
// of always dropping them on the dashboard. Only same-app absolute paths are
// honored; anything else, and /login itself, falls back to the dashboard.

const FALLBACK = '/';

export function returnPathFrom(state) {
  const from = state?.from;
  const pathname = from?.pathname;
  if (typeof pathname !== 'string' || !pathname.startsWith('/')) return FALLBACK;
  // "//host" and "/\host" are protocol-relative URLs to another origin.
  if (pathname.startsWith('//') || pathname.startsWith('/\\')) return FALLBACK;
  if (pathname === '/login' || pathname.startsWith('/login/')) return FALLBACK;
  const search = typeof from.search === 'string' ? from.search : '';
  const hash = typeof from.hash === 'string' ? from.hash : '';
  return pathname + search + hash;
}
