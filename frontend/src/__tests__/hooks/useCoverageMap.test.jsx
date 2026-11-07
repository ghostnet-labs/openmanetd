// =============================================================================
// useCoverageMap.test.jsx — coverage map poll
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { renderHook, waitFor, cleanup } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { CoverageService } from '../../gen/openmanet/coverage/v1/coverage_service_pb.js';

const POLL_MS = 30_000;

async function loadHook(handler) {
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(CoverageService, { getCoverageMap: handler });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const { useCoverageMap } = await import('../../hooks/useCoverageMap.js');
  return useCoverageMap;
}

async function flushTransport() {
  for (let i = 0; i < 10; i += 1) {
    await new Promise((r) => setTimeout(r, 0));
  }
}

const RESPONSE = {
  enabled: true,
  local: { hostname: 'alpha', cellMeters: 50, cells: [{ row: 1, col: 2, samples: 4 }] },
};

beforeEach(() => vi.resetModules());
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetModules(); });

describe('TestUseCoverageMap', () => {
  it('publishes the coverage response', async () => {
    const useCoverageMap = await loadHook(() => RESPONSE);
    const { result } = renderHook(() => useCoverageMap(POLL_MS));

    expect(result.current).toBeNull();
    await waitFor(() => expect(result.current?.res).toBeTruthy());
    expect(result.current.res.enabled).toBe(true);
    expect(result.current.res.local.hostname).toBe('alpha');
    expect(result.current.res.local.cells[0].samples).toBe(4);
    expect(result.current.error).toBeUndefined();
  });

  it('stops polling on an older daemon without the service', async () => {
    const handler = vi.fn(() => { throw new ConnectError('no coverage', Code.Unimplemented); });
    const useCoverageMap = await loadHook(handler);

    const first = renderHook(() => useCoverageMap(POLL_MS));
    await waitFor(() => expect(first.result.current?.unavailable).toBe(true));
    first.unmount();

    const second = renderHook(() => useCoverageMap(POLL_MS));
    await flushTransport();
    expect(handler).toHaveBeenCalledTimes(1);
    expect(second.result.current.unavailable).toBe(true);
  });

  it('reports an error with no response yet', async () => {
    const useCoverageMap = await loadHook(() => { throw new ConnectError('db closed', Code.Internal); });
    const { result } = renderHook(() => useCoverageMap(POLL_MS));

    await waitFor(() => expect(result.current?.error).toBeTruthy());
    expect(result.current.error).toContain('db closed');
    expect(result.current.res).toBeNull();
  });

  it('keeps the last response alongside a later error', async () => {
    let calls = 0;
    const handler = vi.fn(() => {
      calls += 1;
      if (calls > 1) throw new ConnectError('db closed', Code.Internal);
      return RESPONSE;
    });
    const useCoverageMap = await loadHook(handler);

    const first = renderHook(() => useCoverageMap(POLL_MS));
    await waitFor(() => expect(first.result.current?.res).toBeTruthy());
    first.unmount();

    const second = renderHook(() => useCoverageMap(POLL_MS));
    await waitFor(() => expect(second.result.current?.error).toBeTruthy());
    expect(second.result.current.res.local.hostname).toBe('alpha');
  });
});
