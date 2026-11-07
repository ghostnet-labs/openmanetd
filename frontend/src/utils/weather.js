// =============================================================================
// weather.js — display helpers for WeatherService responses
// =============================================================================
//
// Pure functions: unit conversion, labels and the station view the Weather
// page and the Dashboard panel render. No React. Every reading field is
// optional on the wire (a sensor that has not reported it yet), so each
// formatter returns a dash for a missing value instead of a fake zero.
//
// Units follow what the field team reads: °F, mph and inches first, with
// °C beside the temperature. Pressure stays in hPa (what forecasts and the
// ATAK marker use).

import { SensorState } from '../gen/openmanet/weather/v1/weather_pb.js';

export const DASH = '—';

const MPH_PER_MS = 2.236936;
const MM_PER_INCH = 25.4;
// A 3 h pressure change smaller than this is reported as steady.
const STEADY_HPA = 0.5;

const COMPASS = ['N', 'NNE', 'NE', 'ENE', 'E', 'ESE', 'SE', 'SSE', 'S', 'SSW', 'SW', 'WSW', 'W', 'WNW', 'NW', 'NNW'];

const STATES = {
  [SensorState.STARTING]: { label: 'STARTING', className: 'warn' },
  [SensorState.OK]: { label: 'OK', className: 'ok' },
  [SensorState.ERROR]: { label: 'FAIL', className: 'crit' },
};

const has = (v) => typeof v === 'number' && Number.isFinite(v);

export function fahrenheit(c) {
  return (c * 9) / 5 + 32;
}

export function formatTempF(c) {
  return has(c) ? `${Math.round(fahrenheit(c))}°F` : DASH;
}

export function formatTempC(c) {
  return has(c) ? `${c.toFixed(1)}°C` : DASH;
}

export function formatMph(ms) {
  return has(ms) ? `${Math.round(ms * MPH_PER_MS)} mph` : DASH;
}

// compass16 names the 16-point compass direction for degrees from north.
export function compass16(deg) {
  if (!has(deg)) return DASH;
  const norm = ((deg % 360) + 360) % 360;
  return COMPASS[Math.round(norm / 22.5) % 16];
}

// formatDirection is where the wind blows from: "NW (315°)".
export function formatDirection(deg) {
  return has(deg) ? `${compass16(deg)} (${Math.round(deg)}°)` : DASH;
}

export function formatHumidity(pct) {
  return has(pct) ? `${Math.round(pct)}%` : DASH;
}

export function formatPressure(hpa) {
  return has(hpa) ? `${hpa.toFixed(1)} hPa` : DASH;
}

// pressureTrend describes the 3 h change with a status class: a fast fall
// is the classic storm sign, so it is a warning.
export function pressureTrend(change) {
  if (!has(change)) return { label: DASH, className: '' };
  if (Math.abs(change) < STEADY_HPA) return { label: 'STEADY', className: '' };
  const amount = `${Math.abs(change).toFixed(1)} hPa / 3 h`;
  if (change > 0) return { label: `RISING ${amount}`, className: '' };
  return { label: `FALLING ${amount}`, className: change <= -3 ? 'warn' : '' };
}

export function formatRainInches(mm) {
  return has(mm) ? `${(mm / MM_PER_INCH).toFixed(2)} in` : DASH;
}

export function formatRainMm(mm) {
  return has(mm) ? `${mm.toFixed(1)} mm` : DASH;
}

export function formatLux(lux) {
  return has(lux) ? `${Math.round(lux).toLocaleString('en-US')} lux` : DASH;
}

// formatAge renders the reading age in seconds; -1 means never read.
export function formatAge(seconds) {
  if (!has(seconds) || seconds < 0) return 'NEVER';
  if (seconds < 2) return 'just now';
  if (seconds < 60) return `${seconds}s ago`;
  const m = Math.floor(seconds / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

function formatPosition(report) {
  if (!report?.hasPosition) return 'NO FIX';
  return `${report.latitude.toFixed(5)}, ${report.longitude.toFixed(5)}`;
}

// stationView flattens one WeatherStation into display strings.
export function stationView(station) {
  const report = station?.report ?? {};
  const r = report.reading ?? {};
  const state = STATES[report.state] ?? { label: 'UNKNOWN', className: '' };
  const stale = Boolean(station?.stale);
  const ageSeconds = station?.ageSeconds ?? -1;
  const neverRead = ageSeconds < 0;

  let status = state;
  if (neverRead && report.state !== SensorState.ERROR) status = { label: 'NO DATA', className: 'warn' };
  else if (stale && report.state !== SensorState.ERROR) status = { label: 'STALE', className: 'warn' };

  return {
    key: report.primaryMac || report.hostname || 'local',
    name: report.hostname || report.primaryMac || 'This node',
    local: Boolean(station?.local),
    stale,
    neverRead,
    status: status.label,
    statusClass: status.className,
    error: report.state === SensorState.ERROR ? report.error || 'sensor read failed' : '',
    model: report.model || DASH,
    age: formatAge(ageSeconds),
    tempF: formatTempF(r.temperatureC),
    tempC: formatTempC(r.temperatureC),
    hasTemp: has(r.temperatureC),
    wind: formatMph(r.windSpeedMs),
    gust: formatMph(r.windGustMs),
    direction: formatDirection(r.windDirectionDeg),
    humidity: formatHumidity(r.humidityPct),
    pressure: formatPressure(r.pressureHpa),
    trend: pressureTrend(r.pressureChange3hHpa),
    rainHour: formatRainInches(r.rainLastHourMm),
    rainTotal: formatRainMm(r.rainTotalMm),
    light: formatLux(r.lightLux),
    position: formatPosition(report),
  };
}

// stationViews maps a ListWeatherStationsResponse to views, keeping the
// server order (this node's station first, then by hostname).
export function stationViews(resp) {
  const stations = resp?.stations ?? [];
  return stations.map(stationView);
}

// weatherSummary picks the station the Dashboard shows: this node's own
// when it has one, else the first fresh station heard, else the first one.
// Returns null when there is nothing to show.
export function weatherSummary(resp) {
  const views = stationViews(resp);
  if (views.length === 0) return null;
  const pick = views.find((v) => v.local)
    ?? views.find((v) => !v.stale && !v.neverRead)
    ?? views[0];
  return {
    station: pick,
    count: views.length,
    staleCount: views.filter((v) => v.stale || v.neverRead).length,
  };
}
