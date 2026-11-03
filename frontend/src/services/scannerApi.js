// =============================================================================
// scannerApi.js — ScannerService client (aircraft + P25 site monitor)
// =============================================================================

import { createClient } from '@connectrpc/connect';
import { transport } from './connectClient.js';
import { ScannerService } from '../gen/openmanet/scanner/v1/scanner_service_pb.js';

const client = createClient(ScannerService, transport);

// getScannerStatus returns receivers, position, selected site, decoder state
// and the stale / unavailable flags. With the scanner disabled it succeeds
// with enabled false.
export function getScannerStatus() {
  return client.getScannerStatus({});
}

// listAircraft returns the merged 1090/978 aircraft and feed health.
export function listAircraft(includeStale = false) {
  return client.listAircraft({ includeStale });
}

// listTalkgroupActivity returns recent voice grants, newest first.
export function listTalkgroupActivity(limit = 50) {
  return client.listTalkgroupActivity({ limit });
}

export function getScannerConfig() {
  return client.getScannerConfig({});
}

// setScannerConfig persists and applies a full ScannerConfig.
export function setScannerConfig(config) {
  return client.setScannerConfig({ config });
}
