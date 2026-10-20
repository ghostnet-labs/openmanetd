package radiogate

import (
	"strconv"
	"strings"

	"github.com/openmanet/openmanetd/internal/radiocap"
)

// Channel identity constants. radiocap compares channels by (band,
// center, width) only, so the centers below need to be distinct per
// channel, not regulatory-exact.
const (
	// s1gBaseKHz and s1gStepKHz give an S1G channel number a center on the
	// US 802.11ah channelization (902 MHz + 0.5 MHz × n). Morse reuses the
	// same numbers in every region, so the value is an identity key.
	s1gBaseKHz = 902000
	s1gStepKHz = 500
	// autoCenterKHz is the synthetic center of an "auto" channel. The
	// slot added to it keeps radios distinct (each picks its own channel)
	// while every interface on one radio shares it. Real centers are all
	// above 900 MHz, so the two never collide.
	autoCenterKHz = 1
	// Default widths when htmode is unset or unknown.
	defaultWidthMHz    = 20
	defaultS1GWidthMHz = 1
)

// UCIBand maps a wifi-device's band option to a radiocap band. A morse
// (HaLow) device is always S1G whatever its band option says. Without a
// band option, channels 1..14 mean 2.4 GHz and other numbers 5 GHz; ""
// is returned when the band cannot be determined.
func UCIBand(uciBand string, isMorse bool, channel string) radiocap.Band {
	if isMorse {
		return radiocap.BandS1G
	}

	switch strings.ToLower(strings.TrimSpace(uciBand)) {
	case "2g":
		return radiocap.Band2G4
	case "5g":
		return radiocap.Band5G
	case "6g":
		return radiocap.Band6G
	case "60g":
		return radiocap.Band60G
	case "s1g":
		return radiocap.BandS1G
	}

	n, err := strconv.Atoi(strings.TrimSpace(channel))
	switch {
	case err != nil || n <= 0:
		return ""
	case n <= 14:
		return radiocap.Band2G4
	default:
		return radiocap.Band5G
	}
}

// UCIChannel builds the channel identity of a wifi-device. channel is the
// UCI channel option; "auto" or "" uses a synthetic center derived from
// autoSlot, which must be unique per wifi-device. widthMHz 0 uses the
// band's default width. ok is false for an unknown band or an unparsable
// channel number.
func UCIChannel(band radiocap.Band, channel string, widthMHz uint32, autoSlot int) (radiocap.Channel, bool) {
	if widthMHz == 0 {
		widthMHz = defaultWidthMHz
		if band == radiocap.BandS1G {
			widthMHz = defaultS1GWidthMHz
		}
	}

	ch := radiocap.Channel{Band: band, WidthKHz: int(widthMHz) * 1000}

	channel = strings.TrimSpace(channel)
	if channel == "" || strings.EqualFold(channel, "auto") {
		ch.CenterKHz = autoCenterKHz + max(autoSlot, 0)

		return ch, band != ""
	}

	n, err := strconv.Atoi(channel)
	if err != nil || n <= 0 {
		return radiocap.Channel{}, false
	}

	mhz := centerMHz(band, n)
	if mhz <= 0 {
		return radiocap.Channel{}, false
	}

	if band == radiocap.BandS1G {
		ch.CenterKHz = s1gBaseKHz + s1gStepKHz*n
	} else {
		ch.CenterKHz = mhz * 1000
	}

	return ch, true
}

// centerMHz returns the 802.11 channel center for n in band, or 0. S1G
// returns 1 (the identity is computed separately).
func centerMHz(band radiocap.Band, n int) int {
	switch band {
	case radiocap.BandS1G:
		return 1
	case radiocap.Band2G4:
		if n == 14 {
			return 2484
		}

		return 2407 + 5*n
	case radiocap.Band5G:
		return 5000 + 5*n
	case radiocap.Band6G:
		if n == 2 {
			return 5935
		}

		return 5950 + 5*n
	case radiocap.Band60G:
		return 56160 + 2160*n
	default:
		return 0
	}
}
