// =============================================================================
// DashboardWeather.test.jsx — Dashboard weather KPI panel
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { createRouterTransport } from '@connectrpc/connect';
import { DashboardService } from '../../gen/openmanet/dashboard/v1/dashboard_service_pb.js';
import { SensorState } from '../../gen/openmanet/weather/v1/weather_pb.js';
import DashboardWeather from '../../pages/DashboardWeather.jsx';

const state = { weather: null };

vi.mock('../../hooks/useMeshStatus.js', () => ({ useMeshStatus: () => null }));
vi.mock('../../hooks/useMeshTopology.js', () => ({ useMeshTopology: () => null }));
vi.mock('../../hooks/useGnssStatus.js', () => ({ useGnssStatus: () => null }));
vi.mock('../../hooks/useBLOSStatus.js', () => ({ useBLOSStatus: () => null }));
vi.mock('../../hooks/useNetworkInterfaces.js', () => ({ useNetworkInterfaces: () => null }));
vi.mock('../../hooks/useHardwareBattery.js', () => ({ useHardwareBattery: () => null }));
vi.mock('../../hooks/useWeatherStations.js', () => ({ useWeatherStations: () => state.weather }));

function station(overrides = {}, report = {}) {
  return {
    local: false,
    stale: false,
    ageSeconds: 30,
    report: {
      primaryMac: 'bb',
      hostname: 'MAST-2',
      state: SensorState.OK,
      reading: { temperatureC: 20, windSpeedMs: 2, windGustMs: 4, windDirectionDeg: 180, pressureHpa: 850, pressureChange3hHpa: 0.1 },
      ...report,
    },
    ...overrides,
  };
}

async function renderDashboard(weather) {
  state.weather = weather;
  const transport = createRouterTransport(({ service }) => {
    service(DashboardService, { getDashboardStatus: () => ({}) });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const { default: DashboardPage } = await import('../../pages/Dashboard.jsx');
  const utils = render(<DashboardPage />);
  await waitFor(() => screen.getByText('System Resources'));
  return utils;
}

beforeEach(() => { vi.resetModules(); state.weather = null; });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetModules(); });

describe('TestDashboardWeather', () => {
  it('renders nothing without stations', () => {
    expect(render(<DashboardWeather stations={null} />).container.innerHTML).toBe('');
    cleanup();
    expect(render(<DashboardWeather stations={{ stations: [] }} />).container.innerHTML).toBe('');
  });

  it('shows a remote station with its name', () => {
    const { container } = render(<DashboardWeather stations={{ stations: [station()] }} />);
    expect(screen.getByText('Weather · MAST-2')).toBeInTheDocument();
    expect(container.querySelector('.big-num').textContent).toBe('68°F');
    expect(screen.getByText('4 mph · S (180°)')).toHaveClass('accent');
    expect(screen.getByText('850.0 hPa')).toBeInTheDocument();
    expect(screen.getByText('STEADY')).toBeInTheDocument();
    expect(screen.getByText('1')).toBeInTheDocument();
  });

  it('prefers this node and counts stale stations', () => {
    const local = station({ local: true }, { hostname: 'HERE', primaryMac: 'aa' });
    const stale = station({ stale: true, ageSeconds: 600 }, { hostname: 'OLD', primaryMac: 'cc' });
    render(<DashboardWeather stations={{ stations: [stale, local] }} />);
    expect(screen.getByText('Weather · here')).toBeInTheDocument();
    expect(screen.getByText('2 · 1 stale')).toBeInTheDocument();
  });

  it('greys out a stale reading', () => {
    const { container } = render(<DashboardWeather stations={{ stations: [station({ stale: true, ageSeconds: 600 })] }} />);
    expect(container.querySelector('.big-num')).toHaveClass('muted');
    expect(screen.getByText('10m ago')).toHaveClass('warn');
  });

  it('joins the Dashboard KPI row only when a station is heard', async () => {
    const without = await renderDashboard(null);
    expect(without.container.querySelector('.dashboard-kpi-row')).not.toHaveClass('with-weather');
    expect(without.container.querySelector('.dashboard-weather')).toBeNull();
    cleanup();
    vi.resetModules();

    const withWx = await renderDashboard({ stations: [station()] });
    expect(withWx.container.querySelector('.dashboard-kpi-row')).toHaveClass('with-weather');
    expect(withWx.container.querySelector('.dashboard-kpi-row .dashboard-weather')).not.toBeNull();
  });
});
