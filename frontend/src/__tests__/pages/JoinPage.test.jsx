// =============================================================================
// JoinPage.test.jsx — public phone setup page
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup } from '@testing-library/react';
import JoinPage from '../../pages/JoinPage.jsx';

const ANDROID_UA = 'Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/129';
const IPHONE_UA = 'Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X)';

function status(overrides = {}) {
  return {
    node: 'node-7',
    apk: { available: true, name: 'ATAK.apk', size: 215_000_000, sha256: 'ab', uploaded: '2026-11-07T01:00:00Z' },
    apkUrl: 'http://10.41.3.7:8080/onboard/atak.apk',
    packageUrl: 'http://10.41.3.7:8080/onboard/atak-setup.zip',
    teams: ['Cyan', 'Red'],
    roles: ['Team Member', 'Medic'],
    mapSources: ['Manet Satellite', 'Manet Topo'],
    ...overrides,
  };
}

function setUA(ua) {
  vi.spyOn(window.navigator, 'userAgent', 'get').mockReturnValue(ua);
}

function serve(body, ok = true) {
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok, status: ok ? 200 : 500, json: async () => body })));
}

describe('JoinPage', () => {
  beforeEach(() => {
    localStorage.clear();
    setUA(ANDROID_UA);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('shows loading, then the download link with the APK size', async () => {
    serve(status());
    render(<JoinPage />);
    expect(screen.getByText('Loading…')).toBeInTheDocument();

    const link = await screen.findByRole('link', { name: /Download ATAK · 215 MB/ });
    expect(link).toHaveAttribute('href', 'http://10.41.3.7:8080/onboard/atak.apk');
    expect(screen.getByText(/OpenMANET · node-7/)).toBeInTheDocument();
    expect(screen.getByText(/turn off Auto Blocker/)).toBeInTheDocument();
  });

  it('builds the tak:// import link from the profile and remembers it', async () => {
    serve(status());
    render(<JoinPage />);

    const input = await screen.findByLabelText('Callsign');
    fireEvent.change(input, { target: { value: 'Bravo 6' } });

    const setup = screen.getByRole('link', { name: 'Set up ATAK' });
    const href = setup.getAttribute('href');
    expect(href.startsWith('tak://com.atakmap.app/import?url=')).toBe(true);
    const pkg = new URL(decodeURIComponent(href.split('url=')[1]));
    expect(pkg.origin + pkg.pathname).toBe('http://10.41.3.7:8080/onboard/atak-setup.zip');
    expect(pkg.searchParams.get('callsign')).toBe('Bravo 6');

    expect(screen.getByRole('link', { name: /settings file instead/ })).toHaveAttribute('href', pkg.toString());
    expect(JSON.parse(localStorage.getItem('openmanetd.join.profile')).callsign).toBe('Bravo 6');
    expect(screen.getByText(/Manet Satellite and Manet Topo maps/)).toBeInTheDocument();
  });

  it('disables setup for an invalid callsign', async () => {
    serve(status());
    render(<JoinPage />);

    fireEvent.change(await screen.findByLabelText('Callsign'), { target: { value: '<b>' } });
    expect(screen.getByRole('button', { name: 'Set up ATAK' })).toBeDisabled();
    expect(screen.queryByRole('link', { name: 'Set up ATAK' })).toBeNull();
    expect(screen.getByText(/Letters, digits/)).toBeInTheDocument();
  });

  it('explains when the node has no APK', async () => {
    serve(status({ apk: { available: false } }));
    render(<JoinPage />);

    expect(await screen.findByText(/no ATAK file yet/)).toBeInTheDocument();
    expect(screen.queryByRole('link', { name: /Download ATAK/ })).toBeNull();
    expect(screen.getByRole('link', { name: 'Set up ATAK' })).toBeInTheDocument();
  });

  it('tells iPhone users ATAK is Android only and hides the steps', async () => {
    setUA(IPHONE_UA);
    serve(status());
    render(<JoinPage />);

    expect(await screen.findByText(/ATAK is Android only/)).toBeInTheDocument();
    expect(screen.queryByText('1 · Install ATAK')).toBeNull();
  });

  it('offers the address for Chrome inside the Wi-Fi sign-in window', async () => {
    setUA('Mozilla/5.0 (Linux; Android 14; SM-G556B Build/UP1A; wv) AppleWebKit/537.36 Chrome/130 Mobile Safari/537.36');
    serve(status());
    render(<JoinPage />);

    expect(await screen.findByText(/sign-in window/)).toBeInTheDocument();
    expect(screen.getByText('http://10.41.3.7:8080/join')).toBeInTheDocument();
  });

  it('shows no sign-in window notice in Chrome', async () => {
    serve(status());
    render(<JoinPage />);

    await screen.findByText('1 · Install ATAK');
    expect(screen.queryByText(/sign-in window/)).toBeNull();
  });

  it('shows an error when the node cannot be reached', async () => {
    serve(null, false);
    render(<JoinPage />);

    expect(await screen.findByText(/Cannot reach this node/)).toBeInTheDocument();
  });

  it('restores a saved profile', async () => {
    localStorage.setItem('openmanetd.join.profile', JSON.stringify({ callsign: 'Echo', team: 'Red', role: '' }));
    serve(status());
    render(<JoinPage />);

    expect(await screen.findByLabelText('Callsign')).toHaveValue('Echo');
    const href = screen.getByRole('link', { name: 'Set up ATAK' }).getAttribute('href');
    expect(decodeURIComponent(href)).toContain('team=Red');
  });
});
