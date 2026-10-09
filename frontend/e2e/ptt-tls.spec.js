// =============================================================================
// ptt-tls.spec.js — Browser PTT over TLS, WebSocket routing, secure context
// =============================================================================
//
// The frontend server's TLS listener (self-signed) serves the same SPA. Over
// https the page is a secure context, so the microphone (Chromium's fake
// capture device here) is available and PTT streams Opus frames on the
// same-origin wss:///ws socket that the Go audio bridge terminates. Over
// plain http on a non-loopback host name the browser withholds
// navigator.mediaDevices; that is a browser rule, not a product defect, and
// the test pins that the UI says so instead of failing silently.
//
// Desktop project only: PTT input is the same code path on both layouts and
// the mobile project adds nothing here.

import { test, expect } from '@playwright/test';
import { httpsURL } from './support/env.js';

const MSG = { TX_AUDIO: 0x02, PTT_DOWN: 0x09, PTT_UP: 0x0a };
// Resolves to 127.0.0.1 via --host-resolver-rules in playwright.config.js,
// so it is an insecure origin to Chromium (unlike 127.0.0.1/localhost).
const INSECURE_HOST = 'node.e2e.test';

test.skip(({ isMobile }) => isMobile, 'PTT path is layout independent; run once on desktop');

function recordFrames(page) {
  const frames = { sockets: [], sent: [] };
  page.on('websocket', (ws) => {
    frames.sockets.push(ws.url());
    ws.on('framesent', ({ payload }) => {
      if (typeof payload !== 'string' && payload.length > 0) frames.sent.push(payload[0]);
    });
  });
  return frames;
}

async function holdPtt(page, frames) {
  const ptt = page.locator('.ptt-ring');
  await ptt.hover();
  await page.mouse.down();
  await expect(ptt).toHaveText(/TX · ON/);
  return async () => {
    await page.mouse.up();
    await expect(ptt).toHaveText(/TX · HOLD/);
    await expect.poll(() => frames.sent.includes(MSG.PTT_UP)).toBe(true);
  };
}

test('https: secure context, wss /ws, PTT streams Opus frames', async ({ browser }) => {
  test.skip(!httpsURL, 'no TLS frontend URL for this run (set E2E_HTTPS_URL)');
  const context = await browser.newContext({
    baseURL: httpsURL,
    ignoreHTTPSErrors: true, // the frontend's self-signed certificate
    permissions: ['microphone'],
    viewport: { width: 1280, height: 800 },
  });
  const page = await context.newPage();
  const frames = recordFrames(page);

  await page.goto('/comms');
  await expect(page.getByText('WS UP')).toBeVisible();
  expect(await page.evaluate(() => window.isSecureContext && !!navigator.mediaDevices)).toBe(true);
  expect(frames.sockets.some((u) => u.startsWith('wss://') && u.endsWith('/ws'))).toBe(true);

  const release = await holdPtt(page, frames);
  await expect.poll(() => frames.sent.includes(MSG.PTT_DOWN)).toBe(true);
  // Mic -> WebCodecs Opus -> TX_AUDIO frames on the socket.
  await expect.poll(() => frames.sent.filter((b) => b === MSG.TX_AUDIO).length).toBeGreaterThan(5);
  await expect(page.getByText('TX start')).toBeVisible();
  await release();
  await context.close();
});

test('plain http on a LAN host: PTT signals, mic is withheld and the UI says why', async ({ browser }, testInfo) => {
  test.skip(!!process.env.E2E_BASE_URL, 'needs the local frontend reachable under a resolver-mapped name');
  const port = new URL(testInfo.project.use.baseURL).port;
  const context = await browser.newContext({
    baseURL: `http://${INSECURE_HOST}:${port}`,
    viewport: { width: 1280, height: 800 },
  });
  const page = await context.newPage();
  const frames = recordFrames(page);

  await page.goto('/comms');
  await expect(page.getByText('WS UP')).toBeVisible();
  expect(await page.evaluate(() => ({ secure: window.isSecureContext, media: !!navigator.mediaDevices })))
    .toEqual({ secure: false, media: false });

  const release = await holdPtt(page, frames);
  await expect.poll(() => frames.sent.includes(MSG.PTT_DOWN)).toBe(true);
  await expect(page.getByText(/Mic unavailable: page must be served over HTTPS/)).toBeVisible();
  expect(frames.sent.includes(MSG.TX_AUDIO)).toBe(false);
  await release();
  await context.close();
});
