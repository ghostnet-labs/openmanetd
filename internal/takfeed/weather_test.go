package takfeed_test

import (
	"context"
	"strings"
	"testing"
	"time"

	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/openmanet/openmanetd/internal/takfeed"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func weatherReport(observed time.Time) *weatherv1.WeatherReport {
	return &weatherv1.WeatherReport{
		Hostname: "manet01",
		Model:    "sen0657",
		State:    weatherv1.SensorState_SENSOR_STATE_OK,
		Reading: &weatherv1.WeatherReading{
			ObservedAt:           timestamppb.New(observed),
			TemperatureC:         proto.Float64(5),    // 41 F
			WindSpeedMs:          proto.Float64(5.4),  // 12 mph
			WindGustMs:           proto.Float64(8.9),  // 20 mph
			WindDirectionDeg:     proto.Float64(315),  // NW
			HumidityPct:          proto.Float64(44.6), // 45 %
			PressureHpa:          proto.Float64(851.2),
			PressureChange_3HHpa: proto.Float64(-2.1),
			RainLastHourMm:       proto.Float64(2.54), // 0.10 in
		},
	}
}

func TestPublishOnce_weatherMarker(t *testing.T) {
	h := newHarness(t, "")
	h.weather.set(weatherReport(testNow.Add(-10 * time.Second)))
	p := takfeed.New(h.opts)

	p.PublishOnce(context.Background())

	wx := byType(h.sender.events(t), takfeed.TypeWeather)
	require.Len(t, wx, 1)
	assert.Equal(t, "manet01-MANET-wx", wx[0].GetUid())
	assert.InDelta(t, selfLat, wx[0].GetLat(), 1e-9, "without its own position the station sits on the node")
	assert.Equal(t, "WX manet01-MANET 41°F NW 12G20 mph", wx[0].GetDetail().GetContact().GetCallsign())

	remarks := wx[0].GetDetail().GetXmlDetail()
	assert.Contains(t, remarks, "Wind from NW (315°) 12 mph, gusts 20 mph.")
	assert.Contains(t, remarks, "Humidity 45%.")
	assert.Contains(t, remarks, "Pressure 851.2 hPa, falling 2.1 hPa in 3 h.")
	assert.Contains(t, remarks, "Rain last hour 0.10 in.")
	assert.Contains(t, remarks, "(5.0°C)")
}

func TestPublishOnce_weatherUsesReportPosition(t *testing.T) {
	h := newHarness(t, "")
	rep := weatherReport(testNow)
	rep.HasPosition, rep.Latitude, rep.Longitude = true, 40.31, -112.01
	h.weather.set(rep)
	h.pos.set(takfeed.Position{}, false)

	takfeed.New(h.opts).PublishOnce(context.Background())

	wx := byType(h.sender.events(t), takfeed.TypeWeather)
	require.Len(t, wx, 1)
	assert.InDelta(t, 40.31, wx[0].GetLat(), 1e-9)
}

func TestPublishOnce_weatherLabelWithoutGustOrDirection(t *testing.T) {
	h := newHarness(t, "")
	rep := weatherReport(testNow)
	rep.Reading.WindGustMs = proto.Float64(5.4)
	rep.Reading.WindDirectionDeg = nil
	h.weather.set(rep)

	takfeed.New(h.opts).PublishOnce(context.Background())

	wx := byType(h.sender.events(t), takfeed.TypeWeather)
	require.Len(t, wx, 1)
	assert.Equal(t, "WX manet01-MANET 41°F 12 mph", wx[0].GetDetail().GetContact().GetCallsign())
}

func TestPublishOnce_weatherLifecycle(t *testing.T) {
	tests := []struct {
		name  string
		yaml  string
		clear func(h *harness)
	}{
		{"station gone", "", func(h *harness) { h.weather.set(nil) }},
		{"reading too old", "", func(h *harness) { h.weather.set(weatherReport(testNow.Add(-6 * time.Minute))) }},
		{"no reading yet", "", func(h *harness) {
			h.weather.set(&weatherv1.WeatherReport{State: weatherv1.SensorState_SENSOR_STATE_STARTING})
		}},
		{"no position", "", func(h *harness) { h.pos.set(takfeed.Position{}, false) }},
		{"tak.weather off", "tak:\n  weather: false\n", nil},
		{"feed off", "tak:\n  enable: false\n", nil},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "")
			h.weather.set(weatherReport(testNow))
			p := takfeed.New(h.opts)

			p.PublishOnce(context.Background())
			require.Len(t, byType(h.sender.events(t), takfeed.TypeWeather), 1)
			h.sender.reset()

			if tc.yaml != "" {
				h.cfg.load(t, tc.yaml)
			}

			if tc.clear != nil {
				tc.clear(h)
			}

			p.PublishOnce(context.Background())

			evs := h.sender.events(t)
			assert.Empty(t, byType(evs, takfeed.TypeWeather))

			var wxDeletes int

			for _, d := range byType(evs, takfeed.TypeDelete) {
				if strings.Contains(d.GetDetail().GetXmlDetail(), `uid="manet01-MANET-wx"`) {
					wxDeletes++
				}
			}

			assert.Equal(t, 1, wxDeletes, "the marker is pulled right away")

			h.sender.reset()
			p.PublishOnce(context.Background())
			assert.Empty(t, byType(h.sender.events(t), takfeed.TypeWeather))
		})
	}
}

func TestPublishOnce_noWeatherSourceSendsNothing(t *testing.T) {
	h := newHarness(t, "")
	h.opts.Weather = nil

	takfeed.New(h.opts).PublishOnce(context.Background())
	assert.Empty(t, byType(h.sender.events(t), takfeed.TypeWeather))
	assert.Empty(t, byType(h.sender.events(t), takfeed.TypeDelete))
}
