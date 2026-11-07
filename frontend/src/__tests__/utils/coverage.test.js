// =============================================================================
// coverage.test.js — coverage map grid math and aggregation
// =============================================================================

import { describe, it, expect } from 'vitest';
import { CoverageGrade } from '../../gen/openmanet/coverage/v1/coverage_pb.js';
import {
  cellBounds,
  deadSpots,
  formatAgoMs,
  formatMeters,
  mergeCoverage,
  projectCells,
  scaleBarMeters,
  summarize,
} from '../../utils/coverage.js';

const T0 = 1_762_480_000; // seconds

function cell(row, col, grade, extra = {}) {
  return {
    row, col, grade, signalDbm: -65, samples: 3, maxLinks: 2,
    lastSeen: { seconds: BigInt(T0) }, ...extra,
  };
}

function report(hostname, cells, cellMeters = 50) {
  return { hostname, primaryMac: `${hostname}-mac`, cellMeters, cells };
}

describe('TestCellBounds', () => {
  it('matches the daemon grid for a point in Eagle Mountain', () => {
    // The daemon keys a point with row = floor(lat / latStep) and
    // col = floor(lon / lonStep(row)); the cell it names must hold it.
    const lat = 40.3141;
    const lon = -112.0105;
    const latStep = 50 / 111_320;
    const row = Math.floor(lat / latStep);
    const centerLat = ((row + 0.5) * latStep * Math.PI) / 180;
    const lonStep = latStep / Math.cos(centerLat);
    const col = Math.floor(lon / lonStep);

    const b = cellBounds(row, col, 50);
    expect(b.south).toBeLessThanOrEqual(lat);
    expect(b.north).toBeGreaterThan(lat);
    expect(b.west).toBeLessThanOrEqual(lon);
    expect(b.east).toBeGreaterThan(lon);
  });

  it('keeps cells about square on the ground', () => {
    const b = cellBounds(89_000, -300_000, 50);
    const midLat = (((b.south + b.north) / 2) * Math.PI) / 180;
    const heightM = (b.north - b.south) * 111_320;
    const widthM = (b.east - b.west) * 111_320 * Math.cos(midLat);
    expect(heightM).toBeCloseTo(50, 6);
    expect(widthM).toBeCloseTo(50, 3);
  });

  it('stays finite at the pole', () => {
    const b = cellBounds(Math.floor(90 / (50 / 111_320)), 0, 50);
    expect(Number.isFinite(b.east)).toBe(true);
  });
});

describe('TestMergeCoverage', () => {
  it('returns nothing for null, empty and cell-less responses', () => {
    expect(mergeCoverage(null)).toEqual({ cells: [], nodes: [] });
    expect(mergeCoverage({})).toEqual({ cells: [], nodes: [] });
    expect(mergeCoverage({ local: report('a', []), remote: [] })).toEqual({ cells: [], nodes: [] });
    expect(mergeCoverage({ local: report('a', [cell(1, 1, CoverageGrade.GOOD)], 0) }).cells).toEqual([]);
  });

  it('keeps the better grade where two nodes measured one cell', () => {
    const res = {
      local: report('alpha', [cell(1, 1, CoverageGrade.WEAK, { signalDbm: -88, maxLinks: 1 })]),
      remote: [report('bravo', [cell(1, 1, CoverageGrade.GOOD, { signalDbm: -60, maxLinks: 4 })])],
    };
    const { cells, nodes } = mergeCoverage(res);
    expect(cells).toHaveLength(1);
    expect(cells[0].grade).toBe(CoverageGrade.GOOD);
    expect(cells[0].signalDbm).toBe(-60);
    expect(cells[0].samples).toBe(6);
    expect(cells[0].maxLinks).toBe(4);
    expect(cells[0].nodes).toEqual(['alpha', 'bravo']);
    expect(nodes.map((n) => n.name)).toEqual(['alpha', 'bravo']);
    expect(nodes[0].local).toBe(true);
    expect(nodes[1].local).toBe(false);
  });

  it('keeps cells of different sizes apart', () => {
    const res = {
      local: report('alpha', [cell(1, 1, CoverageGrade.GOOD)], 50),
      remote: [report('bravo', [cell(1, 1, CoverageGrade.GOOD)], 100)],
    };
    expect(mergeCoverage(res).cells).toHaveLength(2);
  });

  it("marks each node's most recent cell as its last spot", () => {
    const res = {
      local: report('alpha', [
        cell(1, 1, CoverageGrade.GOOD, { lastSeen: { seconds: BigInt(T0) } }),
        cell(2, 2, CoverageGrade.GOOD, { lastSeen: { seconds: BigInt(T0 + 60) } }),
      ]),
    };
    const { nodes } = mergeCoverage(res);
    expect(nodes[0].key).toBe('50:2:2');
    expect(nodes[0].lastSeen).toBe((T0 + 60) * 1000);
  });

  it('names a node by its MAC when it has no hostname', () => {
    const rep = report('', [cell(1, 1, CoverageGrade.FAIR)]);
    expect(mergeCoverage({ local: rep }).nodes[0].name).toBe('-mac');
  });
});

describe('TestSummarize', () => {
  it('counts zero cells', () => {
    const s = summarize([]);
    expect(s.total).toBe(0);
    expect(s.lastSeen).toBe(0);
    expect(s.counts[CoverageGrade.GOOD]).toBe(0);
  });

  it('counts cells per grade', () => {
    const { cells } = mergeCoverage({
      local: report('a', [
        cell(1, 1, CoverageGrade.GOOD),
        cell(1, 2, CoverageGrade.NO_LINK),
        cell(1, 3, CoverageGrade.NO_LINK),
      ]),
    });
    const s = summarize(cells);
    expect(s.total).toBe(3);
    expect(s.counts[CoverageGrade.NO_LINK]).toBe(2);
    expect(s.counts[CoverageGrade.GOOD]).toBe(1);
    expect(s.lastSeen).toBe(T0 * 1000);
  });
});

describe('TestProjectCells', () => {
  it('lays out nothing for no cells', () => {
    expect(projectCells([])).toEqual({ rects: [], width: 0, height: 0 });
  });

  it('spans two cells for two neighbours in one row', () => {
    const { cells } = mergeCoverage({
      local: report('a', [cell(89_000, -300_000, CoverageGrade.GOOD), cell(89_000, -299_999, CoverageGrade.FAIR)]),
    });
    const { width, height } = projectCells(cells);
    expect(width).toBeCloseTo(100, 0);
    expect(height).toBeCloseTo(50, 0);
  });

  // Rows are sized at their own latitude, so columns in neighbouring rows
  // are staggered like bricks; only same-row x and per-row y line up.
  it('places neighbours side by side in meters', () => {
    const { cells } = mergeCoverage({
      local: report('a', [cell(89_000, -300_000, CoverageGrade.GOOD), cell(89_000, -299_999, CoverageGrade.FAIR), cell(89_001, -300_000, CoverageGrade.WEAK)]),
    });
    const { rects, height } = projectCells(cells);
    expect(height).toBeCloseTo(100, 0);

    const at = (row, col) => rects.find((r) => r.cell.row === row && r.cell.col === col);
    expect(at(89_000, -299_999).x - at(89_000, -300_000).x).toBeCloseTo(50, 0);
    // North is up: the higher row sits at the top.
    expect(at(89_001, -300_000).y).toBeCloseTo(0, 6);
    expect(at(89_000, -300_000).y).toBeCloseTo(50, 0);
    expect(at(89_000, -300_000).w).toBeCloseTo(50, 0);
  });
});

describe('TestDeadSpots', () => {
  it('lists no-link cells newest first, up to the limit', () => {
    const { cells } = mergeCoverage({
      local: report('a', [
        cell(1, 1, CoverageGrade.NO_LINK, { lastSeen: { seconds: BigInt(T0) } }),
        cell(1, 2, CoverageGrade.NO_LINK, { lastSeen: { seconds: BigInt(T0 + 5) } }),
        cell(1, 3, CoverageGrade.WEAK),
        cell(1, 4, CoverageGrade.NO_LINK, { lastSeen: { seconds: BigInt(T0 - 5) } }),
      ]),
    });
    const dead = deadSpots(projectCells(cells).rects, 2);
    expect(dead.map((r) => r.cell.col)).toEqual([2, 1]);
  });
});

describe('TestScaleBarMeters', () => {
  it.each([
    [0, 0],
    [-5, 0],
    [NaN, 0],
    [50, 10],
    [400, 100],
    [900, 200],
    [2400, 500],
    [10_000, 2000],
  ])('picks a round length for a %s m map', (width, want) => {
    expect(scaleBarMeters(width)).toBe(want);
  });
});

describe('TestCoverageFormat', () => {
  it('formats ages', () => {
    const now = 10_000_000;
    expect(formatAgoMs(0, now)).toBe('—');
    expect(formatAgoMs(now - 10_000, now)).toBe('just now');
    expect(formatAgoMs(now - 5 * 60_000, now)).toBe('5m ago');
    expect(formatAgoMs(now - 3 * 3_600_000, now)).toBe('3h ago');
    expect(formatAgoMs(now - 2 * 86_400_000, now)).toBe('2d ago');
  });

  it('formats scale lengths', () => {
    expect(formatMeters(200)).toBe('200 m');
    expect(formatMeters(2000)).toBe('2 km');
  });
});
