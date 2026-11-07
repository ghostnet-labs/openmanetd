// =============================================================================
// DashboardJam.test.jsx — Dashboard jamming panel and alerts (GHO-93)
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { createRouterTransport } from '@connectrpc/connect';
import { DashboardService } from '../../gen/openmanet/dashboard/v1/dashboard_service_pb.js';
import { JamState, RadioBand } from '../../gen/openmanet/jamdetect/v1/jamdetect_pb.js';

const state = { jam: null };

vi.mock('../../hooks/useMeshStatus.js', () => ({ useMeshStatus: () => null }));
vi.mock('../../hooks/useMeshTopology.js', () => ({ useMeshTopology: () => null }));
vi.mock('../../hooks/useGnssStatus.js', () => ({ useGnssStatus: () => null }));
vi.mock('../../hooks/useBLOSStatus.js', () => ({ useBLOSStatus: () => null }));
vi.mock('../../hooks/useNetworkInterfaces.js', () => ({ useNetworkInterfaces: () => null }));
vi.mock('../../hooks/useHardwareBattery.js', () => ({ useHardwareBattery: () => null }));
vi.mock('../../hooks/useJamStatus.js', () => ({ useJamStatus: () => state.jam }));

async function renderDashboard(jam) {
  state.jam = jam;
  const transport = createRouterTransport(({ service }) => {
    service(DashboardService, { getDashboardStatus: () => ({}) });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const { default: DashboardPage } = await import('../../pages/Dashboard.jsx');
  const utils = render(<DashboardPage />);
  await waitFor(() => screen.getByText('System Resources'));
  return utils;
}

const jammedRadio = {
  radio: 'wlh0',
  band: RadioBand.RADIO_BAND_S1G,
  freqMhz: 915.5,
  state: JamState.JAMMED,
  noiseKnown: true,
  noiseDbm: -84,
  baselineNoiseDbm: -100,
  unexplainedBusyPct: 71,
  baselineUnexplainedBusyPct: 4,
};

beforeEach(() => { vi.resetModules(); state.jam = null; });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetModules(); });

describe('TestDashboardJam', () => {
  it('hides the panel on a daemon without jamming detection', async () => {
    await renderDashboard(null);
    expect(screen.queryByText('Jamming · Radios')).toBeNull();
  });

  it('shows detection off', async () => {
    await renderDashboard({ enabled: false });
    expect(screen.getByText('Jamming · Radios')).toBeInTheDocument();
    expect(screen.getByText('Jamming detection off')).toBeInTheDocument();
  });

  it('shows a jammed radio and raises a crit alert', async () => {
    const { container } = await renderDashboard({
      enabled: true,
      local: { radios: [jammedRadio], activeEvents: [{ id: 'wlh0-1', state: JamState.JAMMED, radio: jammedRadio }] },
      remote: [],
    });

    expect(screen.getByText('1 JAMMED')).toHaveClass('v', 'crit');
    expect(screen.getByText('0 REPORTING')).toBeInTheDocument();
    const alert = [...container.querySelectorAll('.lat-alert.crit')].find((el) => el.textContent === 'JAMMING · HALOW 915.5 MHz');
    expect(alert).toBeTruthy();
    expect(screen.getAllByText('-84 dBm').length).toBeGreaterThan(0);
  });
});
