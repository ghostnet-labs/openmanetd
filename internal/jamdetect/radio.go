package jamdetect

import (
	"bufio"
	"bytes"
	"slices"
	"strings"
)

// Band is the band a radio operates in. The values match the band strings
// the ATAK feed and the API use.
type Band string

// Bands.
const (
	BandUnknown Band = ""
	BandS1G     Band = "s1g"
	Band2G4     Band = "2g4"
	Band5G      Band = "5g"
	Band6G      Band = "6g"
)

// Radio is one wireless interface the detector reads. One interface per
// wiphy is enough: the survey describes the wiphy's channel.
type Radio struct {
	Ifname string
	PHY    string
	// HaLow is true for a Morse Micro radio. Its driver may report a
	// mapped 5 GHz frequency, so the band cannot come from the frequency.
	HaLow bool
}

// iwInterface is one `iw dev` interface entry.
type iwInterface struct {
	name string
	phy  string
	typ  string
}

// ParseIWDev parses `iw dev` output and returns one radio per wiphy,
// preferring a mesh point, then an AP, then a station interface (monitor
// interfaces are only used when nothing else exists on the wiphy). The
// result is sorted by PHY. isHaLow may be nil.
func ParseIWDev(out []byte, isHaLow func(ifname string) bool) []Radio {
	ifaces := make([]iwInterface, 0, 4)

	phy := ""

	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())

		switch {
		case strings.HasPrefix(line, "phy#"):
			phy = "phy" + strings.TrimPrefix(line, "phy#")
		case strings.HasPrefix(line, "Interface "):
			ifaces = append(ifaces, iwInterface{name: strings.TrimSpace(strings.TrimPrefix(line, "Interface ")), phy: phy})
		case strings.HasPrefix(line, "type ") && len(ifaces) > 0:
			ifaces[len(ifaces)-1].typ = strings.TrimSpace(strings.TrimPrefix(line, "type "))
		}
	}

	best := make(map[string]iwInterface, len(ifaces))

	for _, ifc := range ifaces {
		if ifc.phy == "" || ifc.name == "" {
			continue
		}

		cur, ok := best[ifc.phy]
		if !ok || typeRank(ifc.typ) < typeRank(cur.typ) || (typeRank(ifc.typ) == typeRank(cur.typ) && ifc.name < cur.name) {
			best[ifc.phy] = ifc
		}
	}

	radios := make([]Radio, 0, len(best))

	for _, ifc := range best {
		r := Radio{Ifname: ifc.name, PHY: ifc.phy}
		if isHaLow != nil {
			r.HaLow = isHaLow(ifc.name)
		}

		radios = append(radios, r)
	}

	slices.SortFunc(radios, func(a, b Radio) int { return strings.Compare(a.PHY, b.PHY) })

	return radios
}

func typeRank(t string) int {
	switch t {
	case "mesh point":
		return 0
	case "AP":
		return 1
	case "managed":
		return 2
	case "monitor":
		return 4
	default:
		return 3
	}
}

// BandFor classifies a radio's band from its frequency. A HaLow radio is
// always S1G whatever frequency its driver reports.
func BandFor(halow bool, freqMHz float64) Band {
	switch {
	case halow || (freqMHz >= 700 && freqMHz < 1000):
		return BandS1G
	case freqMHz >= 2400 && freqMHz < 2500:
		return Band2G4
	case freqMHz >= 4900 && freqMHz < 5925:
		return Band5G
	case freqMHz >= 5925 && freqMHz < 7200:
		return Band6G
	default:
		return BandUnknown
	}
}
