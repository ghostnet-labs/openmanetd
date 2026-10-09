// =============================================================================
// helpers.js — Shared steps for the browser end-to-end suite
// =============================================================================

import { expect } from '@playwright/test';

// The SPA routes the suite visits, with the <h2> each one renders. Settings
// sub-pages that need hardware the sample backend does not simulate
// (terminal, firmware flashing) are left out.
export const PAGES = [
  { path: '/', heading: 'Dashboard' },
  { path: '/comms', heading: 'Comms' },
  { path: '/topology', heading: 'Topology' },
  { path: '/gps', heading: 'GPS / GNSS' },
  { path: '/blos', heading: 'BLOS' },
  { path: '/settings', heading: 'Settings' },
  { path: '/settings/wireless', heading: 'Wireless Radios' },
  { path: '/settings/network', heading: 'Network' },
];

export const LUCI_PATH = '/cgi-bin/luci/';
const RPC = '**/rpc/';

export function isMobile(testInfo) {
  return testInfo.project.name === 'mobile';
}

// pageHeading is the page's <h2> title. Each page prefixes it with "◇ ".
export function pageHeading(page, text) {
  const escaped = text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return page.getByRole('heading', { level: 2, name: new RegExp(`^◇ ${escaped}`) }).first();
}

export async function gotoPage(page, { path, heading }) {
  await page.goto(path);
  await expect(pageHeading(page, heading)).toBeVisible();
}

// advancedEntry returns the Advanced (LuCI) link, opening the mobile More
// sheet first when the shell is in its bottom-tab layout.
export async function advancedEntry(page, testInfo) {
  if (isMobile(testInfo)) {
    await page.getByRole('button', { name: 'More' }).click();
  }
  const link = page.getByRole('link', { name: /Advanced/ });
  await expect(link).toBeVisible();
  return link;
}

// navigateTo follows an in-app nav link (sidebar on desktop, bottom tab bar
// on mobile), so the change is a client-side route change, not a reload.
export async function navigateTo(page, testInfo, { desktop, mobile }) {
  const nav = isMobile(testInfo) ? page.locator('.bottom-tab-bar') : page.locator('.sidebar');
  await nav.getByRole('link', { name: isMobile(testInfo) ? mobile : desktop, exact: true }).click();
}

export async function signOut(page, testInfo) {
  if (isMobile(testInfo)) {
    await page.getByRole('button', { name: 'More' }).click();
    await page.locator('.tab-sheet').getByRole('button', { name: /Sign Out/ }).click();
    return;
  }
  await page.getByRole('button', { name: /Sign Out/ }).click();
}

// rpcPath is the glob for one ConnectRPC method behind the /rpc proxy.
export function rpcPath(service, method) {
  return `${RPC}${service}/${method}`;
}

// mockSetupStatus answers GetSetupStatus in the browser so the setup gate
// can be driven into each branch without touching the sample backend.
export async function mockSetupStatus(page, status) {
  await page.route(rpcPath('openmanet.setup.v1.SetupService', 'GetSetupStatus'), (route) =>
    route.fulfill({
      json: {
        currentHostname: 'UI-LAB-SIMULATED',
        currentCountry: 'US',
        currentTimezone: 'America/Denver',
        ...status,
      },
    }),
  );
}

// mockAuth puts the browser in front of an auth-enabled daemon. The sample
// backend runs with auth disabled, so /auth/* and the 401 a daemon returns
// for an expired session are answered here. The returned controller flips
// the session between valid and expired.
export async function mockAuth(page, { authenticated = true } = {}) {
  const state = { authenticated, logins: [] };
  await page.route('**/auth/check', (route) =>
    route.fulfill({
      json: state.authenticated
        ? { authenticated: true, username: 'e2e-operator', authEnabled: true }
        : { authenticated: false, authEnabled: true },
    }),
  );
  await page.route('**/auth/login', async (route) => {
    const body = route.request().postDataJSON();
    state.logins.push(body?.username);
    state.authenticated = true;
    await route.fulfill({ json: { authenticated: true, username: body?.username } });
  });
  await page.route('**/auth/logout', (route) => {
    state.authenticated = false;
    return route.fulfill({ status: 204, body: '' });
  });
  // Every RPC gets the daemon's 401 while the session is expired.
  await page.route(`${RPC}**`, (route) => {
    if (state.authenticated) return route.fallback();
    return route.fulfill({ status: 401, json: { code: 'unauthenticated', message: 'session expired' } });
  });
  return {
    expire() { state.authenticated = false; },
    get logins() { return state.logins; },
  };
}

// expectNoHorizontalScroll fails when the document is wider than the
// viewport, i.e. the page scrolls sideways.
export async function expectNoHorizontalScroll(page) {
  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(scrollWidth, 'page scrolls horizontally').toBeLessThanOrEqual(clientWidth);
}

// expectTouchTarget fails when the element is under the 44px touch floor
// from .claude/rules/frontend.md.
export async function expectTouchTarget(locator, min = 44) {
  const box = await locator.boundingBox();
  expect(box, 'element has no layout box').not.toBeNull();
  expect(box.height, `touch target height of ${await locator.innerText()}`).toBeGreaterThanOrEqual(min);
  expect(box.width, `touch target width of ${await locator.innerText()}`).toBeGreaterThanOrEqual(min);
}
