// =============================================================================
// useCoverageMap — mesh coverage map from CoverageService (GHO-94)
// =============================================================================
//
// Polls `CoverageService.getCoverageMap()` through a shared poll store and
// publishes the response: this node's map and the latest from every other
// node. Publishes null on an older daemon without the service
// (Unimplemented); that does not change while the daemon runs, so the store
// stops calling the RPC after it. Any other error is published with the
// last good response, so the page keeps its map and shows the error.

import { useEffect, useState } from 'react';
import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { transport } from '../services/connectClient.js';
import { CoverageService } from '../gen/openmanet/coverage/v1/coverage_service_pb.js';
import { createPollStore } from '../services/pollStore.js';

const coverageClient = createClient(CoverageService, transport);

let unavailable = false;
let lastRes = null;

const store = createPollStore(async () => {
  if (unavailable) return { unavailable: true };
  try {
    lastRes = await coverageClient.getCoverageMap({});
    return { res: lastRes };
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.Unimplemented) {
      unavailable = true;
      return { unavailable: true };
    }
    return { res: lastRes, error: err?.message || 'request failed' };
  }
});

export function useCoverageMap(intervalMs) {
  const [snapshot, setSnapshot] = useState(() => store.getSnapshot());

  useEffect(() => store.subscribe(intervalMs, setSnapshot), [intervalMs]);

  return snapshot;
}
