package weather

// Field is a bit set of the values a Sample carries.
type Field uint16

// Fields a sensor can report.
const (
	FieldTemperature Field = 1 << iota
	FieldHumidity
	FieldPressure
	FieldWindSpeed
	FieldWindDirection
	FieldRainTotal
	FieldLight
)

// Sample is one poll of a sensor, in SI units. Has says which values are
// set; a value the sensor did not report, or reported out of its range,
// is left out rather than shown as zero.
type Sample struct {
	TemperatureC     float64
	HumidityPct      float64
	PressureHPa      float64
	WindSpeedMS      float64
	WindDirectionDeg float64
	RainTotalMM      float64
	LightLux         float64
	Has              Field
}

// Valid ranges, from the SEN0657 datasheet. Anything outside is a bad
// read (or, for pressure, a wrong weather.sensor.pressureScale).
const (
	minTemperatureC = -40.0
	maxTemperatureC = 80.0
	maxHumidityPct  = 100.0
	minPressureHPa  = 300.0
	maxPressureHPa  = 1250.0
	maxWindSpeedMS  = 75.0
	maxDirectionDeg = 360.0
	maxLightLux     = 200000.0
	maxRainTotalMM  = 100000.0
)

// set stores v in *dst and marks f when v lies in [lo, hi].
func (s *Sample) set(f Field, dst *float64, v, lo, hi float64) {
	if v < lo || v > hi {
		return
	}

	*dst = v
	s.Has |= f
}
