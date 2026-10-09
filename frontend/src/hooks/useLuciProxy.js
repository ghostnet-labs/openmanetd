// =============================================================================
// useLuciProxy.js — Whether the shell should offer the Advanced (LuCI) entry
// =============================================================================
//
// The frontend daemon reports `luci_proxy_enabled` on /api/system/info when
// frontend.luciProxy.enable is on and its upstream is usable, which means
// /cgi-bin/luci/ on this origin reaches LuCI. The value only changes when the
// operator edits config.yml, so it is fetched once per app load and shared by
// every mount; a failed fetch is not cached, so the next mount retries.
// Anything other than an explicit `true` keeps the entry hidden.

import { useEffect, useState } from 'react';
import authFetch from '../services/authFetch.js';

export const LUCI_PATH = '/cgi-bin/luci/';

let cached = null; // resolved boolean once a fetch succeeds
let inflight = null; // shared promise while a fetch is running

export function fetchLuciProxyEnabled() {
  if (cached !== null) return Promise.resolve(cached);
  if (inflight) return inflight;
  inflight = authFetch('/api/system/info')
    .then((resp) => (resp.ok ? resp.json() : Promise.reject(new Error(String(resp.status)))))
    .then((info) => {
      cached = info?.luci_proxy_enabled === true;
      return cached;
    })
    .catch(() => false)
    .finally(() => {
      inflight = null;
    });
  return inflight;
}

// Test seam: forget the cached answer.
export function resetLuciProxyCache() {
  cached = null;
  inflight = null;
}

export default function useLuciProxy() {
  const [enabled, setEnabled] = useState(() => cached === true);

  useEffect(() => {
    let active = true;
    fetchLuciProxyEnabled().then((value) => {
      if (active) setEnabled(value);
    });
    return () => {
      active = false;
    };
  }, []);

  return enabled;
}
