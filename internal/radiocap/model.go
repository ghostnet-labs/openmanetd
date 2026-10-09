// Package radiocap models the physical radio capacity of a node and decides
// whether a set of simultaneous radio demands (mesh, migration target mesh,
// EUD access point) can run on it.
//
// A PHY is one independently tunable radio as the kernel exposes it (one
// wiphy). Virtual interfaces on a PHY are not extra radios: every interface
// on a PHY counts against one of the PHY's interface combinations, and the
// distinct channels they use count against that combination's #channels.
// A DBDC chip such as MT7916 appears as two PHYs; a PHY that merely lists two
// bands is still one radio.
//
// The package is a port of the offline resource model in the firmware
// repository (scripts/hardware-qualification/resources.py, radio portion).
// It evaluates the supplied model only. Its answers never assert physical
// qualification: concurrency, RF isolation and driver behavior must still
// be measured on hardware.
//
// All types are plain values. A Model is immutable after NewModel and is safe
// for concurrent use.
package radiocap

import (
	"errors"
	"fmt"
	"slices"
)

// Band identifies a frequency band.
type Band string

// Bands understood by the model.
const (
	BandS1G Band = "s1g" // sub-1 GHz 802.11ah (HaLow)
	Band2G4 Band = "2.4"
	Band5G  Band = "5"
	Band6G  Band = "6"
	Band60G Band = "60"
)

// BandForMHz classifies a channel center frequency. It returns "" for a
// frequency outside every known band.
func BandForMHz(mhz float64) Band {
	switch {
	case mhz > 0 && mhz < 1000:
		return BandS1G
	case mhz >= 2400 && mhz < 2500:
		return Band2G4
	case mhz >= 4900 && mhz < 5935:
		return Band5G
	case mhz >= 5935 && mhz <= 7125:
		return Band6G
	case mhz >= 57000 && mhz <= 71000:
		return Band60G
	default:
		return ""
	}
}

// IfType is an nl80211 interface type spelled the way `iw` prints it.
type IfType string

// Interface types used by roles.
const (
	IfTypeManaged IfType = "managed"
	IfTypeAP      IfType = "AP"
	IfTypeMesh    IfType = "mesh point"
)

// Role is what a demand does on the air.
type Role string

// Roles a demand may request.
const (
	RoleMesh    Role = "mesh"
	RoleAP      Role = "ap"
	RoleStation Role = "station"
)

// IfType maps the role to the interface type that the combination limits
// count. It returns "" for an unknown role.
func (r Role) IfType() IfType {
	switch r {
	case RoleMesh:
		return IfTypeMesh
	case RoleAP:
		return IfTypeAP
	case RoleStation:
		return IfTypeManaged
	default:
		return ""
	}
}

// Limit caps how many interfaces of the listed types may coexist.
type Limit struct {
	Types []IfType `json:"types"`
	Max   int      `json:"max"`
}

// Combination is one entry of a PHY's "valid interface combinations".
type Combination struct {
	Limits        []Limit `json:"limits"`
	MaxInterfaces int     `json:"max_interfaces"`
	MaxChannels   int     `json:"max_channels"`
}

// PHY is one independently tunable radio.
type PHY struct {
	Name           string        `json:"name"`
	Bands          []Band        `json:"bands"`
	SupportedTypes []IfType      `json:"supported_types,omitempty"`
	SoftwareTypes  []IfType      `json:"software_types,omitempty"`
	Combinations   []Combination `json:"combinations"`
	// Qualified is set from qualification evidence, never from enumeration.
	Qualified bool `json:"qualified"`
	// Available is false for a failed, missing or stale PHY.
	Available bool `json:"available"`
	// SharingQualified permits more than one interface on the same channel
	// (for example a same-channel EUD AP beside the mesh). Even when the
	// driver's combinations allow it, sharing is refused until qualified.
	SharingQualified bool `json:"sharing_qualified"`
}

// SetHaLow marks the PHY as an 802.11ah radio. Morse Micro's driver maps S1G
// channels onto 5 GHz channel numbers for mac80211, so `iw` reports 5 GHz
// frequencies; the band must come from the driver type (UCI type=morse).
func (p *PHY) SetHaLow() {
	p.Bands = []Band{BandS1G}
}

// TypeLimit is the most interfaces of one type any combination allows.
type TypeLimit struct {
	Type IfType `json:"type"`
	Max  int    `json:"max"`
}

// Capacity summarizes what a PHY can carry in its most permissive
// combination for each dimension. The dimensions can come from different
// combinations, so Capacity is a ceiling for display; admission always
// checks a single combination.
type Capacity struct {
	PHY           string      `json:"phy"`
	Bands         []Band      `json:"bands"`
	MaxByType     []TypeLimit `json:"max_by_type"`
	MaxInterfaces int         `json:"max_interfaces"`
	MaxChannels   int         `json:"max_channels"`
}

// Capacity returns the per-dimension ceilings of p.
func (p *PHY) Capacity() Capacity {
	c := Capacity{PHY: p.Name, Bands: slices.Clone(p.Bands)}

	byType := make(map[IfType]int, 4)

	for _, combo := range p.Combinations {
		c.MaxInterfaces = max(c.MaxInterfaces, combo.MaxInterfaces)
		c.MaxChannels = max(c.MaxChannels, combo.MaxChannels)

		for _, l := range combo.Limits {
			for _, t := range l.Types {
				byType[t] = max(byType[t], min(l.Max, combo.MaxInterfaces))
			}
		}
	}

	c.MaxByType = make([]TypeLimit, 0, len(byType))
	for t, n := range byType {
		c.MaxByType = append(c.MaxByType, TypeLimit{Type: t, Max: n})
	}

	slices.SortFunc(c.MaxByType, func(a, b TypeLimit) int {
		switch {
		case a.Type < b.Type:
			return -1
		case a.Type > b.Type:
			return 1
		default:
			return 0
		}
	})

	return c
}

// MaxEntries bounds PHYs and demands per evaluation, as in the offline model.
const MaxEntries = 32

// ErrInvalid wraps every validation failure.
var ErrInvalid = errors.New("radiocap: invalid input")

func invalidf(format string, args ...any) error {
	return fmt.Errorf("%w: "+format, append([]any{ErrInvalid}, args...)...)
}

func validatePHYs(phys []PHY) error {
	if len(phys) > MaxEntries {
		return invalidf("at most %d PHYs", MaxEntries)
	}

	seen := make(map[string]struct{}, len(phys))
	for i := range phys {
		p := &phys[i]
		if p.Name == "" {
			return invalidf("PHY %d has no name", i)
		}

		if _, dup := seen[p.Name]; dup {
			return invalidf("duplicate PHY %q", p.Name)
		}

		seen[p.Name] = struct{}{}

		if len(p.Bands) == 0 {
			return invalidf("PHY %q has no bands", p.Name)
		}

		if len(p.Combinations) == 0 {
			return invalidf("PHY %q has no interface combinations", p.Name)
		}

		for _, c := range p.Combinations {
			if err := validateCombination(p.Name, c); err != nil {
				return err
			}
		}
	}

	return nil
}

func validateCombination(phy string, c Combination) error {
	if c.MaxInterfaces <= 0 || c.MaxChannels <= 0 {
		return invalidf("PHY %q combination needs positive total and #channels", phy)
	}

	if len(c.Limits) == 0 {
		return invalidf("PHY %q combination has no limits", phy)
	}

	covered := make(map[IfType]struct{}, 4)

	for _, l := range c.Limits {
		if l.Max <= 0 || len(l.Types) == 0 {
			return invalidf("PHY %q combination has an empty or non-positive limit", phy)
		}

		for _, t := range l.Types {
			if _, dup := covered[t]; dup {
				return invalidf("PHY %q combination lists %q twice", phy, t)
			}

			covered[t] = struct{}{}
		}
	}

	return nil
}
