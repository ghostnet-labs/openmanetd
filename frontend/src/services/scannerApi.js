// =============================================================================
// scannerApi.js — ScannerService client (aircraft, P25, conventional)
// =============================================================================

import { createClient } from '@connectrpc/connect';
import { transport } from './connectClient.js';
import { ScannerService } from '../gen/openmanet/scanner/v1/scanner_service_pb.js';
import { AudioEncoding } from '../gen/openmanet/scanner/v1/scanner_pb.js';

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

// listConventionalChannels returns the nearby conventional / aviation
// channels with live and last-heard state.
export function listConventionalChannels() {
  return client.listConventionalChannels({});
}

// streamAudio returns an async iterable of StreamAudioResponse messages:
// squelch-open 8 kHz PCM (s16le) of one channel. The first message carries
// only the encoding and sample rate. Abort signal to stop listening.
export function streamAudio(channelKey, signal) {
  return client.streamAudio({ channelKey, encoding: AudioEncoding.PCM_S16LE }, { signal });
}

export function getScannerConfig() {
  return client.getScannerConfig({});
}

// setScannerConfig persists and applies a full ScannerConfig.
export function setScannerConfig(config) {
  return client.setScannerConfig({ config });
}
