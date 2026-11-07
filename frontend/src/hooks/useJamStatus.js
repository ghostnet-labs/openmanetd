// =============================================================================
// useJamStatus — jamming detection state from JamDetectService (GHO-93)
// =============================================================================
//
// Polls `JamDetectService.getJamStatus()` through a shared poll store and
// publishes the response: this node's radios and open episodes, the latest
// report from every other node, and recent episodes.
//
// Publishes null on an older daemon without the service (Unimplemented);
// that answer does not change while the daemon runs, so the store stops
// calling the RPC after it. Any other error keeps the previous snapshot.

import { useEffect, useState } from 'react';
import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { transport } from '../services/connectClient.js';
import { JamDetectService } from '../gen/openmanet/jamdetect/v1/jamdetect_service_pb.js';
import { createPollStore } from '../services/pollStore.js';

const jamClient = createClient(JamDetectService, transport);

let unavailable = false;

const store = createPollStore(async () => {
  if (unavailable) return null;
  try {
    return await jamClient.getJamStatus({});
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.Unimplemented) {
      unavailable = true;
      return null;
    }
    throw err;
  }
});

export function useJamStatus(intervalMs) {
  const [status, setStatus] = useState(() => store.getSnapshot());

  useEffect(() => store.subscribe(intervalMs, setStatus), [intervalMs]);

  return status;
}
