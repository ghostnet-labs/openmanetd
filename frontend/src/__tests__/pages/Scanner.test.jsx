// =============================================================================
// Scanner.test.jsx — Scanner page states (GHO-53)
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import { Code, ConnectError, createRouterTransport } from '@connectrpc/connect';
import { ScannerService } from '../../gen/openmanet/scanner/v1/scanner_service_pb.js';
import {
  AircraftSource,
  ControlChannelState,
  PositionSource,
  ReceiverHardware,
  ReceiverRole,
  ReceiverState,
  SiteEncryption,
  SiteSelectionMode,
  SiteSelectionReason,
} from '../../gen/openmanet/scanner/v1/scanner_pb.js';

vi.mock('../../services/connectClient.js', () => ({ transport: {} }));

const nowTs = () => ({ seconds: BigInt(Math.floor(Date.now() / 1000)), nanos: 0 });

const ENABLED_STATUS = () => ({
  enabled: true,
  receivers: [
    { role: ReceiverRole.ADSB_1090, serial: '1090', present: true, hardware: ReceiverHardware.RTL_SDR,
      owner: 'dump1090', state: ReceiverState.RUNNING, lastSampleTime: nowTs() },
    { role: ReceiverRole.UAT_978, serial: '978', present: false, owner: 'dump978-fa', state: ReceiverState.MISSING,
      detail: 'no receiver with serial "978" plugged in' },
    { role: ReceiverRole.WIDE, serial: '44a061dc2e395e8f', present: true, hardware: ReceiverHardware.AIRSPY,
      owner: 'openmanetd', state: ReceiverState.RUNNING },
  ],
  position: { source: PositionSource.GPSD, latitude: 40.7608, longitude: -111.891, stale: false },
  selectedSite: {
    site: { key: 'UT-1', name: 'Nelson Peak [1-010]', system: 'Utah Communications Authority', county: 'Salt Lake',
      state: 'UT', distanceKm: 8.2, encryption: SiteEncryption.PARTIAL },
    mode: SiteSelectionMode.AUTO,
    reason: SiteSelectionReason.NEAREST,
    controlChannelHz: 770006250n,
    wideTuner: { centerHz: 770500000n, usableBandwidthHz: 8000000n, fits: true, uncoveredHz: [] },
  },
  controlChannel: { state: ControlChannelState.LOCKED, frequencyHz: 770006250n, nac: 0x3a1, wacn: 0xbee00, systemId: 0x3a1 },
  lastDecodeTime: nowTs(),
});

const AIRCRAFT = () => ({
  enabled: true,
  aircraft: [
    { icao: 'a1b2c3', callsign: 'UAL123', squawk: '1200', altitudeFt: 35000, groundSpeedKt: 450, trackDeg: 270,
      latitude: 40.7, longitude: -111.9, source: AircraftSource.BOTH, lastSeen: nowTs() },
  ],
  feeds: [],
});

const ACTIVITY = () => ({
  enabled: true,
  historyCapacity: 128,
  grants: [
    { talkgroup: 101, sourceUnit: 1234567, frequencyHz: 770500000n, emergency: true, grants: 2, startTime: nowTs(), lastTime: nowTs() },
    { talkgroup: 202, frequencyHz: 771000000n, encrypted: true, grants: 1, startTime: nowTs(), lastTime: nowTs() },
  ],
});

const CONFIG = () => ({ config: { enabled: true, mode: SiteSelectionMode.AUTO, hysteresisKm: 5, states: [] } });

let handlers;
let setCalls;

function defaultHandlers() {
  return {
    status: ENABLED_STATUS,
    aircraft: AIRCRAFT,
    activity: ACTIVITY,
    config: CONFIG,
  };
}

async function renderScanner(overrides = {}) {
  handlers = { ...defaultHandlers(), ...overrides };
  vi.resetModules();
  const transport = createRouterTransport(({ service }) => {
    service(ScannerService, {
      getScannerStatus: () => handlers.status(),
      listAircraft: () => handlers.aircraft(),
      listTalkgroupActivity: () => handlers.activity(),
      getScannerConfig: () => handlers.config(),
      setScannerConfig: (req) => {
        setCalls.push(req.config);
        return { config: { ...req.config } };
      },
    });
  });
  vi.doMock('../../services/connectClient.js', () => ({ transport }));
  const mod = await import('../../pages/Scanner.jsx');
  return { ...render(<mod.default />), mod };
}

beforeEach(() => {
  vi.resetModules();
  setCalls = [];
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.restoreAllMocks();
  vi.resetModules();
});

describe('TestScannerPage', () => {
  it('shows a loading state first', async () => {
    let release;
    await renderScanner({ status: () => new Promise((r) => { release = r; }) });
    expect(screen.getByText('Loading…')).toBeInTheDocument();
    await waitFor(() => expect(release).toBeDefined());
    release(ENABLED_STATUS());
    await waitFor(() => screen.getByText('Nelson Peak [1-010]'));
  });

  it('renders site, control channel, receivers, aircraft and talkgroups', async () => {
    const { container } = await renderScanner();
    await waitFor(() => screen.getByText('Nelson Peak [1-010]'));

    expect(screen.getByRole('heading', { name: 'Scanner' })).toBeInTheDocument();
    expect(screen.getByText('8.2 km')).toBeInTheDocument();
    expect(screen.getByText('Nearest to fix')).toBeInTheDocument();
    expect(screen.getAllByText('770.00625 MHz').length).toBeGreaterThan(0);
    expect(screen.getByText('CC LOCKED')).toHaveClass('lat-chip', 'ok');
    expect(screen.getByText('3A1')).toBeInTheDocument();

    // Receivers carry status chips; MISSING pairs the crit color with a label.
    expect(screen.getAllByText('MISSING')[0]).toHaveClass('lat-chip', 'crit');
    expect(screen.getAllByText('Airspy').length).toBeGreaterThan(0);

    expect(screen.getAllByText('UAL123').length).toBeGreaterThan(0);
    expect(screen.getAllByText('35,000 ft').length).toBeGreaterThan(0);
    expect(screen.getByText('1 AIRCRAFT')).toBeInTheDocument();

    expect(screen.getAllByText('EMERGENCY')[0]).toHaveClass('lat-chip', 'crit');
    expect(screen.getAllByText('ENCRYPTED')[0]).toHaveClass('lat-chip', 'warn');

    expect(container.querySelectorAll('.table-scroll').length).toBe(3);
    expect(container.querySelector('.lat-alert')).toBeNull();
  });

  it('shows the disabled state', async () => {
    await renderScanner({
      status: () => ({ enabled: false }),
      aircraft: () => ({ enabled: false }),
      activity: () => ({ enabled: false }),
      config: () => ({ config: { enabled: false, mode: SiteSelectionMode.AUTO } }),
    });
    await waitFor(() => screen.getByText(/Scanner disabled/));
    expect(screen.getByText('OFF', { selector: '.lat-chip' })).toBeInTheDocument();
    expect(screen.queryByText('Receivers')).toBeNull();
  });

  it('shows explicit stale and unavailable alerts and empty tables', async () => {
    await renderScanner({
      status: () => ({
        enabled: true,
        receivers: [],
        position: { source: PositionSource.NONE, stale: true },
        controlChannel: { state: ControlChannelState.UNAVAILABLE },
        positionStale: true,
        siteUnavailable: true,
        decoderUnavailable: true,
        decodeStale: true,
        aircraftStale: true,
      }),
      aircraft: () => ({ enabled: true, aircraft: [] }),
      activity: () => ({ enabled: true, grants: [] }),
    });
    await waitFor(() => screen.getByText('No site selected.'));

    expect(screen.getByText('Position unavailable: no GPS fix.')).toHaveClass('lat-alert', 'warn');
    expect(screen.getByText(/No P25 site selected/)).toHaveClass('lat-alert', 'crit');
    expect(screen.getByText(/decoder not available/)).toBeInTheDocument();
    expect(screen.getByText(/Control channel stale/)).toBeInTheDocument();
    expect(screen.getByText(/Aircraft feeds stale/)).toBeInTheDocument();
    expect(screen.getByText('No aircraft.')).toBeInTheDocument();
    expect(screen.getByText('No talkgroup activity.')).toBeInTheDocument();
    expect(screen.getByText('No receivers.')).toBeInTheDocument();
    expect(screen.getByText('CC NO DECODER')).toHaveClass('lat-chip', 'warn');
  });

  it('shows an error when the first status fails', async () => {
    await renderScanner({ status: () => { throw new ConnectError('backend down', Code.Unavailable); } });
    await waitFor(() => screen.getByText(/Scanner status unavailable/));
    expect(screen.getByText('Scanner status unavailable: backend down')).toHaveClass('lat-alert', 'crit');
  });

  it('keeps the last good data when a later poll fails', async () => {
    let fail = false;
    await renderScanner({
      status: () => {
        if (fail) throw new ConnectError('timeout', Code.DeadlineExceeded);
        return ENABLED_STATUS();
      },
    });
    await waitFor(() => screen.getByText('Nelson Peak [1-010]'));

    fail = true;
    fireEvent.click(screen.getByText('REFRESH'));
    await waitFor(() => screen.getByText('Last refresh failed: timeout'));
    expect(screen.getByText('Nelson Peak [1-010]')).toBeInTheDocument();
  });

  it('toggles the scanner through SetScannerConfig', async () => {
    await renderScanner();
    await waitFor(() => screen.getByText('Nelson Peak [1-010]'));

    const toggle = screen.getByRole('switch', { name: 'Enable scanner' });
    await waitFor(() => expect(toggle).not.toBeDisabled());
    expect(toggle).toHaveAttribute('aria-checked', 'true');

    fireEvent.click(toggle);
    await waitFor(() => expect(setCalls).toHaveLength(1));
    expect(setCalls[0].enabled).toBe(false);
    expect(setCalls[0].mode).toBe(SiteSelectionMode.AUTO);
    await waitFor(() => expect(toggle).toHaveAttribute('aria-checked', 'false'));
  });

  it('shows a config error', async () => {
    await renderScanner({ config: () => { throw new ConnectError('denied', Code.PermissionDenied); } });
    await waitFor(() => screen.getByText('Scanner config: denied'));
    expect(screen.getByRole('switch', { name: 'Enable scanner' })).toBeDisabled();
  });
});
