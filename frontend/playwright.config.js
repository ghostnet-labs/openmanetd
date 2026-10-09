// =============================================================================
// playwright.config.js — Browser end-to-end suite (frontend/e2e)
// =============================================================================
//
// The suite drives the built SPA through the real Go frontend server
// (tools/e2e-frontend wraps internal/frontend), which proxies /rpc and /auth
// to the ui-lab sample backend and /cgi-bin, /luci-static and /ubus to a fake
// LuCI upstream. Run it with `make e2e` (builds the bundle into e2e/.dist
// first) or `pnpm run e2e` from frontend/.
//
// Set E2E_BASE_URL to point the suite at an already running frontend (for
// example a POC node over https) instead of starting the local servers; see
// docs/poc-node-measurements.md. Specs that need the local fakes skip.
//
// Chromium only: it is the engine on the field Android devices and the only
// browser preinstalled in the dev container. Point PLAYWRIGHT_BROWSERS_PATH at
// an existing browser cache, or set E2E_CHROMIUM to a Chromium binary.

import { defineConfig } from '@playwright/test';
import { ports, external, baseURL } from './e2e/support/env.js';

// The browser only talks to the local servers (or a node on the LAN), so it
// must not inherit a corporate/sandbox HTTP proxy from the environment:
// Chromium on Linux honors *_proxy variables, which would route the
// resolver-mapped test host below through that proxy.
const browserEnv = Object.fromEntries(
  Object.entries(process.env).filter(([key]) => !/^(https?|all|no)_proxy$/i.test(key)),
);

const launchOptions = {
  executablePath: process.env.E2E_CHROMIUM || undefined,
  env: browserEnv,
  // Fake capture device + auto-accepted permission prompt so the PTT
  // microphone path can be exercised headless.
  args: [
    '--use-fake-device-for-media-stream',
    '--use-fake-ui-for-media-stream',
    // ptt-tls.spec.js needs a non-loopback (so insecure) host name for the
    // local frontend, and every request stays local.
    '--host-resolver-rules=MAP node.e2e.test 127.0.0.1',
    '--no-proxy-server',
  ],
};

const webServer = external ? undefined : [
  {
    command: 'python3 ../tools/ui-lab/tools/sample-api.py',
    env: { SAMPLE_API_PORT: String(ports.api) },
    // Its access log (and the Go bridge's audio-stream retries below) is
    // noise for a browser run; DEBUG=pw:webserver shows server output.
    stderr: 'ignore',
    url: `http://127.0.0.1:${ports.api}/health`,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
  {
    command: 'node e2e/support/fake-luci.mjs',
    env: { FAKE_LUCI_PORT: String(ports.luci) },
    url: `http://127.0.0.1:${ports.luci}/cgi-bin/luci/`,
    reuseExistingServer: !process.env.CI,
    timeout: 30_000,
  },
  {
    // `go run` compiles the frontend package on first use; allow for it.
    command: [
      'go run ../tools/e2e-frontend',
      `--static ${process.env.E2E_STATIC_DIR || 'e2e/.dist'}`,
      `--listen 127.0.0.1:${ports.http}`,
      `--tls-listen 127.0.0.1:${ports.https}`,
      `--api http://127.0.0.1:${ports.api}`,
      `--luci-upstream http://127.0.0.1:${ports.luci}`,
    ].join(' '),
    url: `http://127.0.0.1:${ports.http}/`,
    stderr: 'ignore',
    reuseExistingServer: !process.env.CI,
    timeout: 240_000,
  },
];

export default defineConfig({
  testDir: './e2e',
  outputDir: './e2e/.results',
  fullyParallel: false,
  // The sample backend keeps settings in memory, so tests that write share
  // state; one worker keeps save/readback deterministic.
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never', outputFolder: 'e2e/.report' }]] : 'list',
  timeout: 30_000,
  expect: { timeout: 10_000 },
  use: {
    baseURL,
    browserName: 'chromium',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions,
    // A node's TLS listener uses a self-signed certificate by default.
    ignoreHTTPSErrors: !!external,
    // For an auth-enabled node: a storage state saved after logging in, e.g.
    // `pnpm exec playwright codegen --save-storage=node.json https://<node>:8081`.
    storageState: process.env.E2E_STORAGE_STATE || undefined,
  },
  projects: [
    {
      name: 'desktop',
      use: { viewport: { width: 1280, height: 800 } },
    },
    {
      name: 'mobile',
      use: {
        viewport: { width: 360, height: 640 },
        deviceScaleFactor: 2,
        isMobile: true,
        hasTouch: true,
      },
    },
  ],
  webServer,
});
