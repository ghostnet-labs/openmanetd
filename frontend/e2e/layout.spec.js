// =============================================================================
// layout.spec.js — 1280x800 and 360x640 layout, touch targets, keyboard
// =============================================================================
//
// Runs once per project (desktop 1280x800, mobile 360x640). These are the
// checks .claude/rules/frontend.md asks for by hand before a page ships.

import { test, expect } from '@playwright/test';
import {
  PAGES, advancedEntry, expectNoHorizontalScroll, expectTouchTarget, gotoPage, isMobile,
} from './support/helpers.js';

test.describe('Page layout', () => {
  for (const target of PAGES) {
    test(`${target.path} has no horizontal scroll, squeezed grid cells or page errors`, async ({ page }, testInfo) => {
      const errors = [];
      page.on('pageerror', (err) => errors.push(err.message));
      await gotoPage(page, target);
      await page.waitForLoadState('networkidle');
      await expectNoHorizontalScroll(page);
      expect(errors, 'uncaught exceptions on load').toEqual([]);

      if (!isMobile(testInfo)) return;
      // Below 768px every .lat-body grid is one column, so each cell should
      // span (nearly) the full content width. A stray span-N rule once left
      // dashboard and GPS panels 26px wide without any horizontal scroll.
      const cells = await page.evaluate(() => [...document.querySelectorAll('.lat-body')].flatMap((grid) => {
        const cs = getComputedStyle(grid);
        const inner = grid.clientWidth - parseFloat(cs.paddingLeft) - parseFloat(cs.paddingRight);
        return [...grid.children]
          .filter((c) => c.getBoundingClientRect().width > 0)
          .map((c) => ({ cls: c.className, width: c.getBoundingClientRect().width, inner }));
      }));
      for (const cell of cells) {
        expect(cell.width, `.lat-body cell "${cell.cls}" width`).toBeGreaterThanOrEqual(cell.inner * 0.9);
      }
    });
  }
});

test.describe('Touch targets', () => {
  test('Advanced entry meets the 44px floor', async ({ page }, testInfo) => {
    await gotoPage(page, PAGES[0]);
    const link = await advancedEntry(page, testInfo);
    const box = await link.boundingBox();
    expect(box.height).toBeGreaterThanOrEqual(44);
  });

  test('mobile shell: bottom tabs and More sheet items meet the 44px floor', async ({ page }, testInfo) => {
    test.skip(!isMobile(testInfo), 'bottom tab bar is the mobile shell only');
    await gotoPage(page, PAGES[0]);
    const tabs = page.locator('.bottom-tab-bar .tab-item');
    await expect(tabs).toHaveCount(5);
    for (const tab of await tabs.all()) await expectTouchTarget(tab);

    await page.getByRole('button', { name: 'More' }).click();
    const items = page.locator('.tab-sheet .tab-sheet-item');
    await expect(items.first()).toBeVisible();
    for (const item of await items.all()) await expectTouchTarget(item);
  });

  test('mobile: primary page actions meet the 44px floor', async ({ page }, testInfo) => {
    test.skip(!isMobile(testInfo), 'touch floor applies to the mobile layout');
    await gotoPage(page, { path: '/comms', heading: 'Comms' });
    await expectTouchTarget(page.locator('.ptt-ring'));

    await gotoPage(page, { path: '/settings', heading: 'Settings' });
    const primaries = page.locator('.lat-btn.primary');
    await expect(primaries.first()).toBeVisible();
    for (const btn of await primaries.all()) {
      if (await btn.isVisible()) expect((await btn.boundingBox()).height).toBeGreaterThanOrEqual(44);
    }
  });
});

test.describe('Keyboard access', () => {
  test('Tab reaches the Advanced entry with a visible focus indicator', async ({ page }, testInfo) => {
    await gotoPage(page, PAGES[0]);
    if (isMobile(testInfo)) {
      // The entry lives in the More sheet: reach the More button by
      // keyboard, open it with Enter, then continue tabbing.
      await tabUntil(page, '.bottom-tab-bar button.tab-item');
      await page.keyboard.press('Enter');
    }
    await tabUntil(page, 'a[href="/cgi-bin/luci/"]');

    const focus = await page.evaluate(() => {
      const el = document.activeElement;
      const cs = getComputedStyle(el);
      return {
        focusVisible: el.matches(':focus-visible'),
        outline: cs.outlineStyle !== 'none' && parseFloat(cs.outlineWidth) > 0,
        ring: cs.boxShadow !== 'none',
      };
    });
    expect(focus.focusVisible).toBe(true);
    expect(focus.outline || focus.ring, 'focused Advanced entry draws an outline or ring').toBe(true);

    // Enter follows the link: the keyboard path completes the handoff.
    await page.keyboard.press('Enter');
    await expect(page).toHaveURL(/\/cgi-bin\/luci\//);
  });
});

// tabUntil presses Tab until the focused element matches selector, failing
// after a bounded number of presses so a focus trap cannot hang the test.
async function tabUntil(page, selector, max = 60) {
  for (let i = 0; i < max; i++) {
    await page.keyboard.press('Tab');
    const hit = await page.evaluate((sel) => document.activeElement?.matches(sel) ?? false, selector);
    if (hit) return;
  }
  throw new Error(`focus never reached ${selector} within ${max} Tab presses`);
}
