// =============================================================================
// dashboardBattery.test.js — BATT row derivation for the Dashboard
// =============================================================================

import { describe, it, expect } from 'vitest';
import { batteryView } from '../../pages/dashboardBattery.js';

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
