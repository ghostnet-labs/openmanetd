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
  AudioEncoding,
  ControlChannelState,
  ConventionalCategory,
  ConventionalMode,
  ConventionalScanState,
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

const CONVENTIONAL = () => ({
  enabled: true,
  radiusKm: 80,
  status: { state: ConventionalScanState.HOLDING, centerHz: 120000000n, window: 1, windows: 3, sampleRate: 2048000 },
  channels: [
    { key: 'C-0000aaaa', label: 'SLC Tower', frequencyHz: 118300000n, mode: ConventionalMode.AM,
      category: ConventionalCategory.AVIATION, distanceKm: 6.1, window: 1, active: true, lastHeardTime: nowTs() },
    { key: 'C-0000bbbb', label: 'Guard', frequencyHz: 121500000n, mode: ConventionalMode.AM,
      category: ConventionalCategory.AVIATION, alwaysOn: true, window: 1 },
  ],
});

const CONFIG = () => ({ config: { enabled: true, mode: SiteSelectionMode.AUTO, hysteresisKm: 5, states: [] } });

let handlers;
let setCalls;
let audioCalls;

function defaultHandlers() {
  return {
    status: ENABLED_STATUS,
    aircraft: AIRCRAFT,
    activity: ACTIVITY,
    config: CONFIG,
    conventional: CONVENTIONAL,
    audio: async function* audio() {},
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
      listConventionalChannels: () => handlers.conventional(),
      streamAudio: (req, ctx) => {
        audioCalls.push(req);
        return handlers.audio(req, ctx);
      },
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
  audioCalls = [];
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

    expect(container.querySelectorAll('.table-scroll').length).toBe(4);
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

  it('shows the decoder cause as a warning when unavailable and as a failure on error', async () => {
    await renderScanner({
      status: () => ({
        ...ENABLED_STATUS(),
        decoderUnavailable: true,
        controlChannel: { state: ControlChannelState.UNAVAILABLE, detail: 'capture tool rtl_sdr: executable file not found' },
      }),
    });
    await waitFor(() => screen.getByText(/capture tool rtl_sdr/));
    expect(screen.getByText(/capture tool rtl_sdr/)).toHaveClass('lat-alert', 'warn');
    expect(screen.getByText('CC NO DECODER')).toHaveClass('lat-chip', 'warn');
    cleanup();

    await renderScanner({
      status: () => ({
        ...ENABLED_STATUS(),
        controlChannel: { state: ControlChannelState.ERROR, detail: 'source: capture process exited' },
      }),
    });
    await waitFor(() => screen.getByText('source: capture process exited'));
    expect(screen.getByText('source: capture process exited')).toHaveClass('lat-alert', 'crit');
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

function stubAudio() {
  const ctxs = [];
  const AudioContext = vi.fn(function MockAudioContext() {
    this.state = 'running';
    this.currentTime = 0;
    this.destination = {};
    this.close = vi.fn(() => Promise.resolve());
    this.createBuffer = vi.fn((c, n) => {
      const d = new Float32Array(n);
      return { getChannelData: () => d };
    });
    this.createBufferSource = vi.fn(() => ({ connect: vi.fn(), start: vi.fn() }));
    ctxs.push(this);
  });
  vi.stubGlobal('AudioContext', AudioContext);
  return ctxs;
}

describe('TestScannerConventionalPanel', () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('lists nearby channels with live and last-heard state', async () => {
    await renderScanner();
    await waitFor(() => screen.getAllByText('SLC Tower'));

    expect(screen.getByRole('heading', { name: 'Conventional' })).toBeInTheDocument();
    expect(screen.getAllByText('LIVE')[0].closest('.lat-chip')).not.toBeNull();
    expect(screen.getAllByText('QUIET').length).toBeGreaterThan(0);
    expect(screen.getAllByText('NATIONWIDE').length).toBeGreaterThan(0);
    expect(screen.getAllByText('never').length).toBeGreaterThan(0);
    expect(screen.getByText('HOLDING')).toHaveClass('v', 'ok');
    expect(screen.getByText('1 / 3')).toBeInTheDocument();
    expect(screen.getByText('80 km')).toBeInTheDocument();
  });

  it('shows the empty state and a receiver-busy cause', async () => {
    await renderScanner({
      conventional: () => ({
        enabled: true,
        status: { state: ConventionalScanState.RECEIVER_BUSY, detail: 'scan receiver decodes the P25 control channel' },
        channels: [],
      }),
    });
    await waitFor(() => screen.getByText('No conventional channels nearby.'));
    expect(screen.getByText('RX BUSY')).toHaveClass('v', 'warn');
    expect(screen.getByText(/decodes the P25 control channel/)).toHaveClass('lat-alert', 'warn');
  });

  it('shows an error when the channel list fails', async () => {
    await renderScanner({ conventional: () => { throw new ConnectError('no table', Code.Internal); } });
    await waitFor(() => screen.getByText('Conventional channels unavailable: no table'));
    expect(screen.getByText('Conventional channels unavailable: no table')).toHaveClass('lat-alert', 'crit');
    // The other panels still render.
    expect(screen.getByText('Nelson Peak [1-010]')).toBeInTheDocument();
  });

  it('plays a channel stream through WebAudio and stops on STOP', async () => {
    const ctxs = stubAudio();
    let aborted = false;
    let pushed = 0;
    await renderScanner({
      audio: async function* audio(_req, ctx) {
        ctx.signal.addEventListener('abort', () => { aborted = true; });
        yield { encoding: AudioEncoding.PCM_S16LE, sampleRate: 8000 };
        yield { data: new Uint8Array(320), encoding: AudioEncoding.PCM_S16LE, sampleRate: 8000, sequence: 1 };
        pushed++;
        await new Promise((resolve) => ctx.signal.addEventListener('abort', resolve));
      },
    });
    await waitFor(() => screen.getAllByText('SLC Tower'));

    const listen = screen.getAllByRole('button', { name: 'Listen to SLC Tower 118.3 MHz' })[0];
    fireEvent.click(listen);

    await waitFor(() => expect(audioCalls).toHaveLength(1));
    expect(audioCalls[0].channelKey).toBe('C-0000aaaa');
    expect(audioCalls[0].encoding).toBe(AudioEncoding.PCM_S16LE);
    await waitFor(() => expect(pushed).toBe(1));
    await waitFor(() => expect(ctxs[0].createBufferSource).toHaveBeenCalledTimes(1));
    expect(screen.getByText('AUDIO')).toBeInTheDocument();

    const stop = screen.getAllByRole('button', { name: 'Stop listening to SLC Tower 118.3 MHz' })[0];
    expect(stop).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(stop);

    await waitFor(() => expect(aborted).toBe(true));
    expect(ctxs[0].close).toHaveBeenCalled();
    expect(screen.queryByText('AUDIO')).toBeNull();
  });

  it('switches channels by closing the previous stream', async () => {
    const ctxs = stubAudio();
    await renderScanner({
      audio: async function* audio(_req, ctx) {
        yield { encoding: AudioEncoding.PCM_S16LE, sampleRate: 8000 };
        await new Promise((resolve) => ctx.signal.addEventListener('abort', resolve));
      },
    });
    await waitFor(() => screen.getAllByText('SLC Tower'));

    fireEvent.click(screen.getAllByRole('button', { name: /^Listen to SLC Tower/ })[0]);
    await waitFor(() => expect(audioCalls).toHaveLength(1));
    fireEvent.click(screen.getAllByRole('button', { name: /^Listen to Guard/ })[0]);
    await waitFor(() => expect(audioCalls).toHaveLength(2));
    expect(audioCalls[1].channelKey).toBe('C-0000bbbb');
    expect(ctxs[0].close).toHaveBeenCalled();
    expect(ctxs[1].close).not.toHaveBeenCalled();
  });

  it('shows a stream error and returns the button to LISTEN', async () => {
    stubAudio();
    await renderScanner({
      audio: async function* audio() {
        yield { encoding: AudioEncoding.PCM_S16LE, sampleRate: 8000 };
        throw new ConnectError('too many scanner audio streams', Code.ResourceExhausted);
      },
    });
    await waitFor(() => screen.getAllByText('SLC Tower'));

    fireEvent.click(screen.getAllByRole('button', { name: /^Listen to SLC Tower/ })[0]);
    await waitFor(() => screen.getByText('Audio: too many scanner audio streams'));
    expect(screen.getByText('Audio: too many scanner audio streams')).toHaveClass('lat-alert', 'crit');
    expect(screen.getAllByRole('button', { name: /^Listen to SLC Tower/ })[0]).toHaveAttribute('aria-pressed', 'false');
  });

  it('closes the stream on unmount', async () => {
    const ctxs = stubAudio();
    let aborted = false;
    const { unmount } = await renderScanner({
      audio: async function* audio(_req, ctx) {
        ctx.signal.addEventListener('abort', () => { aborted = true; });
        yield { encoding: AudioEncoding.PCM_S16LE, sampleRate: 8000 };
        await new Promise((resolve) => ctx.signal.addEventListener('abort', resolve));
      },
    });
    await waitFor(() => screen.getAllByText('SLC Tower'));
    fireEvent.click(screen.getAllByRole('button', { name: /^Listen to SLC Tower/ })[0]);
    await waitFor(() => expect(audioCalls).toHaveLength(1));

    unmount();
    await waitFor(() => expect(aborted).toBe(true));
    expect(ctxs[0].close).toHaveBeenCalled();
  });

  it('reports missing WebAudio', async () => {
    vi.stubGlobal('AudioContext', undefined);
    vi.stubGlobal('webkitAudioContext', undefined);
    await renderScanner();
    await waitFor(() => screen.getAllByText('SLC Tower'));
    fireEvent.click(screen.getAllByRole('button', { name: /^Listen to SLC Tower/ })[0]);
    await waitFor(() => screen.getByText(/Audio: WebAudio not supported/));
    expect(audioCalls).toHaveLength(0);
  });
});
