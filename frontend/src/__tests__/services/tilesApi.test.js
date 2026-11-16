// =============================================================================
// tilesApi.test.js — map tile cache admin client
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  TILES_SEED_URL,
  TILES_STATUS_URL,
  cancelTileSeed,
  estimateSeedTiles,
  fetchTilesStatus,
  startTileSeed,
} from '../../services/tilesApi.js';

function jsonResponse(status, body) {
  return { ok: status >= 200 && status < 300, status, json: () => Promise.resolve(body) };
}

describe('TilesApi', () => {
  let fetchMock;

  beforeEach(() => {
    fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
  });

  afterEach(() => {
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('fetches the status with credentials and no cache', async () => {
    fetchMock.mockResolvedValue(jsonResponse(200, { enabled: true }));
    await expect(fetchTilesStatus()).resolves.toEqual({ enabled: true });
    expect(fetchMock).toHaveBeenCalledWith(TILES_STATUS_URL, expect.objectContaining({ credentials: 'include', cache: 'no-store' }));
  });

  it('surfaces the server error message', async () => {
    fetchMock.mockResolvedValue(jsonResponse(400, { error: 'area is too large for the tile cache' }));
    await expect(startTileSeed({ lat: 1, lon: 2, radiusKm: 150 })).rejects.toThrow('area is too large');
  });

  it('falls back to a status code when the body is not JSON', async () => {
    fetchMock.mockResolvedValue({ ok: false, status: 503, json: () => Promise.reject(new Error('nope')) });
    await expect(fetchTilesStatus()).rejects.toThrow('status failed (503)');
  });

  it('posts the area as JSON', async () => {
    fetchMock.mockResolvedValue(jsonResponse(202, { running: true }));
    await startTileSeed({ lat: 40.3, lon: -112, radiusKm: 10, maxZoom: 16 });

    const [url, opts] = fetchMock.mock.calls[0];
    expect(url).toBe(TILES_SEED_URL);
    expect(opts.method).toBe('POST');
    expect(JSON.parse(opts.body)).toEqual({ lat: 40.3, lon: -112, radiusKm: 10, maxZoom: 16 });
  });

  it('cancels with DELETE', async () => {
    fetchMock.mockResolvedValue({ ok: true, status: 204, json: () => Promise.resolve(null) });
    await cancelTileSeed();
    expect(fetchMock).toHaveBeenCalledWith(TILES_SEED_URL, expect.objectContaining({ method: 'DELETE' }));
  });
});

describe('EstimateSeedTiles', () => {
  it('returns 0 for missing or invalid input', () => {
    expect(estimateSeedTiles(Number.NaN, 10, 16)).toBe(0);
    expect(estimateSeedTiles(40, 0, 16)).toBe(0);
    expect(estimateSeedTiles(40, 10, -1)).toBe(0);
  });

  it('counts one tile at zoom 0', () => {
    expect(estimateSeedTiles(40, 1, 0)).toBe(1);
  });

  it('grows with radius and zoom', () => {
    const small = estimateSeedTiles(40.3, 5, 14);
    expect(estimateSeedTiles(40.3, 10, 14)).toBeGreaterThan(small);
    expect(estimateSeedTiles(40.3, 5, 16)).toBeGreaterThan(small);
  });

  it('is in the same range as the node for a town-sized area', () => {
    // The node plans about 2,000 to 3,000 tiles for 10 km to zoom 16.
    const n = estimateSeedTiles(40.314, 10, 16);
    expect(n).toBeGreaterThan(1500);
    expect(n).toBeLessThan(5000);
  });
});
