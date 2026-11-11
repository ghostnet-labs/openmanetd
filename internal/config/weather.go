package config

import (
	"fmt"
	"slices"
	"strings"
	"time"
)

// Weather station defaults. Sharing is on everywhere so every node shows
// the stations it hears; polling a sensor is off until a node has one
// wired to it (normally a mast).
const (
	DefaultWeatherEnable          = true
	DefaultWeatherSensorEnable    = false
	DefaultWeatherSensorModel     = WeatherModelSEN0657
	DefaultWeatherSensorDevice    = "/dev/ttyUSB0"
	DefaultWeatherSensorBaud      = 4800
	DefaultWeatherSensorAddress   = 1
	DefaultWeatherPressureScale   = 1.0
	DefaultWeatherPollInterval    = 5 * time.Second
	DefaultWeatherPublishInterval = 30 * time.Second
	DefaultWeatherStaleAfter      = 5 * time.Minute
	DefaultWeatherExpiry          = 30 * time.Minute
	DefaultWeatherGustWindow      = 10 * time.Minute
)

// Weather sensor models.
const (
	// WeatherModelSEN0657 is the DFRobot SEN0657 ultrasonic 7-in-1 sensor
	// on RS485 Modbus-RTU.
	WeatherModelSEN0657 = "sen0657"
	// WeatherModelSim is a simulated sensor for testing a node, the web
	// app and ATAK without hardware.
	WeatherModelSim = "sim"
)

// Accepted ranges. The sensor wants at least 200 ms between polls.
const (
	minWeatherPoll    = time.Second
	maxWeatherPoll    = time.Minute
	minWeatherPublish = 5 * time.Second
	maxWeatherPublish = 5 * time.Minute
	minWeatherStale   = 30 * time.Second
	maxWeatherStale   = time.Hour
	maxWeatherExpiry  = 24 * time.Hour
	minWeatherGust    = time.Minute
	maxWeatherGust    = time.Hour
	maxModbusAddress  = 247
	maxPressureScale  = 1000.0
)

// weatherBauds are the rates the SEN0657 supports.
var weatherBauds = []int{1200, 2400, 4800, 9600, 19200, 38400, 57600, 115200} //nolint:gochecknoglobals // read-only lookup table

// WeatherConfig holds the weather station settings (YAML section
// "weather"). It is returned by value from GetWeather.
type WeatherConfig struct {
	// SensorModel is WeatherModelSEN0657 or WeatherModelSim.
	SensorModel string
	// SensorDevice is the serial port the RS485 adapter appears as.
	SensorDevice string
	// Problems lists values rejected at load and replaced by defaults.
	Problems []string
	// SensorBaud is the sensor's serial rate.
	SensorBaud int
	// SensorAddress is the sensor's Modbus address.
	SensorAddress int
	// PressureScale multiplies the raw pressure register to get hPa.
	PressureScale float64
	// PollInterval is how often the sensor is read.
	PollInterval time.Duration
	// PublishInterval is how often this node shares its reading and
	// refreshes the readings other nodes share.
	PublishInterval time.Duration
	// StaleAfter marks a reading stale once it is this old.
	StaleAfter time.Duration
	// Expiry drops a station that has been silent this long.
	Expiry time.Duration
	// GustWindow is the period the reported gust is the maximum over.
	GustWindow time.Duration
	// Enable turns weather sharing on: this node lists the stations it
	// hears and, with SensorEnable, shares its own.
	Enable bool
	// SensorEnable polls the sensor wired to this node.
	SensorEnable bool
}

// loadWeather reads the weather section, applying defaults. The caller
// holds c.mu.
func (c *Config) loadWeather() WeatherConfig {
	wc := WeatherConfig{
		Enable:          DefaultWeatherEnable,
		SensorEnable:    DefaultWeatherSensorEnable,
		SensorModel:     DefaultWeatherSensorModel,
		SensorDevice:    DefaultWeatherSensorDevice,
		SensorBaud:      DefaultWeatherSensorBaud,
		SensorAddress:   DefaultWeatherSensorAddress,
		PressureScale:   DefaultWeatherPressureScale,
		PollInterval:    DefaultWeatherPollInterval,
		PublishInterval: DefaultWeatherPublishInterval,
		StaleAfter:      DefaultWeatherStaleAfter,
		Expiry:          DefaultWeatherExpiry,
		GustWindow:      DefaultWeatherGustWindow,
	}

	if c.v.IsSet("weather.enable") {
		wc.Enable = c.v.GetBool("weather.enable")
	}

	if c.v.IsSet("weather.sensor.enable") {
		wc.SensorEnable = c.v.GetBool("weather.sensor.enable")
	}

	c.loadWeatherSensor(&wc)
	c.loadWeatherDurations(&wc)

	// A station must stay listed at least until it turns stale, or it
	// would vanish before the UI could grey it out.
	if wc.Expiry < wc.StaleAfter {
		wc.Problems = append(wc.Problems, fmt.Sprintf("weather.expiry: %s is shorter than weather.staleAfter %s; using %s", wc.Expiry, wc.StaleAfter, wc.StaleAfter))
		wc.Expiry = wc.StaleAfter
	}

	return wc
}

// loadWeatherSensor reads weather.sensor.*. The caller holds c.mu.
func (c *Config) loadWeatherSensor(wc *WeatherConfig) {
	if c.v.IsSet("weather.sensor.model") {
		model := strings.ToLower(strings.TrimSpace(c.v.GetString("weather.sensor.model")))
		if model == WeatherModelSEN0657 || model == WeatherModelSim {
			wc.SensorModel = model
		} else {
			wc.Problems = append(wc.Problems, fmt.Sprintf("weather.sensor.model: %q is not %q or %q", model, WeatherModelSEN0657, WeatherModelSim))
		}
	}

	if dev := strings.TrimSpace(c.v.GetString("weather.sensor.device")); dev != "" {
		if strings.HasPrefix(dev, "/dev/") {
			wc.SensorDevice = dev
		} else {
			wc.Problems = append(wc.Problems, fmt.Sprintf("weather.sensor.device: %q is not under /dev/", dev))
		}
	}

	if c.v.IsSet("weather.sensor.baud") {
		if v := c.v.GetInt("weather.sensor.baud"); slices.Contains(weatherBauds, v) {
			wc.SensorBaud = v
		} else {
			wc.Problems = append(wc.Problems, fmt.Sprintf("weather.sensor.baud: %d is not one of %v", v, weatherBauds))
		}
	}

	if c.v.IsSet("weather.sensor.address") {
		if v := c.v.GetInt("weather.sensor.address"); v >= 1 && v <= maxModbusAddress {
			wc.SensorAddress = v
		} else {
			wc.Problems = append(wc.Problems, fmt.Sprintf("weather.sensor.address: %d is outside [1, %d]", v, maxModbusAddress))
		}
	}

	if c.v.IsSet("weather.sensor.pressureScale") {
		if v := c.v.GetFloat64("weather.sensor.pressureScale"); v > 0 && v <= maxPressureScale {
			wc.PressureScale = v
		} else {
			wc.Problems = append(wc.Problems, fmt.Sprintf("weather.sensor.pressureScale: %v is outside (0, %v]", v, maxPressureScale))
		}
	}
}

// loadWeatherDurations reads the weather durations. The caller holds
// c.mu.
func (c *Config) loadWeatherDurations(wc *WeatherConfig) {
	durations := []struct {
		dst      *time.Duration
		key      string
		min, max time.Duration
	}{
		{&wc.PollInterval, "weather.pollInterval", minWeatherPoll, maxWeatherPoll},
		{&wc.PublishInterval, "weather.publishInterval", minWeatherPublish, maxWeatherPublish},
		{&wc.StaleAfter, "weather.staleAfter", minWeatherStale, maxWeatherStale},
		{&wc.Expiry, "weather.expiry", minWeatherStale, maxWeatherExpiry},
		{&wc.GustWindow, "weather.gustWindow", minWeatherGust, maxWeatherGust},
	}
	for _, d := range durations {
		raw := c.v.GetString(d.key)
		if raw == "" {
			continue
		}

		v, err := time.ParseDuration(raw)
		if err == nil && v >= d.min && v <= d.max {
			*d.dst = v
		} else {
			wc.Problems = append(wc.Problems, fmt.Sprintf("%s: %q is not a duration in [%s, %s]", d.key, raw, d.min, d.max))
		}
	}
}

// GetWeather returns the weather station settings.
func (c *Config) GetWeather() WeatherConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	wc := c.Weather
	wc.Problems = slices.Clone(wc.Problems)

	return wc
}
