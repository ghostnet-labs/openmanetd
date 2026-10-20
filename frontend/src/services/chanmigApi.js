// =============================================================================
// chanmigApi.js — ChannelMigrationService client (status, read-only)
// =============================================================================

import { createClient } from '@connectrpc/connect';
import { transport } from './connectClient.js';
import { ChannelMigrationService } from '../gen/openmanet/chanmig/v1/chanmig_service_pb.js';

const client = createClient(ChannelMigrationService, transport);

// getChannelMigrationStatus returns { status: { enabled, phase, currentPlan,
// bands, peers, ... } }. With channel migration disabled it succeeds with
// status.enabled false.
export function getChannelMigrationStatus() {
  return client.getChannelMigrationStatus({});
}
