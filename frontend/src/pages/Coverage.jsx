// =============================================================================
// Coverage.jsx — Mesh coverage map (GHO-94)
// =============================================================================
//
// Draws the coverage cells every node records while it moves: each cell is
// graded by how well the node heard the mesh there. This node's map and the
// latest map shared by every other node are merged, so the page shows where
// the whole mesh reaches. There is no basemap yet; the scale bar and the
// MGRS grid references in the inspector and dead-spot list place the cells.

import { useCallback, useMemo, useState } from 'react';
import { useCoverageMap } from '../hooks/useCoverageMap.js';
import { latLonToMGRS } from '../utils/mgrs.js';
import {
  GRADE_INFO,
  GRADE_ORDER,
  deadSpots,
  formatAgoMs,
  formatMeters,
  mergeCoverage,
  projectCells,
  scaleBarMeters,
  summarize,
} from '../utils/coverage.js';
import './Coverage.css';

// Nodes share their maps every 5 minutes; a 30 s poll picks up this node's
// own new cells quickly without loading the daemon.
const POLL_INTERVAL = 30_000;
const DEAD_SPOT_LIMIT = 10;
const EMPTY_SNAPSHOT = {};

function gradeInfo(grade) {
  return GRADE_INFO[grade] ?? GRADE_INFO[GRADE_ORDER[GRADE_ORDER.length - 1]];
}

function mgrs(rect) {
  return latLonToMGRS(rect.lat, rect.lon, 4) ?? `${rect.lat.toFixed(5)}, ${rect.lon.toFixed(5)}`;
}

function cellLabel(rect) {
  const info = gradeInfo(rect.cell.grade);
  return `${info.label} at ${mgrs(rect)}`;
}

function CoverageSvg({ layout, nodeSpots, selectedKey, onSelect }) {
  const pad = Math.max(layout.width, layout.height) * 0.06 + 10;
  const vw = layout.width + pad * 2;
  const vh = layout.height + pad * 2;
  const bar = scaleBarMeters(layout.width) || scaleBarMeters(vw);
  const font = Math.max(vw, vh) * 0.028;
  const spotR = Math.max(vw, vh) * 0.012;

  const onKey = (e, key) => {
    if (e.key === 'Enter' || e.key === ' ') {
      e.preventDefault();
      onSelect(key);
    }
  };

  return (
    <svg
      className="cov-svg"
      viewBox={`${-pad} ${-pad} ${vw} ${vh}`}
      preserveAspectRatio="xMidYMid meet"
      role="img"
      aria-label={`Coverage map, ${layout.rects.length} cells`}
    >
      {layout.rects.map((r) => {
        const info = gradeInfo(r.cell.grade);
        const selected = r.cell.key === selectedKey;
        return (
          <rect
            key={r.cell.key}
            className={`cov-cell ${info.key}${selected ? ' selected' : ''}`}
            x={r.x}
            y={r.y}
            width={r.w}
            height={r.h}
            vectorEffect="non-scaling-stroke"
            role="button"
            tabIndex={0}
            aria-label={cellLabel(r)}
            aria-pressed={selected}
            onClick={() => onSelect(r.cell.key)}
            onKeyDown={(e) => onKey(e, r.cell.key)}
          >
            <title>{cellLabel(r)}</title>
          </rect>
        );
      })}
      {nodeSpots.map(({ node, rect }) => (
        <g key={node.name} className={`cov-node${node.local ? ' local' : ''}`}>
          <circle cx={rect.x + rect.w / 2} cy={rect.y + rect.h / 2} r={spotR} />
          <text x={rect.x + rect.w / 2 + spotR * 1.6} y={rect.y + rect.h / 2} fontSize={font} dominantBaseline="middle">
            {node.name.toUpperCase()}
          </text>
        </g>
      ))}
      {bar > 0 && (
        <g className="cov-scale">
          <line x1={0} y1={layout.height + pad * 0.55} x2={bar} y2={layout.height + pad * 0.55} vectorEffect="non-scaling-stroke" />
          <text x={0} y={layout.height + pad * 0.55 - font * 0.4} fontSize={font}>
            {formatMeters(bar)}
          </text>
        </g>
      )}
    </svg>
  );
}

function CellInspector({ rect }) {
  if (!rect) {
    return <div className="lat-empty">Tap a cell for detail.</div>;
  }
  const { cell } = rect;
  const info = gradeInfo(cell.grade);
  const signal = cell.grade === GRADE_ORDER[GRADE_ORDER.length - 1] && !cell.signalDbm
    ? '—'
    : `${cell.signalDbm} dBm`;
  return (
    <>
      <div className="kv"><span className="k">Grade</span><span className={`v ${info.status}`}>{info.label}</span></div>
      <div className="kv"><span className="k">Grid ref</span><span className="v">{mgrs(rect)}</span></div>
      <div className="kv"><span className="k">Best signal</span><span className="v">{signal}</span></div>
      <div className="kv"><span className="k">Most links</span><span className="v">{cell.maxLinks}</span></div>
      <div className="kv"><span className="k">Readings</span><span className="v">{cell.samples}</span></div>
      <div className="kv"><span className="k">Last reading</span><span className="v">{formatAgoMs(cell.lastSeen)}</span></div>
      <div className="kv"><span className="k">Measured by</span><span className="v">{cell.nodes.join(', ').toUpperCase()}</span></div>
    </>
  );
}

export default function Coverage() {
  const [selectedKey, setSelectedKey] = useState(null);
  const snapshot = useCoverageMap(POLL_INTERVAL) ?? EMPTY_SNAPSHOT;

  const res = snapshot.res;
  const merged = useMemo(() => mergeCoverage(res), [res]);
  const layout = useMemo(() => projectCells(merged.cells), [merged]);
  const summary = useMemo(() => summarize(merged.cells), [merged]);
  const dead = useMemo(() => deadSpots(layout.rects, DEAD_SPOT_LIMIT), [layout]);
  const rectByKey = useMemo(() => new Map(layout.rects.map((r) => [r.cell.key, r])), [layout]);
  const nodeSpots = useMemo(
    () => merged.nodes
      .map((node) => ({ node, rect: rectByKey.get(node.key) }))
      .filter((s) => s.rect),
    [merged, rectByKey],
  );

  const onSelect = useCallback((key) => setSelectedKey((cur) => (cur === key ? null : key)), []);

  const loading = snapshot === EMPTY_SNAPSHOT;
  const enabled = Boolean(res?.enabled);
  const selected = selectedKey ? rectByKey.get(selectedKey) : null;
  const cellMeters = res?.local?.cellMeters || merged.cells[0]?.cellMeters || 0;
  const crumb = cellMeters
    ? `Mesh link quality where nodes have been · ${cellMeters} m cells`
    : 'Mesh link quality where nodes have been';

  let chip = { cls: 'warn', text: 'LOADING' };
  if (snapshot.unavailable) chip = { cls: 'warn', text: 'NOT SUPPORTED' };
  else if (res && enabled) chip = { cls: 'ok', text: 'RECORDING' };
  else if (res) chip = { cls: 'crit', text: 'OFF' };

  let body;
  if (loading) {
    body = <div className="lat-panel col-span-all"><div className="lat-empty">Loading coverage…</div></div>;
  } else if (snapshot.unavailable) {
    body = (
      <div className="lat-panel col-span-all">
        <div className="lat-alert warn">This node&apos;s software does not serve a coverage map. Update the firmware.</div>
      </div>
    );
  } else if (!res) {
    body = (
      <div className="lat-panel col-span-all">
        <div className="lat-alert crit">Coverage map unavailable: {snapshot.error}</div>
      </div>
    );
  } else if (!enabled) {
    body = (
      <div className="lat-panel col-span-all">
        <div className="lat-empty">Coverage recording is off on this node.</div>
        <div className="cov-hint">Turn it on with tak.coverage.enable in the node config.</div>
      </div>
    );
  } else {
    body = (
      <>
        <div className="lat-panel cov-map-panel">
          <div className="panel-head"><h3>Coverage map</h3></div>
          {snapshot.error && <div className="lat-alert crit">Refresh failed: {snapshot.error}</div>}
          {layout.rects.length === 0 ? (
            <>
              <div className="lat-empty">No coverage readings yet.</div>
              <div className="cov-hint">Nodes record a cell once they have a GPS fix and move.</div>
            </>
          ) : (
            <>
              <div className="cov-canvas">
                <CoverageSvg layout={layout} nodeSpots={nodeSpots} selectedKey={selectedKey} onSelect={onSelect} />
              </div>
              <ul className="cov-legend" aria-label="Legend">
                {GRADE_ORDER.map((g) => (
                  <li key={g}>
                    <span className={`cov-swatch ${GRADE_INFO[g].key}`} aria-hidden="true" />
                    {GRADE_INFO[g].label}
                  </li>
                ))}
                <li><span className="cov-swatch node" aria-hidden="true" />NODE · LAST SPOT</li>
              </ul>
            </>
          )}
        </div>
        <div className="cov-side">
          <div className="lat-panel">
            <div className="panel-head"><h3>Summary</h3></div>
            <div className="kv"><span className="k">Cells</span><span className="v accent">{summary.total}</span></div>
            {GRADE_ORDER.map((g) => (
              <div className="kv" key={g}>
                <span className="k">{GRADE_INFO[g].label}</span>
                <span className={`v ${summary.counts[g] ? GRADE_INFO[g].status : ''}`}>{summary.counts[g]}</span>
              </div>
            ))}
            <div className="kv"><span className="k">Nodes reporting</span><span className="v">{merged.nodes.length}</span></div>
            <div className="kv"><span className="k">Last reading</span><span className="v">{formatAgoMs(summary.lastSeen)}</span></div>
          </div>
          <div className="lat-panel">
            <div className="panel-head"><h3>Selected cell</h3></div>
            <CellInspector rect={selected} />
          </div>
          <div className="lat-panel">
            <div className="panel-head"><h3>Dead spots</h3></div>
            {dead.length === 0 ? (
              <div className="lat-empty">No dead spots.</div>
            ) : (
              <ul className="cov-dead">
                {dead.map((r) => (
                  <li key={r.cell.key}>
                    <button
                      type="button"
                      className={r.cell.key === selectedKey ? 'lat-btn cov-dead-btn' : 'lat-btn ghost cov-dead-btn'}
                      onClick={() => onSelect(r.cell.key)}
                      aria-pressed={r.cell.key === selectedKey}
                    >
                      <span className="cov-dead-ref">{mgrs(r)}</span>
                      <span className="cov-dead-age">{formatAgoMs(r.cell.lastSeen)}</span>
                    </button>
                  </li>
                ))}
              </ul>
            )}
          </div>
        </div>
      </>
    );
  }

  return (
    <>
      <div className="lat-topbar">
        <div className="node-id">
          COVERAGE
          <span className="ip">{`${merged.nodes.length} NODE${merged.nodes.length === 1 ? '' : 'S'}`}</span>
        </div>
        <div className="chips">
          <span className={`lat-chip ${chip.cls}`}>
            <span className="dot" />
            {chip.text}
          </span>
        </div>
      </div>
      <div className="lat-view-header">
        <div>
          <h2>◇ Coverage</h2>
          <div className="crumb">{crumb}</div>
        </div>
      </div>
      <div className="lat-body coverage-body">{body}</div>
    </>
  );
}
