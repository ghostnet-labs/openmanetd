// =============================================================================
// ScannerConventional.jsx — Conventional panel of the Scanner page (GHO-53)
// =============================================================================
//
// Lists the nearby conventional / aviation / interop / weather channels the
// scan RTL-SDR sweeps, with live and last-heard state, and plays one
// channel's squelch-open audio through WebAudio. Listening subscribes to
// StreamAudio, which also pins the scanner to that channel's window; the
// stream (and the pin) ends on STOP, on unmount or when another channel is
// chosen. Only 8 kHz PCM crosses the mesh, never IQ.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ConnectError } from '@connectrpc/connect';
import DataTable from '../components/DataTable.jsx';
import { streamAudio } from '../services/scannerApi.js';
import { createPcmPlayer } from '../services/pcmPlayer.js';
import { conventionalRows, conventionalSummary } from '../utils/scanner.js';

const rowKey = (r) => r.key;

function errorMessage(err) {
  return err instanceof ConnectError ? err.rawMessage : String(err?.message ?? err);
}

function StatusCell({ row }) {
  if (row.statusClass === 'live') return <span className="lat-chip"><span className="dot" />LIVE</span>;
  if (row.statusClass === 'warn') return <span className="lat-chip warn">{row.status}</span>;
  return <span className="scanner-quiet">{row.status}</span>;
}

function ListenButton({ row, listening, onListen }) {
  const on = listening === row.key;
  const label = on ? 'STOP' : 'LISTEN';
  return (
    <button
      type="button"
      className={`lat-btn sm scanner-listen${on ? ' primary' : ' ghost'}`}
      onClick={() => onListen(row.key)}
      aria-pressed={on}
      aria-label={`${on ? 'Stop listening to' : 'Listen to'} ${row.label} ${row.frequency}`}
    >
      {label}
    </button>
  );
}

export default function ScannerConventional({ data, error }) {
  const [listening, setListening] = useState(null);
  const [audioError, setAudioError] = useState(null);
  const session = useRef(null);

  const summary = useMemo(() => conventionalSummary(data), [data]);
  const rows = useMemo(() => conventionalRows(data?.channels), [data]);

  const stop = useCallback(() => {
    const cur = session.current;
    session.current = null;
    if (!cur) return;
    cur.abort.abort();
    cur.player.close();
  }, []);

  const onListen = useCallback((key) => {
    const wasOn = session.current?.key === key;
    stop();
    setAudioError(null);
    if (wasOn) {
      setListening(null);
      return;
    }

    let player;
    try {
      player = createPcmPlayer(); // inside the click: the AudioContext may start
    } catch (err) {
      setAudioError(errorMessage(err));
      setListening(null);
      return;
    }
    const abort = new AbortController();
    const cur = { key, abort, player };
    session.current = cur;
    setListening(key);

    (async () => {
      try {
        for await (const msg of streamAudio(key, abort.signal)) {
          if (msg.data?.length) player.push(msg.data);
        }
      } catch (err) {
        if (!abort.signal.aborted) setAudioError(errorMessage(err));
      } finally {
        if (session.current === cur) {
          stop();
          setListening(null);
        }
      }
    })();
  }, [stop]);

  useEffect(() => stop, [stop]);

  const columns = useMemo(() => [
    { key: 'frequency', label: 'Frequency', render: (r) => r.frequency, className: 'mono' },
    { key: 'status', label: 'Status', render: (r) => <StatusCell row={r} /> },
    { key: 'label', label: 'Channel', render: (r) => r.label },
    { key: 'mode', label: 'Mode', render: (r) => r.mode },
    { key: 'category', label: 'Type', render: (r) => r.category },
    { key: 'distance', label: 'Distance', render: (r) => r.distance, headerClass: 'num' },
    { key: 'lastHeard', label: 'Last heard', render: (r) => r.lastHeard },
    {
      key: 'listen',
      label: 'Audio',
      render: (r) => <ListenButton row={r} listening={listening} onListen={onListen} />,
    },
  ], [listening, onListen]);

  let content;
  if (!data && error) {
    content = <div className="lat-alert crit">Conventional channels unavailable: {error}</div>;
  } else if (!data) {
    content = <div className="scanner-loading">Loading…</div>;
  } else {
    const detailClass = summary.stateClass === 'crit' ? 'crit' : 'warn';
    content = (
      <>
        <div className="scanner-conv-kv">
          <div className="kv"><span className="k">State</span><span className={`v ${summary.stateClass}`.trim()}>{summary.state}</span></div>
          <div className="kv"><span className="k">Window</span><span className="v">{summary.window}</span></div>
          <div className="kv"><span className="k">Center</span><span className="v">{summary.center}</span></div>
          <div className="kv"><span className="k">Radius</span><span className="v">{summary.radius}</span></div>
        </div>
        {summary.detail ? <div className={`lat-alert ${detailClass}`}>{summary.detail}</div> : null}
        {error ? <div className="lat-alert warn">Last refresh failed: {error}</div> : null}
        {audioError ? <div className="lat-alert crit">Audio: {audioError}</div> : null}
        <DataTable
          ariaLabel="Conventional channels"
          columns={columns}
          rows={rows}
          rowKey={rowKey}
          cardTitleKey="frequency"
          emptyLabel="No conventional channels nearby."
        />
      </>
    );
  }

  return (
    <div className="lat-panel col-span-all">
      <div className="panel-head">
        <h3>Conventional</h3>
        {listening ? <div className="actions"><span className="lat-chip"><span className="dot" />AUDIO</span></div> : null}
      </div>
      {content}
    </div>
  );
}
