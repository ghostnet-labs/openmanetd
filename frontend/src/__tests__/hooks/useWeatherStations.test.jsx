// =============================================================================
// useWeatherStations.test.jsx — shared weather station poll
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, cleanup } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { WeatherService } from '../../gen/openmanet/weather/v1/weather_service_pb.js';

const POLL_MS = 60_000;

async function loadHook(handler) {
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(WeatherService, { listWeatherStations: handler });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const { useWeatherStations } = await import('../../hooks/useWeatherStations.js');
  return useWeatherStations;
}

async function flushTransport() {
  for (let i = 0; i < 10; i += 1) {
    await new Promise((r) => setTimeout(r, 0));
  }
}

beforeEach(() => vi.resetModules());
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetModules(); });

describe('TestUseWeatherStations', () => {
  it('publishes the station list', async () => {
    const useWeatherStations = await loadHook(() => ({
      sensorEnabled: true,
      stations: [{ local: true, ageSeconds: 3, report: { hostname: 'MAST-1' } }],
    }));
    const { result } = renderHook(() => useWeatherStations(POLL_MS));

    expect(result.current).toBeNull();
    await waitFor(() => expect(result.current).not.toBeNull());
    expect(result.current.sensorEnabled).toBe(true);
    expect(result.current.stations).toHaveLength(1);
    expect(result.current.stations[0].report.hostname).toBe('MAST-1');
  });

  it('stops polling on a daemon without WeatherService', async () => {
    const handler = vi.fn(() => { throw new ConnectError('unimplemented', Code.Unimplemented); });
    const useWeatherStations = await loadHook(handler);

    const first = renderHook(() => useWeatherStations(POLL_MS));
    await waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(first.result.current).toBeNull();
    first.unmount();

    const second = renderHook(() => useWeatherStations(POLL_MS));
    await flushTransport();
    expect(handler).toHaveBeenCalledTimes(1);
    expect(second.result.current).toBeNull();
  });

  it('keeps polling after a transient error', async () => {
    let fail = true;
    const handler = vi.fn(() => {
      if (fail) throw new ConnectError('busy', Code.Unavailable);
      return { stations: [] };
    });
    const useWeatherStations = await loadHook(handler);

    const first = renderHook(() => useWeatherStations(POLL_MS));
    await waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(first.result.current).toBeNull();
    first.unmount();

    fail = false;
    const second = renderHook(() => useWeatherStations(POLL_MS));
    await waitFor(() => expect(second.result.current).not.toBeNull());
    expect(handler).toHaveBeenCalledTimes(2);
  });
});
