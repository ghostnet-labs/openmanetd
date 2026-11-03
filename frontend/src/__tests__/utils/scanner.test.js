// =============================================================================
// scanner.test.js — scanner display helpers (GHO-53)
// =============================================================================

import { describe, it, expect } from 'vitest';
import {
  AircraftSource,
  ControlChannelState,
  PositionSource,
  ReceiverHardware,
  ReceiverRole,
  ReceiverState,
  SiteEncryption,
  SiteSelectionReason,
} from '../../gen/openmanet/scanner/v1/scanner_pb.js';
import {
  aircraftRows,
  controlStateInfo,
  encryptionInfo,
  formatHex,
  formatKm,
  formatMHz,
  grantRows,
  positionLabel,
  reasonLabel,
  receiverRows,
  receiverStateInfo,
  roleLabel,
  summarize,
} from '../../utils/scanner.js';

const NOW = 1762150000000;
const ts = (msAgo) => ({ seconds: BigInt(Math.floor((NOW - msAgo) / 1000)), nanos: 0 });

describe('TestScannerFormatting', () => {
  it('formats frequencies from bigint or number Hz', () => {
    expect(formatMHz(770006250n)).toBe('770.00625 MHz');
    expect(formatMHz(851012500)).toBe('851.0125 MHz');
    expect(formatMHz(155000000n)).toBe('155 MHz');
    expect(formatMHz(0n)).toBe('—');
    expect(formatMHz(null)).toBe('—');
    expect(formatMHz(undefined)).toBe('—');
  });

  it('formats distances and hex ids', () => {
    expect(formatKm(0.85)).toBe('0.8 km');
    expect(formatKm(12.6)).toBe('13 km');
    expect(formatKm(undefined)).toBe('—');
    expect(formatHex(0xbee00)).toBe('BEE00');
    expect(formatHex(0)).toBe('—');
  });

  it('maps enums to labels and status classes', () => {
    expect(roleLabel(ReceiverRole.ADSB_1090)).toBe('1090 ADS-B');
    expect(roleLabel(ReceiverRole.WIDE)).toBe('Wide (P25)');
    expect(roleLabel(99)).toBe('—');
    expect(receiverStateInfo(ReceiverState.RUNNING)).toEqual({ label: 'RUNNING', className: 'ok' });
    expect(receiverStateInfo(ReceiverState.MISSING)).toEqual({ label: 'MISSING', className: 'crit' });
    expect(controlStateInfo(ControlChannelState.LOCKED)).toEqual({ label: 'LOCKED', className: 'ok' });
    expect(controlStateInfo(ControlChannelState.ERROR)).toEqual({ label: 'FAIL', className: 'crit' });
    expect(positionLabel(PositionSource.GPSD)).toBe('GPS');
    expect(positionLabel(undefined)).toBe('NO FIX');
    expect(reasonLabel(SiteSelectionReason.HYSTERESIS)).toBe('Held (hysteresis)');
    expect(encryptionInfo(SiteEncryption.FULL)).toEqual({ label: 'FULL', className: 'crit' });
  });
});

describe('TestScannerRows', () => {
  it('handles null and empty inputs', () => {
    expect(receiverRows(null)).toEqual([]);
    expect(aircraftRows(undefined)).toEqual([]);
    expect(grantRows([])).toEqual([]);
  });

  it('builds receiver rows', () => {
    const rows = receiverRows([
      { role: ReceiverRole.ADSB_1090, serial: '1090', present: true, hardware: ReceiverHardware.RTL_SDR,
        owner: 'dump1090', state: ReceiverState.RUNNING, lastSampleTime: ts(5000), detail: '' },
      { role: ReceiverRole.WIDE, serial: '', present: false, hardware: 0, owner: 'openmanetd',
        state: ReceiverState.MISSING, detail: 'no serial configured' },
    ], NOW);

    expect(rows[0]).toMatchObject({ role: '1090 ADS-B', serial: '1090', hardware: 'RTL-SDR', state: 'RUNNING', stateClass: 'ok', lastSample: '5s ago' });
    expect(rows[1]).toMatchObject({ serial: '—', hardware: '—', state: 'MISSING', stateClass: 'crit', lastSample: '—', detail: 'no serial configured' });
    expect(rows[0].key).not.toBe(rows[1].key);
  });

  it('builds aircraft rows with optional fields', () => {
    const rows = aircraftRows([
      { icao: 'a1b2c3', callsign: 'UAL123', squawk: '7700', altitudeFt: 35000, groundSpeedKt: 450.2, trackDeg: 270.4,
        verticalRateFpm: 64, latitude: 40.76081, longitude: -111.891, source: AircraftSource.BOTH, rssiDbfs: -12.34,
        lastSeen: ts(0), stale: false },
      { icao: '~2a0b0c', callsign: '', squawk: '', onGround: true, altitudeFt: 4200, source: AircraftSource.UAT_978,
        lastSeen: ts(90000), stale: true },
    ], NOW);

    expect(rows[0]).toMatchObject({
      icao: 'A1B2C3', callsign: 'UAL123', squawk: '7700', emergencySquawk: true, altitude: '35,000 ft',
      speed: '450 kt', track: '270°', vrate: '+64', position: '40.7608, -111.8910', source: 'BOTH',
      rssi: '-12.3 dBFS', seen: 'just now', stale: false,
    });
    expect(rows[1]).toMatchObject({
      icao: '~2A0B0C', callsign: '—', squawk: '—', emergencySquawk: false, altitude: 'GND', speed: '—',
      vrate: '—', position: '—', source: '978', rssi: '—', seen: '1m ago', stale: true,
    });
  });

  it('builds grant rows with flags', () => {
    const rows = grantRows([
      { talkgroup: 101, sourceUnit: 1234567, frequencyHz: 770500000n, encrypted: false, emergency: true,
        grants: 3, startTime: ts(4000), lastTime: ts(2000) },
      { talkgroup: 202, sourceUnit: 0, frequencyHz: 0n, encrypted: true, grants: 1, startTime: ts(1000), lastTime: ts(1000) },
    ], NOW);

    expect(rows[0]).toMatchObject({ talkgroup: '101', source: '1234567', frequency: '770.5 MHz', emergency: true, encrypted: false, grants: 3, last: '2s ago' });
    expect(rows[1]).toMatchObject({ source: '—', frequency: '—', encrypted: true, emergency: false });
  });
});

describe('TestScannerSummarize', () => {
  it('reports disabled for missing or disabled status', () => {
    expect(summarize(null)).toEqual({ enabled: false });
    expect(summarize({})).toEqual({ enabled: false });
    expect(summarize({ enabled: false })).toEqual({ enabled: false });
  });

  it('summarizes an enabled status', () => {
    const s = summarize({
      enabled: true,
      position: { source: PositionSource.GPSD, latitude: 40.76081, longitude: -111.891 },
      selectedSite: {
        site: { name: 'Nelson Peak', system: 'UCA', county: 'Salt Lake', state: 'UT', distanceKm: 12.4,
          encryption: SiteEncryption.PARTIAL },
        reason: SiteSelectionReason.NEAREST,
        controlChannelHz: 770006250n,
        wideTuner: { centerHz: 770500000n, fits: false, uncoveredHz: [860000000n] },
      },
      controlChannel: { state: ControlChannelState.LOCKED, nac: 0x3a1, wacn: 0xbee00, systemId: 0x3a1 },
      decodeStale: true,
    });

    expect(s).toMatchObject({
      enabled: true, position: 'GPS', coords: '40.7608, -111.8910', control: 'LOCKED', controlClass: 'ok',
      nac: '3A1', wacn: 'BEE00', systemId: '3A1', decodeStale: true, aircraftStale: false, lastDecode: 'never',
    });
    expect(s.site).toMatchObject({
      name: 'Nelson Peak', distance: '12 km', reason: 'Nearest to fix', controlChannel: '770.00625 MHz',
      center: '770.5 MHz', fits: false, uncovered: 1, encryption: 'PARTIAL', encryptionClass: 'warn',
    });
  });

  it('summarizes an enabled status with nothing selected', () => {
    const s = summarize({ enabled: true, position: { source: PositionSource.NONE }, siteUnavailable: true, positionStale: true });
    expect(s.site).toBeNull();
    expect(s.coords).toBe('—');
    expect(s.siteUnavailable).toBe(true);
    expect(s.positionStale).toBe(true);
    expect(s.control).toBe('—');
  });
});
