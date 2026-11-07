// =============================================================================
// weather.test.js — WeatherService display helpers
// =============================================================================

import { describe, it, expect } from 'vitest';
import { SensorState } from '../../gen/openmanet/weather/v1/weather_pb.js';
import {
  DASH,
  compass16,
  formatAge,
  formatDirection,
  formatHumidity,
  formatLux,
  formatMph,
  formatPressure,
  formatRainInches,
  formatRainMm,
  formatTempC,
  formatTempF,
  pressureTrend,
  stationView,
  stationViews,
  weatherSummary,
} from '../../utils/weather.js';

const READING = {
  temperatureC: 5,
  humidityPct: 41.6,
  pressureHpa: 851.24,
  pressureChange3hHpa: -3.4,
  windSpeedMs: 5.4,
  windGustMs: 8.9,
  windDirectionDeg: 315,
  rainTotalMm: 12.3,
  rainLastHourMm: 2.54,
  lightLux: 45210,
};

function station(overrides = {}, report = {}) {
  return {
    local: false,
    stale: false,
    ageSeconds: 12,
    report: {
      primaryMac: '02:00:00:00:00:02',
      hostname: 'MAST-NORTH',
      model: 'sen0657',
      state: SensorState.OK,
      hasPosition: true,
      latitude: 40.4147,
      longitude: -112.0103,
      reading: READING,
      ...report,
    },
    ...overrides,
  };
}

describe('TestWeatherFormatters', () => {
  it('formats every unit', () => {
    expect(formatTempF(5)).toBe('41°F');
    expect(formatTempF(-40)).toBe('-40°F');
    expect(formatTempC(5)).toBe('5.0°C');
    expect(formatMph(5.4)).toBe('12 mph');
    expect(formatMph(0)).toBe('0 mph');
    expect(formatHumidity(41.6)).toBe('42%');
    expect(formatPressure(851.24)).toBe('851.2 hPa');
    expect(formatRainInches(2.54)).toBe('0.10 in');
    expect(formatRainMm(12.34)).toBe('12.3 mm');
    expect(formatLux(45210)).toBe('45,210 lux');
  });

  it('returns a dash for missing values instead of a fake zero', () => {
    for (const fn of [formatTempF, formatTempC, formatMph, formatHumidity, formatPressure,
      formatRainInches, formatRainMm, formatLux, formatDirection, compass16]) {
      expect(fn(undefined)).toBe(DASH);
      expect(fn(null)).toBe(DASH);
      expect(fn(Number.NaN)).toBe(DASH);
    }
  });

  it('names 16 compass points', () => {
    expect(compass16(0)).toBe('N');
    expect(compass16(11)).toBe('N');
    expect(compass16(12)).toBe('NNE');
    expect(compass16(315)).toBe('NW');
    expect(compass16(359)).toBe('N');
    expect(compass16(-45)).toBe('NW');
    expect(formatDirection(315)).toBe('NW (315°)');
  });

  it('describes the 3 h pressure trend', () => {
    expect(pressureTrend(undefined)).toEqual({ label: DASH, className: '' });
    expect(pressureTrend(0.4)).toEqual({ label: 'STEADY', className: '' });
    expect(pressureTrend(-0.4)).toEqual({ label: 'STEADY', className: '' });
    expect(pressureTrend(1.25)).toEqual({ label: 'RISING 1.3 hPa / 3 h', className: '' });
    expect(pressureTrend(-1)).toEqual({ label: 'FALLING 1.0 hPa / 3 h', className: '' });
    expect(pressureTrend(-3.4)).toEqual({ label: 'FALLING 3.4 hPa / 3 h', className: 'warn' });
  });

  it('formats the reading age', () => {
    expect(formatAge(-1)).toBe('NEVER');
    expect(formatAge(undefined)).toBe('NEVER');
    expect(formatAge(0)).toBe('just now');
    expect(formatAge(42)).toBe('42s ago');
    expect(formatAge(600)).toBe('10m ago');
    expect(formatAge(7200)).toBe('2h ago');
    expect(formatAge(172800)).toBe('2d ago');
  });
});

describe('TestStationView', () => {
  it('flattens a fresh station', () => {
    const v = stationView(station());
    expect(v.key).toBe('02:00:00:00:00:02');
    expect(v.name).toBe('MAST-NORTH');
    expect(v.status).toBe('OK');
    expect(v.statusClass).toBe('ok');
    expect(v.tempF).toBe('41°F');
    expect(v.wind).toBe('12 mph');
    expect(v.gust).toBe('20 mph');
    expect(v.direction).toBe('NW (315°)');
    expect(v.rainHour).toBe('0.10 in');
    expect(v.position).toBe('40.41470, -112.01030');
    expect(v.age).toBe('12s ago');
    expect(v.error).toBe('');
  });

  it('marks a stale station', () => {
    const v = stationView(station({ stale: true, ageSeconds: 900 }));
    expect(v.status).toBe('STALE');
    expect(v.statusClass).toBe('warn');
    expect(v.age).toBe('15m ago');
  });

  it('marks a station that never read', () => {
    const v = stationView(station({ ageSeconds: -1, stale: true }, { state: SensorState.STARTING, reading: undefined }));
    expect(v.status).toBe('NO DATA');
    expect(v.neverRead).toBe(true);
    expect(v.tempF).toBe(DASH);
    expect(v.hasTemp).toBe(false);
  });

  it('shows a sensor failure with its cause', () => {
    const v = stationView(station({ stale: true }, { state: SensorState.ERROR, error: 'modbus: timeout' }));
    expect(v.status).toBe('FAIL');
    expect(v.statusClass).toBe('crit');
    expect(v.error).toBe('modbus: timeout');
    // The last good reading stays visible.
    expect(v.tempF).toBe('41°F');
  });

  it('reports no fix and falls back to the MAC for a name', () => {
    const v = stationView(station({}, { hostname: '', hasPosition: false }));
    expect(v.name).toBe('02:00:00:00:00:02');
    expect(v.position).toBe('NO FIX');
  });

  it('handles empty and null input', () => {
    expect(stationViews(null)).toEqual([]);
    expect(stationViews({})).toEqual([]);
    expect(stationViews({ stations: [] })).toEqual([]);
    const v = stationView({});
    expect(v.name).toBe('This node');
    expect(v.status).toBe('NO DATA');
  });
});

describe('TestWeatherSummary', () => {
  it('is null with no stations', () => {
    expect(weatherSummary(null)).toBeNull();
    expect(weatherSummary({ stations: [] })).toBeNull();
  });

  it('prefers this node, then the first fresh station', () => {
    const remoteStale = station({ stale: true }, { hostname: 'A-STALE', primaryMac: 'a' });
    const remoteFresh = station({}, { hostname: 'B-FRESH', primaryMac: 'b' });
    const local = station({ local: true }, { hostname: 'HERE', primaryMac: 'c' });

    expect(weatherSummary({ stations: [local, remoteStale, remoteFresh] }).station.name).toBe('HERE');

    const s = weatherSummary({ stations: [remoteStale, remoteFresh] });
    expect(s.station.name).toBe('B-FRESH');
    expect(s.count).toBe(2);
    expect(s.staleCount).toBe(1);

    expect(weatherSummary({ stations: [remoteStale] }).station.name).toBe('A-STALE');
  });
});
