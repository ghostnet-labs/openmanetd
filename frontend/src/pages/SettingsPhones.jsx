// =============================================================================
// SettingsPhones.jsx — operator side of phone onboarding
// =============================================================================
//
// Upload or remove the ATAK APK this node hands to phones, and show the
// link phones open (/join over plain HTTP). See docs/phone-onboarding.md.

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  JOIN_PATH,
  fetchOnboardStatus,
  formatBytes,
  removeAtakApk,
  uploadAtakApk,
} from '../services/onboardApi.js';
import './SettingsPhones.css';

export default function SettingsPhones() {
  const [state, setState] = useState({ loading: true, error: null, status: null });
  const [upload, setUpload] = useState(null); // { loaded, total } while uploading
  const [notice, setNotice] = useState(null); // { tone, text }
  const fileRef = useRef(null);
  const abortRef = useRef(null);

  const load = useCallback(async () => {
    try {
      const status = await fetchOnboardStatus();
      setState({ loading: false, error: null, status });
    } catch (err) {
      setState({ loading: false, error: err.message, status: null });
    }
  }, []);

  useEffect(() => {
    // Fetch on mount; the APK only changes through this page.
    // eslint-disable-next-line react-hooks/set-state-in-effect
    load();
    return () => abortRef.current?.abort();
  }, [load]);

  const onPick = useCallback(async (file) => {
    const ctrl = new AbortController();
    abortRef.current = ctrl;
    setNotice(null);
    setUpload({ loaded: 0, total: file.size });
    try {
      const info = await uploadAtakApk(file, { signal: ctrl.signal, onProgress: setUpload });
      setNotice({ tone: 'ok', text: `Stored ${info?.name || file.name}.` });
      await load();
    } catch (err) {
      if (err.name !== 'AbortError') setNotice({ tone: 'crit', text: err.message });
    } finally {
      setUpload(null);
      abortRef.current = null;
    }
  }, [load]);

  const onRemove = useCallback(async () => {
    setNotice(null);
    try {
      await removeAtakApk();
      setNotice({ tone: 'ok', text: 'ATAK file removed.' });
      await load();
    } catch (err) {
      setNotice({ tone: 'crit', text: err.message });
    }
  }, [load]);

  if (state.loading) return <div className="lat-panel">Loading…</div>;
  if (state.error) {
    return <div className="lat-alert crit">Phone onboarding is unavailable ({state.error}).</div>;
  }

  const { status } = state;
  const apk = status.apk;
  const joinUrl = new URL(JOIN_PATH, status.apkUrl).toString();
  const pct = upload?.total ? Math.round((upload.loaded / upload.total) * 100) : 0;

  return (
    <div className="lat-body grid-2x">
      <section className="lat-panel">
        <div className="panel-head"><h3>ATAK for phones</h3></div>
        {apk.available ? (
          <>
            <div className="kv"><span className="k">File</span><span className="v">{apk.name || 'atak.apk'}</span></div>
            <div className="kv"><span className="k">Size</span><span className="v">{formatBytes(apk.size)}</span></div>
            <div className="kv">
              <span className="k">Added</span>
              <span className="v">{apk.uploaded ? new Date(apk.uploaded).toISOString().slice(0, 16).replace('T', ' ') : '—'}</span>
            </div>
            <div className="kv"><span className="k">SHA-256</span><span className="v phones-hash">{apk.sha256}</span></div>
          </>
        ) : (
          <div className="phones-empty">No ATAK file on this node.</div>
        )}

        {upload && (
          <div className="pbar-row phones-progress">
            <span className="pbar-label">Upload</span>
            <div className="pbar"><span style={{ width: `${pct}%` }} /></div>
            <span className="pbar-val">{pct}%</span>
          </div>
        )}

        {notice && <div className={`lat-alert ${notice.tone} phones-notice`}>{notice.text}</div>}

        <div className="phones-actions">
          <input
            ref={fileRef}
            type="file"
            className="phones-file"
            accept=".apk,application/vnd.android.package-archive"
            aria-label="ATAK APK file"
            onChange={(e) => {
              const f = e.target.files?.[0];
              if (f) onPick(f);
              e.target.value = '';
            }}
          />
          <button className="lat-btn primary" type="button" disabled={!!upload} onClick={() => fileRef.current?.click()}>
            {apk.available ? 'Replace APK' : 'Upload APK'}
          </button>
          {upload && (
            <button className="lat-btn ghost" type="button" onClick={() => abortRef.current?.abort()}>Cancel</button>
          )}
          {apk.available && !upload && (
            <button className="lat-btn danger" type="button" onClick={onRemove}>Remove</button>
          )}
        </div>
        <p className="phones-help">
          Use the ATAK-CIV APK from Google Play or tak.gov. Phones on this node download it from here with no internet.
        </p>
      </section>

      <section className="lat-panel">
        <div className="panel-head"><h3>Phone setup link</h3></div>
        <div className="kv"><span className="k">Open on phone</span><span className="v phones-hash">{joinUrl}</span></div>
        <div className="kv">
          <span className="k">Maps sent</span>
          <span className="v">{status.mapSources?.length ? status.mapSources.join(', ') : 'None'}</span>
        </div>
        <p className="phones-help">
          A phone on this node&apos;s Wi-Fi opens this link to install ATAK and load its callsign, team and maps.
        </p>
      </section>
    </div>
  );
}
