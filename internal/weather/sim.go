package weather

import (
	"context"
	"math"
	"time"
)

// Simulated weather around a valley town at about 1,500 m: station
// pressure near 850 hPa, a daily temperature swing, a breeze that builds
// in the afternoon, and a light shower every few hours.
const (
	simBaseTempC      = 10.0
	simTempSwingC     = 8.0
	simBaseHumidity   = 45.0
	simHumiditySwing  = 20.0
	simBasePressure   = 850.0
	simPressureSwing  = 3.0
	simPressurePeriod = 9 * time.Hour
	simBaseWindMS     = 3.0
	simWindSwingMS    = 2.5
	simGustMS         = 1.5
	simBaseDirDeg     = 200.0
	simDirSwingDeg    = 60.0
	simMaxLux         = 90000.0
	simShowerPeriod   = 4 * time.Hour
	simShowerLength   = 20 * time.Minute
	simShowerRateMMH  = 3.0
	simPeakHour       = 15.0
	hoursPerDay       = 24.0
	secondsPerHour    = 3600.0
)

// SimSensor produces deterministic, plausible readings from the clock,
// for testing a node, the web app and ATAK without hardware.
type SimSensor struct {
	now   func() time.Time
	start time.Time
}

// NewSimSensor returns a simulated sensor. now defaults to time.Now.
func NewSimSensor(now func() time.Time) *SimSensor {
	if now == nil {
		now = time.Now
	}

	return &SimSensor{now: now, start: now()}
}

// Read returns the simulated values for the current time.
func (s *SimSensor) Read(ctx context.Context) (Sample, error) {
	if err := ctx.Err(); err != nil {
		return Sample{}, err //nolint:wrapcheck // context errors pass through unchanged
	}

	return simSample(s.now(), s.start), nil
}

// Close does nothing.
func (s *SimSensor) Close() error { return nil }

func simSample(t, start time.Time) Sample {
	local := t.UTC()
	hour := float64(local.Hour()) + float64(local.Minute())/60
	day := math.Cos(2 * math.Pi * (hour - simPeakHour) / hoursPerDay) // 1 at the afternoon peak
	secs := float64(t.Unix())

	var s Sample

	s.set(FieldTemperature, &s.TemperatureC, round1(simBaseTempC+simTempSwingC*day), minTemperatureC, maxTemperatureC)
	s.set(FieldHumidity, &s.HumidityPct, round1(simBaseHumidity-simHumiditySwing*day), 0, maxHumidityPct)
	s.set(FieldPressure, &s.PressureHPa,
		round1(simBasePressure+simPressureSwing*math.Sin(2*math.Pi*secs/simPressurePeriod.Seconds())), minPressureHPa, maxPressureHPa)

	// Two incommensurate sines stand in for gusts without a random source.
	wind := simBaseWindMS + simWindSwingMS*(day+1)/2 + simGustMS*math.Sin(secs/7)*math.Sin(secs/13)
	s.set(FieldWindSpeed, &s.WindSpeedMS, round1(math.Max(0, wind)), 0, maxWindSpeedMS)

	dir := math.Mod(simBaseDirDeg+simDirSwingDeg*math.Sin(secs/1800)+maxDirectionDeg, maxDirectionDeg)
	s.set(FieldWindDirection, &s.WindDirectionDeg, math.Round(dir), 0, maxDirectionDeg)

	s.set(FieldLight, &s.LightLux, math.Round(math.Max(0, simMaxLux*day)), 0, maxLightLux)
	s.set(FieldRainTotal, &s.RainTotalMM, round1(simRain(t.Sub(start))), 0, maxRainTotalMM)

	return s
}

// simRain is the rain since start: simShowerLength of rain at the start
// of every simShowerPeriod.
func simRain(elapsed time.Duration) float64 {
	if elapsed < 0 {
		return 0
	}

	showers := elapsed / simShowerPeriod
	into := min(elapsed%simShowerPeriod, simShowerLength)
	raining := float64(showers)*simShowerLength.Seconds() + into.Seconds()

	return raining / secondsPerHour * simShowerRateMMH
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}
