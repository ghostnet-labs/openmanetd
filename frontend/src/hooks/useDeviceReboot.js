// =============================================================================
// useDeviceReboot — confirm → request → wait-for-down → wait-for-up state machine
// =============================================================================
//
// Drives the "Reboot device" action on the Settings page. The caller supplies
// `requestReboot` (the DashboardService QUICK_ACTION_REBOOT_DEVICE call) and,
// optionally, a `probe` that resolves true when the daemon answers HTTP.
//
// After the reboot request is accepted the hook polls the probe (through
// useVisibleInterval, so a hidden tab stops polling) to report what is really
// happening: first that the device has gone down, then that it is back.
// Timeouts are measured with wall-clock time so a tab that was hidden while
// the device rebooted still reports correctly when it becomes visible again.

import { useState, useRef, useCallback, useEffect } from 'react';
import { Code, ConnectError } from '@connectrpc/connect';
import { useVisibleInterval } from './useVisibleInterval.js';

export const RebootPhase = Object.freeze({
  IDLE: 'idle',
  CONFIRM: 'confirm',
  REQUESTING: 'requesting',
  GOING_DOWN: 'going-down',
  RECONNECTING: 'reconnecting',
  ONLINE: 'online',
  STILL_UP: 'still-up',
  TIMEOUT: 'timeout',
  FAILED: 'failed',
});

export const REBOOT_POLL_INTERVAL_MS = 3000;
export const REBOOT_DOWN_TIMEOUT_MS = 60000;
export const REBOOT_UP_TIMEOUT_MS = 180000;
const PROBE_TIMEOUT_MS = 2500;
const PROBE_URL = '/api/settings/hostname';

// Connect codes that mean the daemon received the request and refused or
// failed it. Anything else (Unknown, Unavailable, Canceled, a raw TypeError)
// means the connection dropped mid-call, which is expected when the reboot
// kills the daemon before the response is flushed.
const EXPLICIT_FAILURE_CODES = new Set([
  Code.Internal,
  Code.InvalidArgument,
  Code.FailedPrecondition,
  Code.Unauthenticated,
  Code.PermissionDenied,
  Code.NotFound,
  Code.Unimplemented,
]);

const POLLING_PHASES = new Set([RebootPhase.GOING_DOWN, RebootPhase.RECONNECTING]);

export function isExplicitRebootFailure(err) {
  return err instanceof ConnectError && EXPLICIT_FAILURE_CODES.has(err.code);
}

function errorMessage(err) {
  if (err instanceof ConnectError) return err.rawMessage || err.message;
  return err?.message || String(err);
}

// probeDevice resolves true when the daemon's HTTP server answers at all
// (any status below 500, including 401 after a session reset) and false on a
// network error, a proxy gateway error, or no answer within PROBE_TIMEOUT_MS.
export async function probeDevice() {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS);
  try {
    const resp = await fetch(PROBE_URL, {
      credentials: 'include',
      cache: 'no-store',
      signal: controller.signal,
    });
    return resp.ok || (resp.status > 0 && resp.status < 500);
  } catch {
    return false;
  } finally {
    clearTimeout(timer);
  }
}

export function useDeviceReboot({
  requestReboot,
  probe = probeDevice,
  pollIntervalMs = REBOOT_POLL_INTERVAL_MS,
  downTimeoutMs = REBOOT_DOWN_TIMEOUT_MS,
  upTimeoutMs = REBOOT_UP_TIMEOUT_MS,
}) {
  const [phase, setPhase] = useState(RebootPhase.IDLE);
  const [error, setError] = useState(null);
  const phaseRef = useRef(RebootPhase.IDLE);
  const phaseStartRef = useRef(0);
  const inFlightRef = useRef(false);
  const mountedRef = useRef(true);

  useEffect(() => {
    mountedRef.current = true;
    return () => { mountedRef.current = false; };
  }, []);

  const enter = useCallback((next) => {
    phaseRef.current = next;
    phaseStartRef.current = Date.now();
    setPhase(next);
  }, []);

  const begin = useCallback(() => {
    setError(null);
    enter(RebootPhase.CONFIRM);
  }, [enter]);

  const reset = useCallback(() => {
    setError(null);
    enter(RebootPhase.IDLE);
  }, [enter]);

  const confirm = useCallback(async () => {
    setError(null);
    enter(RebootPhase.REQUESTING);
    try {
      const resp = await requestReboot();
      if (!mountedRef.current) return;
      if (!resp?.success) {
        setError(resp?.message || 'the device did not accept the reboot request');
        enter(RebootPhase.FAILED);
        return;
      }
      enter(RebootPhase.GOING_DOWN);
    } catch (err) {
      if (!mountedRef.current) return;
      if (isExplicitRebootFailure(err)) {
        setError(errorMessage(err));
        enter(RebootPhase.FAILED);
        return;
      }
      // Connection dropped mid-call: the reboot most likely started. The
      // probe loop confirms it either way (STILL_UP if it did not).
      enter(RebootPhase.GOING_DOWN);
    }
  }, [requestReboot, enter]);

  const tick = useCallback(async () => {
    if (inFlightRef.current) return;
    const current = phaseRef.current;
    if (!POLLING_PHASES.has(current)) return;

    inFlightRef.current = true;
    try {
      const up = await probe();
      if (!mountedRef.current || phaseRef.current !== current) return;
      const elapsed = Date.now() - phaseStartRef.current;

      if (current === RebootPhase.GOING_DOWN) {
        if (!up) enter(RebootPhase.RECONNECTING);
        else if (elapsed >= downTimeoutMs) enter(RebootPhase.STILL_UP);
        return;
      }
      if (up) enter(RebootPhase.ONLINE);
      else if (elapsed >= upTimeoutMs) enter(RebootPhase.TIMEOUT);
    } finally {
      inFlightRef.current = false;
    }
  }, [probe, enter, downTimeoutMs, upTimeoutMs]);

  const polling = POLLING_PHASES.has(phase);
  useVisibleInterval(tick, polling ? pollIntervalMs : 0, [polling]);

  return { phase, error, begin, confirm, reset };
}
