// =============================================================================
// Settings.test.jsx — Tests for device settings page
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';

// Mock the ConnectRPC transport and dashboard client used by Settings.jsx
// for the restart button. vi.hoisted ensures the fn is available when
// vi.mock factories execute (they are hoisted above imports).
const { mockExecuteQuickAction, mockGetCommsConfig, mockUpdateCommsConfig, mockChangePassword, authEnabledRef } = vi.hoisted(() => ({
  mockExecuteQuickAction: vi.fn().mockResolvedValue({ success: true, message: '' }),
  mockGetCommsConfig: vi.fn().mockResolvedValue({ commsEnabled: true, controlSource: 3 }),
  mockUpdateCommsConfig: vi.fn().mockResolvedValue({}),
  mockChangePassword: vi.fn().mockResolvedValue(undefined),
  authEnabledRef: { current: true },
}));
// Keep the real ConnectError / Code exports: useDeviceReboot classifies
// reboot failures with them.
vi.mock('@connectrpc/connect', async (importOriginal) => ({
  ...(await importOriginal()),
  createClient: () => ({
    executeQuickAction: mockExecuteQuickAction,
    getCommsConfig: mockGetCommsConfig,
    updateCommsConfig: mockUpdateCommsConfig,
  }),
}));
vi.mock('../../services/connectClient.js', () => ({ transport: {} }));
vi.mock('../../contexts/useAuth.js', () => ({
  useAuth: () => ({
    changePassword: mockChangePassword,
    authEnabled: authEnabledRef.current,
    user: 'root',
    isAuthenticated: true,
    logout: vi.fn(),
  }),
}));

import { Code, ConnectError } from '@connectrpc/connect';
import { QuickAction } from '../../gen/openmanet/dashboard/v1/dashboard_pb.js';
import SettingsPage from '../../pages/Settings.jsx';

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  mockExecuteQuickAction.mockReset().mockResolvedValue({ success: true, message: '' });
  mockGetCommsConfig.mockReset().mockResolvedValue({ commsEnabled: true, controlSource: 3 });
  mockUpdateCommsConfig.mockReset().mockResolvedValue({});
  mockChangePassword.mockReset().mockResolvedValue(undefined);
  authEnabledRef.current = true;
});

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const HOSTNAME_RESPONSE = { hostname: 'my-device' };
const CONFIG_RESPONSE = {
  comms: {
    enable: true,
    controlSource: 'web',
    debug: false,
    talkgroups: 5,
  },
  network: { mesh: true },
};

function mockFetchSuccess(hostnameRes = HOSTNAME_RESPONSE, configRes = CONFIG_RESPONSE) {
  vi.stubGlobal('fetch', vi.fn((url, opts) => {
    if (opts?.method === 'POST') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    }
    if (url === '/api/settings/hostname') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(hostnameRes) });
    }
    if (url === '/api/settings/config') {
      return Promise.resolve({ ok: true, json: () => Promise.resolve(configRes) });
    }
    return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
  }));
}

/** Click the first Lattice toggle switch (Comms Radio is rendered first). */
function clickCommsToggle(container) {
  const toggle = container.querySelector('.lat-toggle');
  fireEvent.click(toggle);
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe('TestSettingsLoading', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})));
  });


  it('renders loading spinner', () => {
    render(<SettingsPage />);
    expect(screen.getByText('Loading settings...')).toBeTruthy();
  });
});

describe('TestSettingsFetchSuccess', () => {
  beforeEach(() => mockFetchSuccess());


  it('populates hostname and config after fetch', async () => {
    render(<SettingsPage />);
    await waitFor(() => {
      expect(screen.getByDisplayValue('my-device')).toBeTruthy();
    });
    expect(screen.getByText('OpenMANETd Configuration')).toBeTruthy();
    expect(screen.getByText('Enabled')).toBeTruthy();
  });
});

describe('TestSettingsPartialFetchFailure', () => {
  beforeEach(() => {
    vi.stubGlobal('fetch', vi.fn((url) => {
      if (url === '/api/settings/hostname') {
        return Promise.resolve({ ok: false, status: 404 });
      }
      if (url === '/api/settings/config') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(CONFIG_RESPONSE) });
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    }));
  });


  it('still renders config when hostname fails', async () => {
    render(<SettingsPage />);
    await waitFor(() => {
      expect(screen.getByText('OpenMANETd Configuration')).toBeTruthy();
    });
    // Hostname defaults to empty
    expect(screen.getByPlaceholderText('device-hostname').value).toBe('');
  });
});

describe('TestSettingsHostnameDirty', () => {
  beforeEach(() => mockFetchSuccess());


  it('enables save button when hostname changes', async () => {
    render(<SettingsPage />);
    await waitFor(() => screen.getByDisplayValue('my-device'));

    const saveBtn = screen.getAllByRole('button').find(b => b.textContent === 'Save');
    expect(saveBtn.disabled).toBe(true);

    fireEvent.change(screen.getByDisplayValue('my-device'), {
      target: { value: 'new-host' },
    });
    expect(saveBtn.disabled).toBe(false);
  });
});

describe('TestSettingsConfigDirty', () => {
  beforeEach(() => mockFetchSuccess());


  it('shows unsaved changes when config toggled', async () => {
    const { container } = render(<SettingsPage />);
    await waitFor(() => screen.getByText('Enabled'));

    const saveCfgBtn = screen.getByText('Save Configuration');
    expect(saveCfgBtn.disabled).toBe(true);

    // Click the toggle switch div
    clickCommsToggle(container);

    expect(screen.getByText('Unsaved changes')).toBeTruthy();
    expect(saveCfgBtn.disabled).toBe(false);
  });
});

describe('TestSettingsSaveHostname', () => {


  it('shows success message on save', async () => {
    mockFetchSuccess();
    render(<SettingsPage />);
    await waitFor(() => screen.getByDisplayValue('my-device'));

    fireEvent.change(screen.getByDisplayValue('my-device'), {
      target: { value: 'new-host' },
    });
    const saveBtn = screen.getAllByRole('button').find(b => b.textContent === 'Save');
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(screen.getByText('Hostname saved.')).toBeTruthy();
    });
  });

  it('shows error on save failure', async () => {
    vi.stubGlobal('fetch', vi.fn((url, opts) => {
      if (opts?.method === 'POST') {
        return Promise.resolve({ ok: false, status: 500 });
      }
      if (url === '/api/settings/hostname') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(HOSTNAME_RESPONSE) });
      }
      if (url === '/api/settings/config') {
        return Promise.resolve({ ok: true, json: () => Promise.resolve(CONFIG_RESPONSE) });
      }
      return Promise.resolve({ ok: true, json: () => Promise.resolve({}) });
    }));

    render(<SettingsPage />);
    await waitFor(() => screen.getByDisplayValue('my-device'));

    fireEvent.change(screen.getByDisplayValue('my-device'), {
      target: { value: 'new-host' },
    });
    const saveBtn = screen.getAllByRole('button').find(b => b.textContent === 'Save');
    fireEvent.click(saveBtn);

    await waitFor(() => {
      expect(screen.getByText(/Failed to save hostname/)).toBeTruthy();
    });
  });
});

describe('TestSettingsSaveConfig', () => {


  it('shows success on config save', async () => {
    mockFetchSuccess();
    const { container } = render(<SettingsPage />);
    await waitFor(() => screen.getByText('Enabled'));

    clickCommsToggle(container);
    fireEvent.click(screen.getByText('Save Configuration'));

    await waitFor(() => {
      expect(screen.getByText('Configuration saved.')).toBeTruthy();
    });
  });

  it('shows error on config save failure', async () => {
    mockFetchSuccess();
    mockUpdateCommsConfig.mockRejectedValueOnce(new Error('rpc unavailable'));

    const { container } = render(<SettingsPage />);
    await waitFor(() => screen.getByText('Enabled'));

    clickCommsToggle(container);
    fireEvent.click(screen.getByText('Save Configuration'));

    await waitFor(() => {
      expect(screen.getByText(/Failed to save config/)).toBeTruthy();
    });
  });

  it('calls updateCommsConfig when control source changes', async () => {
    mockFetchSuccess();
    render(<SettingsPage />);
    await waitFor(() => screen.getByText('OpenMANETd Configuration'));

    const trigger = screen.getByRole('button', { name: 'Control Source' });
    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole('option', { name: 'OpenVLM (default)' }));
    fireEvent.click(screen.getByText('Save Configuration'));

    await waitFor(() => {
      expect(screen.getByText('Configuration saved.')).toBeTruthy();
    });
    expect(mockUpdateCommsConfig).toHaveBeenCalledTimes(1);
    const arg = mockUpdateCommsConfig.mock.calls[0][0];
    expect(arg.enableComms).toBe(true);
    // ControlSource.OPENVLM === 1
    expect(arg.controlSource).toBe(1);
  });
});

describe('TestSettingsRestart', () => {
  beforeEach(() => mockFetchSuccess());

  it('shows restarting text and success message after click', async () => {
    render(<SettingsPage />);
    await waitFor(() => screen.getByText('Restart openmanetd'));

    fireEvent.click(screen.getByText('Restart openmanetd'));

    await waitFor(() => {
      expect(screen.getByText('openmanetd restart requested.')).toBeTruthy();
    });
    // Button shows "Restarting..." while disabled
    expect(screen.getByText('Restarting...')).toBeTruthy();
    // Verify ConnectRPC was called
    expect(mockExecuteQuickAction).toHaveBeenCalledTimes(1);
  });

  it('shows error when restart fails', async () => {
    mockExecuteQuickAction.mockResolvedValueOnce({ success: false, message: 'service unavailable' });

    render(<SettingsPage />);
    await waitFor(() => screen.getByText('Restart openmanetd'));

    fireEvent.click(screen.getByText('Restart openmanetd'));

    await waitFor(() => {
      expect(screen.getByText(/Failed to restart/)).toBeTruthy();
    });
  });
});

describe('TestSettingsReboot', () => {
  beforeEach(() => mockFetchSuccess());

  async function openConfirm() {
    render(<SettingsPage />);
    await waitFor(() => screen.getByText('Reboot device'));
    fireEvent.click(screen.getByText('Reboot device'));
    return screen.getByRole('alertdialog');
  }

  it('renders the reboot button next to restart with a 44px touch class', async () => {
    const { container } = render(<SettingsPage />);
    await waitFor(() => screen.getByText('Reboot device'));
    const actions = container.querySelector('.settings-service-actions');
    const buttons = actions.querySelectorAll('button');
    expect(buttons).toHaveLength(2);
    expect(buttons[0].textContent).toBe('Restart openmanetd');
    expect(buttons[1].textContent).toBe('Reboot device');
    expect(buttons[1].className).toContain('lat-btn danger solid settings-touch-btn');
  });

  it('asks for confirmation naming the hostname before rebooting', async () => {
    const dialog = await openConfirm();
    expect(dialog.textContent).toContain('Reboot my-device?');
    expect(dialog.textContent).toMatch(/about a minute/);
    expect(screen.getByRole('button', { name: 'Reboot my-device' }).className)
      .toContain('lat-btn danger filled');
    expect(mockExecuteQuickAction).not.toHaveBeenCalled();
  });

  it('cancel closes the confirmation without calling the backend', async () => {
    await openConfirm();
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(screen.queryByRole('alertdialog')).toBeNull();
    expect(mockExecuteQuickAction).not.toHaveBeenCalled();
  });

  it('sends REBOOT_DEVICE and shows the disconnect message on confirm', async () => {
    await openConfirm();
    fireEvent.click(screen.getByRole('button', { name: 'Reboot my-device' }));

    await waitFor(() => {
      expect(screen.getByText('Reboot accepted.')).toBeTruthy();
    });
    expect(mockExecuteQuickAction).toHaveBeenCalledWith({ action: QuickAction.REBOOT_DEVICE });
    expect(screen.getByRole('status').textContent).toMatch(/my-device is shutting down/);
    // Restart and reboot are locked while the reboot is in flight.
    expect(screen.getByText('Restart openmanetd').closest('button').disabled).toBe(true);
    expect(screen.getByText('Reboot device').closest('button').disabled).toBe(true);
  });

  it('shows reconnecting state once the device stops answering', async () => {
    await openConfirm();
    // From here on the device is down: every probe fails.
    vi.stubGlobal('fetch', vi.fn().mockRejectedValue(new TypeError('Failed to fetch')));
    fireEvent.click(screen.getByRole('button', { name: 'Reboot my-device' }));

    await waitFor(() => {
      expect(screen.getByText('Reconnecting…')).toBeTruthy();
    });
    expect(screen.getByRole('status').textContent).toMatch(/UI is disconnected/);
  });

  it('shows a crit alert with the cause when the reboot request fails', async () => {
    mockExecuteQuickAction.mockRejectedValueOnce(
      new ConnectError('action QUICK_ACTION_REBOOT_DEVICE failed: exit status 1', Code.Internal),
    );
    await openConfirm();
    fireEvent.click(screen.getByRole('button', { name: 'Reboot my-device' }));

    const alert = await screen.findByRole('alert');
    expect(alert.className).toContain('lat-alert crit');
    expect(alert.textContent).toContain('Reboot failed.');
    expect(alert.textContent).toContain('exit status 1');

    fireEvent.click(screen.getByRole('button', { name: 'Dismiss' }));
    expect(screen.queryByRole('alert')).toBeNull();
    expect(screen.getByText('Reboot device').closest('button').disabled).toBe(false);
  });

  it('shows a crit alert when the backend reports success=false', async () => {
    mockExecuteQuickAction.mockResolvedValueOnce({ success: false, message: 'reboot blocked' });
    await openConfirm();
    fireEvent.click(screen.getByRole('button', { name: 'Reboot my-device' }));

    const alert = await screen.findByRole('alert');
    expect(alert.className).toContain('lat-alert crit');
    expect(alert.textContent).toContain('reboot blocked');
  });
});

describe('TestSettingsRawYaml', () => {
  beforeEach(() => mockFetchSuccess());


  it('toggles raw YAML display', async () => {
    const { container } = render(<SettingsPage />);
    await waitFor(() => screen.getByText('OpenMANETd Configuration'));

    expect(container.querySelector('pre')).toBeNull();

    fireEvent.click(screen.getByText('Raw Configuration (YAML)'));
    expect(container.querySelector('pre')).toBeTruthy();

    fireEvent.click(screen.getByText('Raw Configuration (YAML)'));
    expect(container.querySelector('pre')).toBeNull();
  });
});

describe('TestSettingsControlSource', () => {
  beforeEach(() => mockFetchSuccess());


  it('changes control source dropdown', async () => {
    render(<SettingsPage />);
    await waitFor(() => screen.getByText('OpenMANETd Configuration'));

    const trigger = screen.getByRole('button', { name: 'Control Source' });
    expect(trigger.textContent).toContain('Web UI');

    fireEvent.click(trigger);
    fireEvent.click(screen.getByRole('option', { name: 'OpenVLM (default)' }));

    expect(trigger.textContent).toContain('OpenVLM (default)');
    expect(screen.getByText('Unsaved changes')).toBeTruthy();
  });
});

// ---------------------------------------------------------------------------
// Passphrase panel
// ---------------------------------------------------------------------------

describe('TestSettingsPassphrasePanel', () => {
  beforeEach(() => mockFetchSuccess());

  async function waitForPanel() {
    await waitFor(() => screen.getByText('Passphrase'));
  }

  it('renders the Passphrase panel when auth is enabled', async () => {
    render(<SettingsPage />);
    await waitForPanel();
    expect(screen.getByLabelText('Current Passphrase')).toBeTruthy();
    expect(screen.getByLabelText('New Passphrase')).toBeTruthy();
    expect(screen.getByLabelText('Confirm New Passphrase')).toBeTruthy();
    expect(screen.getByRole('button', { name: /Update Passphrase/i })).toBeTruthy();
  });

  it('hides the Passphrase panel when authEnabled is false', async () => {
    authEnabledRef.current = false;
    render(<SettingsPage />);
    await waitFor(() => screen.getByText('Hostname'));
    expect(screen.queryByText('Passphrase')).toBeNull();
  });

  it('submits current + new passphrase on success and clears fields', async () => {
    render(<SettingsPage />);
    await waitForPanel();

    fireEvent.change(screen.getByLabelText('Current Passphrase'), { target: { value: '' } });
    fireEvent.change(screen.getByLabelText('New Passphrase'), { target: { value: 'alpha123' } });
    fireEvent.change(screen.getByLabelText('Confirm New Passphrase'), { target: { value: 'alpha123' } });

    fireEvent.click(screen.getByRole('button', { name: /Update Passphrase/i }));

    await waitFor(() => {
      expect(mockChangePassword).toHaveBeenCalledWith('', 'alpha123');
    });
    await waitFor(() => {
      expect(screen.getByText('Passphrase updated.')).toBeTruthy();
    });
    expect(screen.getByLabelText('New Passphrase').value).toBe('');
    expect(screen.getByLabelText('Confirm New Passphrase').value).toBe('');
  });

  it('shows a mismatch error and does not call changePassword when confirm differs', async () => {
    render(<SettingsPage />);
    await waitForPanel();

    fireEvent.change(screen.getByLabelText('New Passphrase'), { target: { value: 'alpha123' } });
    fireEvent.change(screen.getByLabelText('Confirm New Passphrase'), { target: { value: 'alpha999' } });

    fireEvent.click(screen.getByRole('button', { name: /Update Passphrase/i }));

    await waitFor(() => {
      expect(screen.getByText('Passphrases do not match.')).toBeTruthy();
    });
    expect(mockChangePassword).not.toHaveBeenCalled();
  });

  it('disables the submit button when new or confirm is empty', async () => {
    render(<SettingsPage />);
    await waitForPanel();
    const button = screen.getByRole('button', { name: /Update Passphrase/i });
    expect(button.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('New Passphrase'), { target: { value: 'alpha' } });
    expect(button.disabled).toBe(true);

    fireEvent.change(screen.getByLabelText('Confirm New Passphrase'), { target: { value: 'alpha' } });
    expect(button.disabled).toBe(false);
  });

  it('renders a server error when the RPC rejects', async () => {
    mockChangePassword.mockRejectedValueOnce(new Error('invalid credentials'));
    render(<SettingsPage />);
    await waitForPanel();

    fireEvent.change(screen.getByLabelText('Current Passphrase'), { target: { value: 'wrong' } });
    fireEvent.change(screen.getByLabelText('New Passphrase'), { target: { value: 'alpha123' } });
    fireEvent.change(screen.getByLabelText('Confirm New Passphrase'), { target: { value: 'alpha123' } });

    fireEvent.click(screen.getByRole('button', { name: /Update Passphrase/i }));

    await waitFor(() => {
      expect(screen.getByText('invalid credentials')).toBeTruthy();
    });
    expect(mockChangePassword).toHaveBeenCalledTimes(1);
  });
});
