// =============================================================================
// env.js — Ports and URLs shared by playwright.config.js and the specs
// =============================================================================

export const ports = {
  api: Number(process.env.E2E_API_PORT || 18087),
  luci: Number(process.env.E2E_LUCI_PORT || 18088),
  http: Number(process.env.E2E_HTTP_PORT || 18081),
  https: Number(process.env.E2E_HTTPS_PORT || 18443),
};

// E2E_BASE_URL points the suite at an already running frontend (a POC node,
// say) instead of the local servers.
export const external = process.env.E2E_BASE_URL || '';
export const baseURL = external || `http://127.0.0.1:${ports.http}`;
// The TLS listener of the same frontend. Empty when it is unknown.
export const httpsURL = process.env.E2E_HTTPS_URL || (external ? '' : `https://127.0.0.1:${ports.https}`);
