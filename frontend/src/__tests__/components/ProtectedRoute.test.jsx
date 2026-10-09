// =============================================================================
// ProtectedRoute.test.jsx — login redirect keeps the deep link in `next`
// =============================================================================

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup } from '@testing-library/react';
import { MemoryRouter, Routes, Route, useLocation } from 'react-router-dom';

const authState = { isAuthenticated: false, loading: false };
vi.mock('../../contexts/useAuth.js', () => ({
  useAuth: () => authState,
}));

import ProtectedRoute from '../../components/ProtectedRoute.jsx';

function LoginProbe() {
  const location = useLocation();
  return <div data-testid="login">{location.pathname + location.search}</div>;
}

function renderAt(entry) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route path="/login" element={<LoginProbe />} />
        <Route path="*" element={<ProtectedRoute><div>protected page</div></ProtectedRoute>} />
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
  it('renders children when signed in', () => {
    authState.isAuthenticated = true;
    renderAt('/settings/wireless');
    expect(screen.getByText('protected page')).toBeInTheDocument();
  });

  it('renders nothing while the session check is in flight', () => {
    authState.loading = true;
    const { container } = renderAt('/settings');
    expect(container.textContent).toBe('');
  });

  it('sends a signed-out deep link to /login with next', () => {
    renderAt('/settings/wireless?tab=mesh#radio');
    expect(screen.getByTestId('login').textContent).toBe(
      '/login?next=%2Fsettings%2Fwireless%3Ftab%3Dmesh%23radio',
    );
  });

  it('sends the dashboard to plain /login', () => {
    renderAt('/');
    expect(screen.getByTestId('login').textContent).toBe('/login');
  });
});
