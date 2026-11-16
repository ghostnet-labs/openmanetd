// =============================================================================
// tilesApi.js — node map tile cache (offline ATAK maps)
// =============================================================================
//
// Phones fetch tiles from /tiles/<layer>/<z>/<x>/<y> with no login. The
// admin endpoints below (login) show the cache and download an area
// ahead of time. See docs/offline-maps.md.

import authFetch from './authFetch.js';

export const TILES_STATUS_URL = '/api/tiles/status';
export const TILES_SEED_URL = '/api/tiles/seed';

async function errorFrom(resp, fallback) {
  const body = await resp.json().catch(() => null);
  const err = new Error(body?.error || `${fallback} (${resp.status})`);
  err.status = resp.status;
  return err;
}

// fetchTilesStatus returns the cache status, or throws.
export async function fetchTilesStatus({ signal } = {}) {
  const resp = await authFetch(TILES_STATUS_URL, { cache: 'no-store', signal });
  if (!resp.ok) throw await errorFrom(resp, 'status failed');
  return resp.json();
}

// startTileSeed downloads an area: { lat, lon, radiusKm, maxZoom, name }.
export async function startTileSeed(area) {
  const resp = await authFetch(TILES_SEED_URL, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(area),
  });
  if (!resp.ok) throw await errorFrom(resp, 'download failed');
  return resp.json();
}

// cancelTileSeed stops the running download.
export async function cancelTileSeed() {
  const resp = await authFetch(TILES_SEED_URL, { method: 'DELETE' });
  if (!resp.ok) throw await errorFrom(resp, 'stop failed');
}

// estimateSeedTiles approximates how many tiles an area needs across
// zooms 0..maxZoom for one layer, using the same bounding box as the
// node. It lets the form warn before asking the node.
export function estimateSeedTiles(lat, radiusKm, maxZoom) {
  if (!Number.isFinite(lat) || !(radiusKm > 0) || !(maxZoom >= 0)) return 0;
  const kmPerDeg = 111.32;
  const dLat = radiusKm / kmPerDeg;
  const dLon = radiusKm / (kmPerDeg * Math.max(Math.cos((lat * Math.PI) / 180), 0.01));
  let total = 0;
  for (let z = 0; z <= maxZoom; z += 1) {
    const n = 2 ** z;
    const tilesX = Math.min(n, Math.floor(((2 * dLon) / 360) * n) + 2);
    const latRad = (lat * Math.PI) / 180;
    const degPerTileY = (360 / n) * Math.cos(latRad);
    const tilesY = Math.min(n, Math.floor((2 * dLat) / Math.max(degPerTileY, 1e-9)) + 2);
    total += tilesX * tilesY;
  }
  return total;
}

// seedSummary describes a finished download in one line.
export function seedSummary(seed) {
  if (!seed) return null;
  const counts = `${seed.downloaded.toLocaleString()} downloaded, ${seed.cached.toLocaleString()} already stored`;
  if (seed.error === 'canceled') return { tone: 'warn', text: `Download stopped: ${counts}.` };
  if (seed.error) return { tone: 'crit', text: `Download failed: ${seed.error} (${counts}).` };
  if (seed.failed > 0) return { tone: 'warn', text: `Download finished with ${seed.failed} missing tiles: ${counts}.` };
  return { tone: 'ok', text: `Area ready offline: ${counts}.` };
}
