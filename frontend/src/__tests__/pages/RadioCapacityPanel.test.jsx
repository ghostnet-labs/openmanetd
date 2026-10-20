// =============================================================================
// RadioCapacityPanel.test.jsx — Radio capacity panel states (GHO-51)
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup, act } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { RadioService } from '../../gen/openmanet/radio/v1/radio_service_pb.js';
import {
  AdmissionOutcome,
  AdmissionReason,
  Band,
  CapacityGateMode,
} from '../../gen/openmanet/radio/v1/radio_pb.js';

vi.mock('../../services/connectClient.js', () => ({ transport: {} }));

function ts(msAgo) {
  const ms = Date.now() - msAgo;
  return { seconds: BigInt(Math.floor(ms / 1000)), nanos: 0 };
}

const CAPACITY = () => ({
  capacity: {
    gateMode: CapacityGateMode.ADVISORY,
    generatedAt: ts(0),
    inventoryUpdatedAt: ts(20_000),
    phys: [
      { name: 'phy0', wiphy: 'phy0', bands: [Band.BAND_2_4_GHZ], qualified: true, available: true },
      { name: 'phy1', wiphy: 'phy1', bands: [Band.BAND_5_GHZ, Band.BAND_6_GHZ], qualified: false, available: false },
      { name: 'phy2', wiphy: 'phy2', bands: [Band.BAND_HALOW], qualified: false, available: true, isHalow: true },
    ],
  },
});

const DECISIONS = () => ({
  decisions: [
    {
      decidedAt: ts(30_000),
      gateMode: CapacityGateMode.ADVISORY,
      outcome: AdmissionOutcome.REJECT,
      reason: AdmissionReason.UNQUALIFIED_ONLY,
      explanation: 'reject: default_radio3 fits only on unqualified PHYs (phy2)',
    },
    {
      decidedAt: ts(5 * 60_000),
      gateMode: CapacityGateMode.ENFORCE,
      outcome: AdmissionOutcome.ADMIT,
      reason: AdmissionReason.FITS,
      explanation: 'admit: all demands fit',
    },
  ],
});

// calls counts RPCs per method so polling can be asserted.
let calls;

async function renderWith(handlers) {
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(RadioService, {
      getRadioCapacity: () => { calls.capacity += 1; return handlers.getRadioCapacity(); },
      listAdmissionDecisions: (req) => { calls.decisions += 1; calls.lastLimit = req.limit; return handlers.listAdmissionDecisions(req); },
      listPathDecisions: () => ({ decisions: [] }),
    });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const mod = await import('../../pages/RadioCapacityPanel.jsx');
  return { ...render(<mod.default />), mod };
}

beforeEach(() => {
  vi.resetModules();
  calls = { capacity: 0, decisions: 0, lastLimit: null };
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('TestRadioCapacityPanel', () => {
  it('shows a loading state first', async () => {
    let release;
    await renderWith({
      getRadioCapacity: () => new Promise((r) => { release = r; }),
      listAdmissionDecisions: DECISIONS,
    });
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => expect(release).toBeDefined());
    release(CAPACITY());
    await waitFor(() => screen.getByText('PHY inventory'));
  });

  it('renders gate mode, PHY inventory and recent decisions', async () => {
    const { container } = await renderWith({ getRadioCapacity: CAPACITY, listAdmissionDecisions: DECISIONS });
    await waitFor(() => screen.getByText('Recent decisions'));

    expect(screen.getByRole('heading', { name: 'Radio capacity' })).toBeInTheDocument();
    expect(screen.getAllByText('Advisory').length).toBeGreaterThan(0);
    expect(screen.getByText('Recorded, never blocks')).toBeInTheDocument();
    expect(screen.getByText('20s ago')).toBeInTheDocument();

    const phyTable = screen.getByRole('table', { name: 'PHY inventory' });
    expect(phyTable.textContent).toContain('phy0');
    expect(phyTable.textContent).toContain('2.4 GHz');
    expect(phyTable.textContent).toContain('5 GHz / 6 GHz');
    expect(phyTable.textContent).toContain('HaLow');
    expect(phyTable.textContent).toContain('DOWN');

    const decTable = screen.getByRole('table', { name: 'Admission decisions' });
    expect(decTable.textContent).toContain('REJECT');
    expect(decTable.textContent).toContain('Unqualified PHYs only');
    expect(decTable.textContent).toContain('Not blocked (advisory)');
    expect(decTable.textContent).toContain('fits only on unqualified PHYs');
    expect(decTable.textContent).toContain('ADMIT');
    expect(decTable.textContent).toContain('Applied');
    expect(decTable.textContent).toContain('5m ago');
    expect(decTable.querySelector('td.badge-crit').textContent).toBe('REJECT');
    expect(decTable.querySelector('td.badge-ok').textContent).toBe('ADMIT');

    // The same rows render as cards for narrow screens.
    expect(container.querySelectorAll('.lat-cardlist .lat-card')).toHaveLength(5);
    expect(calls.lastLimit).toBe(10);
    expect(screen.getByText(/physical concurrency not evaluated/)).toBeInTheDocument();
  });

  it('shows empty states', async () => {
    await renderWith({
      getRadioCapacity: () => ({ capacity: { gateMode: CapacityGateMode.ENFORCE, phys: [] } }),
      listAdmissionDecisions: () => ({ decisions: [] }),
    });
    await waitFor(() => screen.getByText('No admission decisions yet.'));
    expect(screen.getByText('No PHYs detected.')).toBeInTheDocument();
    expect(screen.getByText('Defer / reject blocks the change')).toBeInTheDocument();
    expect(screen.getByText('—', { selector: '.kv .v' })).toBeInTheDocument();
  });

  it('reports an inventory failure but still lists decisions', async () => {
    await renderWith({
      getRadioCapacity: () => { throw new ConnectError('radio inventory: iw list: exec failed', Code.Internal); },
      listAdmissionDecisions: DECISIONS,
    });
    await waitFor(() => screen.getByText(/Radio inventory unavailable/));
    expect(screen.getByText(/iw list: exec failed/).closest('.lat-alert')).toHaveClass('crit');
    expect(screen.getByRole('table', { name: 'Admission decisions' }).textContent).toContain('REJECT');
    expect(screen.queryByText('PHY inventory')).toBeNull();
  });

  it('reports a decisions failure', async () => {
    await renderWith({
      getRadioCapacity: CAPACITY,
      listAdmissionDecisions: () => { throw new ConnectError('boom', Code.Unavailable); },
    });
    await waitFor(() => screen.getByText(/Decisions unavailable/));
    expect(screen.getByText(/Decisions unavailable/)).toHaveClass('lat-alert', 'crit');
    expect(screen.getByRole('table', { name: 'PHY inventory' })).toBeInTheDocument();
  });

  it('shows the not-running state when the gate is absent', async () => {
    const unavailable = () => { throw new ConnectError('radio capacity gate is not running', Code.FailedPrecondition); };
    await renderWith({ getRadioCapacity: unavailable, listAdmissionDecisions: unavailable });
    await waitFor(() => screen.getByText('Radio capacity gate not running.'));
    expect(screen.queryByRole('table')).toBeNull();
  });

  it('polls every POLL_MS and on refresh, keeping data on a failed poll', async () => {
    vi.useFakeTimers({ toFake: ['setInterval', 'clearInterval'] });
    let fail = false;
    const { mod } = await renderWith({
      getRadioCapacity: () => {
        if (fail) throw new ConnectError('flaky', Code.Unavailable);
        return CAPACITY();
      },
      listAdmissionDecisions: DECISIONS,
    });
    await waitFor(() => screen.getByText('PHY inventory'));
    expect(mod.POLL_MS).toBe(15000);
    expect(calls.capacity).toBe(1);

    fail = true;
    await act(async () => { vi.advanceTimersByTime(mod.POLL_MS); });
    await waitFor(() => expect(calls.capacity).toBe(2));
    await waitFor(() => screen.getByText(/Radio inventory unavailable: flaky/));
    expect(screen.getByText('PHY inventory')).toBeInTheDocument();

    fail = false;
    fireEvent.click(screen.getByRole('button', { name: 'refresh' }));
    await waitFor(() => expect(calls.capacity).toBe(3));
    await waitFor(() => expect(screen.queryByText(/Radio inventory unavailable/)).toBeNull());
    expect(calls.decisions).toBe(3);
  });
});
