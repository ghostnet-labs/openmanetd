// =============================================================================
// ProtectedRoute.jsx — Redirect to /login if the user is not authenticated
// =============================================================================
//
// The current location rides along as `state.from` so LoginPage can send the
// operator back to the page they asked for (or were on when the session
// expired) instead of always landing on the dashboard.

import { Navigate, useLocation } from 'react-router-dom';
import { useAuth } from '../contexts/useAuth.js';

export default function ProtectedRoute({ children }) {
  const { isAuthenticated, loading } = useAuth();
  const location = useLocation();

  // Wait for the initial session check before deciding.
  if (loading) return null;

  if (!isAuthenticated) return <Navigate to="/login" replace state={{ from: location }} />;

  return children;
}
