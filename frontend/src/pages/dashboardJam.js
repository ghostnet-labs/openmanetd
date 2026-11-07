// =============================================================================
// dashboardJam.js — pure helpers for the Dashboard jamming panel and alerts
// =============================================================================
//
// Kept out of Dashboard.jsx so the page stays JSX-only and these can be
// unit-tested without rendering the page. Input is the GetJamStatus
// response (or null when the daemon has no jamming detection).

import { JamState, RadioBand } from '../gen/openmanet/jamdetect/v1/jamdetect_pb.js';

const BAND_LABEL = {
  [RadioBand.RADIO_BAND_S1G]: 'HALOW',
  [RadioBand.RADIO_BAND_2G4]: '2.4 GHZ',
  [RadioBand.RADIO_BAND_5G]: '5 GHZ',
  [RadioBand.RADIO_BAND_6G]: '6 GHZ',
};

const STATE_VIEW = {
  [JamState.WARMING_UP]: { label: 'LEARNING', level: 'muted' },
  [JamState.CLEAR]: { label: 'CLEAR', level: 'ok' },
  [JamState.SUSPECTED]: { label: 'SUSPECTED', level: 'warn' },
  [JamState.JAMMED]: { label: 'JAMMED', level: 'crit' },
};

export function bandLabel(band) {
  return BAND_LABEL[band] ?? 'RADIO';
}

export function stateView(state) {
  return STATE_VIEW[state] ?? { label: 'UNKNOWN', level: 'muted' };
}

// formatFreq shows S1G with one decimal and Wi-Fi as whole MHz. A HaLow
// driver may report a mapped 5 GHz value; it is shown as reported.
export function formatFreq(mhz) {
  const v = Number(mhz ?? 0);
  if (!v) return '—';
  return v < 1000 ? `${v.toFixed(1)} MHz` : `${Math.round(v)} MHz`;
}

function formatNoise(r) {
  if (!r?.noiseKnown) return '—';
  return `${r.noiseDbm} dBm`;
}

function formatBaseNoise(r) {
  if (!r?.noiseKnown || !r.baselineNoiseDbm) return '—';
  return `${Math.round(r.baselineNoiseDbm)} dBm`;
}

function formatPct(v) {
  return `${Math.round(Number(v ?? 0))}%`;
}

// jamRadioRows flattens this node's radios into table rows.
export function jamRadioRows(status) {
  const radios = status?.local?.radios ?? [];
  return radios.map((r) => {
    const sv = stateView(r.state);
    return {
      key: r.radio,
      radio: r.radio,
      band: `${bandLabel(r.band)} · ${formatFreq(r.freqMhz)}`,
      state: sv.label,
      level: sv.level,
      noise: formatNoise(r),
      baseNoise: formatBaseNoise(r),
      busy: `${formatPct(r.unexplainedBusyPct)} / ${formatPct(r.baselineUnexplainedBusyPct)}`,
    };
  });
}

// jamSummary returns the panel's headline: how many of this node's radios
// and how many other nodes are jammed or suspected, with display labels
// and Lattice status levels.
export function jamSummary(status) {
  const radios = status?.local?.radios ?? [];
  const remote = status?.remote ?? [];
  const jammedNode = (rep) => (rep.activeEvents ?? []).some((e) => e.state === JamState.JAMMED);
  const localJammed = radios.filter((r) => r.state === JamState.JAMMED).length;
  const localSuspected = radios.filter((r) => r.state === JamState.SUSPECTED).length;
  const remoteJammed = remote.filter(jammedNode).length;

  let localLabel = 'CLEAR';
  let localLevel = 'ok';
  if (localJammed > 0) {
    localLabel = `${localJammed} JAMMED`;
    localLevel = 'crit';
  } else if (localSuspected > 0) {
    localLabel = `${localSuspected} SUSPECTED`;
    localLevel = 'warn';
  } else if (radios.length === 0) {
    localLabel = 'NO DATA';
    localLevel = 'muted';
  }

  return {
    enabled: Boolean(status?.enabled),
    localJammed,
    localSuspected,
    remoteJammed,
    remoteNodes: remote.length,
    localLabel,
    localLevel,
    remoteLabel: remoteJammed > 0 ? `${remoteJammed} OF ${remote.length} JAMMED` : `${remote.length} REPORTING`,
    remoteLevel: remoteJammed > 0 ? 'crit' : 'muted',
  };
}

// jamAlerts returns Dashboard alerts for open episodes: crit for JAMMED,
// warn for SUSPECTED, this node first, then other nodes by hostname. The
// text is stable for the episode so an ACK survives polling.
export function jamAlerts(status) {
  if (!status?.enabled) return [];
  const out = [];
  const add = (ev, host) => {
    const level = ev.state === JamState.JAMMED ? 'crit' : ev.state === JamState.SUSPECTED ? 'warn' : null;
    if (!level) return;
    const word = level === 'crit' ? 'JAMMING' : 'POSSIBLE JAMMING';
    const where = host ? ` · ${host.toUpperCase()}` : '';
    out.push({ level, text: `${word} · ${bandLabel(ev.radio?.band)} ${formatFreq(ev.radio?.freqMhz)}${where}` });
  };
  for (const ev of status.local?.activeEvents ?? []) add(ev, '');
  for (const rep of status.remote ?? []) {
    for (const ev of rep.activeEvents ?? []) add(ev, rep.hostname || rep.primaryMac);
  }
  out.sort((a, b) => (a.level === b.level ? 0 : a.level === 'crit' ? -1 : 1));
  return out;
}
