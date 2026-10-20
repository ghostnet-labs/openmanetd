// =============================================================================
// chanmig.test.js — channel migration display helpers (GHO-52)
// =============================================================================

import { describe, it, expect } from 'vitest';
import { BandMode, MigrationPhase } from '../../gen/openmanet/chanmig/v1/chanmig_pb.js';
import { Band } from '../../gen/openmanet/radio/v1/radio_pb.js';
import {
  assignmentLabel,
  bandLabel,
  bandRows,
  phaseInfo,
  planVersionLabel,
  summarize,
} from '../../utils/chanmig.js';

describe('TestChanmigPhaseInfo', () => {
  it('maps every phase to a label and status class', () => {
    expect(phaseInfo(MigrationPhase.BOOTING)).toEqual({ label: 'BOOTING', className: '' });
    expect(phaseInfo(MigrationPhase.STABLE)).toEqual({ label: 'STABLE', className: 'ok' });
    expect(phaseInfo(MigrationPhase.PENDING)).toEqual({ label: 'SWITCH PENDING', className: 'accent' });
    expect(phaseInfo(MigrationPhase.MIGRATING)).toEqual({ label: 'MIGRATING', className: 'accent' });
    expect(phaseInfo(MigrationPhase.SEARCHING)).toEqual({ label: 'SEARCHING', className: 'warn' });
    expect(phaseInfo(MigrationPhase.UNSPECIFIED).label).toBe('—');
    expect(phaseInfo(99).label).toBe('—');
  });
});

describe('TestChanmigLabels', () => {
  it('labels bands, assignments and plan versions', () => {
    expect(bandLabel(Band.BAND_HALOW)).toBe('HaLow');
    expect(bandLabel(Band.BAND_2_4_GHZ)).toBe('2.4 GHz');
    expect(bandLabel(Band.BAND_5_GHZ)).toBe('5 GHz');
    expect(bandLabel(Band.BAND_6_GHZ)).toBe('—');

    expect(assignmentLabel({ band: Band.BAND_HALOW, channel: 28, widthMhz: 2 })).toBe('ch 28 · 2 MHz');
    expect(assignmentLabel(null)).toBe('—');
    expect(assignmentLabel(undefined)).toBe('—');
    expect(assignmentLabel({ channel: 0, widthMhz: 0 })).toBe('—');

    expect(planVersionLabel(null)).toBe('Initial');
    expect(planVersionLabel(undefined)).toBe('Initial');
    expect(planVersionLabel({ version: 12n })).toBe('v12');
  });
});

describe('TestChanmigBandRows', () => {
  it('handles missing and empty input', () => {
    expect(bandRows(null)).toEqual([]);
    expect(bandRows(undefined)).toEqual([]);
    expect(bandRows([])).toEqual([]);
  });

  it('maps bands with modes, targets and failures', () => {
    const a = (band, channel, widthMhz) => ({ band, channel, widthMhz });
    const rows = bandRows([
      { band: Band.BAND_HALOW, operating: a(1, 28, 2), target: a(1, 28, 2), mode: BandMode.SETTLED },
      { band: Band.BAND_2_4_GHZ, operating: a(2, 1, 20), target: a(2, 6, 20), mode: BandMode.VERIFYING },
      { band: Band.BAND_5_GHZ, operating: a(3, 36, 80), target: a(3, 36, 40), mode: BandMode.FALLBACK },
      { band: Band.BAND_HALOW, operating: a(1, 30, 2), mode: BandMode.SEARCHING },
      { band: Band.BAND_5_GHZ, operating: a(3, 44, 80), target: a(3, 44, 80), mode: BandMode.SETTLED, radioFailed: true },
      { band: Band.BAND_5_GHZ, mode: BandMode.UNSPECIFIED },
    ]);

    expect(rows[0]).toEqual({
      key: String(Band.BAND_HALOW), band: 'HaLow', channel: 'ch 28 · 2 MHz', target: '—', mode: 'SETTLED', modeClass: 'badge-ok',
    });
    expect(rows[1]).toMatchObject({ target: 'ch 6 · 20 MHz', mode: 'VERIFYING', modeClass: '' });
    expect(rows[2]).toMatchObject({ target: 'ch 36 · 40 MHz', mode: 'FALLBACK', modeClass: 'badge-warn' });
    expect(rows[3]).toMatchObject({ target: '—', mode: 'SEARCHING', modeClass: 'badge-warn' });
    expect(rows[4]).toMatchObject({ mode: 'FAIL', modeClass: 'badge-crit' });
    expect(rows[5]).toMatchObject({ channel: '—', target: '—', mode: '—' });
  });
});

describe('TestChanmigSummarize', () => {
  it('reports disabled for null, empty and disabled status', () => {
    expect(summarize(null)).toEqual({ enabled: false });
    expect(summarize({})).toEqual({ enabled: false });
    expect(summarize({ enabled: false, phase: MigrationPhase.STABLE })).toEqual({ enabled: false });
  });

  it('summarizes an enabled status', () => {
    expect(summarize({
      enabled: true,
      phase: MigrationPhase.SEARCHING,
      currentPlan: { version: 3n },
      peers: [{ nodeId: 1n }, { nodeId: 2n }],
      coordinator: false,
    })).toEqual({
      enabled: true, phase: 'SEARCHING', phaseClass: 'warn', planVersion: 'v3', peers: 2, role: 'Member',
    });
    expect(summarize({ enabled: true, coordinator: true })).toMatchObject({ peers: 0, planVersion: 'Initial', role: 'Coordinator' });
  });
});
