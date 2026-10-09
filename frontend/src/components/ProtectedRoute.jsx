// =============================================================================
// ProtectedRoute.jsx — Redirect to /login if the user is not authenticated
// =============================================================================
//
// The page the operator was on travels in `/login?next=...` so signing in
// again (after expiry or sign-out) returns them to the same deep link.
// loginPathFor validates it as a same-origin SPA path.

import { Navigate, useLocation } from 'react-router-dom';
import { useAuth } from '../contexts/useAuth.js';
import { loginPathFor } from '../utils/returnPath.js';

export default function ProtectedRoute({ children }) {
  const { isAuthenticated, loading } = useAuth();
  const location = useLocation();

  // Wait for the initial session check before deciding.
  if (loading) return null;

  if (!isAuthenticated) {
    const to = loginPathFor(location.pathname + location.search + location.hash);
    return <Navigate to={to} replace />;
  }

  return children;
}
