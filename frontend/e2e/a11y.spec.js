// =============================================================================
// a11y.spec.js — axe-core scan of the main pages against a baseline
// =============================================================================
//
// Every page is scanned for WCAG 2.1 A/AA rules. Serious and critical
// violations fail the test unless the rule is already listed for that page
// in a11y-baseline.json: the baseline records what existed when the suite
// was introduced so the scan blocks regressions without blocking on
// pre-existing debt. Fix a baselined rule, then drop it from the file (the
// test annotates baseline entries that no longer fire). Regenerate with
// E2E_UPDATE_A11Y_BASELINE=1 pnpm run e2e -- a11y  (review the diff!).

import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { test, expect } from '@playwright/test';
import AxeBuilder from '@axe-core/playwright';
import { PAGES, gotoPage, mockAuth, mockSetupStatus } from './support/helpers.js';

const BASELINE_FILE = path.join(path.dirname(fileURLToPath(import.meta.url)), 'a11y-baseline.json');
const UPDATE = process.env.E2E_UPDATE_A11Y_BASELINE === '1';
const BLOCKING = new Set(['serious', 'critical']);

function readBaseline() {
  try {
    return JSON.parse(fs.readFileSync(BASELINE_FILE, 'utf8'));
  } catch {
    return {};
  }
}

const TARGETS = [
  ...PAGES,
  {
    path: '/login',
    prepare: (page) => mockAuth(page, { authenticated: false }),
    ready: (page) => page.getByRole('button', { name: /authenticate/i }),
  },
  {
    path: '/setup',
    prepare: (page) => mockSetupStatus(page, { isEnabled: true, isSetupComplete: false, hasHalowRadio: true }),
    ready: (page) => page.getByRole('heading', { name: 'OpenMANET Setup Wizard' }),
  },
];

for (const target of TARGETS) {
  test(`axe: ${target.path} has no new serious/critical violations`, async ({ page }, testInfo) => {
    if (target.prepare) await target.prepare(page);
    if (target.ready) {
      await page.goto(target.path);
      await expect(target.ready(page)).toBeVisible();
    } else {
      await gotoPage(page, target);
    }
    await page.waitForLoadState('networkidle');

    const results = await new AxeBuilder({ page })
      .withTags(['wcag2a', 'wcag2aa', 'wcag21a', 'wcag21aa'])
      .analyze();
    const blocking = results.violations.filter((v) => BLOCKING.has(v.impact));
    const found = [...new Set(blocking.map((v) => v.id))].sort();
    const key = `${testInfo.project.name} ${target.path}`;

    if (UPDATE) {
      // Each test rewrites only its own key; workers: 1 keeps this serial.
      const baseline = readBaseline();
      if (found.length) baseline[key] = found; else delete baseline[key];
      const sorted = Object.fromEntries(Object.entries(baseline).sort(([a], [b]) => a.localeCompare(b)));
      fs.writeFileSync(BASELINE_FILE, `${JSON.stringify(sorted, null, 2)}\n`);
      return;
    }

    const allowed = new Set(readBaseline()[key] || []);
    const fresh = blocking.filter((v) => !allowed.has(v.id));
    const fixed = [...allowed].filter((id) => !found.includes(id));
    if (fixed.length) {
      testInfo.annotations.push({ type: 'a11y-baseline-stale', description: `${key}: ${fixed.join(', ')} no longer fire; remove from a11y-baseline.json` });
    }
    const report = fresh.map((v) => `${v.impact} ${v.id}: ${v.help} (${v.nodes.length} nodes, e.g. ${v.nodes[0]?.target.join(' ')})`);
    expect(report, `new serious/critical axe violations on ${key}`).toEqual([]);
  });
}
