// =============================================================================
// onboardApi.test.js — phone onboarding helpers and transport
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  buildPackageUrl,
  buildTakImportUrl,
  detectPlatform,
  fetchOnboardStatus,
  formatBytes,
  isValidCallsign,
  removeAtakApk,
  uploadAtakApk,
} from '../../services/onboardApi.js';

describe('BuildPackageUrl', () => {
  const base = 'http://10.41.3.7:8080/onboard/atak-setup.zip';

  it('returns the bare URL for an empty profile', () => {
    expect(buildPackageUrl(base, {})).toBe(base);
    expect(buildPackageUrl(base)).toBe(base);
  });

  it('adds trimmed callsign, team and role', () => {
    const url = new URL(buildPackageUrl(base, { callsign: '  Bravo 6 ', team: 'Dark Blue', role: 'Medic' }));
    expect(url.searchParams.get('callsign')).toBe('Bravo 6');
    expect(url.searchParams.get('team')).toBe('Dark Blue');
    expect(url.searchParams.get('role')).toBe('Medic');
  });

  it('leaves out blank fields', () => {
    const url = new URL(buildPackageUrl(base, { callsign: '   ', team: '', role: 'HQ' }));
    expect(url.searchParams.has('callsign')).toBe(false);
    expect(url.searchParams.has('team')).toBe(false);
    expect(url.searchParams.get('role')).toBe('HQ');
  });
});

describe('BuildTakImportUrl', () => {
  it('percent-encodes the whole package URL', () => {
    const pkg = 'http://10.41.3.7:8080/onboard/atak-setup.zip?callsign=Bravo+6&team=Cyan';
    const link = buildTakImportUrl(pkg);
    expect(link.startsWith('tak://com.atakmap.app/import?url=')).toBe(true);
    expect(link).not.toContain('&team');
    expect(decodeURIComponent(link.split('url=')[1])).toBe(pkg);
  });
});

describe('DetectPlatform', () => {
  it.each([
    ['Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/129', 0, 'android'],
    ['Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)', 5, 'ios'],
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605', 5, 'ios'],
    ['Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Safari/605', 0, 'other'],
    ['Mozilla/5.0 (X11; Linux x86_64) Firefox/131', 0, 'other'],
    ['', 0, 'other'],
  ])('%s (%i touch points) is %s', (ua, touch, want) => {
    expect(detectPlatform(ua, touch)).toBe(want);
  });
});

describe('IsValidCallsign', () => {
  it.each([
    ['', true],
    ['Bravo 6', true],
    ['alpha-1.b_c', true],
    ['Ñandú', true],
    ['a'.repeat(40), true],
    ['a'.repeat(41), false],
    ['<b>', false],
    ['semi;colon', false],
  ])('%s → %s', (s, want) => {
    expect(isValidCallsign(s)).toBe(want);
  });
});

describe('FormatBytes', () => {
  it.each([
    [0, '0 B'],
    [-5, '0 B'],
    [Number.NaN, '0 B'],
    [512, '512 B'],
    [1500, '1.5 KB'],
    [215_000_000, '215 MB'],
    [1_200_000_000, '1.2 GB'],
  ])('%s → %s', (n, want) => {
    expect(formatBytes(n)).toBe(want);
  });
});

describe('FetchOnboardStatus', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('returns the JSON body', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true, json: async () => ({ node: 'n1' }) }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(fetchOnboardStatus()).resolves.toEqual({ node: 'n1' });
    expect(fetchMock).toHaveBeenCalledWith('/onboard/status', expect.objectContaining({ cache: 'no-store' }));
  });

  it('throws on an HTTP error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 503 })));
    await expect(fetchOnboardStatus()).rejects.toThrow('status 503');
  });
});

describe('RemoveAtakApk', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('sends DELETE and resolves on 204', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true, status: 204 }));
    vi.stubGlobal('fetch', fetchMock);
    await expect(removeAtakApk()).resolves.toBeUndefined();
    expect(fetchMock).toHaveBeenCalledWith('/api/onboard/atak', expect.objectContaining({ method: 'DELETE' }));
  });

  it('throws the server error', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 500, json: async () => ({ error: 'disk full' }) })));
    await expect(removeAtakApk()).rejects.toThrow('disk full');
  });
});

// MockXHR records the last request and lets a test finish it.
class MockXHR {
  static last = null;

  constructor() {
    this.upload = {};
    this.headers = {};
    MockXHR.last = this;
  }

  open(method, url) {
    this.method = method;
    this.url = url;
  }

  send(body) {
    this.body = body;
  }

  abort() {
    this.onabort?.();
  }

  respond(status, response) {
    this.status = status;
    this.response = response;
    this.onload?.();
  }
}

describe('UploadAtakApk', () => {
  beforeEach(() => {
    MockXHR.last = null;
    vi.stubGlobal('XMLHttpRequest', MockXHR);
  });

  afterEach(() => vi.unstubAllGlobals());

  it('posts the file as multipart field "file" and reports progress', async () => {
    const onProgress = vi.fn();
    const file = new File(['apk'], 'ATAK.apk');
    const p = uploadAtakApk(file, { onProgress });

    const xhr = MockXHR.last;
    expect(xhr.method).toBe('POST');
    expect(xhr.url).toBe('/api/onboard/atak');
    expect(xhr.body.get('file').name).toBe('ATAK.apk');

    xhr.upload.onprogress({ lengthComputable: true, loaded: 1, total: 3 });
    expect(onProgress).toHaveBeenCalledWith({ loaded: 1, total: 3 });

    xhr.respond(200, { name: 'ATAK.apk', size: 3 });
    await expect(p).resolves.toEqual({ name: 'ATAK.apk', size: 3 });
  });

  it('rejects with the server message and status', async () => {
    const p = uploadAtakApk(new File(['x'], 'x.apk'));
    MockXHR.last.respond(413, '{"error":"APK exceeds the size limit"}');
    await expect(p).rejects.toMatchObject({ message: 'APK exceeds the size limit', status: 413 });
  });

  it('signals session expiry on 401', async () => {
    const listener = vi.fn();
    window.addEventListener('session-expired', listener);
    const p = uploadAtakApk(new File(['x'], 'x.apk'));
    MockXHR.last.respond(401, null);
    await expect(p).rejects.toMatchObject({ status: 401 });
    expect(listener).toHaveBeenCalled();
    window.removeEventListener('session-expired', listener);
  });

  it('rejects with AbortError when aborted', async () => {
    const ctrl = new AbortController();
    const p = uploadAtakApk(new File(['x'], 'x.apk'), { signal: ctrl.signal });
    ctrl.abort();
    await expect(p).rejects.toMatchObject({ name: 'AbortError' });
  });
});
