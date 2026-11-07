package openmanet

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"

	"github.com/openmanet/go-alfred"
	netv1 "github.com/openmanet/openmanetd/internal/api/openmanet/network/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/mgmt"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/util/logger"
	"github.com/openmanet/openmanetd/internal/weather"
	"github.com/rs/zerolog"
)

// alfred record identity for weather reports. The data type comes from
// openmanet.network.v1.DataType (DATA_TYPE_WEATHER = 106).
const (
	weatherAlfredDataType    = uint8(netv1.DataType_DATA_TYPE_WEATHER)
	weatherAlfredDataVersion = uint8(1)
)

// weatherGossip binds an alfred client to the weather data type.
type weatherGossip struct {
	client alfred.ReadWriteClient
}

func (g weatherGossip) Publish(payload []byte) error {
	if err := g.client.Set(weatherAlfredDataType, weatherAlfredDataVersion, payload); err != nil {
		return fmt.Errorf("alfred set: %w", err)
	}

	return nil
}

// Fetch returns every weather report alfred holds. Records of another
// version are skipped so a mixed-firmware mesh degrades to "station not
// heard".
func (g weatherGossip) Fetch() ([]weather.GossipRecord, error) {
	recs, err := g.client.Request(weatherAlfredDataType)
	if err != nil {
		return nil, fmt.Errorf("alfred request: %w", err)
	}

	out := make([]weather.GossipRecord, 0, len(recs))

	for _, r := range recs {
		if r.Version != weatherAlfredDataVersion {
			continue
		}

		out = append(out, weather.GossipRecord{Source: r.Source, Data: r.Data})
	}

	return out, nil
}

// startWeather builds the weather monitor and runs it until ctx is
// canceled. It returns nil, starting nothing, when weather.enable is
// false. A sensor that fails to open is logged and the node still lists
// the stations it hears. The returned channel closes once the monitor's
// goroutine has exited; it is already closed when nothing started.
func startWeather(ctx context.Context, cfg *config.Config, manager *mgmt.ManagementConfig, gps *gpsd.GPSService, log zerolog.Logger) (*weather.Monitor, <-chan struct{}) {
	wc := cfg.GetWeather()
	if !wc.Enable {
		log.Info().Msg("weather: disabled")

		done := make(chan struct{})
		close(done)

		return nil, done
	}

	if len(wc.Problems) > 0 {
		log.Warn().Strs("problems", wc.Problems).Msg("weather: rejected settings, using defaults for them")
	}

	batIface := cfg.GetAlfredBatInterface()

	opts := weatherOptions(wc)
	opts.Log = logger.GetLogger("weather")
	opts.Identity = func() (string, string) { return weatherIdentity(batIface) }
	opts.Position = weatherPosition(gps)

	if wc.SensorEnable {
		sensor, err := openWeatherSensor(wc)
		if err != nil {
			log.Error().Err(err).Str("device", wc.SensorDevice).Msg("weather: sensor unavailable; listing mesh stations only")
		} else {
			opts.Sensor = sensor
			opts.Model = wc.SensorModel
		}
	}

	if manager == nil || manager.Client() == nil {
		log.Info().Msg("weather: alfred is disabled; the station stays on this node")
	} else {
		opts.Gossip = weatherGossip{client: manager.Client()}
	}

	mon := weather.New(opts)
	done := make(chan struct{})

	go func() {
		defer close(done)

		mon.Run(ctx)
	}()

	return mon, done
}

// weatherOptions maps the config section onto monitor options.
func weatherOptions(wc config.WeatherConfig) weather.Options {
	return weather.Options{
		PollInterval:    wc.PollInterval,
		PublishInterval: wc.PublishInterval,
		StaleAfter:      wc.StaleAfter,
		Expiry:          wc.Expiry,
		GustWindow:      wc.GustWindow,
	}
}

// openWeatherSensor opens the configured sensor.
func openWeatherSensor(wc config.WeatherConfig) (weather.Sensor, error) {
	if wc.SensorModel == config.WeatherModelSim {
		return weather.NewSimSensor(nil), nil
	}

	port, err := weather.OpenSerial(wc.SensorDevice, wc.SensorBaud)
	if err != nil {
		return nil, fmt.Errorf("open weather sensor: %w", err)
	}

	return weather.NewSEN0657(port, weather.SEN0657Options{
		Address:       byte(wc.SensorAddress), //nolint:gosec // config bounds it to [1, 247]
		PressureScale: wc.PressureScale,
	}), nil
}

// weatherPosition adapts the GNSS service; nil (GNSS disabled) means the
// report carries no position.
func weatherPosition(gps *gpsd.GPSService) func() (weather.Position, bool) {
	if gps == nil {
		return nil
	}

	return func() (weather.Position, bool) {
		p := gps.GetPosition()
		if !p.Valid {
			return weather.Position{}, false
		}

		return weather.Position{Latitude: p.Latitude, Longitude: p.Longitude, AltitudeM: p.Altitude}, true
	}
}

// weatherAPI converts the monitor for the API server, keeping a disabled
// monitor a nil interface rather than a typed nil pointer.
func weatherAPI(mon *weather.Monitor) handlers.WeatherStations {
	if mon == nil {
		return nil
	}

	return mon
}

// weatherIdentity returns the batman interface MAC and the hostname that
// identify this node in weather reports. Either may be empty; receivers
// then fall back to the alfred source MAC.
func weatherIdentity(batIface string) (primaryMAC, hostname string) {
	if ifc, err := net.InterfaceByName(batIface); err == nil {
		primaryMAC = strings.ToLower(ifc.HardwareAddr.String())
	}

	hostname, _ = os.Hostname() //nolint:errcheck // an unknown hostname publishes empty

	return primaryMAC, hostname
}
