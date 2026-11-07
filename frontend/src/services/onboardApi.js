// =============================================================================
// onboardApi.js — phone onboarding (ATAK from the node, no internet)
// =============================================================================
//
// Public endpoints (no login): /onboard/status, /onboard/atak.apk,
// /onboard/atak-setup.zip. Admin endpoints (login): /api/onboard/atak.
// See docs/phone-onboarding.md.

import authFetch from './authFetch.js';

export const ONBOARD_STATUS_URL = '/onboard/status';
export const ONBOARD_ADMIN_URL = '/api/onboard/atak';
export const JOIN_PATH = '/join';

// fetchOnboardStatus returns the node's onboarding offer, or throws.
export async function fetchOnboardStatus({ signal } = {}) {
  const resp = await fetch(ONBOARD_STATUS_URL, { cache: 'no-store', signal });
  if (!resp.ok) throw new Error(`status ${resp.status}`);
  return resp.json();
}

// buildPackageUrl appends the non-empty profile fields to the package URL.
export function buildPackageUrl(packageUrl, { callsign = '', team = '', role = '' } = {}) {
  const url = new URL(packageUrl);
  const cs = callsign.trim();
  if (cs) url.searchParams.set('callsign', cs);
  if (team) url.searchParams.set('team', team);
  if (role) url.searchParams.set('role', role);
  return url.toString();
}

// buildTakImportUrl is the link that makes ATAK (5.1+) import a data
// package from a URL. ATAK asks the user to confirm once.
export function buildTakImportUrl(packageUrl) {
  return `tak://com.atakmap.app/import?url=${encodeURIComponent(packageUrl)}`;
}

// detectPlatform classifies a user agent as 'android', 'ios' or 'other'.
// iPadOS reports a desktop Safari UA, so touch points break the tie.
export function detectPlatform(ua = '', maxTouchPoints = 0) {
  if (/android/i.test(ua)) return 'android';
  if (/iphone|ipad|ipod/i.test(ua)) return 'ios';
  if (/macintosh/i.test(ua) && maxTouchPoints > 1) return 'ios';
  return 'other';
}

// isSignInWindow reports whether the page is open in Android's "Sign in
// to network" window (a WebView, marked "; wv)" in its user agent), which
// the node's phone portal opens. Downloads and tak:// links may not work
// there, so the page offers the address to open in Chrome.
export function isSignInWindow(ua = '') {
  return /android/i.test(ua) && /;\s*wv\)/.test(ua);
}

// CALLSIGN_RE mirrors the server's rule: up to 40 letters, digits,
// space, '-', '_' or '.'.
const CALLSIGN_RE = /^[\p{L}\p{N} ._-]{0,40}$/u;

export function isValidCallsign(s) {
  return CALLSIGN_RE.test(s);
}

// formatBytes renders a byte count as "215 MB" style text.
export function formatBytes(n) {
  if (!Number.isFinite(n) || n <= 0) return '0 B';
  const units = ['B', 'KB', 'MB', 'GB'];
  let v = n;
  let i = 0;
  while (v >= 1000 && i < units.length - 1) {
    v /= 1000;
    i += 1;
  }
  return `${v >= 10 || i === 0 ? Math.round(v) : v.toFixed(1)} ${units[i]}`;
}

// uploadAtakApk POSTs the APK as multipart field "file". XHR (not fetch)
// so the progress bar gets byte counts. Resolves with the stored APK info.
export function uploadAtakApk(file, { signal, onProgress } = {}) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    const form = new FormData();
    form.append('file', file, file?.name ?? 'atak.apk');

    xhr.open('POST', ONBOARD_ADMIN_URL, true);
    xhr.responseType = 'json';
    xhr.withCredentials = true;

    xhr.upload.onprogress = (ev) => {
      if (ev.lengthComputable && typeof onProgress === 'function') {
        onProgress({ loaded: ev.loaded, total: ev.total });
      }
    };

    xhr.onload = () => {
      const body = typeof xhr.response === 'string' ? safeJSON(xhr.response) : xhr.response;
      if (xhr.status >= 200 && xhr.status < 300) {
        resolve(body);
        return;
      }
      if (xhr.status === 401) window.dispatchEvent(new Event('session-expired'));
      const err = new Error(body?.error || `upload failed (${xhr.status})`);
      err.status = xhr.status;
      reject(err);
    };
    xhr.onerror = () => reject(new Error('upload network error'));
    xhr.onabort = () => reject(new DOMException('upload aborted', 'AbortError'));

    if (signal) {
      if (signal.aborted) {
        xhr.abort();
        return;
      }
      signal.addEventListener('abort', () => xhr.abort(), { once: true });
    }

    xhr.send(form);
  });
}

// removeAtakApk deletes the stored APK.
export async function removeAtakApk() {
  const resp = await authFetch(ONBOARD_ADMIN_URL, { method: 'DELETE' });
  if (!resp.ok) {
    const body = await resp.json().catch(() => null);
    throw new Error(body?.error || `remove failed (${resp.status})`);
  }
}

function safeJSON(s) {
  if (typeof s !== 'string' || s === '') return null;
  try { return JSON.parse(s); } catch { return null; }
}
