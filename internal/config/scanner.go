package config

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Scanner (aircraft + P25 site monitor) defaults. The subsystem is off by
// default: with scanner.enable unset or false no goroutine is started and
// no receiver is touched, so existing nodes are unchanged.
//
// Receiver serials: the scan tuner is an RTL-SDR whose serial is set with
// `rtl_eeprom -s scan`, so it defaults to "scan". The wide tuner is an
// Airspy, whose serial is a factory-burned 64-bit hex number that cannot
// be renamed, so it has no default and must be configured
// (scanner.receivers.wideSerial; ScannerService lists an unclaimed Airspy
// with its serial to copy). Never use "1090" or "978": those belong to
// dump1090 and dump978-fa.
const (
	DefaultScannerEnable             = false
	DefaultScannerMode               = ScannerModeAuto
	DefaultScannerHysteresisKm       = 5.0
	DefaultScannerPositionStaleAfter = 60 * time.Second
	DefaultScannerAircraftStaleAfter = 60 * time.Second
	DefaultScannerDecodeStaleAfter   = 10 * time.Second
	DefaultScannerWideSerial         = ""
	DefaultScannerScanSerial         = "scan"
	// DefaultScannerWideUsableHz and DefaultScannerRTLUsableHz are planning
	// assumptions (8 MHz of a 10 MS/s Airspy, 2.0 MHz of a 2.4 MS/s
	// RTL-SDR) until bench-measured.
	DefaultScannerWideUsableHz uint64 = 8_000_000
	DefaultScannerRTLUsableHz  uint64 = 2_000_000
	// DefaultScannerConventionalRadiusKm bounds the positioned
	// conventional/aviation/weather channels the scan receiver monitors.
	DefaultScannerConventionalRadiusKm = 80.0
	maxScannerConventionalRadiusKm     = 500.0
	DefaultScannerDump1090Path         = "/var/run/dump1090/aircraft.json"
	DefaultScannerSkyaware978Path      = "/var/run/skyaware978/aircraft.json"
	DefaultScannerMaxAircraft          = 512
	DefaultScannerTalkgroupHistory     = 128
	maxScannerTalkgroupHistory         = 1024
	maxScannerAircraft                 = 4096
)

// Scanner site-selection modes.
const (
	ScannerModeAuto   = "auto"
	ScannerModeManual = "manual"
)

// ScannerConfig holds the scanner settings (YAML section "scanner"). It is
// returned by value from GetScanner with States copied.
type ScannerConfig struct {
	// Mode is ScannerModeAuto (nearest site to the GPS fix) or
	// ScannerModeManual (ManualSite).
	Mode string
	// ManualSite is the site key pinned in manual mode.
	ManualSite string
	// WideSerial and ScanSerial claim the scanner's receivers by USB
	// serial (scanner.receivers.wideSerial / scanSerial).
	WideSerial      string
	ScanSerial      string
	Dump1090Path    string
	Skyaware978Path string
	// States restricts site selection; empty allows every state.
	States []string
	// Problems lists config values that were rejected at load and
	// replaced by defaults; the scanner manager logs them when it starts.
	Problems           []string
	HysteresisKm       float64
	PositionStaleAfter time.Duration
	AircraftStaleAfter time.Duration
	DecodeStaleAfter   time.Duration
	WideUsableHz       uint64
	RTLUsableHz        uint64
	// WideSampleRate forces the wide tuner capture rate
	// (scanner.tuner.wideSampleRate, one of WideSampleRates); 0 picks it
	// from the plan width. WideUsableHz is capped at 80% of it.
	WideSampleRate int
	// ConventionalRadiusKm bounds the positioned conventional channels
	// (scanner.conventional.radiusKm).
	ConventionalRadiusKm float64
	MaxAircraft          int
	TalkgroupHistory     int
	Enable               bool
}

// loadScanner reads the scanner section, applying defaults. The caller
// holds c.mu.
func (c *Config) loadScanner() ScannerConfig {
	sc := ScannerConfig{
		Enable:             DefaultScannerEnable,
		Mode:               DefaultScannerMode,
		ManualSite:         c.v.GetString("scanner.manualSite"),
		HysteresisKm:       DefaultScannerHysteresisKm,
		PositionStaleAfter: positiveDuration(c.v.GetString("scanner.positionStaleAfter"), DefaultScannerPositionStaleAfter),
		AircraftStaleAfter: positiveDuration(c.v.GetString("scanner.aircraftStaleAfter"), DefaultScannerAircraftStaleAfter),
		DecodeStaleAfter:   positiveDuration(c.v.GetString("scanner.decodeStaleAfter"), DefaultScannerDecodeStaleAfter),
		WideSerial:         DefaultScannerWideSerial,
		ScanSerial:         DefaultScannerScanSerial,
		WideUsableHz:       DefaultScannerWideUsableHz,
		RTLUsableHz:        DefaultScannerRTLUsableHz,
		Dump1090Path:       DefaultScannerDump1090Path,
		Skyaware978Path:    DefaultScannerSkyaware978Path,
		MaxAircraft:        DefaultScannerMaxAircraft,
		TalkgroupHistory:   DefaultScannerTalkgroupHistory,

		ConventionalRadiusKm: DefaultScannerConventionalRadiusKm,
	}

	if c.v.IsSet("scanner.enable") {
		sc.Enable = c.v.GetBool("scanner.enable")
	}

	if m := strings.ToLower(c.v.GetString("scanner.mode")); m == ScannerModeManual || m == ScannerModeAuto {
		sc.Mode = m
	}

	for _, s := range c.v.GetStringSlice("scanner.states") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" && !slices.Contains(sc.States, s) {
			sc.States = append(sc.States, s)
		}
	}

	if c.v.IsSet("scanner.hysteresisKm") {
		if h := c.v.GetFloat64("scanner.hysteresisKm"); h >= 0 && h <= 100 {
			sc.HysteresisKm = h
		}
	}

	if c.v.IsSet("scanner.receivers.wideSerial") {
		sc.WideSerial = strings.TrimSpace(c.v.GetString("scanner.receivers.wideSerial"))
	}

	if c.v.IsSet("scanner.receivers.scanSerial") {
		sc.ScanSerial = strings.TrimSpace(c.v.GetString("scanner.receivers.scanSerial"))
	}

	if v := c.v.GetUint64("scanner.tuner.wideUsableHz"); v > 0 {
		sc.WideUsableHz = v
	}

	if v := c.v.GetUint64("scanner.tuner.rtlUsableHz"); v > 0 {
		sc.RTLUsableHz = v
	}

	c.loadScannerTuning(&sc)

	if v := c.v.GetString("scanner.aircraft.dump1090Path"); v != "" {
		sc.Dump1090Path = v
	}

	if v := c.v.GetString("scanner.aircraft.skyaware978Path"); v != "" {
		sc.Skyaware978Path = v
	}

	if v := c.v.GetInt("scanner.aircraft.maxAircraft"); v > 0 {
		sc.MaxAircraft = min(v, maxScannerAircraft)
	}

	if v := c.v.GetInt("scanner.talkgroupHistory"); v > 0 {
		sc.TalkgroupHistory = min(v, maxScannerTalkgroupHistory)
	}

	return sc
}

// loadScannerTuning reads the wide tuner capture rate and the
// conventional scan radius into sc, recording rejected values in
// sc.Problems. The caller holds c.mu.
func (c *Config) loadScannerTuning(sc *ScannerConfig) {
	if c.v.IsSet("scanner.tuner.wideSampleRate") {
		rate := c.v.GetInt("scanner.tuner.wideSampleRate")
		if err := ValidateScannerWideSampleRate(rate); err != nil {
			sc.Problems = append(sc.Problems, "scanner.tuner.wideSampleRate: "+err.Error())
		} else {
			sc.WideSampleRate = rate
		}
	}

	if sc.WideSampleRate > 0 {
		if limit := uint64(sc.WideSampleRate) * 8 / 10; sc.WideUsableHz > limit {
			sc.WideUsableHz = limit
		}
	}

	if c.v.IsSet("scanner.conventional.radiusKm") {
		if r := c.v.GetFloat64("scanner.conventional.radiusKm"); r > 0 && r <= maxScannerConventionalRadiusKm {
			sc.ConventionalRadiusKm = r
		} else {
			sc.Problems = append(sc.Problems, fmt.Sprintf("scanner.conventional.radiusKm: %v is outside (0, %v]", r, maxScannerConventionalRadiusKm))
		}
	}
}

// WideSampleRates are the airspy_rx capture rates the wide tuner accepts:
// Airspy R2 (10 and 2.5 MS/s) and Airspy Mini (6 and 3 MS/s).
func WideSampleRates() []int { return []int{2_500_000, 3_000_000, 6_000_000, 10_000_000} }

// ValidateScannerWideSampleRate checks scanner.tuner.wideSampleRate: 0
// (automatic) or one of WideSampleRates.
func ValidateScannerWideSampleRate(rate int) error {
	if rate == 0 || slices.Contains(WideSampleRates(), rate) {
		return nil
	}

	return fmt.Errorf("%d S/s is not an Airspy rate (want 2500000 or 10000000 for an R2, 3000000 or 6000000 for a Mini)", rate)
}

// positiveDuration parses raw, falling back to def when it is empty,
// unparsable or not positive.
func positiveDuration(raw string, def time.Duration) time.Duration {
	if d := parseDurationOrDefault(raw, def); d > 0 {
		return d
	}

	return def
}

// GetScanner returns the scanner settings.
func (c *Config) GetScanner() ScannerConfig {
	c.mu.RLock()
	defer c.mu.RUnlock()

	sc := c.Scanner
	sc.States = slices.Clone(sc.States)
	sc.Problems = slices.Clone(sc.Problems)

	return sc
}

// ScannerSettings are the runtime-editable scanner settings persisted by
// PersistScannerConfig. Receiver serials, tuner widths, feed paths and
// bounds stay file-only.
type ScannerSettings struct {
	Mode               string
	ManualSite         string
	States             []string
	HysteresisKm       float64
	PositionStaleAfter time.Duration
	AircraftStaleAfter time.Duration
	DecodeStaleAfter   time.Duration
	Enable             bool
}

// PersistScannerConfig writes the editable scanner settings to the YAML
// config file (preserving comments and ordering) and reloads them.
func (c *Config) PersistScannerConfig(s ScannerSettings) error {
	if s.Mode != ScannerModeAuto && s.Mode != ScannerModeManual {
		return fmt.Errorf("invalid scanner mode %q", s.Mode)
	}

	c.persistMu.Lock()
	defer c.persistMu.Unlock()

	filePath := c.v.ConfigFileUsed()
	if filePath == "" {
		return fmt.Errorf("no config file path configured")
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return fmt.Errorf("reading config file: %w", err)
	}

	var doc yaml.Node

	if err = yaml.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("parsing config file: %w", err)
	}

	if doc.Kind != yaml.DocumentNode || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("unexpected YAML structure: expected a mapping document")
	}

	m := findOrCreateMapping(doc.Content[0], "scanner")
	setScalarWithTag(m, "enable", strconv.FormatBool(s.Enable), "!!bool")
	setScalarWithTag(m, "mode", s.Mode, yamlStrTag)
	setScalarWithTag(m, "manualSite", s.ManualSite, yamlStrTag)
	setSequence(m, "states", s.States)
	setScalarWithTag(m, "hysteresisKm", strconv.FormatFloat(s.HysteresisKm, 'f', -1, 64), "!!float")
	setScalarWithTag(m, "positionStaleAfter", s.PositionStaleAfter.String(), yamlStrTag)
	setScalarWithTag(m, "aircraftStaleAfter", s.AircraftStaleAfter.String(), yamlStrTag)
	setScalarWithTag(m, "decodeStaleAfter", s.DecodeStaleAfter.String(), yamlStrTag)

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	//nolint:gosec // config file permissions match the original file
	if err = os.WriteFile(filePath, out, 0644); err != nil {
		return fmt.Errorf("writing config file: %w", err)
	}

	c.v.Set("scanner.enable", s.Enable)
	c.v.Set("scanner.mode", s.Mode)
	c.v.Set("scanner.manualSite", s.ManualSite)
	c.v.Set("scanner.states", slices.Clone(s.States))
	c.v.Set("scanner.hysteresisKm", s.HysteresisKm)
	c.v.Set("scanner.positionStaleAfter", s.PositionStaleAfter.String())
	c.v.Set("scanner.aircraftStaleAfter", s.AircraftStaleAfter.String())
	c.v.Set("scanner.decodeStaleAfter", s.DecodeStaleAfter.String())
	c.reload()

	return nil
}

// setSequence sets key in mapping to a block sequence of strings,
// replacing any existing value.
func setSequence(mapping *yaml.Node, key string, values []string) {
	seq := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: make([]*yaml.Node, 0, len(values))}
	for _, v := range values {
		seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStrTag, Value: v})
	}

	for i := 0; i < len(mapping.Content)-1; i += 2 {
		if mapping.Content[i].Value == key {
			mapping.Content[i+1] = seq

			return
		}
	}

	mapping.Content = append(mapping.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStrTag, Value: key}, seq)
}
