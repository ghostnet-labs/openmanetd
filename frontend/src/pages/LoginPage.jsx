// =============================================================================
// LoginPage.jsx — Operator authentication terminal
// =============================================================================

import { useState, useEffect } from 'react';
import { useNavigate, useSearchParams } from 'react-router-dom';
import { createClient } from '@connectrpc/connect';
import { DashboardService } from '../gen/openmanet/dashboard/v1/dashboard_service_pb.js';
import { transport } from '../services/connectClient.js';
import { useAuth } from '../contexts/useAuth.js';
import { knownLuciProxyEnabled } from '../hooks/useLuciProxy.js';
import { safeReturnPath } from '../utils/returnPath.js';
import openmanetMark from '../assets/openmanet-mark.svg';
import './LoginPage.css';

const dashboardClient = createClient(DashboardService, transport);

function useUtcClock() {
  const [now, setNow] = useState(() => new Date());
  useEffect(() => {
    const id = setInterval(() => setNow(new Date()), 1000);
    return () => clearInterval(id);
  }, []);
  return now;
}

function formatUtc(d) {
  return d.toISOString().slice(11, 19);
}
function formatDate(d) {
  return d.toISOString().slice(0, 10);
}

// Why the operator is on the login page. Only the OpenMANET sign-in is
// described: LuCI (Advanced) has its own root login that this page neither
// creates nor ends.
function sessionNotice(endReason, luciEnabled) {
  if (endReason === 'expired') {
    return { tone: 'warn', text: 'Your OpenMANET session expired. Sign in again to continue.' };
  }
  if (endReason === 'signed-out') {
    const luci = luciEnabled
      ? ' LuCI (Advanced) has its own sign-in and is not signed out here.'
      : '';
    return { tone: '', text: `Signed out of OpenMANET.${luci}` };
  }
  return null;
}

export default function LoginPage() {
  const { login, isAuthenticated, endReason } = useAuth();
  const navigate = useNavigate();
  const [searchParams] = useSearchParams();

  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [deviceInfo, setDeviceInfo] = useState(null);
  const now = useUtcClock();

  // Where to go after sign-in: the validated `next` deep link, else '/'.
  const returnPath = safeReturnPath(searchParams.get('next'));

  // Redirect if already authenticated.
  useEffect(() => {
    if (isAuthenticated) navigate(returnPath, { replace: true });
  }, [isAuthenticated, navigate, returnPath]);

  // Best-effort unauthenticated device-info fetch for the corner readouts.
  useEffect(() => {
    dashboardClient.getDashboardStatus({})
      .then(resp => setDeviceInfo(resp.deviceInfo ?? null))
      .catch(() => { /* empty state is fine — corners just show em-dashes */ });
  }, []);

  async function handleSubmit(e) {
    e.preventDefault();

    if (!username) {
      setError('Please enter operator');
      return;
    }

    setError('');
    setSubmitting(true);

    try {
      await login(username, password);
      navigate(returnPath, { replace: true });
    } catch (err) {
      setError(err.message || 'Authentication failed');
    } finally {
      setSubmitting(false);
    }
  }

  const hostname = deviceInfo?.hostname || '—';
  const firmware = deviceInfo?.firmware || '—';
  const model = deviceInfo?.model || '—';
  const kernel = deviceInfo?.kernel || '';
  const arch = deviceInfo?.architecture || '';
  const notice = sessionNotice(endReason, knownLuciProxyEnabled());
  const noticeClass = notice?.tone ? `lat-alert ${notice.tone}` : 'lat-alert';
  const returnHint = returnPath === '/' ? null : `Returns to ${returnPath} after sign-in.`;

  return (
    <div className="login-screen">
      <div className="login-corner tl">
        NODE<br/><span className="v">{hostname}</span>
      </div>
      <div className="login-corner tr">
        {formatDate(now)}<br/><span className="v">{formatUtc(now)} UTC</span>
      </div>
      <div className="login-corner bl">
        OPENMANETD<br/><span className="v">{firmware}</span>
      </div>
      <div className="login-corner br">
        {model}<br/><span className="v">{[kernel, arch].filter(Boolean).join(' · ') || '—'}</span>
      </div>

      <div className="login-card">
        <img src={openmanetMark} alt="" className="login-logo" />
        <div className="login-mark">OPENMANET</div>
        <div className="login-sub">Mesh Operator Terminal</div>

        <form className="login-form" onSubmit={handleSubmit} noValidate>
          {notice ? (
            <div className={noticeClass} role="status">{notice.text}</div>
          ) : null}
          {returnHint ? (
            <div className="login-return">{returnHint}</div>
          ) : null}

          <div className="lat-field">
            <label htmlFor="username">Operator</label>
            <input
              id="username"
              className="lat-input"
              type="text"
              autoComplete="username"
              value={username}
              onChange={e => setUsername(e.target.value)}
              disabled={submitting}
            />
          </div>

          <div className="lat-field">
            <label htmlFor="password">Passphrase</label>
            <input
              id="password"
              className="lat-input"
              type="password"
              autoComplete="current-password"
              value={password}
              onChange={e => setPassword(e.target.value)}
              disabled={submitting}
            />
          </div>

          {error && (
            <div className="lat-alert crit" role="alert">{error}</div>
          )}

          <button
            type="submit"
            className="lat-btn primary login-submit"
            disabled={submitting}
          >
            {submitting ? 'AUTHENTICATING…' : '◇ AUTHENTICATE'}
          </button>
        </form>
      </div>
    </div>
  );
}
