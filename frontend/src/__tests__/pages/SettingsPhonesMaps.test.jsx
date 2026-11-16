// =============================================================================
// SettingsPhonesMaps.test.jsx — offline maps panel
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react';

const api = vi.hoisted(() => ({
  fetchTilesStatus: vi.fn(),
  startTileSeed: vi.fn(),
  cancelTileSeed: vi.fn(),
}));

const gnss = vi.hoisted(() => ({ value: null }));

vi.mock('../../services/tilesApi.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchTilesStatus: api.fetchTilesStatus,
  startTileSeed: api.startTileSeed,
  cancelTileSeed: api.cancelTileSeed,
}));

vi.mock('../../hooks/useGnssStatus.js', () => ({
  useGnssStatus: () => gnss.value,
}));

import SettingsPhonesMaps from '../../pages/SettingsPhonesMaps.jsx';
import { seedSummary } from '../../services/tilesApi.js';

function status(overrides = {}) {
  return {
    enabled: true,
    dir: '/srv/openmanet/tiles',
    usedBytes: 128_000_000,
    maxBytes: 512_000_000,
    freeBytes: 2_000_000_000,
    minFreeBytes: 64_000_000,
    layers: [
      { layer: 'satellite', tileType: 'jpg', minZoom: 0, maxZoom: 16, internet: true },
      { layer: 'topo', tileType: 'jpg', minZoom: 0, maxZoom: 16, internet: true },
    ],
    peers: [],
    internet: { name: 'internet', health: 'ok' },
    counters: { hits: 10, misses: 2, fromPeer: 0, fromInternet: 2, notCached: 0 },
    ...overrides,
  };
}

const SEED = { running: true, total: 2000, done: 500, cached: 0, downloaded: 500, failed: 0, bytes: 10_000_000, areas: ['x'], source: 'manual' };

describe('SettingsPhonesMaps', () => {
  beforeEach(() => {
    api.fetchTilesStatus.mockReset();
    api.startTileSeed.mockReset();
    api.cancelTileSeed.mockReset();
    gnss.value = null;
  });

  afterEach(() => cleanup());

  it('shows cache use, layers and internet health', async () => {
    api.fetchTilesStatus.mockResolvedValue(status());
    render(<SettingsPhonesMaps />);

    expect(await screen.findByText('128 MB of 512 MB')).toBeInTheDocument();
    expect(screen.getByText('25%')).toBeInTheDocument();
    expect(screen.getByText('satellite (to zoom 16), topo (to zoom 16)')).toBeInTheDocument();
    expect(screen.getByText('Reachable')).toBeInTheDocument();
    expect(screen.getByText('None set')).toBeInTheDocument();
  });

  it('says when tiles are off', async () => {
    api.fetchTilesStatus.mockResolvedValue({ enabled: false });
    render(<SettingsPhonesMaps />);
    expect(await screen.findByText('Map tiles are off on this node.')).toBeInTheDocument();
  });

  it('shows an error when status fails', async () => {
    api.fetchTilesStatus.mockRejectedValue(new Error('status failed (502)'));
    render(<SettingsPhonesMaps />);
    expect(await screen.findByText('Map status is unavailable (status failed (502)).')).toBeInTheDocument();
  });

  it('flags low disk and offline sources', async () => {
    api.fetchTilesStatus.mockResolvedValue(status({
      freeBytes: 10_000_000,
      internet: { name: 'internet', health: 'offline' },
      peers: [{ name: 'http://10.41.0.1:8080', health: 'ok' }],
    }));
    render(<SettingsPhonesMaps />);

    expect(await screen.findByText('10 MB (low, not caching)')).toBeInTheDocument();
    expect(screen.getByText('Offline')).toBeInTheDocument();
    expect(screen.getByText('10.41.0.1:8080 · Reachable')).toBeInTheDocument();
  });

  it('downloads an area from the form', async () => {
    api.fetchTilesStatus.mockResolvedValue(status());
    api.startTileSeed.mockResolvedValue(SEED);
    render(<SettingsPhonesMaps />);

    const button = await screen.findByRole('button', { name: 'Download area' });
    expect(button).toBeDisabled();

    fireEvent.change(screen.getByLabelText('Latitude'), { target: { value: '40.314' } });
    fireEvent.change(screen.getByLabelText('Longitude'), { target: { value: '-112.006' } });
    fireEvent.change(screen.getByLabelText('Detail (max zoom)'), { target: { value: '15' } });
    expect(screen.getByText(/^About [\d,]+ tiles/)).toBeInTheDocument();

    fireEvent.click(button);

    await waitFor(() => expect(api.startTileSeed).toHaveBeenCalledTimes(1));
    expect(api.startTileSeed.mock.calls[0][0]).toMatchObject({ lat: 40.314, lon: -112.006, radiusKm: 10, maxZoom: 15 });
    expect(await screen.findByText('Download started.')).toBeInTheDocument();
  });

  it('shows a rejected area', async () => {
    api.fetchTilesStatus.mockResolvedValue(status());
    api.startTileSeed.mockRejectedValue(new Error('area is too large for the tile cache'));
    render(<SettingsPhonesMaps />);

    fireEvent.change(await screen.findByLabelText('Latitude'), { target: { value: '40' } });
    fireEvent.change(screen.getByLabelText('Longitude'), { target: { value: '-112' } });
    fireEvent.click(screen.getByRole('button', { name: 'Download area' }));

    expect(await screen.findByText('area is too large for the tile cache')).toBeInTheDocument();
  });

  it('fills the area from the node GPS fix', async () => {
    gnss.value = { position: { latitude: 40.3141234, longitude: -112.0061234 } };
    api.fetchTilesStatus.mockResolvedValue(status());
    render(<SettingsPhonesMaps />);

    fireEvent.click(await screen.findByRole('button', { name: 'Use node position' }));
    expect(screen.getByLabelText('Latitude')).toHaveValue(40.31412);
    expect(screen.getByLabelText('Longitude')).toHaveValue(-112.00612);
  });

  it('disables the GPS button without a fix', async () => {
    gnss.value = { position: { latitude: 0, longitude: 0 } };
    api.fetchTilesStatus.mockResolvedValue(status());
    render(<SettingsPhonesMaps />);
    expect(await screen.findByRole('button', { name: 'No GPS fix' })).toBeDisabled();
  });

  it('shows progress and stops a running download', async () => {
    api.fetchTilesStatus.mockResolvedValue(status({ seed: SEED }));
    api.cancelTileSeed.mockResolvedValue();
    render(<SettingsPhonesMaps />);

    expect(await screen.findByText('500 of 2,000 tiles · 10 MB downloaded')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Stop download' }));
    await waitFor(() => expect(api.cancelTileSeed).toHaveBeenCalledTimes(1));
  });
});

describe('SeedSummary', () => {
  const base = { downloaded: 1200, cached: 34, failed: 0, error: '' };

  it('handles no job', () => {
    expect(seedSummary(null)).toBeNull();
  });

  it('reports success, stop, failure and gaps', () => {
    expect(seedSummary(base)).toEqual({ tone: 'ok', text: 'Area ready offline: 1,200 downloaded, 34 already stored.' });
    expect(seedSummary({ ...base, error: 'canceled' }).tone).toBe('warn');
    expect(seedSummary({ ...base, error: 'storage is full' })).toEqual({
      tone: 'crit',
      text: 'Download failed: storage is full (1,200 downloaded, 34 already stored).',
    });
    expect(seedSummary({ ...base, failed: 3 }).text).toContain('3 missing tiles');
  });
});
