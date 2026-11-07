// =============================================================================
// Weather.jsx — weather stations heard over the mesh
// =============================================================================
//
// Polls WeatherService while the tab is visible and shows one panel per
// station: this node's own sensor first (when it has one), then every
// station other nodes share. A stale station stays listed but greyed out
// with its age, so old weather is never shown as current. A failed poll
// keeps the last good data and says so.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import { ConnectError } from '@connectrpc/connect';
import { useVisibleInterval } from '../hooks/useVisibleInterval.js';
import { listWeatherStations } from '../services/weatherApi.js';
import { stationViews } from '../utils/weather.js';
import './Weather.css';

export const POLL_MS = 10000;

const INITIAL = { loading: true, data: null, error: null };

function errorMessage(err) {
  return err instanceof ConnectError ? err.rawMessage : String(err?.message ?? err);
}

function Kv({ k, v, className = '' }) {
  return (
    <div className="kv"><span className="k">{k}</span><span className={`v ${className}`.trim()}>{v}</span></div>
  );
}

function StationPanel({ view }) {
  const tempClass = view.stale || view.neverRead ? 'muted' : '';
  const sub = view.local ? 'This node' : view.model;
  return (
    <div className="lat-panel" data-testid="weather-station">
      <div className="panel-head">
        <h3>{view.name}</h3>
        <div className="actions">
          {view.local ? <span className="lat-chip">LOCAL</span> : null}
          <span className={`lat-chip ${view.statusClass}`.trim()}>{view.status}</span>
        </div>
      </div>
      <div className="weather-temp">
        <div className={`big-num ${tempClass}`.trim()}>{view.tempF}</div>
        <div className="weather-temp-sub">{view.tempC} · {sub}</div>
      </div>
      {view.error ? <div className="lat-alert crit">Sensor: {view.error}</div> : null}
      {view.stale && !view.error ? <div className="lat-alert warn">Reading is old: last update {view.age}.</div> : null}
      <Kv k="Wind" v={view.wind} className="accent" />
      <Kv k="Gust" v={view.gust} />
      <Kv k="From" v={view.direction} />
      <Kv k="Humidity" v={view.humidity} />
      <Kv k="Pressure" v={view.pressure} />
      <Kv k="Trend · 3 h" v={view.trend.label} className={view.trend.className} />
      <Kv k="Rain · 1 h" v={view.rainHour} />
      <Kv k="Rain total" v={view.rainTotal} />
      <Kv k="Light" v={view.light} />
      <Kv k="Position" v={view.position} className={view.position === 'NO FIX' ? 'warn' : ''} />
      <Kv k="Updated" v={view.age} className={view.stale || view.neverRead ? 'warn' : ''} />
    </div>
  );
}

export default function Weather() {
  const [state, setState] = useState(INITIAL);
  const alive = useRef(true);

  const { loading, data, error } = state;
  const views = useMemo(() => stationViews(data), [data]);

  const load = useCallback(async () => {
    try {
      const res = await listWeatherStations();
      if (alive.current) setState({ loading: false, data: res, error: null });
    } catch (err) {
      if (alive.current) setState((prev) => ({ ...prev, loading: false, error: errorMessage(err) }));
    }
  }, []);

  useEffect(() => {
    alive.current = true;
    return () => { alive.current = false; };
  }, []);

  useVisibleInterval(load, POLL_MS);

  const sensorOn = Boolean(data?.sensorEnabled);
  const staleCount = views.filter((v) => v.stale || v.neverRead).length;

  let body;
  if (loading) {
    body = <div className="lat-panel col-span-all weather-loading">Loading…</div>;
  } else if (!data && error) {
    body = (
      <div className="weather-alerts">
        <div className="lat-alert crit">Weather unavailable: {error}</div>
      </div>
    );
  } else {
    body = (
      <>
        {error ? (
          <div className="weather-alerts">
            <div className="lat-alert warn">Last refresh failed: {error}</div>
          </div>
        ) : null}
        {views.length === 0 ? (
          <div className="lat-panel col-span-all">
            <div className="lat-empty">No weather stations heard.</div>
            <div className="weather-hint">
              A node with a weather sensor shares its readings with every node on the mesh.
            </div>
          </div>
        ) : views.map((v) => <StationPanel key={v.key} view={v} />)}
      </>
    );
  }

  return (
    <>
      <div className="lat-topbar">
        <div className="node-id">
          WEATHER
          <span className="ip">{views.length} {views.length === 1 ? 'STATION' : 'STATIONS'}</span>
        </div>
        <div className="chips">
          <span className={`lat-chip ${sensorOn ? 'ok' : ''}`.trim()}>{sensorOn ? 'SENSOR ON' : 'NO LOCAL SENSOR'}</span>
          {staleCount > 0 ? <span className="lat-chip warn">{staleCount} STALE</span> : null}
        </div>
      </div>
      <div className="lat-view-header">
        <div>
          <h2>◇ Weather</h2>
          <div className="crumb">Stations on the mesh · also shown in ATAK</div>
        </div>
        <div className="lat-view-toolbar weather-toolbar">
          <button type="button" className="lat-btn ghost" onClick={load}>REFRESH</button>
        </div>
      </div>
      <div className="lat-body grid-3">{body}</div>
    </>
  );
}
