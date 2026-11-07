// =============================================================================
// JoinPage.jsx — public phone setup page: get ATAK from the node
// =============================================================================
//
// Reached at /join with no login by a phone that has just joined the
// node's access point. Step 1 downloads the ATAK APK from the node; step 2
// opens a tak:// link so ATAK imports the node's settings package
// (callsign, team, role, map sources) with one confirm. Works with no
// internet. See docs/phone-onboarding.md.

import { useEffect, useMemo, useState } from 'react';
import LatSelect from '../components/LatSelect.jsx';
import {
  JOIN_PATH,
  buildPackageUrl,
  buildTakImportUrl,
  detectPlatform,
  fetchOnboardStatus,
  formatBytes,
  isValidCallsign,
} from '../services/onboardApi.js';
import './JoinPage.css';

const PROFILE_KEY = 'openmanetd.join.profile';

function loadProfile() {
  try {
    const p = JSON.parse(localStorage.getItem(PROFILE_KEY) || '{}');
    return { callsign: p.callsign ?? '', team: p.team ?? '', role: p.role ?? '' };
  } catch {
    return { callsign: '', team: '', role: '' };
  }
}

function saveProfile(p) {
  try {
    localStorage.setItem(PROFILE_KEY, JSON.stringify(p));
  } catch {
    // Storage blocked (private tab): the form still works, it just
    // won't be remembered.
  }
}

function toOptions(list, emptyLabel) {
  return [{ value: '', label: emptyLabel }, ...(list ?? []).map(v => ({ value: v, label: v }))];
}

export default function JoinPage() {
  const [state, setState] = useState({ loading: true, error: null, status: null });
  const [profile, setProfile] = useState(loadProfile);

  const platform = useMemo(
    () => detectPlatform(navigator.userAgent, navigator.maxTouchPoints ?? 0),
    [],
  );

  const { status } = state;
  const teamOptions = useMemo(() => toOptions(status?.teams, 'Keep ATAK default'), [status]);
  const roleOptions = useMemo(() => toOptions(status?.roles, 'Keep ATAK default'), [status]);

  useEffect(() => {
    const ctrl = new AbortController();
    fetchOnboardStatus({ signal: ctrl.signal })
      .then(status => setState({ loading: false, error: null, status }))
      .catch(err => {
        if (err.name === 'AbortError') return;
        setState({ loading: false, error: err.message, status: null });
      });
    return () => ctrl.abort();
  }, []);

  useEffect(() => {
    saveProfile(profile);
  }, [profile]);

  const callsignOk = isValidCallsign(profile.callsign);
  const packageUrl = status && callsignOk ? buildPackageUrl(status.packageUrl, profile) : '';
  const importUrl = packageUrl ? buildTakImportUrl(packageUrl) : '';
  // Chrome blocks an HTTP download started from an HTTPS page, and the
  // APK is only served over HTTP, so send HTTPS visitors to the HTTP page.
  const httpJoinUrl = status ? new URL(JOIN_PATH, status.apkUrl).toString() : '';
  const onHttps = window.location.protocol === 'https:';
  const apk = status?.apk;
  const maps = status?.mapSources?.length ? status.mapSources.join(' and ') : '';

  const setField = (key) => (value) => setProfile(p => ({ ...p, [key]: value }));

  if (state.loading) {
    return <div className="join-screen"><div className="join-loading">Loading…</div></div>;
  }

  if (state.error) {
    return (
      <div className="join-screen">
        <div className="lat-alert crit">Cannot reach this node&apos;s setup service ({state.error}).</div>
      </div>
    );
  }

  return (
    <div className="join-screen">
      <header className="join-head">
        <div className="join-node">OpenMANET · {status.node || 'node'}</div>
        <h2>Set up your phone</h2>
      </header>

      {platform === 'ios' && (
        <div className="lat-alert warn">
          ATAK is Android only. On iPhone, iTAK comes from the App Store and needs internet to install.
        </div>
      )}

      {platform === 'other' && (
        <div className="lat-alert">Open this page on the Android phone you are setting up.</div>
      )}

      {onHttps && (
        <div className="lat-alert warn join-https">
          <span>Downloads don&apos;t work on this secure page.</span>
          <a className="lat-btn join-touch" href={httpJoinUrl}>Open setup page</a>
        </div>
      )}

      {platform !== 'ios' && (
        <>
          <section className="lat-panel">
            <div className="panel-head"><h3>1 · Install ATAK</h3></div>
            {apk?.available ? (
              <>
                <a className="lat-btn primary join-touch join-cta" href={status.apkUrl} download="ATAK-CIV.apk">
                  Download ATAK · {formatBytes(apk.size)}
                </a>
                <ol className="join-steps">
                  <li>If Chrome warns about the download, tap Keep or Download anyway.</li>
                  <li>Open the file. Allow installs from Chrome when asked, then tap Install.</li>
                  <li>Open ATAK once and accept its prompts.</li>
                </ol>
              </>
            ) : (
              <div className="lat-alert warn">
                This node has no ATAK file yet. Ask your team lead to add it under Settings, Phones.
              </div>
            )}
            <div className="join-note">Already have ATAK? Go to step 2.</div>
          </section>

          <section className="lat-panel">
            <div className="panel-head"><h3>2 · Set up ATAK</h3></div>
            <div className="join-form">
              <div className="lat-field">
                <label htmlFor="join-callsign">Callsign</label>
                <input
                  id="join-callsign"
                  className="lat-input join-touch"
                  type="text"
                  autoComplete="off"
                  maxLength={40}
                  value={profile.callsign}
                  onChange={(e) => setField('callsign')(e.target.value)}
                  placeholder="e.g. Bravo 6"
                />
                {!callsignOk && (
                  <span className="hint join-error">Letters, digits, space, - _ and . only.</span>
                )}
              </div>
              <div className="lat-field">
                <label>Team</label>
                <LatSelect value={profile.team} onChange={setField('team')} options={teamOptions} ariaLabel="Team" />
              </div>
              <div className="lat-field">
                <label>Role</label>
                <LatSelect value={profile.role} onChange={setField('role')} options={roleOptions} ariaLabel="Role" />
              </div>
            </div>
            {importUrl ? (
              <a className="lat-btn primary join-touch join-cta" href={importUrl}>Set up ATAK</a>
            ) : (
              <button className="lat-btn primary join-touch join-cta" type="button" disabled>Set up ATAK</button>
            )}
            <ol className="join-steps">
              <li>ATAK opens and asks to import. Tap Import.</li>
              <li>
                It adds your callsign and team{maps ? ` and the ${maps} maps` : ''}.
              </li>
            </ol>
            {packageUrl && (
              <a className="join-note join-link" href={packageUrl}>Download the settings file instead</a>
            )}
          </section>
        </>
      )}
    </div>
  );
}
