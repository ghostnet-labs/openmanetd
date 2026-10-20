// =============================================================================
// ChannelMigrationPanel.test.jsx — Channel migration panel states (GHO-52)
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { ChannelMigrationService } from '../../gen/openmanet/chanmig/v1/chanmig_service_pb.js';
import { BandMode, MigrationPhase } from '../../gen/openmanet/chanmig/v1/chanmig_pb.js';
import { Band } from '../../gen/openmanet/radio/v1/radio_pb.js';

vi.mock('../../services/connectClient.js', () => ({ transport: {} }));

const halow = (channel, widthMhz = 2) => ({ band: Band.BAND_HALOW, channel, widthMhz });
const wifi5 = (channel, widthMhz = 80) => ({ band: Band.BAND_5_GHZ, channel, widthMhz });

const ENABLED = () => ({
  status: {
    enabled: true,
    nodeId: 7n,
    meshId: 3n,
    coordinator: true,
    phase: MigrationPhase.PENDING,
    currentPlan: { version: 4n, assignments: [halow(30), wifi5(44)] },
    bands: [
      { band: Band.BAND_HALOW, operating: halow(28), target: halow(30), mode: BandMode.SETTLED },
      { band: Band.BAND_5_GHZ, operating: wifi5(44), target: wifi5(44), mode: BandMode.FALLBACK, radioFailed: true },
    ],
    peers: [{ nodeId: 1n }, { nodeId: 2n }, { nodeId: 3n }],
  },
});

let calls;

async function renderWith(handler) {
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(ChannelMigrationService, {
      getChannelMigrationStatus: () => { calls += 1; return handler(); },
      proposeChannelPlan: () => { throw new ConnectError('not used', Code.Unimplemented); },
    });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const mod = await import('../../pages/ChannelMigrationPanel.jsx');
  return { ...render(<mod.default />), mod };
}

beforeEach(() => {
  vi.resetModules();
  calls = 0;
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('TestChannelMigrationPanel', () => {
  it('shows a loading state first', async () => {
    let release;
    await renderWith(() => new Promise((r) => { release = r; }));
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => expect(release).toBeDefined());
    release(ENABLED());
    await waitFor(() => screen.getByText('Bands'));
  });

  it('renders phase, plan version, peers and per-band channels', async () => {
    const { container } = await renderWith(ENABLED);
    await waitFor(() => screen.getByText('Bands'));

    expect(screen.getByRole('heading', { name: 'Channel migration' })).toBeInTheDocument();
    expect(screen.getByText('ON')).toHaveClass('lat-chip', 'ok');
    expect(screen.getByText('SWITCH PENDING')).toHaveClass('v', 'accent');
    expect(screen.getByText('v4')).toBeInTheDocument();
    expect(screen.getByText('3')).toBeInTheDocument();
    expect(screen.getByText('Coordinator')).toBeInTheDocument();

    const table = screen.getByRole('table', { name: 'Channel migration bands' });
    expect(table.textContent).toContain('HaLow');
    expect(table.textContent).toContain('ch 28 · 2 MHz');
    expect(table.textContent).toContain('ch 30 · 2 MHz');
    expect(table.textContent).toContain('5 GHz');
    expect(table.textContent).toContain('ch 44 · 80 MHz');
    expect(table.querySelector('td.badge-ok').textContent).toBe('SETTLED');
    expect(table.querySelector('td.badge-crit').textContent).toBe('FAIL');

    // The same rows render as cards for narrow screens.
    expect(container.querySelectorAll('.lat-cardlist .lat-card')).toHaveLength(2);
    expect(screen.queryByRole('button', { name: /propose/i })).toBeNull();
  });

  it('shows the disabled state', async () => {
    await renderWith(() => ({ status: { enabled: false } }));
    await waitFor(() => screen.getByText('Channel migration disabled.'));
    expect(screen.getByText('OFF')).toHaveClass('lat-chip');
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('shows the initial plan and an empty band list', async () => {
    await renderWith(() => ({ status: { enabled: true, phase: MigrationPhase.STABLE, bands: [], peers: [] } }));
    await waitFor(() => screen.getByText('No managed bands.'));
    expect(screen.getByText('Initial')).toBeInTheDocument();
    expect(screen.getByText('STABLE')).toHaveClass('v', 'ok');
    expect(screen.getByText('0')).toBeInTheDocument();
    expect(screen.getByText('Member')).toBeInTheDocument();
  });

  it('shows an error when the first load fails', async () => {
    await renderWith(() => { throw new ConnectError('connection refused', Code.Unavailable); });
    await waitFor(() => screen.getByText(/Channel migration status unavailable/));
    expect(screen.getByText(/connection refused/)).toHaveClass('lat-alert', 'crit');
  });

  it('polls every POLL_MS and on refresh, keeping data on a failed poll', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    let fail = false;
    const { mod } = await renderWith(() => {
      if (fail) throw new ConnectError('flaky', Code.Unavailable);
      return ENABLED();
    });
    await waitFor(() => screen.getByText('Bands'));
    expect(mod.POLL_MS).toBe(15000);
    expect(calls).toBe(1);

    fail = true;
    await act(async () => { vi.advanceTimersByTime(mod.POLL_MS); });
    await waitFor(() => expect(calls).toBe(2));
    await waitFor(() => screen.getByText(/Last refresh failed: flaky/));
    expect(screen.getByText('v4')).toBeInTheDocument();

    fail = false;
    fireEvent.click(screen.getByRole('button', { name: 'refresh' }));
    await waitFor(() => expect(calls).toBe(3));
    await waitFor(() => expect(screen.queryByText(/Last refresh failed/)).toBeNull());
  });
});
