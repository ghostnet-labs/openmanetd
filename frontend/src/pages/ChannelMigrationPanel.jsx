// =============================================================================
// ChannelMigrationPanel.jsx — channel migration state on the Wireless page
// =============================================================================
//
// GHO-52: read-only view of the authenticated channel-migration node —
// enabled, phase, plan version, peers heard and each managed band's channel
// and width. Polls ChannelMigrationService every POLL_MS while the tab is
// visible; a failed poll keeps the last good status. No propose UI.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ConnectError } from '@connectrpc/connect';
import DataTable from '../components/DataTable.jsx';
import { useVisibleInterval } from '../hooks/useVisibleInterval.js';
import { getChannelMigrationStatus } from '../services/chanmigApi.js';
import { bandRows, summarize } from '../utils/chanmig.js';

export const POLL_MS = 15000;

const BAND_COLUMNS = [
  { key: 'band', label: 'Band', render: (r) => r.band },
  { key: 'channel', label: 'Channel', render: (r) => r.channel },
  { key: 'target', label: 'Plan target', render: (r) => r.target },
  { key: 'mode', label: 'Mode', render: (r) => r.mode, cellClass: (r) => r.modeClass },
];

const bandKey = (r) => r.key;

function errorMessage(err) {
  return err instanceof ConnectError ? err.rawMessage : String(err?.message ?? err);
}

const INITIAL = { loading: true, status: null, error: null };

export default function ChannelMigrationPanel() {
  const [state, setState] = useState(INITIAL);
  const alive = useRef(true);

  const { loading, status, error } = state;
  const summary = useMemo(() => summarize(status), [status]);
  const rows = useMemo(() => bandRows(status?.bands), [status]);

  const load = useCallback(async () => {
    try {
      const res = await getChannelMigrationStatus();
      if (!alive.current) return;
      setState({ loading: false, status: res.status ?? null, error: null });
    } catch (err) {
      if (!alive.current) return;
      setState((prev) => ({ loading: false, status: prev.status, error: errorMessage(err) }));
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useVisibleInterval(load, POLL_MS);

  const chipLabel = summary.enabled ? 'ON' : 'OFF';
  const chipClass = summary.enabled ? 'lat-chip ok' : 'lat-chip';

  let body;
  if (loading) {
    body = <div className="lat-empty">Loading…</div>;
  } else if (!status && error) {
    body = <div className="lat-alert crit">Channel migration status unavailable: {error}</div>;
  } else if (!summary.enabled) {
    body = <div className="lat-empty">Channel migration disabled.</div>;
  } else {
    body = (
      <>
        {error ? <div className="lat-alert warn">Last refresh failed: {error}</div> : null}
        <div className="kv"><span className="k">Phase</span><span className={`v ${summary.phaseClass}`.trim()}>{summary.phase}</span></div>
        <div className="kv"><span className="k">Plan version</span><span className="v">{summary.planVersion}</span></div>
        <div className="kv"><span className="k">Peers</span><span className="v">{summary.peers}</span></div>
        <div className="kv"><span className="k">Role</span><span className="v">{summary.role}</span></div>
        <h4 className="chanmig-sub">Bands</h4>
        <DataTable
          ariaLabel="Channel migration bands"
          columns={BAND_COLUMNS}
          rows={rows}
          rowKey={bandKey}
          emptyLabel="No managed bands."
        />
      </>
    );
  }

  return (
    <div className="lat-panel chanmig-panel">
      <div className="panel-head">
        <h3>Channel migration</h3>
        <div className="actions">
          {loading ? null : <span className={chipClass}>{chipLabel}</span>}
          <button type="button" onClick={load}>refresh</button>
        </div>
      </div>
      {body}
    </div>
  );
}
