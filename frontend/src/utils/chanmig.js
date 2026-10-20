// =============================================================================
// chanmig.js — display helpers for ChannelMigrationService responses
// =============================================================================
//
// Pure functions: enum → label / status class and the row shapes the
// Channel migration panel renders. No React.

import { BandMode, MigrationPhase } from '../gen/openmanet/chanmig/v1/chanmig_pb.js';
import { Band } from '../gen/openmanet/radio/v1/radio_pb.js';

// Phase labels with the .kv value class they render in. Stable is healthy,
// a scheduled or running switch is live activity, rediscovery is degraded.
const PHASES = {
  [MigrationPhase.BOOTING]: { label: 'BOOTING', className: '' },
  [MigrationPhase.STABLE]: { label: 'STABLE', className: 'ok' },
  [MigrationPhase.PENDING]: { label: 'SWITCH PENDING', className: 'accent' },
  [MigrationPhase.MIGRATING]: { label: 'MIGRATING', className: 'accent' },
  [MigrationPhase.SEARCHING]: { label: 'SEARCHING', className: 'warn' },
};

const BAND_LABELS = {
  [Band.BAND_HALOW]: 'HaLow',
  [Band.BAND_2_4_GHZ]: '2.4 GHz',
  [Band.BAND_5_GHZ]: '5 GHz',
};

// Band modes with the DataTable badge class they render in.
const MODES = {
  [BandMode.SETTLED]: { label: 'SETTLED', className: 'badge-ok' },
  [BandMode.VERIFYING]: { label: 'VERIFYING', className: '' },
  [BandMode.FALLBACK]: { label: 'FALLBACK', className: 'badge-warn' },
  [BandMode.SEARCHING]: { label: 'SEARCHING', className: 'badge-warn' },
};

const UNKNOWN = { label: '—', className: '' };

export function phaseInfo(phase) {
  return PHASES[phase] ?? UNKNOWN;
}

export function bandLabel(band) {
  return BAND_LABELS[band] ?? '—';
}

// assignmentLabel renders a ChannelAssignment as "ch 28 · 2 MHz".
export function assignmentLabel(a) {
  if (!a || !a.channel) return '—';
  return `ch ${a.channel} · ${a.widthMhz} MHz`;
}

function sameAssignment(a, b) {
  return Boolean(a && b) && a.band === b.band && a.channel === b.channel && a.widthMhz === b.widthMhz;
}

// planVersionLabel is "v4", or "Initial" while the node runs on its
// provisioned assignments.
export function planVersionLabel(plan) {
  if (!plan) return 'Initial';
  return `v${plan.version}`;
}

// bandRows maps BandStatus entries to table rows. A failed radio reads
// FAIL whatever its mode; the target column shows "—" when the band
// already sits on it.
export function bandRows(bands) {
  if (!bands) return [];
  return bands.map((b) => {
    const mode = b.radioFailed ? { label: 'FAIL', className: 'badge-crit' } : (MODES[b.mode] ?? UNKNOWN);
    return {
      key: String(b.band),
      band: bandLabel(b.band),
      channel: assignmentLabel(b.operating),
      target: sameAssignment(b.operating, b.target) ? '—' : assignmentLabel(b.target),
      mode: mode.label,
      modeClass: mode.className,
    };
  });
}

// summarize reduces a ChannelMigrationStatus to the panel's header values.
export function summarize(status) {
  if (!status || !status.enabled) {
    return { enabled: false };
  }
  const phase = phaseInfo(status.phase);
  return {
    enabled: true,
    phase: phase.label,
    phaseClass: phase.className,
    planVersion: planVersionLabel(status.currentPlan),
    peers: status.peers?.length ?? 0,
    role: status.coordinator ? 'Coordinator' : 'Member',
  };
}
