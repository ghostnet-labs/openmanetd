//go:build integration

package handlers_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1/weatherv1connect"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/weather"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWeatherIntegration(t *testing.T, mon handlers.WeatherStations) weatherv1connect.WeatherServiceClient {
	t.Helper()

	mux := http.NewServeMux()
	mux.Handle(weatherv1connect.NewWeatherServiceHandler(&handlers.WeatherService{
		Log:     zerolog.Nop(),
		Monitor: mon,
	}, connect.WithInterceptors(validate.NewInterceptor())))

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return weatherv1connect.NewWeatherServiceClient(http.DefaultClient, srv.URL, connect.WithGRPCWeb())
}

func TestWeatherIntegration_disabled(t *testing.T) {
	client := newWeatherIntegration(t, nil)

	res, err := client.ListWeatherStations(context.Background(), &weatherv1.ListWeatherStationsRequest{})
	require.NoError(t, err)
	assert.False(t, res.GetSensorEnabled())
	assert.Empty(t, res.GetStations())
}

// TestWeatherIntegration_simulatedSensor runs a real Monitor on the
// simulated sensor and reads it back over ConnectRPC, as the web app does.
func TestWeatherIntegration_simulatedSensor(t *testing.T) {
	now := time.Date(2026, 11, 7, 21, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }

	mon := weather.New(weather.Options{
		Log:      zerolog.Nop(),
		Sensor:   weather.NewSimSensor(clock),
		Model:    "sim",
		Now:      clock,
		Identity: func() (string, string) { return "02:00:00:00:00:01", "mast-1" },
	})
	mon.PollOnce(context.Background())

	client := newWeatherIntegration(t, mon)

	res, err := client.ListWeatherStations(context.Background(), &weatherv1.ListWeatherStationsRequest{})
	require.NoError(t, err)
	assert.True(t, res.GetSensorEnabled())
	require.Len(t, res.GetStations(), 1)

	st := res.GetStations()[0]
	assert.True(t, st.GetLocal())
	assert.False(t, st.GetStale())
	assert.Equal(t, int32(0), st.GetAgeSeconds())
	assert.Equal(t, "mast-1", st.GetReport().GetHostname())
	assert.Equal(t, weatherv1.SensorState_SENSOR_STATE_OK, st.GetReport().GetState())

	r := st.GetReport().GetReading()
	require.NotNil(t, r.TemperatureC)
	require.NotNil(t, r.WindSpeedMs)
	require.NotNil(t, r.PressureHpa)
	assert.InDelta(t, 850, r.GetPressureHpa(), 5)
}
