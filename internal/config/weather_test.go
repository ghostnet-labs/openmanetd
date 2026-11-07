package config

import (
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func weatherFromYAML(t *testing.T, doc string) WeatherConfig {
	t.Helper()

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	return NewWithoutWatch(v).GetWeather()
}

func TestGetWeather_defaults(t *testing.T) {
	assert.Equal(t, WeatherConfig{
		Enable:          true,
		SensorEnable:    false,
		SensorModel:     WeatherModelSEN0657,
		SensorDevice:    "/dev/ttyUSB0",
		SensorBaud:      4800,
		SensorAddress:   1,
		PressureScale:   1,
		PollInterval:    5 * time.Second,
		PublishInterval: 30 * time.Second,
		StaleAfter:      5 * time.Minute,
		Expiry:          30 * time.Minute,
		GustWindow:      10 * time.Minute,
	}, NewWithoutWatch(viper.New()).GetWeather(), "every node lists stations; only a node with a sensor polls one")
}

func TestGetWeather_yamlBinding(t *testing.T) {
	wc := weatherFromYAML(t, `
weather:
  enable: false
  pollInterval: 10s
  publishInterval: 1m
  staleAfter: 2m
  expiry: 1h
  gustWindow: 5m
  sensor:
    enable: true
    model: SIM
    device: /dev/serial/by-id/usb-FTDI-if00
    baud: 9600
    address: 7
    pressureScale: 0.1
`)

	assert.Equal(t, WeatherConfig{
		Enable:          false,
		SensorEnable:    true,
		SensorModel:     WeatherModelSim,
		SensorDevice:    "/dev/serial/by-id/usb-FTDI-if00",
		SensorBaud:      9600,
		SensorAddress:   7,
		PressureScale:   0.1,
		PollInterval:    10 * time.Second,
		PublishInterval: time.Minute,
		StaleAfter:      2 * time.Minute,
		Expiry:          time.Hour,
		GustWindow:      5 * time.Minute,
	}, wc)
}

func TestGetWeather_invalidValuesFallBack(t *testing.T) {
	tests := []struct {
		name    string
		doc     string
		problem string
		check   func(t *testing.T, wc WeatherConfig)
	}{
		{"unknown model", "weather:\n  sensor:\n    model: davis\n", "weather.sensor.model", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, WeatherModelSEN0657, wc.SensorModel)
		}},
		{"device outside /dev", "weather:\n  sensor:\n    device: ttyUSB0\n", "weather.sensor.device", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherSensorDevice, wc.SensorDevice)
		}},
		{"unsupported baud", "weather:\n  sensor:\n    baud: 4801\n", "weather.sensor.baud", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherSensorBaud, wc.SensorBaud)
		}},
		{"address zero", "weather:\n  sensor:\n    address: 0\n", "weather.sensor.address", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherSensorAddress, wc.SensorAddress)
		}},
		{"address too high", "weather:\n  sensor:\n    address: 248\n", "weather.sensor.address", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherSensorAddress, wc.SensorAddress)
		}},
		{"pressure scale zero", "weather:\n  sensor:\n    pressureScale: 0\n", "weather.sensor.pressureScale", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.InDelta(t, DefaultWeatherPressureScale, wc.PressureScale, 0)
		}},
		{"poll too fast", "weather:\n  pollInterval: 200ms\n", "weather.pollInterval", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherPollInterval, wc.PollInterval)
		}},
		{"publish not a duration", "weather:\n  publishInterval: soon\n", "weather.publishInterval", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherPublishInterval, wc.PublishInterval)
		}},
		{"gust window too long", "weather:\n  gustWindow: 2h\n", "weather.gustWindow", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, DefaultWeatherGustWindow, wc.GustWindow)
		}},
		{"expiry shorter than stale", "weather:\n  staleAfter: 10m\n  expiry: 1m\n", "weather.expiry", func(t *testing.T, wc WeatherConfig) {
			t.Helper()
			assert.Equal(t, 10*time.Minute, wc.Expiry, "expiry is raised to staleAfter")
		}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			wc := weatherFromYAML(t, tc.doc)
			require.Len(t, wc.Problems, 1)
			assert.Contains(t, wc.Problems[0], tc.problem)
			tc.check(t, wc)
		})
	}
}

func TestGetWeather_problemsAreCopied(t *testing.T) {
	v := viper.New()
	v.Set("weather.sensor.baud", 1)

	c := NewWithoutWatch(v)
	first := c.GetWeather()
	require.Len(t, first.Problems, 1)
	first.Problems[0] = "changed"

	assert.NotEqual(t, "changed", c.GetWeather().Problems[0])
}
