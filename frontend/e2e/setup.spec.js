// =============================================================================
// setup.spec.js — Setup gate handoff (components/SetupGate.jsx)
// =============================================================================
//
// GetSetupStatus is answered in the browser to drive each gate branch; the
// sample backend reports a configured node. Applying setup needs hardware
// and is not exercised.

import { test, expect } from '@playwright/test';
import { mockSetupStatus, pageHeading } from './support/helpers.js';

const UNCONFIGURED = { isEnabled: true, isSetupComplete: false, hasHalowRadio: true, alreadyConfigured: false };

test('unconfigured node: any route hands off to the wizard, skip returns to the app', async ({ page }) => {
  await mockSetupStatus(page, UNCONFIGURED);
  await page.goto('/settings/wireless');
  await expect(page).toHaveURL(/\/setup$/);
  await expect(page.getByRole('heading', { name: 'OpenMANET Setup Wizard' })).toBeVisible();

  await page.locator('.lat-view-toolbar').getByRole('button', { name: 'Skip for now' }).click();
  // The confirmation offers Cancel and a second Skip for now.
  await page.getByRole('button', { name: 'Skip for now' }).last().click();
  await expect(page).toHaveURL(/\/$/);
  await expect(pageHeading(page, 'Dashboard')).toBeVisible();
  await expect(page.locator('.setup-dismiss-banner')).toBeVisible();

  // Skipping is per session: a reload keeps the routes open.
  await page.reload();
  await expect(pageHeading(page, 'Dashboard')).toBeVisible();
});

test('node without a HaLow radio shows the no-radio page at /setup', async ({ page }) => {
  await mockSetupStatus(page, { ...UNCONFIGURED, hasHalowRadio: false });
  await page.goto('/gps');
  await expect(page).toHaveURL(/\/setup$/);
  await expect(page.getByRole('heading', { name: 'No HaLow Radio' })).toBeVisible();
});

test('configured node: /setup is locked and redirects to the dashboard', async ({ page }) => {
  await mockSetupStatus(page, { isEnabled: true, isSetupComplete: true, hasHalowRadio: true, alreadyConfigured: true });
  await page.goto('/setup');
  await expect(page).toHaveURL(/\/$/);
  await expect(pageHeading(page, 'Dashboard')).toBeVisible();
});

test('setup status failure fails closed into the app', async ({ page }) => {
  await page.route('**/rpc/openmanet.setup.v1.SetupService/GetSetupStatus', (route) =>
    route.fulfill({ status: 503, json: { code: 'unavailable', message: 'e2e' } }),
  );
  await page.goto('/comms');
  await expect(pageHeading(page, 'Comms')).toBeVisible();
});
