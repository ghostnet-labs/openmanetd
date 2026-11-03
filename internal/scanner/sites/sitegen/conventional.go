package main

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// maxLabel bounds a channel label (runes) to keep the table small.
const maxLabel = 56

// conventionalLines turns the analog rows of the non-trunked categories
// into C records. Digital (P25) and data rows are skipped, as are the
// summary rows that duplicate per-transmitter weather entries. Identical
// state/category/frequency/label rows are written once.
func conventionalLines(rows []map[string]string) ([]string, error) {
	seen := make(map[string]struct{}, len(rows))
	out := make([]string, 0, len(rows))

	for i, r := range rows {
		cat := category(r["category"])
		mode, spacing := analogMode(r["mode"], cat)

		if cat == sites.CategoryUnknown || mode == sites.ModeUnknown || summaryRow(r) {
			continue
		}

		hz, err := mhzToHz(r["freq_mhz"])
		if err != nil {
			return nil, fmt.Errorf("scanner-frequencies %s row %d: %w", r["category"], i+2, err)
		}

		lat, lon, err := position(r)
		if err != nil {
			return nil, fmt.Errorf("scanner-frequencies %s row %d: %w", r["category"], i+2, err)
		}

		label := channelLabel(r, cat)
		if label == "" {
			return nil, fmt.Errorf("scanner-frequencies %s row %d: no label", r["category"], i+2)
		}

		key := sites.ChannelKey(r[colState], cat, hz, label)
		if _, dup := seen[key]; dup {
			continue
		}

		seen[key] = struct{}{}

		out = append(out, strings.Join([]string{
			"C", r[colState], strconv.Itoa(int(cat)), strconv.Itoa(int(mode)), strconv.Itoa(spacing),
			strconv.FormatUint(hz, 10), lat, lon, label,
		}, "\t"))
	}

	return out, nil
}

func category(s string) sites.ChannelCategory {
	switch s {
	case "conventional":
		return sites.CategoryConventional
	case "aviation":
		return sites.CategoryAviation
	case "interop":
		return sites.CategoryInterop
	case "weather":
		return sites.CategoryWeather
	default:
		return sites.CategoryUnknown
	}
}

// analogMode maps the source mode column. "NFM" is 12.5 kHz narrowband
// (NOAA weather radio uses 5 kHz deviation, so 25 kHz); "FM" rows are the
// 800 MHz NPSPAC mutual aid channels (4 kHz deviation, 25 kHz filter); "AM"
// is 25 kHz aviation (no 8.33 kHz channels in the US).
func analogMode(s string, cat sites.ChannelCategory) (sites.ChannelMode, int) {
	switch s {
	case "NFM":
		if cat == sites.CategoryWeather {
			return sites.ModeNFM, 25000
		}

		return sites.ModeNFM, 12500
	case "FM":
		return sites.ModeNFM, 25000
	case "AM":
		return sites.ModeAM, 25000
	default:
		return sites.ModeUnknown, 0
	}
}

// summaryRow reports interop rows that only restate the per-transmitter
// weather entries ("WX1-WX7 NOAA Weather Radio").
func summaryRow(r map[string]string) bool {
	return r["category"] == "interop" && strings.HasPrefix(r[colSite], "WX")
}

func position(r map[string]string) (lat, lon string, err error) {
	lat, lon = strings.TrimSpace(r[colLat]), strings.TrimSpace(r[colLon])
	if lat == "" && lon == "" {
		return "", "", nil
	}

	for _, v := range []string{lat, lon} {
		if _, perr := strconv.ParseFloat(v, 64); perr != nil {
			return "", "", fmt.Errorf("bad position %q,%q", lat, lon)
		}
	}

	return lat, lon, nil
}

func channelLabel(r map[string]string, cat sites.ChannelCategory) string {
	var s string

	switch cat {
	case sites.CategoryAviation:
		s = r[colSite]
		if r[colState] == sites.NationwideState {
			s += " (" + r["use"] + ")"
		}
	case sites.CategoryInterop:
		s = r[colSite] + " - " + r["use"]
	case sites.CategoryWeather:
		s = r[colSystem] + " " + r[colSite]
	default:
		s = r["use"]
	}

	s = strings.Join(strings.Fields(strings.Map(func(c rune) rune {
		if c == '\t' || c == '\n' || c == '\r' {
			return ' '
		}

		return c
	}, s)), " ")

	if utf8.RuneCountInString(s) > maxLabel {
		s = strings.TrimSpace(string([]rune(s)[:maxLabel-1])) + "…"
	}

	return s
}
