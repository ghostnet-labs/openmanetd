package openmanet

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/openmanet/go-alfred"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/weather"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWeatherGossip(t *testing.T) {
	src := net.HardwareAddr{2, 0, 0, 0, 0, 9}
	client := &fakeAlfredClient{records: []alfred.Record{
		{Source: src, Version: weatherAlfredDataVersion, Data: []byte("report")},
		{Source: src, Version: weatherAlfredDataVersion + 1, Data: []byte("future format")},
	}}
	g := weatherGossip{client: client}

	require.NoError(t, g.Publish([]byte("mine")))
	assert.Equal(t, []fakeAlfredSet{{dataType: 106, version: weatherAlfredDataVersion, payload: []byte("mine")}}, client.setCalls())

	recs, err := g.Fetch()
	require.NoError(t, err)
	assert.Equal(t, []weather.GossipRecord{{Source: src, Data: []byte("report")}}, recs, "another record version is skipped")
	assert.Equal(t, []uint8{106}, client.requestCalls())

	client.setErr = errFakeAlfred
	require.ErrorIs(t, g.Publish([]byte("x")), errFakeAlfred)

	client.requestErr = errFakeAlfred
	_, err = g.Fetch()
	require.ErrorIs(t, err, errFakeAlfred)
}

func TestWeatherOptions(t *testing.T) {
	got := weatherOptions(config.WeatherConfig{
		PollInterval: time.Second, PublishInterval: 2 * time.Second, StaleAfter: 3 * time.Second,
		Expiry: 4 * time.Second, GustWindow: 5 * time.Second,
	})

	assert.Equal(t, time.Second, got.PollInterval)
	assert.Equal(t, 2*time.Second, got.PublishInterval)
	assert.Equal(t, 3*time.Second, got.StaleAfter)
	assert.Equal(t, 4*time.Second, got.Expiry)
	assert.Equal(t, 5*time.Second, got.GustWindow)
}

func TestOpenWeatherSensor(t *testing.T) {
	sim, err := openWeatherSensor(config.WeatherConfig{SensorModel: config.WeatherModelSim})
	require.NoError(t, err)
	assert.IsType(t, &weather.SimSensor{}, sim)

	_, err = openWeatherSensor(config.WeatherConfig{
		SensorModel:  config.WeatherModelSEN0657,
		SensorDevice: "/dev/does-not-exist-weather",
		SensorBaud:   4800,
	})
	require.ErrorContains(t, err, "/dev/does-not-exist-weather")
}

func TestStartWeather_disabled(t *testing.T) {
	v := viper.New()
	v.Set("weather.enable", false)

	mon, done := startWeather(context.Background(), config.NewWithoutWatch(v), nil, nil, zerolog.Nop())
	assert.Nil(t, mon)
	<-done // already closed, so shutdown never waits on a monitor that never ran
}

func TestStartWeather_missingSensorStillListsMesh(t *testing.T) {
	v := viper.New()
	v.Set("weather.sensor.enable", true)
	v.Set("weather.sensor.device", "/dev/does-not-exist-weather")

	ctx, cancel := context.WithCancel(context.Background())
	mon, done := startWeather(ctx, config.NewWithoutWatch(v), nil, nil, zerolog.Nop())

	t.Cleanup(func() {
		cancel()
		<-done
	})

	require.NotNil(t, mon)
	assert.False(t, mon.SensorEnabled(), "a sensor that will not open leaves the node receive-only")
	assert.Empty(t, mon.Stations())
}

func TestStartWeather_simulatedSensor(t *testing.T) {
	v := viper.New()
	v.Set("weather.sensor.enable", true)
	v.Set("weather.sensor.model", "sim")

	ctx, cancel := context.WithCancel(context.Background())
	mon, done := startWeather(ctx, config.NewWithoutWatch(v), nil, nil, zerolog.Nop())

	require.NotNil(t, mon)
	assert.True(t, mon.SensorEnabled())
	assert.Equal(t, "sim", mon.Local().GetModel())

	cancel()
	<-done
}

func TestWeatherAPI_nilStaysNilInterface(t *testing.T) {
	assert.Nil(t, weatherAPI(nil))
	assert.NotNil(t, weatherAPI(weather.New(weather.Options{})))
}

func TestWeatherIdentity_unknownInterface(t *testing.T) {
	mac, host := weatherIdentity("does-not-exist0")
	assert.Empty(t, mac)
	assert.NotEmpty(t, host)
}
