// =============================================================================
// dashboardBattery.js — pure helper for the Dashboard battery row
// =============================================================================

const BATTERY_CRIT_PCT = 10;
const BATTERY_WARN_PCT = 25;

/**
 * Converts the dashboard BatteryStatus message into what the BATT bar row
 * renders. Returns null when the node has no battery monitor.
 *
 * Low charge is the bad direction for a battery, so the level is inverted
 * relative to the CPU/MEM bars.
 */
export function batteryView(battery) {
  if (!battery?.present) return null;

  const volts = battery.voltageVolts ?? 0;
  const watts = battery.powerWatts ?? 0;
  const charge = battery.chargePercent ?? -1;
  const known = charge >= 0;
  const pct = known ? Math.max(0, Math.min(100, charge)) : 0;

  let level = '';
  if (known && pct <= BATTERY_CRIT_PCT) level = 'crit';
  else if (known && pct <= BATTERY_WARN_PCT) level = 'warn';

  const parts = [];
  if (known) parts.push(`${pct.toFixed(0)}%`);
  parts.push(`${volts.toFixed(2)} V`);
  parts.push(`${Math.abs(watts).toFixed(1)} W`);

  return { pct, level, detail: parts.join(' · ') };
}
