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
};
vi.mock('../../contexts/useAuth.js', () => ({
  useAuth: () => authState,
}));

import LoginPage from '../../pages/LoginPage.jsx';

beforeEach(() => {
  authState.login = vi.fn();
  authState.isAuthenticated = false;
});

afterEach(() => {
  cleanup();
});

function LocationProbe() {
  const loc = useLocation();
  return <div data-testid="location">{loc.pathname + loc.search}</div>;
}

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

describe('TestLoginPageReturnPath', () => {
  function renderAt(entry) {
    return render(
      <MemoryRouter initialEntries={[entry]}>
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="*" element={<LocationProbe />} />
        </Routes>
      </MemoryRouter>,
    );
  }

  it('returns to the page ProtectedRoute bounced from after login', async () => {
    authState.login.mockResolvedValue();
    renderAt({ pathname: '/login', state: { from: { pathname: '/settings/wireless', search: '', hash: '' } } });
    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'admin' } });
    fireEvent.submit(screen.getByRole('button', { name: /authenticate/i }).closest('form'));
    expect(await screen.findByTestId('location')).toHaveTextContent('/settings/wireless');
  });

  it('falls back to the dashboard without a return location', async () => {
    authState.login.mockResolvedValue();
    renderAt('/login');
    fireEvent.change(screen.getByLabelText('Operator'), { target: { value: 'admin' } });
    fireEvent.submit(screen.getByRole('button', { name: /authenticate/i }).closest('form'));
    expect(await screen.findByTestId('location')).toHaveTextContent(/^\/$/);
  });

  it('sends an already authenticated operator to the return location', async () => {
    authState.isAuthenticated = true;
    renderAt({ pathname: '/login', state: { from: { pathname: '/gps', search: '?x=1', hash: '' } } });
    expect(await screen.findByTestId('location')).toHaveTextContent('/gps?x=1');
  });
});
