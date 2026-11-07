// =============================================================================
// Weather.test.jsx — Weather page states
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { WeatherService } from '../../gen/openmanet/weather/v1/weather_service_pb.js';
import { SensorState } from '../../gen/openmanet/weather/v1/weather_pb.js';

vi.mock('../../services/connectClient.js', () => ({ transport: {} }));

const READING = {
  temperatureC: 5,
  humidityPct: 40,
  pressureHpa: 851.2,
  pressureChange3hHpa: -3.4,
  windSpeedMs: 5.4,
  windGustMs: 8.9,
  windDirectionDeg: 315,
  rainTotalMm: 12.3,
  rainLastHourMm: 0,
  lightLux: 1000,
};

const LOCAL = () => ({
  local: true,
  stale: false,
  ageSeconds: 3,
  report: { primaryMac: 'aa', hostname: 'MAST-1', model: 'sim', state: SensorState.OK, hasPosition: true,
    latitude: 40.4, longitude: -112.0, reading: READING },
});

const REMOTE_STALE = () => ({
  local: false,
  stale: true,
  ageSeconds: 900,
  report: { primaryMac: 'bb', hostname: 'MAST-2', model: 'sen0657', state: SensorState.OK, reading: READING },
});

const REMOTE_FAIL = () => ({
  local: false,
  stale: false,
  ageSeconds: 20,
  report: { primaryMac: 'cc', hostname: 'MAST-3', model: 'sen0657', state: SensorState.ERROR,
    error: 'modbus: timeout', reading: READING },
});

let handler;

async function renderWeather(fn) {
  handler = fn;
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(WeatherService, { listWeatherStations: () => handler() });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const mod = await import('../../pages/Weather.jsx');
  return { ...render(<mod.default />), mod };
}

beforeEach(() => vi.resetModules());
afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('TestWeatherPage', () => {
  it('shows a loading state first', async () => {
    let release;
    await renderWeather(() => new Promise((r) => { release = r; }));
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => expect(release).toBeDefined());
    release({ sensorEnabled: true, stations: [LOCAL()] });
    await waitFor(() => screen.getByText('MAST-1'));
  });

  it('renders one panel per station with fresh, stale and failed states', async () => {
    const { container } = await renderWeather(() => ({ sensorEnabled: true, stations: [LOCAL(), REMOTE_STALE(), REMOTE_FAIL()] }));
    await waitFor(() => screen.getByText('MAST-1'));

    expect(screen.getByRole('heading', { level: 2, name: '◇ Weather' })).toBeInTheDocument();
    expect(container.querySelectorAll('[data-testid="weather-station"]')).toHaveLength(3);
    expect(screen.getByText('3 STATIONS')).toBeInTheDocument();
    expect(screen.getByText('SENSOR ON')).toHaveClass('lat-chip', 'ok');
    expect(screen.getByText('LOCAL')).toHaveClass('lat-chip');
    expect(screen.getByText('1 STALE')).toHaveClass('lat-chip', 'warn');

    // Fresh station: accent temperature, wind in mph from NW.
    const [fresh, stale, failed] = container.querySelectorAll('[data-testid="weather-station"]');
    expect(fresh.querySelector('.big-num').textContent).toBe('41°F');
    expect(fresh.querySelector('.big-num')).not.toHaveClass('muted');
    expect(fresh.textContent).toContain('12 mph');
    expect(fresh.textContent).toContain('NW (315°)');
    expect(fresh.querySelector('.kv .v.warn').textContent).toBe('FALLING 3.4 hPa / 3 h');

    // Stale station: greyed temperature, STALE label (not color alone) and an alert.
    expect(stale.querySelector('.big-num')).toHaveClass('muted');
    expect(stale.textContent).toContain('STALE');
    expect(stale.querySelector('.lat-alert.warn').textContent).toContain('15m ago');

    // Failed sensor: FAIL label and the cause.
    expect(failed.querySelector('.lat-chip.crit').textContent).toBe('FAIL');
    expect(failed.querySelector('.lat-alert.crit').textContent).toContain('modbus: timeout');
  });

  it('shows the empty state when no station is heard', async () => {
    await renderWeather(() => ({ sensorEnabled: false, stations: [] }));
    await waitFor(() => screen.getByText('No weather stations heard.'));
    expect(screen.getByText('NO LOCAL SENSOR')).toBeInTheDocument();
    expect(screen.getByText('0 STATIONS')).toBeInTheDocument();
  });

  it('shows an error when the first load fails', async () => {
    await renderWeather(() => { throw new ConnectError('daemon down', Code.Unavailable); });
    await waitFor(() => screen.getByText(/Weather unavailable/));
    expect(screen.getByText(/daemon down/).closest('.lat-alert')).toHaveClass('crit');
  });

  it('keeps the last data when a refresh fails', async () => {
    let fail = false;
    await renderWeather(() => {
      if (fail) throw new ConnectError('timeout', Code.DeadlineExceeded);
      return { sensorEnabled: true, stations: [LOCAL()] };
    });
    await waitFor(() => screen.getByText('MAST-1'));

    fail = true;
    fireEvent.click(screen.getByRole('button', { name: 'REFRESH' }));
    await waitFor(() => screen.getByText(/Last refresh failed/));
    expect(screen.getByText('MAST-1')).toBeInTheDocument();
    expect(screen.getByText(/Last refresh failed/)).toHaveClass('lat-alert', 'warn');
  });
});
