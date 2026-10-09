// =============================================================================
// navigation.spec.js — Primary UI <-> Advanced (LuCI) handoff, deep links
// =============================================================================
//
// LuCI is reached through the real Go frontend server's reverse proxy
// (frontend.luciProxy) in front of e2e/support/fake-luci.mjs.

import { test, expect } from '@playwright/test';
import { PAGES, LUCI_PATH, advancedEntry, gotoPage, pageHeading } from './support/helpers.js';
import { external } from './support/env.js';

// Against a real node the upstream is LuCI itself, which has none of the
// fake's markers; those tests check reachability only.
const FAKE = !external;

async function expectLuci(page) {
  await expect(page).toHaveURL(new RegExp(`${LUCI_PATH}`));
  if (FAKE) await expect(page.getByRole('heading', { name: /LuCI/ })).toBeVisible();
  else await expect(page.locator('body')).not.toBeEmpty();
}

test.describe('Advanced handoff', () => {
  test('primary -> Advanced -> back to primary via the LuCI page link', async ({ page }, testInfo) => {
    await gotoPage(page, PAGES[0]);
    const link = await advancedEntry(page, testInfo);
    await expect(link).toHaveAttribute('href', LUCI_PATH);

    const css = page.waitForResponse((r) => r.url().includes('/luci-static/') && r.ok());
    await link.click();
    await expectLuci(page);
    // The page's own assets came through the same origin's proxy too.
    const asset = await css;
    if (FAKE) expect(asset.headers()['x-e2e-fake-luci']).toBe('1');

    if (FAKE) await page.getByRole('link', { name: 'Back to OpenMANET' }).click();
    else await page.goto('/');
    await expect(page).toHaveURL(/\/$/);
    await expect(pageHeading(page, 'Dashboard')).toBeVisible();
  });

  test('browser Back from LuCI restores the SPA page it left', async ({ page }, testInfo) => {
    await gotoPage(page, { path: '/gps', heading: 'GPS / GNSS' });
    await (await advancedEntry(page, testInfo)).click();
    await expectLuci(page);

    await page.goBack();
    await expect(page).toHaveURL(/\/gps$/);
    await expect(pageHeading(page, 'GPS / GNSS')).toBeVisible();

    await page.goForward();
    await expectLuci(page);
  });

  test('LuCI deep link loads directly and the ubus endpoint is proxied', async ({ page, request }) => {
    test.skip(!FAKE, 'asserts the fake upstream\'s echo of the proxied path');
    await page.goto(`${LUCI_PATH}admin/system/system`);
    await expect(page.locator('#luci-path')).toHaveText('/cgi-bin/luci/admin/system/system');

    const ubus = await request.post('/ubus/', { data: { jsonrpc: '2.0', id: 1, method: 'call', params: [] } });
    expect(ubus.ok()).toBe(true);
    expect((await ubus.json()).result[1]).toEqual({ e2e: true });
  });

  test('daemon reports the proxy so the shell offers the entry', async ({ request }) => {
    const info = await request.get('/api/system/info');
    expect(info.ok()).toBe(true);
    expect((await info.json()).luci_proxy_enabled).toBe(true);
  });
});

test.describe('Deep links and reload', () => {
  for (const target of PAGES) {
    test(`${target.path} renders from a cold load and after reload`, async ({ page }) => {
      await gotoPage(page, target);
      await page.reload();
      await expect(pageHeading(page, target.heading)).toBeVisible();
      await expect(page).toHaveURL(new RegExp(`${target.path.replace(/\//g, '\\/')}$`));
    });
  }
});
