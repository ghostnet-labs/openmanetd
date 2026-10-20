// =============================================================================
// dashboardBattery.test.js — BATT row derivation for the Dashboard
// =============================================================================

import { describe, it, expect } from 'vitest';
import { batteryView } from '../../pages/dashboardBattery.js';
import { BatteryAbsentReason, BatteryLevel } from '../../gen/openmanet/hardware/v1/hardware_pb.js';

describe('TestBatteryView', () => {
  it('returns null for missing or absent battery', () => {
    expect(batteryView(null)).toBeNull();
    expect(batteryView(undefined)).toBeNull();
    expect(batteryView({})).toBeNull();
    expect(batteryView({ present: false, chargePercent: -1 })).toBeNull();
  });

  it('formats a healthy battery', () => {
    const v = batteryView({ present: true, voltageVolts: 11.4, powerWatts: 9.69, chargePercent: 60 });
    expect(v).toEqual({ pct: 60, level: '', detail: '60% · 11.40 V · 9.7 W' });
  });

  it('flags low and critical charge', () => {
    expect(batteryView({ present: true, voltageVolts: 10.8, powerWatts: 5, chargePercent: 25 }).level).toBe('warn');
    expect(batteryView({ present: true, voltageVolts: 9.9, powerWatts: 5, chargePercent: 10 }).level).toBe('crit');
    expect(batteryView({ present: true, voltageVolts: 9.0, powerWatts: 5, chargePercent: 0 }).level).toBe('crit');
  });

  it('omits the percentage when charge cannot be estimated', () => {
    const v = batteryView({ present: true, voltageVolts: 5.1, powerWatts: 2, chargePercent: -1 });
    expect(v).toEqual({ pct: 0, level: '', detail: '5.10 V · 2.0 W' });
  });

  it('clamps out-of-range charge and shows power as a magnitude', () => {
    const v = batteryView({ present: true, voltageVolts: 12.7, powerWatts: -3.25, chargePercent: 104 });
    expect(v.pct).toBe(100);
    expect(v.detail).toBe('100% · 12.70 V · 3.3 W');
  });

  it('treats missing numeric fields as zero', () => {
    const v = batteryView({ present: true });
    expect(v).toEqual({ pct: 0, level: '', detail: '0.00 V · 0.0 W' });
  });
});

describe('TestBatteryViewHardware', () => {
  const telemetry = (overrides = {}) => ({
    present: true,
    stale: false,
    absentReason: BatteryAbsentReason.NONE,
    busVoltageMv: 11400,
    currentMa: 850,
    powerMw: 9690,
    level: BatteryLevel.NORMAL,
    seriesCells: 3,
    ...overrides,
  });

  it('keeps the dashboard view when there is no hardware manager', () => {
    const dash = { present: true, voltageVolts: 11.4, powerWatts: 9.69, chargePercent: 60 };
    expect(batteryView(dash, null)).toEqual(batteryView(dash));
    expect(batteryView(null, null)).toBeNull();
  });

  it('renders manager telemetry with a NORMAL state and current', () => {
    const v = batteryView({ present: true, chargePercent: -1 }, telemetry());
    expect(v).toEqual({
      pct: 0,
      level: '',
      detail: '11.40 V · 9.7 W',
      state: { label: 'NORMAL', level: 'ok' },
      current: '0.85 A',
    });
  });

  it('uses manager values even when the dashboard battery is absent', () => {
    const v = batteryView({ present: false, chargePercent: -1 }, telemetry({ busVoltageMv: 12000 }));
    expect(v.detail).toBe('12.00 V · 9.7 W');
  });

  it('keeps a known charge estimate from the dashboard section', () => {
    const v = batteryView({ present: true, chargePercent: 60 }, telemetry());
    expect(v.pct).toBe(60);
    expect(v.detail).toBe('60% · 11.40 V · 9.7 W');
  });

  it('maps low-battery levels to Lattice status classes with labels', () => {
    expect(batteryView(null, telemetry({ level: BatteryLevel.WARNING }))).toMatchObject({
      level: 'warn', state: { label: 'LOW', level: 'warn' },
    });
    expect(batteryView(null, telemetry({ level: BatteryLevel.CRITICAL }))).toMatchObject({
      level: 'crit', state: { label: 'CRITICAL', level: 'crit' },
    });
    expect(batteryView(null, telemetry({ level: BatteryLevel.SHUTDOWN }))).toMatchObject({
      level: 'crit', state: { label: 'SHUTDOWN', level: 'crit' },
    });
    expect(batteryView(null, telemetry({ level: BatteryLevel.UNKNOWN }))).toMatchObject({
      level: '', state: { label: 'UNKNOWN', level: 'muted' },
    });
    expect(batteryView(null, telemetry({ level: undefined }))).toMatchObject({
      state: { label: 'UNKNOWN', level: 'muted' },
    });
  });

  it('lets a low charge estimate drive the bar while the policy is normal', () => {
    expect(batteryView({ present: true, chargePercent: 8 }, telemetry()).level).toBe('crit');
  });

  it('flags a stale reading and never shows it healthier than warn', () => {
    expect(batteryView(null, telemetry({ stale: true })).state).toEqual({ label: 'STALE · NORMAL', level: 'warn' });
    expect(batteryView(null, telemetry({ stale: true, level: BatteryLevel.CRITICAL })).state)
      .toEqual({ label: 'STALE · CRITICAL', level: 'crit' });
  });

  it('shows an unreachable monitor as a critical row without values', () => {
    const v = batteryView(
      { present: false, chargePercent: -1 },
      telemetry({ present: false, absentReason: BatteryAbsentReason.MONITOR_UNREACHABLE }),
    );
    expect(v).toEqual({
      pct: 0, level: 'crit', detail: '—', state: { label: 'UNREACHABLE', level: 'crit' }, current: null,
    });
  });

  it('falls back to the dashboard section without a manager reading', () => {
    const dash = { present: true, voltageVolts: 7.6, powerWatts: 1.5, chargePercent: 55 };
    const noMonitor = telemetry({ present: false, absentReason: BatteryAbsentReason.NO_MONITOR });
    expect(batteryView(dash, noMonitor)).toEqual(batteryView(dash));
    expect(batteryView({ present: false }, noMonitor)).toBeNull();
    const warmingUp = telemetry({ present: false, absentReason: BatteryAbsentReason.NONE });
    expect(batteryView(dash, warmingUp)).toEqual(batteryView(dash));
  });

  it('treats missing numeric fields as zero', () => {
    const v = batteryView(null, { present: true });
    expect(v.detail).toBe('0.00 V · 0.0 W');
    expect(v.current).toBe('0.00 A');
  });
});
