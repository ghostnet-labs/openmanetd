// =============================================================================
// coverage.js — Coverage map grid math and aggregation (GHO-94)
// =============================================================================
//
// Mirrors internal/coverage on the daemon: cells are cell_meters squares.
// Rows count latitude bands from the equator; columns count longitude steps
// sized at the band's center latitude, so cells stay about square and every
// node lands a spot in the same cell.

import { CoverageGrade } from '../gen/openmanet/coverage/v1/coverage_pb.js';

const METERS_PER_DEG_LAT = 111_320;
const MIN_COS = 0.01;

// Worst to best, so a higher rank is better coverage.
const GRADE_RANK = {
  [CoverageGrade.NO_LINK]: 0,
  [CoverageGrade.WEAK]: 1,
  [CoverageGrade.FAIR]: 2,
  [CoverageGrade.GOOD]: 3,
};

export const GRADE_INFO = {
  [CoverageGrade.GOOD]: { key: 'good', label: 'GOOD', status: 'ok' },
  [CoverageGrade.FAIR]: { key: 'fair', label: 'FAIR', status: 'warn' },
  [CoverageGrade.WEAK]: { key: 'weak', label: 'WEAK', status: 'crit' },
  [CoverageGrade.NO_LINK]: { key: 'none', label: 'NO LINK', status: 'crit' },
};

export const GRADE_ORDER = [CoverageGrade.GOOD, CoverageGrade.FAIR, CoverageGrade.WEAK, CoverageGrade.NO_LINK];

// cellBounds returns a cell's {south, west, north, east} in degrees.
export function cellBounds(row, col, cellMeters) {
  const latStep = cellMeters / METERS_PER_DEG_LAT;
  const centerLat = ((row + 0.5) * latStep * Math.PI) / 180;
  const lonStep = latStep / Math.max(Math.cos(centerLat), MIN_COS);
  const south = row * latStep;
  const west = col * lonStep;
  return { south, west, north: south + latStep, east: west + lonStep };
}

function tsMillis(ts) {
  return ts?.seconds ? Number(ts.seconds) * 1000 : 0;
}

// mergeCoverage folds every node's report into one cell list. Where two
// nodes measured the same cell, the better grade wins and both nodes are
// listed. nodes holds one entry per reporting node with the cell it
// measured last (its latest known spot). Reports with no cell size are
// skipped.
export function mergeCoverage(res) {
  const reports = [];
  if (res?.local) reports.push(res.local);
  for (const r of res?.remote ?? []) reports.push(r);

  const merged = new Map();
  const nodes = [];

  for (const rep of reports) {
    const cellMeters = rep.cellMeters;
    if (!cellMeters || !rep.cells?.length) continue;
    const node = rep.hostname || rep.primaryMac || '?';
    let latest = rep.cells[0];
    for (const c of rep.cells) {
      if (tsMillis(c.lastSeen) > tsMillis(latest.lastSeen)) latest = c;
    }
    nodes.push({
      name: node,
      local: rep === res.local,
      key: `${cellMeters}:${latest.row}:${latest.col}`,
      lastSeen: tsMillis(latest.lastSeen),
    });

    for (const c of rep.cells) {
      const key = `${cellMeters}:${c.row}:${c.col}`;
      const seen = tsMillis(c.lastSeen);
      const cur = merged.get(key);

      if (!cur) {
        merged.set(key, {
          key, row: c.row, col: c.col, cellMeters,
          grade: c.grade, signalDbm: c.signalDbm, samples: c.samples,
          maxLinks: c.maxLinks, lastSeen: seen, nodes: [node],
        });
        continue;
      }

      cur.nodes.push(node);
      cur.samples += c.samples;
      cur.maxLinks = Math.max(cur.maxLinks, c.maxLinks);
      cur.lastSeen = Math.max(cur.lastSeen, seen);
      if ((GRADE_RANK[c.grade] ?? -1) > (GRADE_RANK[cur.grade] ?? -1)) {
        cur.grade = c.grade;
        cur.signalDbm = c.signalDbm;
      }
    }
  }

  return { cells: Array.from(merged.values()), nodes };
}

// summarize counts cells per grade.
export function summarize(cells) {
  const counts = { [CoverageGrade.GOOD]: 0, [CoverageGrade.FAIR]: 0, [CoverageGrade.WEAK]: 0, [CoverageGrade.NO_LINK]: 0 };
  let lastSeen = 0;
  for (const c of cells) {
    if (c.grade in counts) counts[c.grade] += 1;
    lastSeen = Math.max(lastSeen, c.lastSeen);
  }
  return { total: cells.length, counts, lastSeen };
}

// projectCells lays cells out in a local equirectangular frame (meters
// east and north of the south-west corner), which is accurate to well
// under a cell over the few kilometers a mesh covers.
export function projectCells(cells) {
  if (cells.length === 0) return { rects: [], width: 0, height: 0 };

  const bounds = cells.map((c) => ({ c, b: cellBounds(c.row, c.col, c.cellMeters) }));
  let south = Infinity;
  let west = Infinity;
  let north = -Infinity;
  let east = -Infinity;
  for (const { b } of bounds) {
    south = Math.min(south, b.south);
    west = Math.min(west, b.west);
    north = Math.max(north, b.north);
    east = Math.max(east, b.east);
  }

  const midLat = (((south + north) / 2) * Math.PI) / 180;
  const mPerDegLon = METERS_PER_DEG_LAT * Math.max(Math.cos(midLat), MIN_COS);
  const width = (east - west) * mPerDegLon;
  const height = (north - south) * METERS_PER_DEG_LAT;

  const rects = bounds.map(({ c, b }) => ({
    cell: c,
    x: (b.west - west) * mPerDegLon,
    y: (north - b.north) * METERS_PER_DEG_LAT,
    w: (b.east - b.west) * mPerDegLon,
    h: (b.north - b.south) * METERS_PER_DEG_LAT,
    lat: (b.south + b.north) / 2,
    lon: (b.west + b.east) / 2,
  }));

  return { rects, width, height };
}

// deadSpots returns no-link cells, most recently seen first.
export function deadSpots(rects, limit) {
  return rects
    .filter((r) => r.cell.grade === CoverageGrade.NO_LINK)
    .sort((a, b) => b.cell.lastSeen - a.cell.lastSeen)
    .slice(0, limit);
}

// scaleBarMeters picks a round scale-bar length near a quarter of the map.
export function scaleBarMeters(widthMeters) {
  if (!(widthMeters > 0)) return 0;
  const target = widthMeters / 4;
  const pow = 10 ** Math.floor(Math.log10(target));
  for (const step of [5, 2, 1]) {
    if (step * pow <= target) return step * pow;
  }
  return pow;
}

// formatAgoMs renders a millisecond timestamp as a short age.
export function formatAgoMs(ms, now = Date.now()) {
  if (!ms) return '—';
  const s = Math.max(0, Math.floor((now - ms) / 1000));
  if (s < 60) return 'just now';
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m ago`;
  const h = Math.floor(m / 60);
  if (h < 24) return `${h}h ago`;
  return `${Math.floor(h / 24)}d ago`;
}

// formatMeters renders a scale-bar length.
export function formatMeters(m) {
  if (m >= 1000) return `${m / 1000} km`;
  return `${m} m`;
}
