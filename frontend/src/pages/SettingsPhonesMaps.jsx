// =============================================================================
// SettingsPhonesMaps.jsx — offline maps panel on the Phones settings page
// =============================================================================
//
// Shows the node's map tile cache (what ATAK phones load their satellite
// and topo maps from) and downloads an area ahead of time so the maps
// work with no internet. See docs/offline-maps.md.

import { useCallback, useMemo, useState } from 'react';
import { useGnssStatus } from '../hooks/useGnssStatus.js';
import { useVisibleInterval } from '../hooks/useVisibleInterval.js';
import { formatBytes } from '../services/onboardApi.js';
import {
  cancelTileSeed,
  estimateSeedTiles,
  fetchTilesStatus,
  seedSummary,
  startTileSeed,
} from '../services/tilesApi.js';

const POLL_IDLE_MS = 15000;
const POLL_SEEDING_MS = 2000;
const GNSS_POLL_MS = 10000;
const ZOOM_CHOICES = [12, 13, 14, 15, 16];
// EST_TILE_BYTES matches the node's planning size per tile.
const EST_TILE_BYTES = 20 * 1024;

const HEALTH = {
  ok: { tone: 'ok', label: 'Reachable' },
  offline: { tone: 'crit', label: 'Offline' },
  unknown: { tone: '', label: 'Not tried yet' },
};

export default function SettingsPhonesMaps() {
  const [state, setState] = useState({ loading: true, error: null, status: null });
  const [form, setForm] = useState({ lat: '', lon: '', radiusKm: '10', maxZoom: '16' });
  const [notice, setNotice] = useState(null); // { tone, text }
  const [busy, setBusy] = useState(false);
  const gnss = useGnssStatus(GNSS_POLL_MS);

  const seeding = !!state.status?.seed?.running;

  const load = useCallback(async () => {
    try {
      const status = await fetchTilesStatus();
      setState({ loading: false, error: null, status });
    } catch (err) {
      setState((prev) => ({ loading: false, error: err.message, status: prev.status }));
    }
  }, []);

  useVisibleInterval(load, seeding ? POLL_SEEDING_MS : POLL_IDLE_MS, [seeding]);

  const fix = gnss?.position;
  const hasFix = Number.isFinite(fix?.latitude) && Number.isFinite(fix?.longitude)
    && !(fix.latitude === 0 && fix.longitude === 0);

  const estimate = useMemo(() => {
    const lat = Number.parseFloat(form.lat);
    const radius = Number.parseFloat(form.radiusKm);
    const zoom = Number.parseInt(form.maxZoom, 10);
    const layers = state.status?.layers?.length || 1;
    const tiles = estimateSeedTiles(lat, radius, zoom) * layers;
    return { tiles, bytes: tiles * EST_TILE_BYTES };
  }, [form, state.status?.layers?.length]);

  const setField = useCallback((key) => (e) => setForm((f) => ({ ...f, [key]: e.target.value })), []);

  const fillNodePosition = useCallback(() => {
    if (!hasFix) return;
    setForm((f) => ({ ...f, lat: fix.latitude.toFixed(5), lon: fix.longitude.toFixed(5) }));
  }, [hasFix, fix]);

  const onStart = useCallback(async (e) => {
    e.preventDefault();
    setNotice(null);
    setBusy(true);
    try {
      await startTileSeed({
        name: 'Area around ' + form.lat + ', ' + form.lon,
        lat: Number.parseFloat(form.lat),
        lon: Number.parseFloat(form.lon),
        radiusKm: Number.parseFloat(form.radiusKm),
        maxZoom: Number.parseInt(form.maxZoom, 10),
      });
      setNotice({ tone: 'ok', text: 'Download started.' });
      await load();
    } catch (err) {
      setNotice({ tone: 'crit', text: err.message });
    } finally {
      setBusy(false);
    }
  }, [form, load]);

  const onStop = useCallback(async () => {
    setNotice(null);
    try {
      await cancelTileSeed();
      await load();
    } catch (err) {
      setNotice({ tone: 'crit', text: err.message });
    }
  }, [load]);

  if (state.loading) return <section className="lat-panel col-span-all">Loading…</section>;

  if (!state.status) {
    return (
      <section className="lat-panel col-span-all">
        <div className="panel-head"><h3>Offline maps</h3></div>
        <div className="lat-alert crit">Map status is unavailable ({state.error}).</div>
      </section>
    );
  }

  const { status } = state;

  if (!status.enabled) {
    return (
      <section className="lat-panel col-span-all">
        <div className="panel-head"><h3>Offline maps</h3></div>
        <div className="phones-empty">Map tiles are off on this node.</div>
        <p className="phones-help">Phones get no map layers until tiles.enable is turned back on.</p>
      </section>
    );
  }

  const usedPct = status.maxBytes > 0 ? Math.min(100, Math.round((status.usedBytes / status.maxBytes) * 100)) : 0;
  const usedTone = usedPct >= 95 ? 'warn' : '';
  const internet = HEALTH[status.internet?.health] || HEALTH.unknown;
  const layerText = (status.layers || []).map((l) => `${l.layer} (to zoom ${l.maxZoom})`).join(', ') || 'None';
  const peers = status.peers || [];
  const seed = status.seed;
  const seedPct = seed?.total > 0 ? Math.round((seed.done / seed.total) * 100) : 0;
  const lowDisk = status.freeBytes > 0 && status.freeBytes < status.minFreeBytes;
  const formValid = form.lat !== '' && form.lon !== '' && Number.parseFloat(form.radiusKm) > 0;
  const tooBig = estimate.bytes > status.maxBytes * 0.9;
  const lastRun = seed && !seed.running ? seedSummary(seed) : null;
  const estimateText = formValid
    ? `About ${estimate.tiles.toLocaleString()} tiles, ${formatBytes(estimate.bytes)}${tooBig ? ', more than the cache holds' : ''}.`
    : 'Pick the center and radius of the area phones will use.';
  const peerRows = peers.length === 0
    ? <div className="kv"><span className="k">Peer nodes</span><span className="v">None set</span></div>
    : peers.map((p) => {
      const h = HEALTH[p.health] || HEALTH.unknown;
      return (
        <div className="kv" key={p.name}>
          <span className="k">Peer</span>
          <span className={`v ${h.tone}`} title={p.name}>{peerLabel(p.name)} · {h.label}</span>
        </div>
      );
    });

  return (
    <section className="lat-panel col-span-all">
      <div className="panel-head"><h3>Offline maps</h3></div>

      <div className="phones-maps-grid">
        <div>
          <div className="pbar-row">
            <span className="pbar-label">Cache</span>
            <div className={`pbar ${usedTone}`}><span style={{ width: `${usedPct}%` }} /></div>
            <span className="pbar-val">{usedPct}%</span>
          </div>
          <div className="kv">
            <span className="k">Stored</span>
            <span className="v">{formatBytes(status.usedBytes)} of {formatBytes(status.maxBytes)}</span>
          </div>
          <div className="kv">
            <span className="k">Free space</span>
            <span className={`v ${lowDisk ? 'warn' : ''}`}>{formatBytes(status.freeBytes)}{lowDisk ? ' (low, not caching)' : ''}</span>
          </div>
          <div className="kv"><span className="k">Layers</span><span className="v">{layerText}</span></div>
          <div className="kv">
            <span className="k">Internet</span>
            <span className={`v ${internet.tone}`}>{internet.label}</span>
          </div>
          {peerRows}
          <div className="kv">
            <span className="k">Tiles served</span>
            <span className="v">{status.counters?.hits ?? 0} from cache, {status.counters?.misses ?? 0} fetched</span>
          </div>
        </div>

        <form className="phones-maps-form" onSubmit={onStart} aria-label="Download a map area">
          <div className="phones-maps-fields">
            <div className="lat-field">
              <label htmlFor="maps-lat">Latitude</label>
              <input id="maps-lat" className="lat-input phones-touch" type="number" inputMode="decimal" step="any" min="-85" max="85"
                value={form.lat} onChange={setField('lat')} placeholder="40.314" required />
            </div>
            <div className="lat-field">
              <label htmlFor="maps-lon">Longitude</label>
              <input id="maps-lon" className="lat-input phones-touch" type="number" inputMode="decimal" step="any" min="-180" max="180"
                value={form.lon} onChange={setField('lon')} placeholder="-112.006" required />
            </div>
            <div className="lat-field">
              <label htmlFor="maps-radius">Radius (km)</label>
              <input id="maps-radius" className="lat-input phones-touch" type="number" inputMode="decimal" step="any" min="0.1" max="200"
                value={form.radiusKm} onChange={setField('radiusKm')} required />
            </div>
            <div className="lat-field">
              <label htmlFor="maps-zoom">Detail (max zoom)</label>
              <select id="maps-zoom" className="lat-select phones-touch" value={form.maxZoom} onChange={setField('maxZoom')}>
                {ZOOM_CHOICES.map((z) => <option key={z} value={z}>{z}</option>)}
              </select>
            </div>
          </div>

          <div className={`phones-help ${tooBig ? 'phones-warn' : ''}`}>
            {estimateText}
          </div>

          {seeding && (
            <div className="pbar-row phones-progress">
              <span className="pbar-label">Download</span>
              <div className="pbar"><span style={{ width: `${seedPct}%` }} /></div>
              <span className="pbar-val">{seedPct}%</span>
            </div>
          )}
          {seeding && (
            <div className="phones-help">
              {seed.done.toLocaleString()} of {seed.total.toLocaleString()} tiles · {formatBytes(seed.bytes)} downloaded
            </div>
          )}
          {lastRun && <div className={`lat-alert ${lastRun.tone} phones-notice`}>{lastRun.text}</div>}
          {notice && <div className={`lat-alert ${notice.tone} phones-notice`}>{notice.text}</div>}

          <div className="phones-actions">
            {seeding ? (
              <button className="lat-btn danger phones-touch" type="button" onClick={onStop}>Stop download</button>
            ) : (
              <button className="lat-btn primary phones-touch" type="submit" disabled={busy || !formValid}>Download area</button>
            )}
            <button className="lat-btn ghost phones-touch" type="button" onClick={fillNodePosition} disabled={!hasFix}>
              {hasFix ? 'Use node position' : 'No GPS fix'}
            </button>
          </div>
        </form>
      </div>

      <p className="phones-help">
        Phones load satellite and topo maps from this node. Tiles come from peer nodes, then the internet, and are kept
        here so they work offline. Download your area while the node has internet.
      </p>
    </section>
  );
}

// peerLabel shortens a peer URL to host:port for narrow screens.
function peerLabel(url) {
  try {
    return new URL(url).host || url;
  } catch {
    return url;
  }
}
