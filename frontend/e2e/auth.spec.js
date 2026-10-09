// =============================================================================
// auth.spec.js — Session expiry, logout and login return path
// =============================================================================
//
// The sample backend runs with auth disabled, so these tests answer /auth/*
// and the daemon's 401 in the browser (helpers.mockAuth). Everything else,
// including the SPA bundle and the /rpc proxy, is real.

import { test, expect } from '@playwright/test';
import { gotoPage, mockAuth, navigateTo, pageHeading, signOut } from './support/helpers.js';

async function logIn(page) {
  await expect(page).toHaveURL(/\/login$/);
  await page.getByLabel('Operator').fill('e2e-operator');
  await page.getByLabel('Passphrase').fill('e2e-passphrase');
  await page.getByRole('button', { name: /authenticate/i }).click();
}

test('session expiry mid-use -> login -> back to the page in use', async ({ page }, testInfo) => {
  const session = await mockAuth(page);
  await gotoPage(page, { path: '/settings', heading: 'Settings' });

  session.expire();
  // The next RPC (made by the page being opened) gets the 401.
  await navigateTo(page, testInfo, { desktop: 'GPS / GNSS', mobile: 'GPS' });
  await logIn(page);

  await expect(page).toHaveURL(/\/gps$/);
  await expect(pageHeading(page, 'GPS / GNSS')).toBeVisible();
  expect(session.logins).toEqual(['e2e-operator']);
});

test('deep link while signed out -> login -> the deep link', async ({ page }) => {
  await mockAuth(page, { authenticated: false });
  await page.goto('/settings/wireless');
  await logIn(page);
  await expect(page).toHaveURL(/\/settings\/wireless$/);
  await expect(pageHeading(page, 'Wireless Radios')).toBeVisible();
});

test('sign out lands on login and protected routes stay closed', async ({ page }, testInfo) => {
  await mockAuth(page);
  await gotoPage(page, { path: '/', heading: 'Dashboard' });
  await signOut(page, testInfo);
  await expect(page).toHaveURL(/\/login$/);

  await page.goto('/comms');
  await expect(page).toHaveURL(/\/login$/);
  await expect(page.getByRole('button', { name: /authenticate/i })).toBeVisible();
});
