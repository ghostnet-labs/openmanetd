// =============================================================================
// phones.spec.js — phone onboarding pages on phone and desktop sizes
// =============================================================================
//
// /join is the page a phone opens right after joining a node's Wi-Fi, so it
// must work at 360px as well as on a desktop. Runs once per project
// (desktop 1280x800, mobile 360x640). /onboard/status is answered in the
// browser so the page shows its fullest state (APK present, teams, maps).

import { test, expect } from '@playwright/test';
import { expectNoHorizontalScroll, expectTouchTarget, gotoPage, isMobile } from './support/helpers.js';

const ANDROID_UA = 'Mozilla/5.0 (Linux; Android 14; SM-G556B) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0 Mobile Safari/537.36';
const SIGN_IN_UA = 'Mozilla/5.0 (Linux; Android 14; SM-G556B Build/UP1A; wv) AppleWebKit/537.36 (KHTML, like Gecko) Version/4.0 Chrome/130.0 Mobile Safari/537.36';

function status(baseURL) {
  const base = new URL(baseURL);
  const origin = `http://${base.hostname}:${base.port || 80}`;
  return {
    node: 'node-with-a-rather-long-hostname-7a3f',
    apk: {
      available: true,
      name: 'ATAK-CIV-5.4.0.12-release-with-a-long-file-name.apk',
      size: 215_000_000,
      sha256: '9f2c1e5b7a40d3c8e6f1a2b4c5d6e7f8091a2b3c4d5e6f708192a3b4c5d6e7f8',
      uploaded: '2026-11-07T01:30:00Z',
    },
    apkUrl: `${origin}/onboard/atak.apk`,
    packageUrl: `${origin}/onboard/atak-setup.zip`,
    teams: ['White', 'Yellow', 'Orange', 'Magenta', 'Red', 'Maroon', 'Purple', 'Dark Blue', 'Blue', 'Cyan', 'Teal', 'Green', 'Dark Green', 'Brown'],
    roles: ['Team Member', 'Team Lead', 'HQ', 'Sniper', 'Medic', 'Forward Observer', 'RTO', 'K9'],
    mapSources: ['Manet Satellite', 'Manet Topo'],
  };
}

async function serveStatus(page, baseURL) {
  await page.route('**/onboard/status', (route) => route.fulfill({ json: status(baseURL) }));
}

test.describe('Phone setup page (/join)', () => {
  for (const [label, ua] of [['Chrome', ANDROID_UA], ['Wi-Fi sign-in window', SIGN_IN_UA]]) {
    test(`fits the screen with 44px actions in ${label}`, async ({ browser, baseURL }, testInfo) => {
      const context = await browser.newContext({ ...testInfo.project.use, userAgent: ua });
      const page = await context.newPage();
      const errors = [];
      page.on('pageerror', (err) => errors.push(err.message));
      await serveStatus(page, baseURL);

      await page.goto('/join');
      await expect(page.getByRole('heading', { level: 2, name: 'Set up your phone' })).toBeVisible();
      await page.getByLabel('Callsign').fill('Bravo 6');

      await expectNoHorizontalScroll(page);
      expect(errors, 'uncaught exceptions on load').toEqual([]);
      if (isMobile(testInfo)) {
        await expectTouchTarget(page.getByRole('link', { name: /Download ATAK/ }));
        await expectTouchTarget(page.getByRole('link', { name: 'Set up ATAK' }));
        await expectTouchTarget(page.getByLabel('Callsign'));
      }
      if (ua === SIGN_IN_UA) {
        await expect(page.getByText(/sign-in window/)).toBeVisible();
      }
      await context.close();
    });
  }

  test('loads without login and without the setup wizard', async ({ page, baseURL }) => {
    await serveStatus(page, baseURL);
    await page.goto('/join');
    await expect(page).toHaveURL(/\/join$/);
    await expect(page.getByRole('heading', { level: 2, name: 'Set up your phone' })).toBeVisible();
  });
});

test.describe('Settings › Phones', () => {
  test('fits the screen with 44px primary action', async ({ page, baseURL }, testInfo) => {
    await serveStatus(page, baseURL);
    await gotoPage(page, { path: '/settings/phones', heading: 'Phones' });
    await expect(page.getByText('ATAK for phones')).toBeVisible();
    await expectNoHorizontalScroll(page);
    if (isMobile(testInfo)) {
      await expectTouchTarget(page.getByRole('button', { name: 'Replace APK' }));
    }
  });
});
