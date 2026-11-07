package weather_test

import (
	"context"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/weather"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSimSensor_plausibleAcrossADay(t *testing.T) {
	clock := newFakeClock()
	sim := weather.NewSimSensor(clock.Now)

	lastRain := 0.0
	minT, maxT := 100.0, -100.0

	for range 24 * 6 {
		s, err := sim.Read(context.Background())
		require.NoError(t, err)

		assert.Equal(t, weather.FieldTemperature|weather.FieldHumidity|weather.FieldPressure|weather.FieldWindSpeed|
			weather.FieldWindDirection|weather.FieldRainTotal|weather.FieldLight, s.Has)
		assert.InDelta(t, 850, s.PressureHPa, 5, "station pressure near 1,500 m")
		assert.InDelta(t, 50, s.HumidityPct, 50)
		assert.GreaterOrEqual(t, s.WindSpeedMS, 0.0)
		assert.Less(t, s.WindDirectionDeg, 360.0)
		assert.GreaterOrEqual(t, s.RainTotalMM, lastRain, "the rain counter never runs backwards")

		lastRain = s.RainTotalMM
		minT, maxT = min(minT, s.TemperatureC), max(maxT, s.TemperatureC)

		clock.Advance(10 * time.Minute)
	}

	assert.InDelta(t, 16, maxT-minT, 1, "a daily swing of about 16 degC")
	assert.Positive(t, lastRain, "a day includes some showers")
	require.NoError(t, sim.Close())
}

func TestSimSensor_deterministic(t *testing.T) {
	a := weather.NewSimSensor(newFakeClock().Now)
	b := weather.NewSimSensor(newFakeClock().Now)

	sa, err := a.Read(context.Background())
	require.NoError(t, err)
	sb, err := b.Read(context.Background())
	require.NoError(t, err)
	assert.Equal(t, sa, sb)
}

func TestSimSensor_canceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := weather.NewSimSensor(nil).Read(ctx)
	require.ErrorIs(t, err, context.Canceled)
}
