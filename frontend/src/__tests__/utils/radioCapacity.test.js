import { describe, it, expect } from 'vitest';
import {
  AdmissionOutcome,
  AdmissionReason,
  Band,
  CapacityGateMode,
} from '../../gen/openmanet/radio/v1/radio_pb.js';
import {
  bandLabel,
  bandsLabel,
  decisionEffect,
  decisionRows,
  formatAge,
  gateModeEffect,
  gateModeLabel,
  outcome,
  phyRows,
  reasonLabel,
  tsToDate,
} from '../../utils/radioCapacity.js';

const NOW = new Date('2026-10-20T12:00:00Z');

describe('TestRadioCapacityLabels', () => {
  it('labels gate modes', () => {
    expect(gateModeLabel(CapacityGateMode.OFF)).toBe('Off');
    expect(gateModeLabel(CapacityGateMode.ADVISORY)).toBe('Advisory');
    expect(gateModeLabel(CapacityGateMode.ENFORCE)).toBe('Enforce');
    expect(gateModeLabel(CapacityGateMode.UNSPECIFIED)).toBe('Unknown');
    expect(gateModeEffect(CapacityGateMode.ENFORCE)).toBe('Defer / reject blocks the change');
    expect(gateModeEffect(99)).toBe('—');
  });

  it('labels bands', () => {
    expect(bandLabel(Band.BAND_HALOW)).toBe('HaLow');
    expect(bandLabel(Band.BAND_60_GHZ)).toBe('60 GHz');
    expect(bandLabel(Band.BAND_UNSPECIFIED)).toBe('?');
    expect(bandsLabel([Band.BAND_2_4_GHZ, Band.BAND_5_GHZ])).toBe('2.4 GHz / 5 GHz');
    expect(bandsLabel([])).toBe('—');
    expect(bandsLabel(undefined)).toBe('—');
  });

  it('pairs every outcome color with a text label', () => {
    expect(outcome(AdmissionOutcome.ADMIT)).toEqual({ label: 'ADMIT', className: 'badge-ok' });
    expect(outcome(AdmissionOutcome.DEFER)).toEqual({ label: 'DEFER', className: 'badge-warn' });
    expect(outcome(AdmissionOutcome.REJECT)).toEqual({ label: 'REJECT', className: 'badge-crit' });
    expect(outcome(AdmissionOutcome.UNSPECIFIED)).toEqual({ label: 'UNKNOWN', className: 'mono' });
  });

  it('labels every admission reason', () => {
    for (const [name, value] of Object.entries(AdmissionReason)) {
      if (typeof value !== 'number' || name === 'UNSPECIFIED') continue;
      expect(reasonLabel(value)).not.toBe('Unknown');
    }
    expect(reasonLabel(AdmissionReason.UNSPECIFIED)).toBe('Unknown');
  });
});

describe('TestRadioCapacityTime', () => {
  it('converts timestamps', () => {
    expect(tsToDate(null)).toBeNull();
    expect(tsToDate(undefined)).toBeNull();
    expect(tsToDate({ seconds: 'x' })).toBeNull();
    expect(tsToDate({ seconds: 1_792_497_600n, nanos: 500_000_000 }).toISOString()).toBe('2026-10-20T12:00:00.500Z');
    expect(tsToDate({ seconds: 0n }).getTime()).toBe(0);
  });

  it('formats ages', () => {
    const ago = (s) => new Date(NOW.getTime() - s * 1000);
    expect(formatAge(null, NOW)).toBe('—');
    expect(formatAge(ago(0), NOW)).toBe('just now');
    expect(formatAge(ago(-30), NOW)).toBe('just now');
    expect(formatAge(ago(59), NOW)).toBe('59s ago');
    expect(formatAge(ago(60), NOW)).toBe('1m ago');
    expect(formatAge(ago(3599), NOW)).toBe('59m ago');
    expect(formatAge(ago(3600), NOW)).toBe('1h ago');
    expect(formatAge(ago(2 * 86400), NOW)).toBe('2d ago');
  });
});

describe('TestRadioCapacityRows', () => {
  it('handles missing capacity', () => {
    expect(phyRows(null)).toEqual([]);
    expect(phyRows({})).toEqual([]);
    expect(phyRows({ phys: [] })).toEqual([]);
  });

  it('shapes PHY rows', () => {
    const rows = phyRows({
      phys: [
        { name: 'phy2', bands: [Band.BAND_HALOW], isHalow: true, qualified: true, available: true },
        { name: '', bands: [], isHalow: false, qualified: false, available: false },
      ],
    });
    expect(rows).toEqual([
      {
        name: 'phy2', bands: 'HaLow', halow: 'YES', qualified: 'YES', qualifiedClass: 'badge-ok',
        available: 'UP', availableClass: 'badge-ok',
      },
      {
        name: '—', bands: '—', halow: 'NO', qualified: 'NO', qualifiedClass: 'badge-warn',
        available: 'DOWN', availableClass: 'badge-crit',
      },
    ]);
  });

  it('describes the effect of a decision', () => {
    const d = (outcomeValue, gateMode) => ({ outcome: outcomeValue, gateMode });
    expect(decisionEffect(d(AdmissionOutcome.ADMIT, CapacityGateMode.ENFORCE))).toBe('Applied');
    expect(decisionEffect(d(AdmissionOutcome.REJECT, CapacityGateMode.ENFORCE))).toBe('Blocked');
    expect(decisionEffect(d(AdmissionOutcome.DEFER, CapacityGateMode.ENFORCE))).toBe('Blocked');
    expect(decisionEffect(d(AdmissionOutcome.REJECT, CapacityGateMode.ADVISORY))).toBe('Not blocked (advisory)');
    expect(decisionEffect(d(AdmissionOutcome.UNSPECIFIED, CapacityGateMode.ENFORCE))).toBe('—');
  });

  it('shapes decision rows', () => {
    expect(decisionRows(null, NOW)).toEqual([]);
    expect(decisionRows([], NOW)).toEqual([]);

    const rows = decisionRows([
      {
        decidedAt: { seconds: BigInt(NOW.getTime() / 1000 - 90), nanos: 0 },
        gateMode: CapacityGateMode.ENFORCE,
        outcome: AdmissionOutcome.DEFER,
        reason: AdmissionReason.PHY_UNAVAILABLE,
        explanation: 'defer: phy1 unavailable',
      },
      { outcome: AdmissionOutcome.ADMIT, reason: AdmissionReason.FITS, explanation: '' },
    ], NOW);

    expect(rows[0]).toEqual({
      key: `${NOW.getTime() - 90_000}-0`,
      time: '1m ago',
      timeTitle: '2026-10-20T11:58:30.000Z',
      outcome: 'DEFER',
      outcomeClass: 'badge-warn',
      reason: 'PHY unavailable',
      effect: 'Blocked',
      explanation: 'defer: phy1 unavailable',
    });
    expect(rows[1]).toMatchObject({ key: 'na-1', time: '—', timeTitle: '', explanation: '—', effect: 'Applied' });
  });
});
