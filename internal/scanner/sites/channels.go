package sites

import (
	"bytes"
	"cmp"
	"compress/gzip"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"slices"
	"strconv"
	"strings"
)

// ChannelCategory is the source category of a conventional channel.
type ChannelCategory uint8

// ChannelCategory values (the GHO-60 category column).
const (
	CategoryUnknown ChannelCategory = iota
	CategoryConventional
	CategoryAviation
	CategoryInterop
	CategoryWeather
)

// ChannelMode is the analog demodulation of a conventional channel.
type ChannelMode uint8

// ChannelMode values.
const (
	ModeUnknown ChannelMode = iota
	// ModeNFM is narrowband FM (source modes "NFM" and "FM").
	ModeNFM
	// ModeAM is AM (aviation).
	ModeAM
)

// NationwideState is the state column of nationwide channels (national
// interop and aviation emergency/guard frequencies).
const NationwideState = "US"

// Channel is one analog conventional channel: a land mobile or state
// interop channel, an aviation frequency, a national interop channel or a
// NOAA weather transmitter. Digital (P25) conventional channels are not in
// the table.
type Channel struct {
	// Key is "C-" + 8 hex digits of FNV-1a over state|category|freq|label.
	Key   string
	State string
	Label string
	// FrequencyHz is the receive frequency.
	FrequencyHz uint64
	// Lat and Lon are set when HasPosition: airport, ARTCC site or
	// weather transmitter coordinates. Statewide and nationwide channels
	// have none.
	Lat         float64
	Lon         float64
	SpacingHz   int
	Category    ChannelCategory
	Mode        ChannelMode
	HasPosition bool
}

// Nationwide reports whether the channel is a nationwide one (always
// scanned regardless of position).
func (c *Channel) Nationwide() bool { return c.State == NationwideState }

// ChannelKey derives the stable channel key.
func ChannelKey(state string, cat ChannelCategory, freqHz uint64, label string) string {
	h := fnv.New32a()
	_, _ = h.Write([]byte(state + "|" + strconv.Itoa(int(cat)) + "|" + strconv.FormatUint(freqHz, 10) + "|" + label)) // hash writes never fail

	return "C-" + fmt.Sprintf("%08x", h.Sum32())
}

// Channels decodes the conventional channels from the embedded table. Each
// call decodes afresh, so long-lived callers cache the result.
func Channels() ([]Channel, error) {
	zr, err := gzip.NewReader(bytes.NewReader(embedded))
	if err != nil {
		return nil, fmt.Errorf("open embedded site table: %w", err)
	}
	defer zr.Close()

	t, err := ParseTable(zr)
	if err != nil {
		return nil, err
	}

	return t.Channels, nil
}

const channelFields = 8

// parseChannel decodes a C record:
//
//	C<TAB>state<TAB>category<TAB>mode<TAB>spacingHz<TAB>freqHz<TAB>lat<TAB>lon<TAB>label
//
// lat and lon are both empty for channels without a position.
func parseChannel(rest string) (Channel, error) {
	f := strings.SplitN(rest, "\t", channelFields)
	if len(f) != channelFields {
		return Channel{}, fmt.Errorf("want %d fields, got %d", channelFields, len(f))
	}

	c := Channel{State: f[0], Label: f[7]}

	var errs []error

	num := func(v string, bits int) uint64 {
		n, err := strconv.ParseUint(v, 10, bits)
		if err != nil {
			errs = append(errs, fmt.Errorf("bad number %q: %w", v, err))
		}

		return n
	}

	c.Category = ChannelCategory(num(f[1], 8))
	c.Mode = ChannelMode(num(f[2], 8))
	c.SpacingHz = int(num(f[3], 32))
	c.FrequencyHz = num(f[4], 64)

	if f[5] != "" || f[6] != "" {
		lat, latErr := strconv.ParseFloat(f[5], 64)
		lon, lonErr := strconv.ParseFloat(f[6], 64)

		if latErr != nil || lonErr != nil {
			errs = append(errs, fmt.Errorf("bad position %q,%q", f[5], f[6]))
		}

		c.Lat, c.Lon, c.HasPosition = lat, lon, true
	}

	if c.Category < CategoryConventional || c.Category > CategoryWeather {
		errs = append(errs, fmt.Errorf("unknown category %d", c.Category))
	}

	if c.Mode != ModeNFM && c.Mode != ModeAM {
		errs = append(errs, fmt.Errorf("unknown mode %d", c.Mode))
	}

	if c.FrequencyHz == 0 || c.SpacingHz <= 0 || c.Label == "" {
		errs = append(errs, errors.New("frequency, spacing and label are required"))
	}

	if err := errors.Join(errs...); err != nil {
		return Channel{}, err
	}

	c.Key = ChannelKey(c.State, c.Category, c.FrequencyHz, c.Label)

	return c, nil
}

// NearbyChannel is a selected channel with its distance from the
// reference point (NaN when the channel has no position).
type NearbyChannel struct {
	Channel    *Channel
	DistanceKm float64
	// AlwaysOn is set for nationwide channels, which are selected
	// regardless of position.
	AlwaysOn bool
}

// ChannelQuery selects conventional channels near a position.
type ChannelQuery struct {
	// State is the state the reference point is in (the nearest P25
	// site's state); it selects statewide channels without a position.
	State string
	// Lat and Lon are the reference point; ignored unless Valid.
	Lat, Lon float64
	// RadiusKm bounds channels that have a position.
	RadiusKm float64
	Valid    bool
}

// NearbyChannels returns the channels selected for q: every nationwide
// channel, and with a valid position the positioned channels within the
// radius and the statewide channels of q.State. A frequency appears once:
// a nationwide entry wins, else the nearest. The result is sorted by
// frequency.
func NearbyChannels(all []Channel, q ChannelQuery) []NearbyChannel {
	best := make(map[uint64]NearbyChannel, 64)

	for i := range all {
		c := &all[i]
		n := NearbyChannel{Channel: c, DistanceKm: math.NaN(), AlwaysOn: c.Nationwide()}

		switch {
		case n.AlwaysOn:
		case !q.Valid:
			continue
		case c.HasPosition:
			n.DistanceKm = DistanceKm(q.Lat, q.Lon, c.Lat, c.Lon)
			if n.DistanceKm > q.RadiusKm {
				continue
			}
		case c.State != q.State:
			continue
		}

		if prev, ok := best[c.FrequencyHz]; ok && !better(n, prev) {
			continue
		}

		best[c.FrequencyHz] = n
	}

	out := make([]NearbyChannel, 0, len(best))
	for _, n := range best {
		out = append(out, n)
	}

	slices.SortFunc(out, func(a, b NearbyChannel) int { return cmp.Compare(a.Channel.FrequencyHz, b.Channel.FrequencyHz) })

	return out
}

// better reports whether a should replace b for the same frequency.
func better(a, b NearbyChannel) bool {
	if a.AlwaysOn != b.AlwaysOn {
		return a.AlwaysOn
	}

	an, bn := math.IsNaN(a.DistanceKm), math.IsNaN(b.DistanceKm)

	switch {
	case an && bn:
		return a.Channel.Key < b.Channel.Key
	case an || bn:
		return bn // a positioned entry beats a statewide one
	default:
		return a.DistanceKm < b.DistanceKm
	}
}

// TableData is a fully decoded table.
type TableData struct {
	Sites    []Site
	Channels []Channel
}
