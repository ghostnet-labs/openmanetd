// =============================================================================
// useWeatherStations — weather stations heard over the mesh
// =============================================================================
//
// Polls `WeatherService.listWeatherStations()` through a shared poll store,
// so the Dashboard panel and the Weather page share one RPC per cycle.
//
// Publishes null when the daemon has no WeatherService (Unimplemented on an
// older daemon). That answer does not change while the daemon runs, so the
// store stops calling the RPC after it. Any other error keeps the previous
// snapshot.

import { useEffect, useState } from 'react';
import { Code, ConnectError } from '@connectrpc/connect';
import { listWeatherStations } from '../services/weatherApi.js';
import { createPollStore } from '../services/pollStore.js';

let unavailable = false;

const store = createPollStore(async () => {
  if (unavailable) return null;
  try {
    return await listWeatherStations();
  } catch (err) {
    if (err instanceof ConnectError && err.code === Code.Unimplemented) {
      unavailable = true;
      return null;
    }
    throw err;
  }
});

export function useWeatherStations(intervalMs) {
  const [stations, setStations] = useState(() => store.getSnapshot());

  useEffect(() => store.subscribe(intervalMs, setStations), [intervalMs]);

  return stations;
}
