// =============================================================================
// radioApi.js — RadioService client (radio capacity and decisions, read-only)
// =============================================================================

import { createClient } from '@connectrpc/connect';
import { transport } from './connectClient.js';
import { RadioService } from '../gen/openmanet/radio/v1/radio_service_pb.js';

const client = createClient(RadioService, transport);

// getRadioCapacity returns { capacity: { gateMode, phys, generatedAt,
// inventoryUpdatedAt } }.
export function getRadioCapacity() {
  return client.getRadioCapacity({});
}

// listAdmissionDecisions returns { decisions } newest first; limit is 1..64.
export function listAdmissionDecisions(limit) {
  return client.listAdmissionDecisions({ limit });
}
