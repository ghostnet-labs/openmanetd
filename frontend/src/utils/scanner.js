// =============================================================================
// scanner.js — display helpers for ScannerService responses
// =============================================================================
//
// Pure functions: enum → label / status class and the row shapes the
// Scanner page renders. No React. Status classes follow Lattice semantics:
// ok = healthy, warn = degraded, crit = failure, accent = live/active.

import {
  AircraftSource,
  ControlChannelState,
  PositionSource,
  ReceiverHardware,
  ReceiverRole,
  ReceiverState,
  SiteEncryption,
  SiteSelectionReason,
} from '../gen/openmanet/scanner/v1/scanner_pb.js';
import { formatAgo, timestampToDate } from './gnss.js';

const DASH = '—';

const ROLE_LABELS = {
  [ReceiverRole.ADSB_1090]: '1090 ADS-B',
  [ReceiverRole.UAT_978]: '978 UAT',
  [ReceiverRole.WIDE]: 'Wide (P25)',
  [ReceiverRole.SCAN]: 'Scan',
  [ReceiverRole.UNASSIGNED]: 'Unassigned',
};

// Receiver states with the chip class they render in.
const RECEIVER_STATES = {
  [ReceiverState.RUNNING]: { label: 'RUNNING', className: 'ok' },
  [ReceiverState.STOPPED]: { label: 'STOPPED', className: 'warn' },
  [ReceiverState.MISSING]: { label: 'MISSING', className: 'crit' },
  [ReceiverState.ERROR]: { label: 'ERROR', className: 'crit' },
};

const HARDWARE_LABELS = {
  [ReceiverHardware.RTL_SDR]: 'RTL-SDR',
  [ReceiverHardware.AIRSPY]: 'Airspy',
};

const CONTROL_STATES = {
  [ControlChannelState.UNAVAILABLE]: { label: 'NO DECODER', className: 'warn' },
  [ControlChannelState.IDLE]: { label: 'IDLE', className: '' },
  [ControlChannelState.HUNTING]: { label: 'HUNTING', className: 'warn' },
  [ControlChannelState.LOCKED]: { label: 'LOCKED', className: 'ok' },
  [ControlChannelState.ERROR]: { label: 'FAIL', className: 'crit' },
};

const POSITION_LABELS = {
  [PositionSource.NONE]: 'NO FIX',
  [PositionSource.GPSD]: 'GPS',
  [PositionSource.LAST_KNOWN]: 'LAST KNOWN',
  [PositionSource.MANUAL]: 'MANUAL SITE',
};

const REASON_LABELS = {
  [SiteSelectionReason.NEAREST]: 'Nearest to fix',
  [SiteSelectionReason.HYSTERESIS]: 'Held (hysteresis)',
  [SiteSelectionReason.LAST_KNOWN]: 'Held (fix stale)',
  [SiteSelectionReason.MANUAL]: 'Manual',
  [SiteSelectionReason.STALE_FIX]: 'Nearest to stale fix',
};

const ENCRYPTION_LABELS = {
  [SiteEncryption.NONE]: { label: 'NONE', className: 'ok' },
  [SiteEncryption.PARTIAL]: { label: 'PARTIAL', className: 'warn' },
  [SiteEncryption.FULL]: { label: 'FULL', className: 'crit' },
};

const SOURCE_LABELS = {
  [AircraftSource.ADSB_1090]: '1090',
  [AircraftSource.UAT_978]: '978',
  [AircraftSource.BOTH]: 'BOTH',
};

const UNKNOWN = { label: DASH, className: '' };

export function roleLabel(role) {
  return ROLE_LABELS[role] ?? DASH;
}

export function receiverStateInfo(state) {
  return RECEIVER_STATES[state] ?? UNKNOWN;
}

export function controlStateInfo(state) {
  return CONTROL_STATES[state] ?? UNKNOWN;
}

export function positionLabel(source) {
  return POSITION_LABELS[source] ?? POSITION_LABELS[PositionSource.NONE];
}

export function reasonLabel(reason) {
  return REASON_LABELS[reason] ?? DASH;
}

export function encryptionInfo(enc) {
  return ENCRYPTION_LABELS[enc] ?? UNKNOWN;
}

// formatMHz renders a Hz value (bigint or number) as MHz with up to five
// decimals, trailing zeros trimmed: 770006250n → "770.00625 MHz".
export function formatMHz(hz) {
  if (hz == null) return DASH;
  const n = Number(hz);
  if (!Number.isFinite(n) || n <= 0) return DASH;
  const mhz = (n / 1e6).toFixed(5).replace(/0+$/, '').replace(/\.$/, '');
  return `${mhz} MHz`;
}

export function formatKm(km) {
  if (km == null || !Number.isFinite(km)) return DASH;
  return km < 10 ? `${km.toFixed(1)} km` : `${Math.round(km)} km`;
}

// formatHex renders an id as upper-case hex: 0xBEE00 → "BEE00".
export function formatHex(v) {
  if (v == null || v === 0) return DASH;
  return Number(v).toString(16).toUpperCase();
}

function optNumber(v, digits = 0, unit = '') {
  if (v == null || !Number.isFinite(v)) return DASH;
  return `${digits ? v.toFixed(digits) : Math.round(v)}${unit}`;
}

export function receiverRows(receivers, now = Date.now()) {
  return (receivers ?? []).map((r, i) => {
    const state = receiverStateInfo(r.state);
    return {
      key: `${r.role}-${r.serial || i}`,
      role: roleLabel(r.role),
      serial: r.serial || DASH,
      hardware: r.present ? (HARDWARE_LABELS[r.hardware] ?? DASH) : DASH,
      owner: r.owner || DASH,
      state: state.label,
      stateClass: state.className,
      lastSample: formatAgo(r.lastSampleTime, now) ?? DASH,
      detail: r.detail || '',
    };
  });
}

export function aircraftRows(aircraft, now = Date.now()) {
  return (aircraft ?? []).map((a) => {
    let altitude = DASH;
    if (a.onGround) altitude = 'GND';
    else if (a.altitudeFt != null) altitude = `${a.altitudeFt.toLocaleString('en-US')} ft`;

    return {
      key: a.icao,
      icao: a.icao.toUpperCase(),
      callsign: a.callsign || DASH,
      squawk: a.squawk || DASH,
      emergencySquawk: a.squawk === '7500' || a.squawk === '7600' || a.squawk === '7700',
      altitude,
      speed: optNumber(a.groundSpeedKt, 0, ' kt'),
      track: optNumber(a.trackDeg, 0, '°'),
      vrate: a.verticalRateFpm != null ? `${a.verticalRateFpm > 0 ? '+' : ''}${a.verticalRateFpm}` : DASH,
      position: a.latitude != null && a.longitude != null
        ? `${a.latitude.toFixed(4)}, ${a.longitude.toFixed(4)}`
        : DASH,
      source: SOURCE_LABELS[a.source] ?? DASH,
      rssi: optNumber(a.rssiDbfs, 1, ' dBFS'),
      seen: formatAgo(a.lastSeen, now) ?? DASH,
      stale: !!a.stale,
    };
  });
}

export function grantRows(grants, now = Date.now()) {
  return (grants ?? []).map((g, i) => {
    const start = timestampToDate(g.startTime);
    return {
      key: `${g.talkgroup}-${start?.getTime() ?? i}`,
      talkgroup: String(g.talkgroup),
      source: g.sourceUnit ? String(g.sourceUnit) : DASH,
      frequency: formatMHz(g.frequencyHz),
      encrypted: !!g.encrypted,
      emergency: !!g.emergency,
      grants: g.grants ?? 0,
      last: formatAgo(g.lastTime, now) ?? DASH,
    };
  });
}

// summarize flattens GetScannerStatusResponse into the values the page
// shows, with explicit flags for every degraded state.
export function summarize(status) {
  if (!status || !status.enabled) return { enabled: false };

  const sel = status.selectedSite;
  const site = sel?.site;
  const cc = controlStateInfo(status.controlChannel?.state);
  const enc = encryptionInfo(site?.encryption);

  return {
    enabled: true,
    position: positionLabel(status.position?.source),
    positionStale: !!status.positionStale,
    coords: status.position && status.position.source !== PositionSource.NONE
      ? `${status.position.latitude.toFixed(4)}, ${status.position.longitude.toFixed(4)}`
      : DASH,
    siteUnavailable: !!status.siteUnavailable,
    decoderUnavailable: !!status.decoderUnavailable,
    decodeStale: !!status.decodeStale,
    aircraftStale: !!status.aircraftStale,
    site: site
      ? {
        name: site.name,
        system: site.system,
        county: site.county,
        state: site.state,
        distance: formatKm(site.distanceKm),
        reason: reasonLabel(sel.reason),
        controlChannel: formatMHz(sel.controlChannelHz),
        center: formatMHz(sel.wideTuner?.centerHz),
        fits: !!sel.wideTuner?.fits,
        uncovered: sel.wideTuner?.uncoveredHz?.length ?? 0,
        encryption: enc.label,
        encryptionClass: enc.className,
      }
      : null,
    control: cc.label,
    controlClass: cc.className,
    nac: formatHex(status.controlChannel?.nac),
    wacn: formatHex(status.controlChannel?.wacn),
    systemId: formatHex(status.controlChannel?.systemId),
    lastDecode: formatAgo(status.lastDecodeTime) ?? 'never',
    detail: status.controlChannel?.detail || '',
  };
}
