// =============================================================================
// radioCapacity.js — display helpers for RadioService responses
// =============================================================================
//
// Pure functions: enum → label / status class, timestamps → Date / age, and
// the row shapes the Radio capacity panel renders. No React.

import {
  AdmissionOutcome,
  AdmissionReason,
  Band,
  CapacityGateMode,
} from '../gen/openmanet/radio/v1/radio_pb.js';

const GATE_MODE_LABELS = {
  [CapacityGateMode.OFF]: 'Off',
  [CapacityGateMode.ADVISORY]: 'Advisory',
  [CapacityGateMode.ENFORCE]: 'Enforce',
};

// What each mode does to a defer or reject, in one line.
const GATE_MODE_EFFECTS = {
  [CapacityGateMode.OFF]: 'Not checked',
  [CapacityGateMode.ADVISORY]: 'Recorded, never blocks',
  [CapacityGateMode.ENFORCE]: 'Defer / reject blocks the change',
};

const BAND_LABELS = {
  [Band.BAND_HALOW]: 'HaLow',
  [Band.BAND_2_4_GHZ]: '2.4 GHz',
  [Band.BAND_5_GHZ]: '5 GHz',
  [Band.BAND_6_GHZ]: '6 GHz',
  [Band.BAND_60_GHZ]: '60 GHz',
};

const OUTCOMES = {
  [AdmissionOutcome.ADMIT]: { label: 'ADMIT', className: 'badge-ok' },
  [AdmissionOutcome.DEFER]: { label: 'DEFER', className: 'badge-warn' },
  [AdmissionOutcome.REJECT]: { label: 'REJECT', className: 'badge-crit' },
};

const REASON_LABELS = {
  [AdmissionReason.FITS]: 'Fits',
  [AdmissionReason.CAPACITY_SHORT]: 'Capacity short',
  [AdmissionReason.NO_ELIGIBLE_PHY]: 'No eligible PHY',
  [AdmissionReason.UNQUALIFIED_ONLY]: 'Unqualified PHYs only',
  [AdmissionReason.PHY_UNAVAILABLE]: 'PHY unavailable',
  [AdmissionReason.BOUNDED_INTERRUPTION_APPROVED]: 'Bounded interruption',
  [AdmissionReason.SEARCH_BUDGET_EXCEEDED]: 'Search budget exceeded',
};

export function gateModeLabel(mode) {
  return GATE_MODE_LABELS[mode] ?? 'Unknown';
}

export function gateModeEffect(mode) {
  return GATE_MODE_EFFECTS[mode] ?? '—';
}

export function bandLabel(band) {
  return BAND_LABELS[band] ?? '?';
}

// bandsLabel joins a PHY's bands: "2.4 GHz / 5 GHz", "—" when none.
export function bandsLabel(bands) {
  if (!bands || bands.length === 0) return '—';
  return bands.map(bandLabel).join(' / ');
}

// outcome returns { label, className }; className is a Lattice badge class
// so the color always comes with a text label.
export function outcome(o) {
  return OUTCOMES[o] ?? { label: 'UNKNOWN', className: 'mono' };
}

export function reasonLabel(reason) {
  return REASON_LABELS[reason] ?? 'Unknown';
}

// tsToDate converts a google.protobuf.Timestamp to a Date, null when unset.
export function tsToDate(ts) {
  if (!ts) return null;
  const seconds = Number(ts.seconds);
  if (!Number.isFinite(seconds)) return null;
  return new Date(seconds * 1000 + Math.floor((ts.nanos ?? 0) / 1e6));
}

// formatAge renders how long before now a Date is: "12s ago", "4m ago",
// "3h ago", "2d ago". A future date (clock skew) reads "just now".
export function formatAge(date, now) {
  if (!date) return '—';
  const sec = Math.floor((now.getTime() - date.getTime()) / 1000);
  if (sec < 1) return 'just now';
  if (sec < 60) return `${sec}s ago`;
  if (sec < 3600) return `${Math.floor(sec / 60)}m ago`;
  if (sec < 86400) return `${Math.floor(sec / 3600)}h ago`;
  return `${Math.floor(sec / 86400)}d ago`;
}

function yesNo(v) {
  return v ? 'YES' : 'NO';
}

// phyRows shapes RadioCapacity.phys for the inventory table.
export function phyRows(capacity) {
  const phys = capacity?.phys ?? [];
  return phys.map((p) => ({
    name: p.name || '—',
    bands: bandsLabel(p.bands),
    halow: yesNo(p.isHalow),
    qualified: yesNo(p.qualified),
    qualifiedClass: p.qualified ? 'badge-ok' : 'badge-warn',
    available: p.available ? 'UP' : 'DOWN',
    availableClass: p.available ? 'badge-ok' : 'badge-crit',
  }));
}

// decisionEffect says what the decision did to the change it was made for:
// an admit applies; a defer or reject blocks only under enforce.
export function decisionEffect(d) {
  if (d.outcome === AdmissionOutcome.ADMIT) return 'Applied';
  if (!OUTCOMES[d.outcome]) return '—';
  if (d.gateMode === CapacityGateMode.ENFORCE) return 'Blocked';
  return 'Not blocked (advisory)';
}

// decisionRows shapes AdmissionDecision messages for the decision table.
// now anchors the relative time so a render is deterministic.
export function decisionRows(decisions, now) {
  return (decisions ?? []).map((d, i) => {
    const at = tsToDate(d.decidedAt);
    const o = outcome(d.outcome);
    return {
      key: `${at ? at.getTime() : 'na'}-${i}`,
      time: formatAge(at, now),
      timeTitle: at ? at.toISOString() : '',
      outcome: o.label,
      outcomeClass: o.className,
      reason: reasonLabel(d.reason),
      effect: decisionEffect(d),
      explanation: d.explanation || '—',
    };
  });
}
