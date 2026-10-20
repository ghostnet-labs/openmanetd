// =============================================================================
// useHardwareBattery.test.jsx — hardware manager battery poll
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, cleanup } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { HardwareService } from '../../gen/openmanet/hardware/v1/hardware_service_pb.js';
import { BatteryLevel } from '../../gen/openmanet/hardware/v1/hardware_pb.js';

const POLL_MS = 60_000;

// loadHook returns a fresh useHardwareBattery whose RPCs go to handler.
async function loadHook(handler) {
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(HardwareService, { getHardwareStatus: handler });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const { useHardwareBattery } = await import('../../hooks/useHardwareBattery.js');
  return useHardwareBattery;
}

// flushTransport yields a few macrotasks: createRouterTransport dispatches
// through a Fetch-like Request/Response, so a started call reaches its
// handler only after later macrotasks. Used to prove a call never started.
async function flushTransport() {
  for (let i = 0; i < 10; i += 1) {
    await new Promise((r) => setTimeout(r, 0));
  }
}

const BATTERY = { present: true, busVoltageMv: 11400, currentMa: 850, level: BatteryLevel.WARNING, stale: true };

beforeEach(() => vi.resetModules());
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetModules(); });

describe('TestUseHardwareBattery', () => {
  it('publishes the battery block of the hardware status', async () => {
    const useHardwareBattery = await loadHook(() => ({ status: { battery: BATTERY } }));
    const { result } = renderHook(() => useHardwareBattery(POLL_MS));

    expect(result.current).toBeNull();
    await waitFor(() => expect(result.current).not.toBeNull());
    expect(result.current.present).toBe(true);
    expect(result.current.busVoltageMv).toBe(11400);
    expect(result.current.currentMa).toBe(850);
    expect(result.current.level).toBe(BatteryLevel.WARNING);
    expect(result.current.stale).toBe(true);
  });

  it('publishes null when the status carries no battery block', async () => {
    const handler = vi.fn(() => ({ status: {} }));
    const useHardwareBattery = await loadHook(handler);
    const { result } = renderHook(() => useHardwareBattery(POLL_MS));

    await waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(result.current).toBeNull();
  });

  it.each([
    ['FailedPrecondition', Code.FailedPrecondition],
    ['Unimplemented', Code.Unimplemented],
  ])('stops polling when the manager is unavailable (%s)', async (_name, code) => {
    const handler = vi.fn(() => { throw new ConnectError('hardware manager is not running', code); });
    const useHardwareBattery = await loadHook(handler);

    const first = renderHook(() => useHardwareBattery(POLL_MS));
    await waitFor(() => expect(handler).toHaveBeenCalledTimes(1));
    expect(first.result.current).toBeNull();
    first.unmount();

    // A fresh mount primes a new fetch; the store must not call the RPC again.
    const second = renderHook(() => useHardwareBattery(POLL_MS));
    await flushTransport();
    expect(handler).toHaveBeenCalledTimes(1);
    expect(second.result.current).toBeNull();
  });

  it('keeps the last snapshot on a transient error', async () => {
    let calls = 0;
    const handler = vi.fn(() => {
      calls += 1;
      if (calls > 1) throw new ConnectError('i2c busy', Code.Internal);
      return { status: { battery: BATTERY } };
    });
    const useHardwareBattery = await loadHook(handler);

    const first = renderHook(() => useHardwareBattery(POLL_MS));
    await waitFor(() => expect(first.result.current).not.toBeNull());
    first.unmount();

    const second = renderHook(() => useHardwareBattery(POLL_MS));
    await waitFor(() => expect(handler).toHaveBeenCalledTimes(2));
    await flushTransport();
    expect(second.result.current?.busVoltageMv).toBe(11400);
  });
});
