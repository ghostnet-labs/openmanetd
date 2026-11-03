// Package aircraft merges the aircraft.json files written by dump1090
// (1090 MHz ADS-B / Mode S) and skyaware978 (978 MHz UAT) into one bounded,
// ICAO-keyed aircraft table with stale eviction.
package aircraft

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// Default aircraft.json paths on an OpenWrt node.
const (
	DefaultDump1090Path    = "/var/run/dump1090/aircraft.json"
	DefaultSkyaware978Path = "/var/run/skyaware978/aircraft.json"
)

// optFloat is a JSON number that may be absent.
type optFloat struct {
	V  float64
	OK bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (o *optFloat) UnmarshalJSON(b []byte) error {
	if bytes.Equal(b, []byte("null")) {
		*o = optFloat{}

		return nil
	}

	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return fmt.Errorf("number: %w", err)
	}

	*o = optFloat{V: v, OK: true}

	return nil
}

// altitude is alt_baro: a number of feet or the string "ground".
type altitude struct {
	Ft     float64
	OK     bool
	Ground bool
}

// UnmarshalJSON implements json.Unmarshaler.
func (a *altitude) UnmarshalJSON(b []byte) error {
	*a = altitude{}

	switch {
	case bytes.Equal(b, []byte("null")):
		return nil
	case len(b) > 0 && b[0] == '"':
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return fmt.Errorf("altitude: %w", err)
		}

		a.Ground = s == "ground"

		return nil
	}

	v, err := strconv.ParseFloat(string(b), 64)
	if err != nil {
		return fmt.Errorf("altitude: %w", err)
	}

	a.Ft, a.OK = v, true

	return nil
}

// rawAircraft is one entry of aircraft.json. dump1090-fa and skyaware978
// share the layout; skyaware978 has used vert_rate where dump1090 writes
// baro_rate/geom_rate, so all three are read.
type rawAircraft struct {
	Hex      string   `json:"hex"`
	Flight   string   `json:"flight"`
	Squawk   string   `json:"squawk"`
	Lat      optFloat `json:"lat"`
	Lon      optFloat `json:"lon"`
	GS       optFloat `json:"gs"`
	Track    optFloat `json:"track"`
	BaroRate optFloat `json:"baro_rate"`
	GeomRate optFloat `json:"geom_rate"`
	VertRate optFloat `json:"vert_rate"`
	AltGeom  optFloat `json:"alt_geom"`
	Seen     optFloat `json:"seen"`
	SeenPos  optFloat `json:"seen_pos"`
	RSSI     optFloat `json:"rssi"`
	AltBaro  altitude `json:"alt_baro"`
}

// rawFile is aircraft.json.
type rawFile struct {
	Aircraft []rawAircraft `json:"aircraft"`
	// Now is the producer's wall clock in Unix seconds.
	Now float64 `json:"now"`
}

// normalizeHex lowercases and trims an address, keeping a leading "~"
// (non-ICAO / TIS-B address).
func normalizeHex(h string) string {
	return strings.ToLower(strings.TrimSpace(h))
}
