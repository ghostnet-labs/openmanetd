// =============================================================================
// SettingsPhones.test.jsx — operator APK upload / remove
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, cleanup, waitFor } from '@testing-library/react';

const api = vi.hoisted(() => ({
  fetchOnboardStatus: vi.fn(),
  uploadAtakApk: vi.fn(),
  removeAtakApk: vi.fn(),
}));

vi.mock('../../services/onboardApi.js', async (importOriginal) => ({
  ...(await importOriginal()),
  fetchOnboardStatus: api.fetchOnboardStatus,
  uploadAtakApk: api.uploadAtakApk,
  removeAtakApk: api.removeAtakApk,
}));

import SettingsPhones from '../../pages/SettingsPhones.jsx';

function status(apk) {
  return {
    node: 'n1',
    apk,
    apkUrl: 'http://10.41.3.7:8080/onboard/atak.apk',
    packageUrl: 'http://10.41.3.7:8080/onboard/atak-setup.zip',
    teams: [],
    roles: [],
    mapSources: ['Manet Satellite'],
  };
}

const STORED = { available: true, name: 'ATAK-CIV.apk', size: 215_000_000, sha256: 'deadbeef', uploaded: '2026-11-07T01:30:00Z' };

describe('SettingsPhones', () => {
  beforeEach(() => {
    api.fetchOnboardStatus.mockReset();
    api.uploadAtakApk.mockReset();
    api.removeAtakApk.mockReset();
  });

  afterEach(() => cleanup());

  it('shows the empty state and the phone link', async () => {
    api.fetchOnboardStatus.mockResolvedValue(status({ available: false }));
    render(<SettingsPhones />);

    expect(await screen.findByText('No ATAK file on this node.')).toBeInTheDocument();
    expect(screen.getByText('http://10.41.3.7:8080/join')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Upload APK' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Remove' })).toBeNull();
  });

  it('shows the stored APK', async () => {
    api.fetchOnboardStatus.mockResolvedValue(status(STORED));
    render(<SettingsPhones />);

    expect(await screen.findByText('ATAK-CIV.apk')).toBeInTheDocument();
    expect(screen.getByText('215 MB')).toBeInTheDocument();
    expect(screen.getByText('2026-11-07 01:30')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Replace APK' })).toBeInTheDocument();
  });

  it('uploads a picked file and reloads', async () => {
    api.fetchOnboardStatus
      .mockResolvedValueOnce(status({ available: false }))
      .mockResolvedValueOnce(status(STORED));
    api.uploadAtakApk.mockResolvedValue({ name: 'ATAK-CIV.apk' });
    render(<SettingsPhones />);

    const input = await screen.findByLabelText('ATAK APK file');
    const file = new File(['apk'], 'ATAK-CIV.apk');
    fireEvent.change(input, { target: { files: [file] } });

    expect(await screen.findByText('Stored ATAK-CIV.apk.')).toBeInTheDocument();
    expect(api.uploadAtakApk).toHaveBeenCalledWith(file, expect.objectContaining({ onProgress: expect.any(Function) }));
    await waitFor(() => expect(screen.getByText('215 MB')).toBeInTheDocument());
  });

  it('shows an upload error', async () => {
    api.fetchOnboardStatus.mockResolvedValue(status({ available: false }));
    api.uploadAtakApk.mockRejectedValue(new Error('file is not an Android APK'));
    render(<SettingsPhones />);

    fireEvent.change(await screen.findByLabelText('ATAK APK file'), { target: { files: [new File(['x'], 'x.txt')] } });
    expect(await screen.findByText('file is not an Android APK')).toBeInTheDocument();
  });

  it('removes the APK', async () => {
    api.fetchOnboardStatus
      .mockResolvedValueOnce(status(STORED))
      .mockResolvedValueOnce(status({ available: false }));
    api.removeAtakApk.mockResolvedValue();
    render(<SettingsPhones />);

    fireEvent.click(await screen.findByRole('button', { name: 'Remove' }));
    expect(await screen.findByText('ATAK file removed.')).toBeInTheDocument();
    expect(await screen.findByText('No ATAK file on this node.')).toBeInTheDocument();
  });

  it('shows an error when status fails', async () => {
    api.fetchOnboardStatus.mockRejectedValue(new Error('status 404'));
    render(<SettingsPhones />);

    expect(await screen.findByText(/Phone onboarding is unavailable \(status 404\)/)).toBeInTheDocument();
  });
});
