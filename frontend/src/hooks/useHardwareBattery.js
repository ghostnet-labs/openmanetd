// =============================================================================
// useHardwareBattery — INA228 battery telemetry from the hardware manager
// =============================================================================
//
// Polls `HardwareService.getHardwareStatus()` through a shared poll store and
// publishes only the battery block (BatteryTelemetry): voltage, current,
// low-battery level and the stale / absent flags the dashboard's own battery
// section cannot carry.
//
// Publishes null when the daemon runs no hardware manager (FailedPrecondition
// with hardware.enable: false, or Unimplemented on an older daemon). That
// answer does not change while the daemon runs, so the store stops calling
// the RPC after it. Any other error keeps the previous snapshot, like the
// dashboard's own poll.

import { useEffect, useState } from 'react';
import { Code, ConnectError, createClient } from '@connectrpc/connect';
import { transport } from '../services/connectClient.js';
import { HardwareService } from '../gen/openmanet/hardware/v1/hardware_service_pb.js';
import { createPollStore } from '../services/pollStore.js';

const hardwareClient = createClient(HardwareService, transport);

const UNAVAILABLE_CODES = new Set([Code.FailedPrecondition, Code.Unimplemented]);

let unavailable = false;

const store = createPollStore(async () => {
  if (unavailable) return null;
  try {
    const resp = await hardwareClient.getHardwareStatus({});
    return resp.status?.battery ?? null;
  } catch (err) {
    if (err instanceof ConnectError && UNAVAILABLE_CODES.has(err.code)) {
      unavailable = true;
      return null;
    }
    throw err;
  }
});

export function useHardwareBattery(intervalMs) {
  const [battery, setBattery] = useState(() => store.getSnapshot());

  useEffect(() => store.subscribe(intervalMs, setBattery), [intervalMs]);

  return battery;
}
