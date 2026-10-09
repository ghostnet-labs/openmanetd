// =============================================================================
// polling.spec.js — Request budget for the always-open pages
// =============================================================================
//
// Field nodes serve every open browser tab, so a page that polls faster than
// it needs to costs the node CPU and airtime. Each page below is opened with
// a fake clock, advanced one simulated minute, and the requests it made are
// counted per endpoint against POLL_BUDGET (requests per minute derived from
// each page's poll interval). An endpoint missing from a page's budget may
// not be polled at all. Hiding the tab must stop polling entirely
// (useVisibleInterval), which is what keeps a backgrounded phone quiet.
//
// Raise a budget only with a reason; lowering one after a deliberate
// slow-down is welcome.

import { test, expect } from '@playwright/test';
import { gotoPage } from './support/helpers.js';

const MINUTE = 60_000;
// Clock step: shorter than every poll interval below, so each tick is seen.
const STEP = 1_000;

// path -> { endpoint suffix: max requests in one simulated minute after load }
// The budgets are the pages' own poll intervals (the *_POLL_* constants in
// src/pages and the shared stores in src/hooks) turned into requests per
// minute, plus one for the tick that can land exactly on the minute.
const perMinute = (intervalMs) => MINUTE / intervalMs + 1;

const POLL_BUDGET = {
  '/': {
    GetDashboardStatus: perMinute(5_000), // DASH_POLL_MS
    GetMeshSnapshot: perMinute(10_000), // MESH_POLL_MS
    GetMeshTopology: perMinute(10_000), // MESH_POLL_MS
    GetMeshTopologyDelta: perMinute(10_000), // rides the topology store
    GetGNSSStatus: perMinute(10_000), // CHIP_POLL_MS
    GetBLOSStatus: perMinute(10_000), // CHIP_POLL_MS
    ListBLOSPeers: perMinute(10_000), // CHIP_POLL_MS
    ListNetworkInterfaces: perMinute(30_000), // IFACE_POLL_MS
  },
  '/comms': {
    GetAudioMixer: perMinute(5_000), // DeviceAudioPanel POLL_MS
    GetCommsStatus: perMinute(10_000), // COMMS_STATUS_POLL_INTERVAL
    GetMeshSnapshot: perMinute(10_000), // MESH_STATUS_POLL_INTERVAL
  },
  '/gps': { GetGNSSStatus: perMinute(2_000) }, // GpsStatus POLL_INTERVAL
  '/topology': {
    GetMeshSnapshot: perMinute(5_000), // Topology POLL_INTERVAL
    GetMeshTopology: perMinute(5_000),
    GetMeshTopologyDelta: perMinute(5_000),
  },
};

const HEADINGS = { '/': 'Dashboard', '/comms': 'Comms', '/gps': 'GPS / GNSS', '/topology': 'Topology' };

function endpoint(url) {
  const { pathname } = new URL(url);
  return pathname.startsWith('/rpc/') ? pathname.slice(pathname.lastIndexOf('/') + 1) : pathname;
}

test.skip(({ isMobile }) => isMobile, 'polling is layout independent; run once on desktop');

// trackRequests counts fetch/XHR requests per endpoint while `counting` is
// on and keeps an in-flight count, so the fake clock can be advanced the way
// real time passes: a step, then wait for the requests it started to settle.
function trackRequests(page) {
  const t = { counting: false, inflight: 0, counts: new Map() };
  const isApi = (req) => req.resourceType() === 'fetch' || req.resourceType() === 'xhr';
  page.on('request', (req) => {
    if (!isApi(req)) return;
    t.inflight++;
    if (!t.counting) return;
    const key = endpoint(req.url());
    t.counts.set(key, (t.counts.get(key) || 0) + 1);
  });
  const done = (req) => { if (isApi(req)) t.inflight--; };
  page.on('requestfinished', done);
  page.on('requestfailed', done);
  return t;
}

async function countOverOneMinute(page, tracker) {
  tracker.counts.clear();
  tracker.counting = true;
  for (let elapsed = 0; elapsed < MINUTE; elapsed += STEP) {
    await page.clock.runFor(STEP);
    await expect.poll(() => tracker.inflight, { timeout: 5_000 }).toBe(0);
  }
  tracker.counting = false;
  return Object.fromEntries([...tracker.counts].sort(([a], [b]) => a.localeCompare(b)));
}

for (const [path, budget] of Object.entries(POLL_BUDGET)) {
  test(`${path} stays within its polling budget and pauses when hidden`, async ({ page }) => {
    test.setTimeout(90_000);
    await page.clock.install();
    const tracker = trackRequests(page);

    await gotoPage(page, { path, heading: HEADINGS[path] });
    await expect.poll(() => tracker.inflight, { timeout: 10_000 }).toBe(0);

    const observed = await countOverOneMinute(page, tracker);
    test.info().annotations.push({ type: 'requests-per-minute', description: `${path} ${JSON.stringify(observed)}` });
    for (const [key, n] of Object.entries(observed)) {
      expect(n, `${key} requests in one minute on ${path} (budget ${budget[key] ?? 0})`).toBeLessThanOrEqual(budget[key] ?? 0);
    }

    // Hidden tab: no polling at all.
    await page.evaluate(() => {
      Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => 'hidden' });
      Object.defineProperty(document, 'hidden', { configurable: true, get: () => true });
      document.dispatchEvent(new Event('visibilitychange'));
    });
    const hidden = await countOverOneMinute(page, tracker);
    test.info().annotations.push({ type: 'requests-per-minute-hidden', description: `${path} ${JSON.stringify(hidden)}` });
    expect(hidden, `requests while the tab is hidden on ${path}`).toEqual({});
  });
}
