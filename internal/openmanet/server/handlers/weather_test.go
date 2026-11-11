package handlers_test

import (
	"context"
	"testing"

	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherService_ListWeatherStations_disabled(t *testing.T) {
	svc := &handlers.WeatherService{Log: zerolog.Nop()}

	res, err := svc.ListWeatherStations(context.Background(), &weatherv1.ListWeatherStationsRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetSensorEnabled())
	assert.Empty(t, res.GetStations())
}

func TestWeatherService_ListWeatherStations(t *testing.T) {
	tests := []struct {
		name     string
		enabled  bool
		stations []*weatherv1.WeatherStation
	}{
		{"receive only, nothing heard", false, nil},
		{"receive only, one mast heard", false, []*weatherv1.WeatherStation{
			{Report: &weatherv1.WeatherReport{Hostname: "mast-2"}, AgeSeconds: 12},
		}},
		{"local sensor and a remote", true, []*weatherv1.WeatherStation{
			{Report: &weatherv1.WeatherReport{Hostname: "mast-1"}, Local: true},
			{Report: &weatherv1.WeatherReport{Hostname: "mast-2"}, Stale: true, AgeSeconds: 400},
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeWeatherStations{enabled: tc.enabled, stations: tc.stations}
			svc := &handlers.WeatherService{Log: zerolog.Nop(), Monitor: fake}

			res, err := svc.ListWeatherStations(context.Background(), &weatherv1.ListWeatherStationsRequest{})
			require.NoError(t, err)
			assert.Equal(t, tc.enabled, res.GetSensorEnabled())
			assert.Equal(t, tc.stations, res.GetStations())
			assert.Equal(t, 1, fake.getCalls())
		})
	}
}
