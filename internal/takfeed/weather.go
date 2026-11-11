package takfeed

import (
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cotproto"
	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
)

// TypeWeather is a friendly emplaced ground sensor: the weather station
// wired to this node. ATAK draws it natively.
const TypeWeather = "a-f-G-E-S-E"

// Unit conversions for the map label: ATAK users here read US units.
const (
	msToMPH     = 2.2369363
	mmToInches  = 0.0393701
	compassStep = 22.5
	// DefaultWeatherMaxAge is how old a reading may be before the marker
	// is pulled; it matches weather.staleAfter's default.
	DefaultWeatherMaxAge = 5 * time.Minute
	// weatherStale keeps the marker up across a few missed ticks.
	weatherStale = 2 * time.Minute
)

// compass16 names the 16 wind directions, clockwise from north.
var compass16 = [...]string{ //nolint:gochecknoglobals // read-only lookup table
	"N", "NNE", "NE", "ENE", "E", "ESE", "SE", "SSE",
	"S", "SSW", "SW", "WSW", "W", "WNW", "NW", "NNW",
}

// WeatherUID is the ATAK uid of a node's weather station marker.
func WeatherUID(callsign string) string {
	return callsign + "-wx"
}

// WeatherEvent builds the weather station marker at pos. The label
// carries the headline (temperature and wind) so it reads on the map
// without a tap; the remarks carry every value.
func WeatherEvent(rep *weatherv1.WeatherReport, callsign string, pos Position, now time.Time) *cotproto.TakMessage {
	ev := baseEvent(TypeWeather, WeatherUID(callsign), now, weatherStale)
	ev.Lat, ev.Lon, ev.Hae = pos.Lat, pos.Lon, pos.HAE

	label := weatherLabel(rep.GetReading(), callsign)

	ev.Detail = &cotproto.Detail{
		XmlDetail: remarksXML(weatherRemarks(rep, callsign)),
		Contact:   &cotproto.Contact{Callsign: label},
	}

	return &cotproto.TakMessage{CotEvent: ev}
}

// weatherLabel is "WX mast-1 41°F NW 12 mph" (gust appended as G20).
func weatherLabel(r *weatherv1.WeatherReading, callsign string) string {
	var b strings.Builder

	b.Grow(48)
	b.WriteString("WX ")
	b.WriteString(callsign)

	if r.TemperatureC != nil {
		b.WriteByte(' ')
		b.WriteString(fahrenheit(r.GetTemperatureC()))
	}

	if r.WindSpeedMs != nil {
		b.WriteByte(' ')

		if r.WindDirectionDeg != nil {
			b.WriteString(compass(r.GetWindDirectionDeg()))
			b.WriteByte(' ')
		}

		b.WriteString(strconv.Itoa(mph(r.GetWindSpeedMs())))

		if g := mph(r.GetWindGustMs()); r.WindGustMs != nil && g > mph(r.GetWindSpeedMs()) {
			b.WriteString("G")
			b.WriteString(strconv.Itoa(g))
		}

		b.WriteString(" mph")
	}

	return b.String()
}

func weatherRemarks(rep *weatherv1.WeatherReport, callsign string) string {
	r := rep.GetReading()

	var b strings.Builder

	b.Grow(320)
	b.WriteString("Weather at ")
	b.WriteString(callsign)
	b.WriteString(".")

	if r.TemperatureC != nil {
		b.WriteString(" Temperature ")
		b.WriteString(fahrenheit(r.GetTemperatureC()))
		b.WriteString(" (")
		b.WriteString(strconv.FormatFloat(r.GetTemperatureC(), 'f', 1, 64))
		b.WriteString("°C).")
	}

	if r.WindSpeedMs != nil {
		b.WriteString(" Wind ")

		if r.WindDirectionDeg != nil {
			b.WriteString("from ")
			b.WriteString(compass(r.GetWindDirectionDeg()))
			b.WriteString(" (")
			b.WriteString(strconv.Itoa(int(math.Round(r.GetWindDirectionDeg()))))
			b.WriteString("°) ")
		}

		b.WriteString(strconv.Itoa(mph(r.GetWindSpeedMs())))
		b.WriteString(" mph")

		if r.WindGustMs != nil {
			b.WriteString(", gusts ")
			b.WriteString(strconv.Itoa(mph(r.GetWindGustMs())))
			b.WriteString(" mph")
		}

		b.WriteString(".")
	}

	if r.HumidityPct != nil {
		b.WriteString(" Humidity ")
		b.WriteString(strconv.Itoa(int(math.Round(r.GetHumidityPct()))))
		b.WriteString("%.")
	}

	if r.PressureHpa != nil {
		b.WriteString(" Pressure ")
		b.WriteString(strconv.FormatFloat(r.GetPressureHpa(), 'f', 1, 64))
		b.WriteString(" hPa")

		if r.PressureChange_3HHpa != nil {
			b.WriteString(", ")
			b.WriteString(pressureTrend(r.GetPressureChange_3HHpa()))
		}

		b.WriteString(".")
	}

	if r.RainLastHourMm != nil {
		b.WriteString(" Rain last hour ")
		b.WriteString(strconv.FormatFloat(r.GetRainLastHourMm()*mmToInches, 'f', 2, 64))
		b.WriteString(" in.")
	}

	if obs := r.GetObservedAt(); obs != nil {
		b.WriteString(" Read ")
		b.WriteString(obs.AsTime().UTC().Format("15:04Z"))
		b.WriteString(".")
	}

	return b.String()
}

// pressureTrend words a 3-hour change the way forecasters do: under
// 1 hPa is steady.
func pressureTrend(d float64) string {
	amount := strconv.FormatFloat(math.Abs(d), 'f', 1, 64) + " hPa in 3 h"

	switch {
	case d <= -1:
		return "falling " + amount
	case d >= 1:
		return "rising " + amount
	default:
		return "steady"
	}
}

func fahrenheit(c float64) string {
	return strconv.Itoa(int(math.Round(c*9/5+32))) + "°F"
}

func mph(ms float64) int {
	return int(math.Round(ms * msToMPH))
}

func compass(deg float64) string {
	i := int(math.Round(math.Mod(deg+360, 360)/compassStep)) % len(compass16)

	return compass16[i]
}
