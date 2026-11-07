// =============================================================================
// DashboardWeather.jsx — Dashboard KPI panel for the nearest weather station
// =============================================================================
//
// Shows this node's own station when it has a sensor, otherwise the first
// fresh station heard over the mesh. Renders nothing when no station is
// heard (or the daemon has no WeatherService), so nodes without weather
// keep the two-panel KPI row.

import { useMemo } from 'react';
import { weatherSummary } from '../utils/weather.js';

export default function DashboardWeather({ stations }) {
  const summary = useMemo(() => weatherSummary(stations), [stations]);
  if (!summary) return null;

  const { station, count, staleCount } = summary;
  const old = station.stale || station.neverRead;
  const tempClass = old ? 'muted' : '';
  const where = station.local ? 'here' : station.name;
  const staleNote = staleCount > 0 ? ` · ${staleCount} stale` : '';

  return (
    <div className="lat-panel dashboard-weather">
      <div className="panel-head"><h3>Weather · {where}</h3></div>
      <div className={`big-num ${tempClass}`.trim()}>{station.tempF}</div>
      <div className="kv"><span className="k">Wind</span><span className="v accent">{station.wind} · {station.direction}</span></div>
      <div className="kv"><span className="k">Gust</span><span className="v">{station.gust}</span></div>
      <div className="kv"><span className="k">Pressure</span><span className="v">{station.pressure}</span></div>
      <div className="kv"><span className="k">Trend · 3 h</span><span className={`v ${station.trend.className}`.trim()}>{station.trend.label}</span></div>
      <div className="kv"><span className="k">Updated</span><span className={`v ${old ? 'warn' : ''}`.trim()}>{station.age}</span></div>
      <div className="kv"><span className="k">Stations</span><span className="v">{count}{staleNote}</span></div>
    </div>
  );
}
