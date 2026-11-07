// =============================================================================
// dashboardJam.test.js — Dashboard jamming panel and alert helpers
// =============================================================================

import { describe, it, expect } from 'vitest';
import { JamState, RadioBand } from '../../gen/openmanet/jamdetect/v1/jamdetect_pb.js';
import { formatFreq, jamAlerts, jamRadioRows, jamSummary, stateView, bandLabel } from '../../pages/dashboardJam.js';
import { classifyAlerts } from '../../pages/dashboardAlerts.js';

function radio(overrides = {}) {
  return {
    radio: 'phy1-mesh0',
    band: RadioBand.RADIO_BAND_5G,
    freqMhz: 5180,
    state: JamState.CLEAR,
    noiseKnown: true,
    noiseDbm: -92,
    baselineNoiseDbm: -91.6,
    unexplainedBusyPct: 2.4,
    baselineUnexplainedBusyPct: 3,
    ...overrides,
  };
}

function event(state, overrides = {}) {
  return { id: 'x-1', state, radio: radio({ state, ...overrides }) };
}

const JAMMED_STATUS = {
  enabled: true,
  local: {
    radios: [
      radio({ state: JamState.JAMMED, noiseDbm: -78 }),
      radio({ radio: 'wlh0', band: RadioBand.RADIO_BAND_S1G, freqMhz: 915.5, state: JamState.WARMING_UP, noiseKnown: false }),
    ],
    activeEvents: [event(JamState.JAMMED)],
  },
  remote: [
    { hostname: 'mast-2', primaryMac: '02:00:00:00:00:02', activeEvents: [event(JamState.SUSPECTED, { band: RadioBand.RADIO_BAND_S1G, freqMhz: 915.5 })] },
    { hostname: 'node-7', primaryMac: '02:00:00:00:00:07', activeEvents: [] },
  ],
};

describe('TestJamHelpers', () => {
  it('formats frequencies and labels', () => {
    expect(formatFreq(915.5)).toBe('915.5 MHz');
    expect(formatFreq(5180)).toBe('5180 MHz');
    expect(formatFreq(0)).toBe('—');
    expect(formatFreq(undefined)).toBe('—');
    expect(bandLabel(RadioBand.RADIO_BAND_S1G)).toBe('HALOW');
    expect(bandLabel(99)).toBe('RADIO');
    expect(stateView(JamState.JAMMED)).toEqual({ label: 'JAMMED', level: 'crit' });
    expect(stateView(undefined).label).toBe('UNKNOWN');
  });
});

describe('TestJamRadioRows', () => {
  it('handles null and empty', () => {
    expect(jamRadioRows(null)).toEqual([]);
    expect(jamRadioRows({})).toEqual([]);
    expect(jamRadioRows({ local: { radios: [] } })).toEqual([]);
  });

  it('formats each radio', () => {
    const rows = jamRadioRows(JAMMED_STATUS);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toMatchObject({
      key: 'phy1-mesh0',
      band: '5 GHZ · 5180 MHz',
      state: 'JAMMED',
      level: 'crit',
      noise: '-78 dBm',
      baseNoise: '-92 dBm',
      busy: '2% / 3%',
    });
    expect(rows[1]).toMatchObject({ band: 'HALOW · 915.5 MHz', state: 'LEARNING', noise: '—', baseNoise: '—' });
  });
});

describe('TestJamSummary', () => {
  it('reports nothing for a null status', () => {
    expect(jamSummary(null)).toMatchObject({ enabled: false, localLabel: 'NO DATA', remoteLabel: '0 REPORTING' });
  });

  it('counts local and remote jamming', () => {
    expect(jamSummary(JAMMED_STATUS)).toMatchObject({
      enabled: true,
      localJammed: 1,
      localLabel: '1 JAMMED',
      localLevel: 'crit',
      remoteJammed: 0,
      remoteNodes: 2,
      remoteLabel: '2 REPORTING',
    });
  });

  it('shows suspected and clear', () => {
    const s = { enabled: true, local: { radios: [radio({ state: JamState.SUSPECTED })] } };
    expect(jamSummary(s)).toMatchObject({ localLabel: '1 SUSPECTED', localLevel: 'warn' });
    const c = { enabled: true, local: { radios: [radio()] } };
    expect(jamSummary(c)).toMatchObject({ localLabel: 'CLEAR', localLevel: 'ok' });
  });

  it('flags remote jammed nodes', () => {
    const s = { enabled: true, remote: [{ hostname: 'a', activeEvents: [event(JamState.JAMMED)] }, { hostname: 'b' }] };
    expect(jamSummary(s)).toMatchObject({ remoteJammed: 1, remoteLabel: '1 OF 2 JAMMED', remoteLevel: 'crit' });
  });
});

describe('TestJamAlerts', () => {
  it('returns nothing when disabled or empty', () => {
    expect(jamAlerts(null)).toEqual([]);
    expect(jamAlerts({ enabled: false, local: { activeEvents: [event(JamState.JAMMED)] } })).toEqual([]);
    expect(jamAlerts({ enabled: true })).toEqual([]);
  });

  it('puts crit before warn and names remote nodes', () => {
    expect(jamAlerts(JAMMED_STATUS)).toEqual([
      { level: 'crit', text: 'JAMMING · 5 GHZ 5180 MHz' },
      { level: 'warn', text: 'POSSIBLE JAMMING · HALOW 915.5 MHz · MAST-2' },
    ]);
  });

  it('falls back to the MAC when a node has no hostname', () => {
    const s = { enabled: true, remote: [{ primaryMac: '02:00:00:00:00:09', activeEvents: [event(JamState.JAMMED)] }] };
    expect(jamAlerts(s)[0].text).toBe('JAMMING · 5 GHZ 5180 MHz · 02:00:00:00:00:09');
  });

  it('leads the dashboard alert list', () => {
    const alerts = classifyAlerts({ mesh: { status: { connected: true } }, jam: jamAlerts(JAMMED_STATUS) });
    expect(alerts[0]).toEqual({ level: 'crit', text: 'JAMMING · 5 GHZ 5180 MHz' });
    expect(alerts[2]).toEqual({ level: 'ok', text: 'MESH UP · CONVERGED' });
  });
});
