package handlers

import (
	"context"

	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/rs/zerolog"
)

// WeatherStations is the subset of *weather.Monitor the handler uses.
type WeatherStations interface {
	SensorEnabled() bool
	Stations() []*weatherv1.WeatherStation
}

// WeatherService implements weatherv1connect.WeatherServiceHandler.
type WeatherService struct {
	Log zerolog.Logger
	// Monitor is the weather monitor; nil when weather.enable is false.
	// It must be a nil interface, not a typed nil pointer.
	Monitor WeatherStations
}

// ListWeatherStations returns this node's station first (when it has a
// sensor), then every station heard over the mesh. With weather disabled
// it succeeds with no stations.
func (s *WeatherService) ListWeatherStations(
	_ context.Context,
	_ *weatherv1.ListWeatherStationsRequest,
) (*weatherv1.ListWeatherStationsResponse, error) {
	s.Log.Debug().Msg("ListWeatherStations request received")

	if s.Monitor == nil {
		return &weatherv1.ListWeatherStationsResponse{}, nil
	}

	return &weatherv1.ListWeatherStationsResponse{
		SensorEnabled: s.Monitor.SensorEnabled(),
		Stations:      s.Monitor.Stations(),
	}, nil
}
