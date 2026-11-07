// =============================================================================
// latticeBadges.test.js — status badge colors must beat the base text color
// =============================================================================
//
// `.lat-table td` and `.kv .v` set color: var(--text) with higher
// specificity than a bare `.badge-*` rule, which once left every DataTable
// status cell (interface UP/DOWN, jamming state) white. The badge rules must
// carry the td and .kv .v forms too.

import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import path from 'node:path';

const css = readFileSync(
  path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../styles/lattice.css'),
  'utf8',
);

describe('TestLatticeBadges', () => {
  for (const level of ['ok', 'warn', 'crit', 'muted']) {
    it(`badge-${level} reaches table cells and card values`, () => {
      expect(css).toContain(`.lat-table td.badge-${level}`);
      expect(css).toContain(`.kv .v.badge-${level}`);
    });
  }
});
