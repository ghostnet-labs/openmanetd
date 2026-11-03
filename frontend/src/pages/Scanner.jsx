// =============================================================================
// Scanner.jsx — aircraft receivers, P25 site monitor, conventional scan (GHO-53)
// =============================================================================
//
// Polls ScannerService while the tab is visible: receiver ownership and
// state, the P25 site the wide tuner follows, control-channel decoder state,
// merged 1090/978 aircraft and recent talkgroup voice grants. Every degraded
// state (no fix, stale aircraft feeds, no decoder, decode stale) is shown as
// an explicit alert rather than silently empty panels. A failed poll keeps
// the last good data and shows the error.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ConnectError } from '@connectrpc/connect';
import DataTable from '../components/DataTable.jsx';
import { useVisibleInterval } from '../hooks/useVisibleInterval.js';
import {
  getScannerConfig,
  getScannerStatus,
  listAircraft,
  listConventionalChannels,
  listTalkgroupActivity,
  setScannerConfig,
} from '../services/scannerApi.js';
import { aircraftRows, grantRows, receiverRows, summarize } from '../utils/scanner.js';
import ScannerConventional from './ScannerConventional.jsx';
import './Scanner.css';

export const POLL_MS = 2000;
const ACTIVITY_LIMIT = 50;

const rowKey = (r) => r.key;

const RECEIVER_COLUMNS = [
  { key: 'role', label: 'Role', render: (r) => r.role },
  {
    key: 'state',
    label: 'State',
    render: (r) => <span className={`lat-chip ${r.stateClass}`.trim()}>{r.state}</span>,
  },
  { key: 'serial', label: 'Serial', render: (r) => r.serial, className: 'mono' },
  { key: 'hardware', label: 'Hardware', render: (r) => r.hardware },
  { key: 'owner', label: 'Owner', render: (r) => r.owner },
  { key: 'lastSample', label: 'Last data', render: (r) => r.lastSample },
  { key: 'detail', label: 'Detail', render: (r) => r.detail },
];

const AIRCRAFT_COLUMNS = [
  { key: 'icao', label: 'ICAO', render: (r) => r.icao, className: 'mono' },
  { key: 'callsign', label: 'Callsign', render: (r) => r.callsign },
  { key: 'squawk', label: 'Squawk', render: (r) => r.squawk, cellClass: (r) => (r.emergencySquawk ? 'badge-crit' : '') },
  { key: 'altitude', label: 'Altitude', render: (r) => r.altitude, headerClass: 'num' },
  { key: 'speed', label: 'GS', render: (r) => r.speed, headerClass: 'num' },
  { key: 'track', label: 'Track', render: (r) => r.track, headerClass: 'num' },
  { key: 'vrate', label: 'V/S fpm', render: (r) => r.vrate, headerClass: 'num' },
  { key: 'position', label: 'Position', render: (r) => r.position, className: 'mono' },
  { key: 'source', label: 'Source', render: (r) => r.source },
  { key: 'rssi', label: 'RSSI', render: (r) => r.rssi },
  { key: 'seen', label: 'Seen', render: (r) => (r.stale ? `${r.seen} · STALE` : r.seen), cellClass: (r) => (r.stale ? 'badge-warn' : '') },
];

function GrantFlags({ row }) {
  if (!row.encrypted && !row.emergency) return '—';
  return (
    <span className="scanner-flags">
      {row.emergency ? <span className="lat-chip crit">EMERGENCY</span> : null}
      {row.encrypted ? <span className="lat-chip warn">ENCRYPTED</span> : null}
    </span>
  );
}

const GRANT_COLUMNS = [
  { key: 'talkgroup', label: 'Talkgroup', render: (r) => r.talkgroup, className: 'mono' },
  { key: 'flags', label: 'Flags', render: (r) => <GrantFlags row={r} /> },
  { key: 'source', label: 'Source unit', render: (r) => r.source, className: 'mono' },
  { key: 'frequency', label: 'Frequency', render: (r) => r.frequency },
  { key: 'grants', label: 'Grants', render: (r) => r.grants, headerClass: 'num' },
  { key: 'last', label: 'Last', render: (r) => r.last },
];

function errorMessage(err) {
  return err instanceof ConnectError ? err.rawMessage : String(err?.message ?? err);
}

const INITIAL = { loading: true, status: null, aircraft: null, grants: null, conventional: null, convError: null, error: null };

function Kv({ k, v, className = '' }) {
  return (
    <div className="kv"><span className="k">{k}</span><span className={`v ${className}`.trim()}>{v}</span></div>
  );
}

export default function Scanner() {
  const [state, setState] = useState(INITIAL);
  const [config, setConfig] = useState(null);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState(null);
  const alive = useRef(true);

  const { loading, status, aircraft, grants, conventional, convError, error } = state;
  const summary = useMemo(() => summarize(status), [status]);
  const receivers = useMemo(() => receiverRows(status?.receivers), [status]);
  const planes = useMemo(() => aircraftRows(aircraft?.aircraft), [aircraft]);
  const calls = useMemo(() => grantRows(grants?.grants), [grants]);

  const load = useCallback(async () => {
    // Independent reads go out together; one failing does not hide the
    // others' data.
    const [st, ac, tg, cv] = await Promise.allSettled([
      getScannerStatus(),
      listAircraft(false),
      listTalkgroupActivity(ACTIVITY_LIMIT),
      listConventionalChannels(),
    ]);
    if (!alive.current) return;

    const failed = [st, ac, tg].find((r) => r.status === 'rejected');
    setState((prev) => ({
      loading: false,
      status: st.status === 'fulfilled' ? st.value : prev.status,
      aircraft: ac.status === 'fulfilled' ? ac.value : prev.aircraft,
      grants: tg.status === 'fulfilled' ? tg.value : prev.grants,
      conventional: cv.status === 'fulfilled' ? cv.value : prev.conventional,
      convError: cv.status === 'rejected' ? errorMessage(cv.reason) : null,
      error: failed ? errorMessage(failed.reason) : null,
    }));
  }, []);

  useEffect(() => {
    alive.current = true;
    getScannerConfig()
      .then((res) => { if (alive.current) setConfig(res.config ?? null); })
      .catch((err) => { if (alive.current) setSaveError(errorMessage(err)); });
    return () => { alive.current = false; };
  }, []);

  useVisibleInterval(load, POLL_MS);

  const toggleEnabled = useCallback(async () => {
    if (!config) return;
    setSaving(true);
    setSaveError(null);
    try {
      const res = await setScannerConfig({ ...config, enabled: !config.enabled });
      if (!alive.current) return;
      setConfig(res.config ?? null);
      load();
    } catch (err) {
      if (alive.current) setSaveError(errorMessage(err));
    } finally {
      if (alive.current) setSaving(false);
    }
  }, [config, load]);

  const enabled = summary.enabled;
  const configOn = config?.enabled ?? enabled;
  const aircraftCount = planes.length;

  const toolbar = (
    <div className="lat-view-toolbar scanner-toolbar">
      <button
        type="button"
        className={`lat-toggle${configOn ? ' on' : ''}`}
        onClick={toggleEnabled}
        disabled={!config || saving}
        role="switch"
        aria-checked={configOn}
        aria-label="Enable scanner"
      >
        <span className="track"><span className="thumb" /></span>
        <span className="label">{configOn ? 'On' : 'Off'}</span>
      </button>
      <button type="button" className="lat-btn ghost" onClick={load}>REFRESH</button>
    </div>
  );

  let body;
  if (loading) {
    body = <div className="lat-panel col-span-all scanner-loading">Loading…</div>;
  } else if (!status && error) {
    body = (
      <div className="scanner-alerts">
        <div className="lat-alert crit">Scanner status unavailable: {error}</div>
      </div>
    );
  } else if (!enabled) {
    body = (
      <div className="lat-panel col-span-all">
        <div className="lat-empty">Scanner disabled. Turn it on to start aircraft and P25 monitoring.</div>
      </div>
    );
  } else {
    const site = summary.site;
    let fitLabel = '—';
    if (site) fitLabel = site.fits ? 'ALL CHANNELS' : `${site.uncovered} OUTSIDE`;
    const positionAlert = summary.position === 'NO FIX' ? 'unavailable: no GPS fix' : 'stale';
    // An unavailable decoder is degraded, not failed: its cause is a warning.
    const detailClass = summary.controlClass === 'crit' ? 'crit' : 'warn';
    const alerts = [];
    if (error) alerts.push({ cls: 'warn', text: `Last refresh failed: ${error}` });
    if (summary.positionStale) alerts.push({ cls: 'warn', text: `Position ${positionAlert}.` });
    if (summary.siteUnavailable) alerts.push({ cls: 'crit', text: 'No P25 site selected: no fix, no last known position and no manual site.' });
    if (summary.decoderUnavailable) alerts.push({ cls: 'warn', text: 'Control-channel decoder not available on this node.' });
    if (summary.decodeStale) alerts.push({ cls: 'warn', text: 'Control channel stale: nothing decoded recently.' });
    if (summary.aircraftStale) alerts.push({ cls: 'warn', text: 'Aircraft feeds stale: dump1090 / skyaware978 not updating.' });

    body = (
      <>
        {alerts.length > 0 ? (
          <div className="scanner-alerts">
            {alerts.map((a) => <div key={a.text} className={`lat-alert ${a.cls}`}>{a.text}</div>)}
          </div>
        ) : null}

        <div className="lat-panel">
          <div className="panel-head"><h3>Selected site</h3></div>
          {site ? (
            <>
              <Kv k="Site" v={site.name} />
              <Kv k="System" v={site.system} />
              <Kv k="County" v={`${site.county}, ${site.state}`} />
              <Kv k="Distance" v={site.distance} className="accent" />
              <Kv k="Why" v={site.reason} />
              <Kv k="Encryption" v={site.encryption} className={site.encryptionClass} />
            </>
          ) : <div className="lat-empty">No site selected.</div>}
        </div>

        <div className="lat-panel">
          <div className="panel-head"><h3>Control channel</h3></div>
          <Kv k="State" v={summary.control} className={summary.controlClass} />
          <Kv k="Frequency" v={site ? site.controlChannel : '—'} />
          <Kv k="Tuner center" v={site ? site.center : '—'} />
          <Kv k="Tuner fit" v={fitLabel} className={site && !site.fits ? 'warn' : ''} />
          <Kv k="NAC" v={summary.nac} />
          <Kv k="WACN / SYS" v={`${summary.wacn} / ${summary.systemId}`} />
          <Kv k="Last decode" v={summary.lastDecode} className={summary.decodeStale ? 'warn' : ''} />
          {summary.detail ? <div className={`lat-alert ${detailClass}`}>{summary.detail}</div> : null}
        </div>

        <div className="lat-panel">
          <div className="panel-head"><h3>Position</h3></div>
          <Kv k="Source" v={summary.position} className={summary.positionStale ? 'warn' : 'ok'} />
          <Kv k="Coordinates" v={summary.coords} />
          <Kv k="Aircraft" v={String(aircraftCount)} className="accent" />
        </div>

        <ScannerConventional data={conventional} error={convError} />

        <div className="lat-panel col-span-all">
          <div className="panel-head"><h3>Receivers</h3></div>
          <DataTable ariaLabel="Receivers" columns={RECEIVER_COLUMNS} rows={receivers} rowKey={rowKey} emptyLabel="No receivers." />
        </div>

        <div className="lat-panel col-span-all">
          <div className="panel-head"><h3>Aircraft</h3></div>
          <DataTable ariaLabel="Aircraft" columns={AIRCRAFT_COLUMNS} rows={planes} rowKey={rowKey} emptyLabel="No aircraft." />
        </div>

        <div className="lat-panel col-span-all">
          <div className="panel-head"><h3>Talkgroup activity</h3></div>
          <DataTable ariaLabel="Talkgroup activity" columns={GRANT_COLUMNS} rows={calls} rowKey={rowKey} emptyLabel="No talkgroup activity." />
        </div>
      </>
    );
  }

  let ccChip = null;
  if (enabled) ccChip = <span className={`lat-chip ${summary.controlClass}`.trim()}>CC {summary.control}</span>;

  return (
    <>
      <div className="lat-topbar">
        <div className="node-id">
          SCANNER
          <span className="ip">{enabled ? 'ACTIVE' : 'OFF'}</span>
        </div>
        <div className="chips">
          <span className={`lat-chip ${enabled ? 'ok' : ''}`.trim()}>{enabled ? 'ON' : 'OFF'}</span>
          {ccChip}
          {enabled ? <span className="lat-chip">{aircraftCount} AIRCRAFT</span> : null}
        </div>
      </div>
      <div className="lat-view-header">
        <div>
          <h2>Scanner</h2>
          <div className="crumb">Aircraft 1090 / 978 · P25 trunking · Conventional FM / AM</div>
        </div>
        {toolbar}
      </div>
      {saveError ? <div className="lat-alert crit scanner-save-error">Scanner config: {saveError}</div> : null}
      <div className="lat-body grid-3">{body}</div>
    </>
  );
}
