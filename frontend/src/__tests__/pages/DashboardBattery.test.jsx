// =============================================================================
// DashboardBattery.test.jsx — Dashboard battery rows fed by the hardware manager
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, cleanup } from '@testing-library/react';
import { createRouterTransport } from '@connectrpc/connect';
import { DashboardService } from '../../gen/openmanet/dashboard/v1/dashboard_service_pb.js';
import { BatteryAbsentReason, BatteryLevel } from '../../gen/openmanet/hardware/v1/hardware_pb.js';

const state = { hwBattery: null };

vi.mock('../../hooks/useMeshStatus.js', () => ({ useMeshStatus: () => null }));
vi.mock('../../hooks/useMeshTopology.js', () => ({ useMeshTopology: () => null }));
vi.mock('../../hooks/useGnssStatus.js', () => ({ useGnssStatus: () => null }));
vi.mock('../../hooks/useBLOSStatus.js', () => ({ useBLOSStatus: () => null }));
vi.mock('../../hooks/useNetworkInterfaces.js', () => ({ useNetworkInterfaces: () => null }));
vi.mock('../../hooks/useHardwareBattery.js', () => ({ useHardwareBattery: () => state.hwBattery }));

const V1_DASH_BATTERY = { present: true, voltageVolts: 11.4, powerWatts: 9.69, chargePercent: -1, cellCount: 3 };

async function renderDashboard({ dashBattery = V1_DASH_BATTERY, hwBattery = null } = {}) {
  state.hwBattery = hwBattery;
  const transport = createRouterTransport(({ service }) => {
    service(DashboardService, { getDashboardStatus: () => ({ battery: dashBattery }) });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const { default: DashboardPage } = await import('../../pages/Dashboard.jsx');
  const utils = render(<DashboardPage />);
  await waitFor(() => screen.getByText('System Resources'));
  return utils;
}

function telemetry(overrides = {}) {
  return {
    present: true,
    stale: false,
    absentReason: BatteryAbsentReason.NONE,
    busVoltageMv: 11400,
    currentMa: 850,
    powerMw: 9690,
    level: BatteryLevel.NORMAL,
    seriesCells: 3,
    ...overrides,
  };
}

// kvValue returns the value cell of the .kv row whose key is label.
function kvValue(container, label) {
  for (const row of container.querySelectorAll('.kv')) {
    if (row.querySelector('.k')?.textContent === label) return row.querySelector('.v');
  }
  return null;
}

beforeEach(() => { vi.resetModules(); state.hwBattery = null; });
afterEach(() => { cleanup(); vi.restoreAllMocks(); vi.resetModules(); });

describe('TestDashboardBattery', () => {
  it('shows only the BATT bar without a hardware manager', async () => {
    const { container } = await renderDashboard();
    expect(screen.getByText('BATT')).toBeInTheDocument();
    expect(screen.getByText('11.40 V · 9.7 W')).toBeInTheDocument();
    expect(kvValue(container, 'Battery')).toBeNull();
    expect(kvValue(container, 'Batt Current')).toBeNull();
  });

  it('shows the manager level and current', async () => {
    const { container } = await renderDashboard({ hwBattery: telemetry() });
    const v = kvValue(container, 'Battery');
    expect(v).toHaveTextContent('NORMAL');
    expect(v).toHaveClass('v', 'ok');
    expect(kvValue(container, 'Batt Current')).toHaveTextContent('0.85 A');
  });

  it('marks a stale reading as warn with a STALE label', async () => {
    const { container } = await renderDashboard({ hwBattery: telemetry({ stale: true }) });
    const v = kvValue(container, 'Battery');
    expect(v).toHaveTextContent('STALE · NORMAL');
    expect(v).toHaveClass('warn');
  });

  it('colors the BATT bar critical at the critical level', async () => {
    const { container } = await renderDashboard({ hwBattery: telemetry({ level: BatteryLevel.CRITICAL }) });
    expect(kvValue(container, 'Battery')).toHaveClass('crit');
    const row = screen.getByText('BATT').closest('.pbar-row');
    expect(row.querySelector('.pbar')).toHaveClass('crit');
  });

  it('shows an unreachable monitor as critical', async () => {
    const { container } = await renderDashboard({
      dashBattery: { present: false, chargePercent: -1 },
      hwBattery: telemetry({ present: false, absentReason: BatteryAbsentReason.MONITOR_UNREACHABLE }),
    });
    const v = kvValue(container, 'Battery');
    expect(v).toHaveTextContent('UNREACHABLE');
    expect(v).toHaveClass('crit');
    expect(kvValue(container, 'Batt Current')).toBeNull();
  });

  it('renders no battery rows on a node without a monitor', async () => {
    const { container } = await renderDashboard({
      dashBattery: { present: false, chargePercent: -1 },
      hwBattery: telemetry({ present: false, absentReason: BatteryAbsentReason.NO_MONITOR }),
    });
    expect(screen.queryByText('BATT')).toBeNull();
    expect(kvValue(container, 'Battery')).toBeNull();
  });
});
