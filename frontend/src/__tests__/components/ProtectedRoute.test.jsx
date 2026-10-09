// =============================================================================
// ProtectedRoute.test.jsx — redirect to /login carrying the return location
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/react';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';

const authState = { isAuthenticated: false, loading: false };
vi.mock('../../contexts/useAuth.js', () => ({
  useAuth: () => authState,
}));

import ProtectedRoute from '../../components/ProtectedRoute.jsx';

function LoginProbe() {
  const loc = useLocation();
  const from = loc.state?.from;
  return <div data-testid="login">{from ? from.pathname + from.search : 'none'}</div>;
}

function renderAt(entry) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/login" element={<LoginProbe />} />
        <Route path="*" element={<ProtectedRoute><div data-testid="page">page</div></ProtectedRoute>} />
      </Routes>
    </MemoryRouter>,
  );
}

beforeEach(() => {
  authState.isAuthenticated = false;
  authState.loading = false;
});

afterEach(() => {
  cleanup();
});

describe('TestProtectedRoute', () => {
  it('renders nothing while the session check is in flight', () => {
    authState.loading = true;
    const { container } = renderAt('/settings');
    expect(container).toBeEmptyDOMElement();
  });

  it('renders the page when authenticated', () => {
    authState.isAuthenticated = true;
    renderAt('/settings');
    expect(screen.getByTestId('page')).toBeInTheDocument();
  });

  it('redirects to /login with the requested location as state.from', () => {
    renderAt('/settings/wireless?tab=radio0');
    expect(screen.getByTestId('login')).toHaveTextContent('/settings/wireless?tab=radio0');
  });
});
