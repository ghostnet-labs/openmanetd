// =============================================================================
// useDeviceReboot.test.jsx — reboot confirm/request/reconnect state machine
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, act } from '@testing-library/react';
import { Code, ConnectError } from '@connectrpc/connect';
import {
  useDeviceReboot,
  RebootPhase,
  isExplicitRebootFailure,
  probeDevice,
} from '../../hooks/useDeviceReboot.js';

const POLL = 1000;
const DOWN_TIMEOUT = 5000;
const UP_TIMEOUT = 10000;

function setVisibility(state) {
  Object.defineProperty(document, 'visibilityState', {
    value: state,
    configurable: true,
    writable: true,
  });
}

function renderReboot(overrides = {}) {
  const props = {
    requestReboot: vi.fn().mockResolvedValue({ success: true, message: 'Reboot initiated' }),
    probe: vi.fn().mockResolvedValue(true),
    pollIntervalMs: POLL,
    downTimeoutMs: DOWN_TIMEOUT,
    upTimeoutMs: UP_TIMEOUT,
    ...overrides,
  };
  const hook = renderHook(() => useDeviceReboot(props));
  return { ...hook, props };
}

async function confirmReboot(result) {
  act(() => result.current.begin());
  await act(async () => { await result.current.confirm(); });
}

async function advance(ms) {
  await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

describe('TestUseDeviceReboot', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    setVisibility('visible');
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
  });

  it('starts idle and does not poll', async () => {
    const { result, props } = renderReboot();
    expect(result.current.phase).toBe(RebootPhase.IDLE);
    await advance(POLL * 3);
    expect(props.probe).not.toHaveBeenCalled();
  });

  it('begin enters confirm and reset returns to idle without a request', () => {
    const { result, props } = renderReboot();
    act(() => result.current.begin());
    expect(result.current.phase).toBe(RebootPhase.CONFIRM);
    act(() => result.current.reset());
    expect(result.current.phase).toBe(RebootPhase.IDLE);
    expect(props.requestReboot).not.toHaveBeenCalled();
  });

  it('walks going-down → reconnecting → online as the probe flips', async () => {
    const probe = vi.fn()
      .mockResolvedValueOnce(true)   // still shutting down
      .mockResolvedValueOnce(false)  // gone
      .mockResolvedValueOnce(false)  // still gone
      .mockResolvedValue(true);      // back
    const { result, props } = renderReboot({ probe });

    await confirmReboot(result);
    expect(props.requestReboot).toHaveBeenCalledTimes(1);
    // useVisibleInterval fires immediately on start: first probe = up.
    await advance(0);
    expect(result.current.phase).toBe(RebootPhase.GOING_DOWN);

    await advance(POLL);
    expect(result.current.phase).toBe(RebootPhase.RECONNECTING);

    await advance(POLL);
    expect(result.current.phase).toBe(RebootPhase.RECONNECTING);

    await advance(POLL);
    expect(result.current.phase).toBe(RebootPhase.ONLINE);

    const calls = probe.mock.calls.length;
    await advance(POLL * 3);
    expect(probe.mock.calls.length).toBe(calls);
  });

  it('reports still-up when the device never goes down', async () => {
    const { result } = renderReboot();
    await confirmReboot(result);
    await advance(DOWN_TIMEOUT - POLL);
    expect(result.current.phase).toBe(RebootPhase.GOING_DOWN);
    await advance(POLL * 2);
    expect(result.current.phase).toBe(RebootPhase.STILL_UP);
  });

  it('reports timeout when the device does not come back', async () => {
    const { result } = renderReboot({ probe: vi.fn().mockResolvedValue(false) });
    await confirmReboot(result);
    await advance(0);
    expect(result.current.phase).toBe(RebootPhase.RECONNECTING);
    await advance(UP_TIMEOUT + POLL);
    expect(result.current.phase).toBe(RebootPhase.TIMEOUT);
  });

  it('fails with the server message when success is false', async () => {
    const { result, props } = renderReboot({
      requestReboot: vi.fn().mockResolvedValue({ success: false, message: 'reboot not permitted' }),
    });
    await confirmReboot(result);
    expect(result.current.phase).toBe(RebootPhase.FAILED);
    expect(result.current.error).toBe('reboot not permitted');
    await advance(POLL * 2);
    expect(props.probe).not.toHaveBeenCalled();
  });

  it('fails with the cause on an explicit Connect error', async () => {
    const { result } = renderReboot({
      requestReboot: vi.fn().mockRejectedValue(
        new ConnectError('action QUICK_ACTION_REBOOT_DEVICE failed: exit status 1', Code.Internal),
      ),
    });
    await confirmReboot(result);
    expect(result.current.phase).toBe(RebootPhase.FAILED);
    expect(result.current.error).toBe('action QUICK_ACTION_REBOOT_DEVICE failed: exit status 1');
  });

  it('treats a dropped connection as an accepted reboot and keeps probing', async () => {
    const { result, props } = renderReboot({
      requestReboot: vi.fn().mockRejectedValue(new TypeError('Failed to fetch')),
      probe: vi.fn().mockResolvedValue(false),
    });
    await confirmReboot(result);
    await advance(0);
    expect(props.requestReboot).toHaveBeenCalledTimes(1);
    expect(props.probe).toHaveBeenCalled();
    expect(result.current.phase).toBe(RebootPhase.RECONNECTING);
  });
});

describe('TestIsExplicitRebootFailure', () => {
  it.each([
    [new ConnectError('x', Code.Internal), true],
    [new ConnectError('x', Code.PermissionDenied), true],
    [new ConnectError('x', Code.Unauthenticated), true],
    [new ConnectError('x', Code.Unavailable), false],
    [new ConnectError('x', Code.Unknown), false],
    [new ConnectError('x', Code.Canceled), false],
    [new TypeError('Failed to fetch'), false],
    [null, false],
  ])('classifies %s as %s', (err, want) => {
    expect(isExplicitRebootFailure(err)).toBe(want);
  });
});

describe('TestProbeDevice', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it.each([
    [200, true],
    [401, true],
    [404, true],
    [502, false],
    [503, false],
  ])('maps HTTP %i to %s', async (status, want) => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ ok: status < 300, status }));
    await expect(probeDevice()).resolves.toBe(want);
  });

  it('returns false on a network error', async () => {
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    await expect(probeDevice()).resolves.toBe(false);
  });

  it('requests an uncached response', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200 });
    vi.stubGlobal('fetch', fetchMock);
    await probeDevice();
    expect(fetchMock).toHaveBeenCalledWith('/api/settings/hostname', expect.objectContaining({
      cache: 'no-store',
      credentials: 'include',
    }));
  });
});
