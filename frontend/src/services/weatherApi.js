// =============================================================================
// weatherApi.js — WeatherService client (weather stations on the mesh)
// =============================================================================

import { createClient } from '@connectrpc/connect';
import { transport } from './connectClient.js';
import { WeatherService } from '../gen/openmanet/weather/v1/weather_service_pb.js';

const client = createClient(WeatherService, transport);

// listWeatherStations returns this node's station (when it has a sensor)
// followed by every station heard over the mesh. With weather sharing off
// it succeeds with no stations.
export function listWeatherStations() {
  return client.listWeatherStations({});
}
