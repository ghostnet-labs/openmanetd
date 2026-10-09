// =============================================================================
// settings.spec.js — Settings save and readback through the real proxy
// =============================================================================
//
// The wireless form saves with UpdateRadioSettings over /rpc, which the Go
// frontend server forwards to the sample backend; a reload then reads the
// value back with GetRadioSettings.

import { test, expect } from '@playwright/test';
import { gotoPage } from './support/helpers.js';
import { external } from './support/env.js';

const WIRELESS = { path: '/settings/wireless', heading: 'Wireless Radios' };

function meshIdInput(page) {
  return page.locator('.lat-field', { has: page.locator('label', { hasText: /^Mesh ID$/ }) }).locator('input');
}

test('wireless mesh ID saves and reads back after reload', async ({ page }, testInfo) => {
  test.skip(!!external && process.env.E2E_ALLOW_WRITES !== '1', 'writes radio settings; set E2E_ALLOW_WRITES=1 to run against a real node');
  const value = `e2e-${testInfo.project.name}-${Date.now().toString(36)}`;
  await gotoPage(page, WIRELESS);

  const input = meshIdInput(page);
  await expect(input).toHaveValue(/.+/);
  await input.fill(value);

  const card = page.locator('.lat-panel', { has: input });
  await expect(card.getByText('Unsaved Changes')).toBeVisible();
  const saved = page.waitForResponse((r) => r.url().endsWith('/UpdateRadioSettings') && r.ok());
  await card.getByRole('button', { name: 'Save', exact: true }).click();
  expect((await saved).request().postDataJSON().settings.meshId).toBe(value);
  await expect(card.locator('.lat-alert.ok')).toContainText('Settings saved');

  await page.reload();
  await expect(meshIdInput(page)).toHaveValue(value);
});
