// =============================================================================
// RadioCapacityPanel.jsx — radio capacity gate state on the Wireless page
// =============================================================================
//
// GHO-51 "EUD decision visibility": shows the capacity gate mode, the PHY
// inventory the gate checks against, and the most recent admission
// decisions with their reason and explanation. Read-only; polls
// RadioService every POLL_MS while the tab is visible. Capacity and
// decisions are fetched in parallel and fail independently.

import { useCallback, useEffect, useRef, useState } from 'react';
import { Code, ConnectError } from '@connectrpc/connect';
import DataTable from '../components/DataTable.jsx';
import { useVisibleInterval } from '../hooks/useVisibleInterval.js';
import { getRadioCapacity, listAdmissionDecisions } from '../services/radioApi.js';
import {
  decisionRows,
  formatAge,
  gateModeEffect,
  gateModeLabel,
  phyRows,
  tsToDate,
} from '../utils/radioCapacity.js';

export const POLL_MS = 15000;
export const DECISION_LIMIT = 10;

const PHY_COLUMNS = [
  { key: 'name', label: 'PHY', render: (r) => r.name },
  { key: 'bands', label: 'Bands', render: (r) => r.bands },
  { key: 'halow', label: 'HaLow', render: (r) => r.halow },
  { key: 'qualified', label: 'Qualified', render: (r) => r.qualified, cellClass: (r) => r.qualifiedClass },
  { key: 'available', label: 'Available', render: (r) => r.available, cellClass: (r) => r.availableClass },
];

const DECISION_COLUMNS = [
  { key: 'outcome', label: 'Outcome', render: (r) => r.outcome, cellClass: (r) => r.outcomeClass },
  { key: 'time', label: 'Time', render: (r) => <span title={r.timeTitle}>{r.time}</span> },
  { key: 'reason', label: 'Reason', render: (r) => r.reason },
  { key: 'effect', label: 'Effect', render: (r) => r.effect },
  { key: 'explanation', label: 'Explanation', render: (r) => r.explanation },
];

const phyKey = (r) => r.name;
const decisionKey = (r) => r.key;

function errorMessage(err) {
  return err instanceof ConnectError ? err.rawMessage : String(err?.message ?? err);
}

function isUnavailable(err) {
  return err instanceof ConnectError && err.code === Code.FailedPrecondition;
}

const INITIAL = {
  loading: true,
  unavailable: false,
  capacity: null,
  capacityError: null,
  decisions: [],
  decisionsError: null,
  fetchedAt: null,
};

export default function RadioCapacityPanel() {
  const [state, setState] = useState(INITIAL);
  const alive = useRef(true);

  const load = useCallback(async () => {
    const [cap, dec] = await Promise.allSettled([
      getRadioCapacity(),
      listAdmissionDecisions(DECISION_LIMIT),
    ]);
    if (!alive.current) return;

    const unavailable = (cap.status === 'rejected' && isUnavailable(cap.reason))
      || (dec.status === 'rejected' && isUnavailable(dec.reason));

    // A failed poll keeps the last good data and reports the failure.
    setState((prev) => ({
      loading: false,
      unavailable,
      capacity: cap.status === 'fulfilled' ? cap.value.capacity ?? null : prev.capacity,
      capacityError: cap.status === 'rejected' ? errorMessage(cap.reason) : null,
      decisions: dec.status === 'fulfilled' ? dec.value.decisions ?? [] : prev.decisions,
      decisionsError: dec.status === 'rejected' ? errorMessage(dec.reason) : null,
      fetchedAt: new Date(),
    }));
  }, []);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useVisibleInterval(load, POLL_MS);

  const { loading, unavailable, capacity, capacityError, decisions, decisionsError, fetchedAt } = state;
  const now = fetchedAt ?? new Date();
  const phys = phyRows(capacity);
  const rows = decisionRows(decisions, now);
  const inventoryAt = tsToDate(capacity?.inventoryUpdatedAt);
  const modeLabel = capacity ? gateModeLabel(capacity.gateMode) : '—';

  let body;
  if (loading) {
    body = <div className="lat-empty">Loading…</div>;
  } else if (unavailable) {
    body = <div className="lat-empty">Radio capacity gate not running.</div>;
  } else {
    body = (
      <>
        {capacityError && <div className="lat-alert crit">Radio inventory unavailable: {capacityError}</div>}
        {capacity && (
          <>
            <div className="kv"><span className="k">Gate mode</span><span className="v accent">{modeLabel}</span></div>
            <div className="kv"><span className="k">Defer / reject</span><span className="v">{gateModeEffect(capacity.gateMode)}</span></div>
            <div className="kv"><span className="k">Inventory</span><span className="v">{formatAge(inventoryAt, now)}</span></div>
            <h4 className="radio-cap-sub">PHY inventory</h4>
            <DataTable
              ariaLabel="PHY inventory"
              columns={PHY_COLUMNS}
              rows={phys}
              rowKey={phyKey}
              emptyLabel="No PHYs detected."
            />
          </>
        )}
        <h4 className="radio-cap-sub">Recent decisions</h4>
        {decisionsError ? (
          <div className="lat-alert crit">Decisions unavailable: {decisionsError}</div>
        ) : (
          <DataTable
            ariaLabel="Admission decisions"
            columns={DECISION_COLUMNS}
            rows={rows}
            rowKey={decisionKey}
            emptyLabel="No admission decisions yet."
          />
        )}
        <div className="radio-cap-note">Capacity model only; physical concurrency not evaluated.</div>
      </>
    );
  }

  return (
    <div className="lat-panel radio-capacity">
      <div className="panel-head">
        <h3>Radio capacity</h3>
        <div className="actions">
          {capacity && <span className="lat-chip">{modeLabel}</span>}
          <button type="button" onClick={load}>refresh</button>
        </div>
      </div>
      {body}
    </div>
  );
}
