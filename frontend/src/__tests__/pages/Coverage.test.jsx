// =============================================================================
// Coverage.test.jsx — coverage map page states and interaction
// =============================================================================

import React from 'react';
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, cleanup, fireEvent } from '@testing-library/react';
import { CoverageGrade } from '../../gen/openmanet/coverage/v1/coverage_pb.js';

const state = { snapshot: null };

vi.mock('../../hooks/useCoverageMap.js', () => ({
  useCoverageMap: () => state.snapshot,
}));

import CoveragePage from '../../pages/Coverage.jsx';

const NOW_S = Math.floor(Date.now() / 1000);

function cell(row, col, grade, extra = {}) {
  return {
    row, col, grade, signalDbm: -66, samples: 5, maxLinks: 3, noLinkPct: 0,
    lastSeen: { seconds: BigInt(NOW_S - 120) }, ...extra,
  };
}

// The daemon's grid cell for a spot in Eagle Mountain, UT.
const LAT_STEP = 50 / 111_320;
const ROW = Math.floor(40.3141 / LAT_STEP);
const COL = Math.floor(-112.0105 / (LAT_STEP / Math.cos(((ROW + 0.5) * LAT_STEP * Math.PI) / 180)));

function measuredBy(container) {
  const row = Array.from(container.querySelectorAll('.kv')).find((el) => el.textContent.startsWith('Measured by'));
  return row?.querySelector('.v').textContent;
}

function fullResponse() {
  return {
    enabled: true,
    local: {
      hostname: 'alpha', cellMeters: 50,
      cells: [cell(ROW, COL, CoverageGrade.GOOD), cell(ROW, COL + 1, CoverageGrade.NO_LINK, { signalDbm: 0, maxLinks: 0 })],
    },
    remote: [{ hostname: 'bravo', cellMeters: 50, cells: [cell(ROW + 1, COL, CoverageGrade.FAIR)] }],
  };
}

beforeEach(() => { state.snapshot = null; });
afterEach(() => { cleanup(); });

describe('TestCoverageStates', () => {
  it('shows loading before the first answer', () => {
    render(<CoveragePage />);
    expect(screen.getByText('Loading coverage…')).toBeInTheDocument();
    expect(screen.getByText('LOADING')).toBeInTheDocument();
  });

  it('says so on an older daemon', () => {
    state.snapshot = { unavailable: true };
    render(<CoveragePage />);
    expect(screen.getByText(/does not serve a coverage map/)).toBeInTheDocument();
    expect(screen.getByText('NOT SUPPORTED')).toBeInTheDocument();
  });

  it('shows the error when the first request fails', () => {
    state.snapshot = { res: null, error: 'db closed' };
    const { container } = render(<CoveragePage />);
    expect(container.querySelector('.lat-alert.crit').textContent).toContain('db closed');
  });

  it('says recording is off when the node has it disabled', () => {
    state.snapshot = { res: { enabled: false, remote: [] } };
    render(<CoveragePage />);
    expect(screen.getByText('Coverage recording is off on this node.')).toBeInTheDocument();
    expect(screen.getByText('OFF')).toBeInTheDocument();
  });

  it('shows an empty map before any readings', () => {
    state.snapshot = { res: { enabled: true, local: { hostname: 'alpha', cellMeters: 50, cells: [] }, remote: [] } };
    render(<CoveragePage />);
    expect(screen.getByText('No coverage readings yet.')).toBeInTheDocument();
    expect(screen.getByText('RECORDING')).toBeInTheDocument();
  });
});

describe('TestCoverageMap', () => {
  it('draws one rect per merged cell, colored by grade', () => {
    state.snapshot = { res: fullResponse() };
    const { container } = render(<CoveragePage />);
    expect(container.querySelectorAll('rect.cov-cell')).toHaveLength(3);
    expect(container.querySelectorAll('rect.cov-cell.good')).toHaveLength(1);
    expect(container.querySelectorAll('rect.cov-cell.fair')).toHaveLength(1);
    expect(container.querySelectorAll('rect.cov-cell.none')).toHaveLength(1);
    expect(screen.getByText('2 NODES')).toBeInTheDocument();
    expect(screen.getByText(/50 m cells/)).toBeInTheDocument();
  });

  it("marks each node's last spot and a scale bar", () => {
    state.snapshot = { res: fullResponse() };
    const { container } = render(<CoveragePage />);
    expect(container.querySelectorAll('.cov-node')).toHaveLength(2);
    expect(container.querySelector('.cov-node.local text').textContent).toBe('ALPHA');
    expect(container.querySelector('.cov-scale text').textContent).toMatch(/\d+ m/);
  });

  it('shows cell detail when a cell is chosen, and clears it on a second tap', () => {
    state.snapshot = { res: fullResponse() };
    const { container } = render(<CoveragePage />);
    expect(screen.getByText('Tap a cell for detail.')).toBeInTheDocument();

    const good = container.querySelector('rect.cov-cell.good');
    fireEvent.click(good);
    expect(good.getAttribute('aria-pressed')).toBe('true');
    expect(screen.getByText('-66 dBm')).toBeInTheDocument();
    expect(measuredBy(container)).toBe('ALPHA');

    fireEvent.click(good);
    expect(screen.getByText('Tap a cell for detail.')).toBeInTheDocument();
  });

  it('chooses a cell from the keyboard', () => {
    state.snapshot = { res: fullResponse() };
    const { container } = render(<CoveragePage />);
    const fair = container.querySelector('rect.cov-cell.fair');
    fireEvent.keyDown(fair, { key: 'Enter' });
    expect(fair.classList.contains('selected')).toBe(true);
    expect(measuredBy(container)).toBe('BRAVO');
  });

  it('lists dead spots with a grid reference and selects them', () => {
    state.snapshot = { res: fullResponse() };
    const { container } = render(<CoveragePage />);
    const rows = container.querySelectorAll('.cov-dead button');
    expect(rows).toHaveLength(1);
    expect(rows[0].querySelector('.cov-dead-ref').textContent).toMatch(/^\d{1,2}[C-X] [A-Z]{2} \d+ \d+$/);
    expect(rows[0].querySelector('.cov-dead-age').textContent).toBe('2m ago');

    fireEvent.click(rows[0]);
    expect(container.querySelector('rect.cov-cell.none').classList.contains('selected')).toBe(true);
    expect(rows[0].getAttribute('aria-pressed')).toBe('true');
  });

  it('counts cells per grade in the summary', () => {
    state.snapshot = { res: fullResponse() };
    const { container } = render(<CoveragePage />);
    const kv = Array.from(container.querySelectorAll('.kv')).map((el) => el.textContent);
    expect(kv).toContain('Cells3');
    expect(kv).toContain('GOOD1');
    expect(kv).toContain('WEAK0');
    expect(kv).toContain('Nodes reporting2');
  });

  it('keeps the map and shows the error on a failed refresh', () => {
    state.snapshot = { res: fullResponse(), error: 'timeout' };
    const { container } = render(<CoveragePage />);
    expect(container.querySelectorAll('rect.cov-cell')).toHaveLength(3);
    expect(screen.getByText('Refresh failed: timeout')).toBeInTheDocument();
  });
});
