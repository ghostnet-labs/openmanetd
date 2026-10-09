// =============================================================================
// LoginPage.test.jsx — smoke tests for the operator authentication terminal
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor, cleanup } from '@testing-library/react';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';

// Stub the Connect-RPC dashboard client used for the corner readouts so the
// real network never opens.
vi.mock('../../services/connectClient.js', () => ({
  transport: {},
}));

vi.mock('@connectrpc/connect', () => ({
  // The corner readouts call getDashboardStatus best-effort; for these tests
  // we don't assert on them, so a never-resolving promise keeps the post-mount
  // setState from leaking past the test boundary.
  createClient: () => ({
    getDashboardStatus: vi.fn(() => new Promise(() => {})),
  }),
}));

const authState = {
  login: vi.fn(),
  isAuthenticated: false,
  endReason: null,
};
const luciState = { known: false };
vi.mock('../../hooks/useLuciProxy.js', () => ({
  knownLuciProxyEnabled: () => luciState.known,
}));
vi.mock('../../contexts/useAuth.js', () => ({
  useAuth: () => authState,
}));

import LoginPage from '../../pages/LoginPage.jsx';

beforeEach(() => {
  authState.login = vi.fn();
  authState.isAuthenticated = false;
  authState.endReason = null;
  luciState.known = false;
});

afterEach(() => {
  cleanup();
});

function renderLogin() {
  return render(
    <MemoryRouter>
      <LoginPage />
    </MemoryRouter>,
  );
}

describe('TestLoginPageRender', () => {
  it('renders the operator and passphrase fields', () => {
    renderLogin();
    expect(screen.getByLabelText('Operator')).toBeInTheDocument();
    expect(screen.getByLabelText('Passphrase')).toBeInTheDocument();
  });

  it('renders the authenticate button enabled by default', () => {
    renderLogin();
    const btn = screen.getByRole('button', { name: /authenticate/i });
    expect(btn).toBeInTheDocument();
    expect(btn).not.toBeDisabled();
  });
});

describe('TestLoginPageValidation', () => {
  it('blocks submit with no operator and shows an error', async () => {
    renderLogin();
    const form = screen.getByRole('button', { name: /authenticate/i }).closest('form');
    fireEvent.submit(form);
    await waitFor(() => {
      expect(screen.getByRole('alert')).toHaveTextContent(/operator/i);
    });
    expect(authState.login).not.toHaveBeenCalled();
  });

  it('calls login with operator and passphrase when both are entered', async () => {
    authState.login.mockResolvedValue();
    renderLogin();
    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'admin' } });
    fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'pw' } });
    fireEvent.submit(screen.getByRole('button', { name: /authenticate/i }).closest('form'));
    await waitFor(() => {
      expect(authState.login).toHaveBeenCalledWith('admin', 'pw');
    });
  });
});

function WhereAmI() {
  const location = useLocation();
  return <div data-testid="where">{location.pathname + location.search + location.hash}</div>;
}

function renderLoginAt(entry) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/login" element={<LoginPage />} />
        <Route path="*" element={<WhereAmI />} />
      </Routes>
    </MemoryRouter>,
  );
}

async function submitLogin() {
  fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'root' } });
  fireEvent.change(screen.getByLabelText('Passphrase'), { target: { value: 'pw' } });
  fireEvent.submit(screen.getByRole('button', { name: /authenticate/i }).closest('form'));
}

describe('TestLoginPageReturnPath', () => {
  it('returns to the validated deep link after sign-in', async () => {
    authState.login.mockResolvedValue();
    renderLoginAt('/login?next=%2Fsettings%2Fwireless%3Ftab%3Dmesh');
    expect(screen.getByText('Returns to /settings/wireless?tab=mesh after sign-in.')).toBeInTheDocument();
    await submitLogin();
    await waitFor(() => {
      expect(screen.getByTestId('where').textContent).toBe('/settings/wireless?tab=mesh');
    });
  });

  it('goes to the dashboard without next', async () => {
    authState.login.mockResolvedValue();
    renderLoginAt('/login');
    expect(screen.queryByText(/Returns to/)).toBeNull();
    await submitLogin();
    await waitFor(() => {
      expect(screen.getByTestId('where').textContent).toBe('/');
    });
  });

  it.each([
    ['//evil.example/'],
    ['https://evil.example/'],
    ['/\\evil.example'],
    ['javascript:alert(1)'],
    ['/cgi-bin/luci/'],
  ])('ignores unsafe next %j and goes to the dashboard', async (next) => {
    authState.login.mockResolvedValue();
    renderLoginAt('/login?next=' + encodeURIComponent(next));
    expect(screen.queryByText(/Returns to/)).toBeNull();
    await submitLogin();
    await waitFor(() => {
      expect(screen.getByTestId('where').textContent).toBe('/');
    });
  });

  it('sends an already signed-in operator straight to next', async () => {
    authState.isAuthenticated = true;
    renderLoginAt('/login?next=%2Fcomms');
    await waitFor(() => {
      expect(screen.getByTestId('where').textContent).toBe('/comms');
    });
  });
});

describe('TestLoginPageSessionNotice', () => {
  it('shows no notice on a fresh visit', () => {
    renderLogin();
    expect(screen.queryByRole('status')).toBeNull();
  });

  it('says the OpenMANET session expired', () => {
    authState.endReason = 'expired';
    renderLogin();
    const notice = screen.getByRole('status');
    expect(notice).toHaveTextContent('Your OpenMANET session expired. Sign in again to continue.');
    expect(notice.classList.contains('warn')).toBe(true);
  });

  it('says sign-out covered OpenMANET only, naming LuCI when Advanced is on', () => {
    authState.endReason = 'signed-out';
    luciState.known = true;
    renderLogin();
    expect(screen.getByRole('status')).toHaveTextContent(
      'Signed out of OpenMANET. LuCI (Advanced) has its own sign-in and is not signed out here.',
    );
  });

  it('does not mention LuCI on sign-out when the proxy is not known to be on', () => {
    authState.endReason = 'signed-out';
    renderLogin();
    const notice = screen.getByRole('status');
    expect(notice).toHaveTextContent('Signed out of OpenMANET.');
    expect(notice.textContent).not.toMatch(/LuCI/);
  });
});
