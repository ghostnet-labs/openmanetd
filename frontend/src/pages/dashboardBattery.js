// =============================================================================
// dashboardBattery.js — pure helper for the Dashboard battery row
// =============================================================================

import { BatteryAbsentReason, BatteryLevel } from '../gen/openmanet/hardware/v1/hardware_pb.js';

const BATTERY_CRIT_PCT = 10;
const BATTERY_WARN_PCT = 25;

// Low-battery policy level → status label and Lattice status class. The
// label always accompanies the color so the state never relies on color.
const POLICY_STATE = {
  [BatteryLevel.NORMAL]: { label: 'NORMAL', level: 'ok' },
  [BatteryLevel.WARNING]: { label: 'LOW', level: 'warn' },
  [BatteryLevel.CRITICAL]: { label: 'CRITICAL', level: 'crit' },
  [BatteryLevel.SHUTDOWN]: { label: 'SHUTDOWN', level: 'crit' },
};
const POLICY_UNKNOWN = { label: 'UNKNOWN', level: 'muted' };

function chargeOf(battery) {
  const charge = battery?.present ? (battery.chargePercent ?? -1) : -1;
  const known = charge >= 0;
  const pct = known ? Math.max(0, Math.min(100, charge)) : 0;
  return { known, pct };
}

function chargeLevel(known, pct) {
  if (known && pct <= BATTERY_CRIT_PCT) return 'crit';
  if (known && pct <= BATTERY_WARN_PCT) return 'warn';
  return '';
}

function formatDetail(known, pct, volts, watts) {
  const parts = [];
  if (known) parts.push(`${pct.toFixed(0)}%`);
  parts.push(`${volts.toFixed(2)} V`);
  parts.push(`${Math.abs(watts).toFixed(1)} W`);
  return parts.join(' · ');
}

// monitorView renders the dashboard's own BatteryStatus.
function monitorView(battery) {
  if (!battery?.present) return null;

  const { known, pct } = chargeOf(battery);
  const detail = formatDetail(known, pct, battery.voltageVolts ?? 0, battery.powerWatts ?? 0);

  return { pct, level: chargeLevel(known, pct), detail };
}

// hardwareView renders the hardware manager's BatteryTelemetry. The charge
// estimate, when there is one, still comes from the dashboard section.
function hardwareView(battery, hw) {
  const { known, pct } = chargeOf(battery);
  const volts = (hw.busVoltageMv ?? 0) / 1000;
  const watts = (hw.powerMw ?? 0) / 1000;
  const amps = (hw.currentMa ?? 0) / 1000;
  const policy = POLICY_STATE[hw.level] ?? POLICY_UNKNOWN;

  const policyAlarm = policy.level === 'warn' || policy.level === 'crit';
  const level = policyAlarm ? policy.level : chargeLevel(known, pct);

  // Stale values are the last good reading: flag them, and never let a
  // stale reading look healthier than "degraded".
  const state = hw.stale
    ? { label: `STALE · ${policy.label}`, level: policy.level === 'crit' ? 'crit' : 'warn' }
    : policy;

  return {
    pct,
    level,
    detail: formatDetail(known, pct, volts, watts),
    state,
    current: `${amps.toFixed(2)} A`,
  };
}

/**
 * Converts the battery data into what the BATT bar row and the battery
 * status rows render. Returns null when the node has no battery monitor.
 *
 * `battery` is the dashboard BatteryStatus message. `hw` is the hardware
 * manager's BatteryTelemetry (null when the manager is not running); when it
 * holds a reading it supplies the values plus the low-battery level and the
 * stale flag (`state`, `current`). A monitor the manager can no longer reach
 * shows as a critical UNREACHABLE row. Otherwise the dashboard section is
 * used on its own.
 *
 * Low charge is the bad direction for a battery, so the level is inverted
 * relative to the CPU/MEM bars.
 */
export function batteryView(battery, hw = null) {
  if (hw?.present) return hardwareView(battery, hw);

  if (hw?.absentReason === BatteryAbsentReason.MONITOR_UNREACHABLE) {
    return {
      pct: 0,
      level: 'crit',
      detail: '—',
      state: { label: 'UNREACHABLE', level: 'crit' },
      current: null,
    };
  }

  return monitorView(battery);
}
