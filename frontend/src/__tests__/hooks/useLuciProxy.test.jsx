// =============================================================================
// useLuciProxy.test.jsx — Tests for the Advanced (LuCI) entry feature probe
// =============================================================================

import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor } from '@testing-library/react';

function jsonResponse(body, status = 200) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) };
}

describe('TestUseLuciProxy', () => {
  let mod;

  beforeEach(async () => {
    vi.resetModules();
    mod = await import('../../hooks/useLuciProxy.js');
    mod.resetLuciProxyCache();
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
    vi.resetModules();
  });

  it('exposes the LuCI path on this origin', () => {
    expect(mod.LUCI_PATH).toBe('/cgi-bin/luci/');
  });

  it('returns true when the daemon reports the proxy enabled', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ hostname: 'n', luci_proxy_enabled: true })));
    vi.stubGlobal('fetch', fetchMock);
    const { result } = renderHook(() => mod.default());
    expect(result.current).toBe(false);
    await waitFor(() => expect(result.current).toBe(true));
    expect(fetchMock).toHaveBeenCalledWith('/api/system/info', expect.objectContaining({ credentials: 'include' }));
  });

  it.each([
    ['flag false', { luci_proxy_enabled: false }],
    ['field missing (older daemon)', { hostname: 'n' }],
    ['non-boolean value', { luci_proxy_enabled: 'true' }],
    ['null body', null],
  ])('stays hidden when %s', async (_name, body) => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse(body))));
    await expect(mod.fetchLuciProxyEnabled()).resolves.toBe(false);
  });

  it('stays hidden on an HTTP error and retries on the next call', async () => {
    const fetchMock = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({}, 500))
      .mockResolvedValueOnce(jsonResponse({ luci_proxy_enabled: true }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(mod.fetchLuciProxyEnabled()).resolves.toBe(false);
    await expect(mod.fetchLuciProxyEnabled()).resolves.toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('stays hidden when the request rejects', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.reject(new TypeError('offline'))));
    await expect(mod.fetchLuciProxyEnabled()).resolves.toBe(false);
  });

  it('fetches once per app load and shares concurrent callers', async () => {
    const fetchMock = vi.fn(() => Promise.resolve(jsonResponse({ luci_proxy_enabled: true })));
    vi.stubGlobal('fetch', fetchMock);
    const [a, b] = await Promise.all([mod.fetchLuciProxyEnabled(), mod.fetchLuciProxyEnabled()]);
    expect([a, b]).toEqual([true, true]);
    await expect(mod.fetchLuciProxyEnabled()).resolves.toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('starts from the cached answer on a later mount', async () => {
    vi.stubGlobal('fetch', vi.fn(() => Promise.resolve(jsonResponse({ luci_proxy_enabled: true }))));
    await mod.fetchLuciProxyEnabled();
    const { result } = renderHook(() => mod.default());
    expect(result.current).toBe(true);
  });
});
