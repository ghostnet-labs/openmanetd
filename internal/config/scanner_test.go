package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetScanner_defaults(t *testing.T) {
	sc := NewWithoutWatch(viper.New()).GetScanner()

	assert.Equal(t, ScannerConfig{
		Enable:             false,
		Mode:               ScannerModeAuto,
		HysteresisKm:       DefaultScannerHysteresisKm,
		PositionStaleAfter: time.Minute,
		AircraftStaleAfter: time.Minute,
		DecodeStaleAfter:   10 * time.Second,
		WideSerial:         "",
		ScanSerial:         "scan",
		WideUsableHz:       8_000_000,
		RTLUsableHz:        2_000_000,
		Dump1090Path:       "/var/run/dump1090/aircraft.json",
		Skyaware978Path:    "/var/run/skyaware978/aircraft.json",
		MaxAircraft:        512,
		TalkgroupHistory:   128,
	}, sc, "scanner must be off by default")
}

func TestGetScanner_yamlBinding(t *testing.T) {
	const doc = `
scanner:
  enable: true
  mode: Manual
  manualSite: UT-3f2a91c0
  states: [ut, "ID", ut, " "]
  hysteresisKm: 2.5
  positionStaleAfter: 2m
  aircraftStaleAfter: 30s
  decodeStaleAfter: 5s
  talkgroupHistory: 5000
  receivers:
    wideSerial: " 44a061dc2e395e8f "
    scanSerial: scan2
  tuner:
    wideUsableHz: 9000000
    rtlUsableHz: 2400000
  aircraft:
    dump1090Path: /tmp/a.json
    skyaware978Path: /tmp/b.json
    maxAircraft: 64
`

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(doc)))

	sc := NewWithoutWatch(v).GetScanner()
	assert.True(t, sc.Enable)
	assert.Equal(t, ScannerModeManual, sc.Mode)
	assert.Equal(t, "UT-3f2a91c0", sc.ManualSite)
	assert.Equal(t, []string{"UT", "ID"}, sc.States)
	assert.InDelta(t, 2.5, sc.HysteresisKm, 1e-9)
	assert.Equal(t, 2*time.Minute, sc.PositionStaleAfter)
	assert.Equal(t, 30*time.Second, sc.AircraftStaleAfter)
	assert.Equal(t, 5*time.Second, sc.DecodeStaleAfter)
	assert.Equal(t, 1024, sc.TalkgroupHistory, "clamped")
	assert.Equal(t, "44a061dc2e395e8f", sc.WideSerial)
	assert.Equal(t, "scan2", sc.ScanSerial)
	assert.Equal(t, uint64(9_000_000), sc.WideUsableHz)
	assert.Equal(t, uint64(2_400_000), sc.RTLUsableHz)
	assert.Equal(t, "/tmp/a.json", sc.Dump1090Path)
	assert.Equal(t, "/tmp/b.json", sc.Skyaware978Path)
	assert.Equal(t, 64, sc.MaxAircraft)
}

func TestGetScanner_invalidValuesFallBack(t *testing.T) {
	v := viper.New()
	v.Set("scanner.mode", "nearest")
	v.Set("scanner.hysteresisKm", 500)
	v.Set("scanner.positionStaleAfter", "0s")
	v.Set("scanner.decodeStaleAfter", "soon")
	v.Set("scanner.receivers.scanSerial", "")

	sc := NewWithoutWatch(v).GetScanner()
	assert.Equal(t, ScannerModeAuto, sc.Mode)
	assert.InDelta(t, DefaultScannerHysteresisKm, sc.HysteresisKm, 1e-9)
	assert.Equal(t, DefaultScannerPositionStaleAfter, sc.PositionStaleAfter)
	assert.Equal(t, DefaultScannerDecodeStaleAfter, sc.DecodeStaleAfter)
	assert.Empty(t, sc.ScanSerial, "an explicit empty serial unclaims the scan tuner")
}

func TestGetScanner_returnsCopies(t *testing.T) {
	v := viper.New()
	v.Set("scanner.states", []string{"UT"})

	cfg := NewWithoutWatch(v)
	sc := cfg.GetScanner()
	sc.States[0] = "XX"
	assert.Equal(t, []string{"UT"}, cfg.GetScanner().States)
}

func TestPersistScannerConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte("# node config\nblos:\n  enable: false\nscanner:\n  receivers:\n    scanSerial: scan9 # keep\n  states: [CO]\n"), 0o600))

	v := viper.New()
	v.SetConfigFile(path)
	require.NoError(t, v.ReadInConfig())

	cfg := NewWithoutWatch(v)
	err := cfg.PersistScannerConfig(ScannerSettings{
		Enable: true, Mode: ScannerModeManual, ManualSite: "UT-1", States: []string{"UT", "ID"}, HysteresisKm: 7.5,
		PositionStaleAfter: 45 * time.Second, AircraftStaleAfter: 20 * time.Second, DecodeStaleAfter: 3 * time.Second,
	})
	require.NoError(t, err)

	sc := cfg.GetScanner()
	assert.True(t, sc.Enable)
	assert.Equal(t, ScannerModeManual, sc.Mode)
	assert.Equal(t, "UT-1", sc.ManualSite)
	assert.Equal(t, []string{"UT", "ID"}, sc.States)
	assert.InDelta(t, 7.5, sc.HysteresisKm, 1e-9)
	assert.Equal(t, 45*time.Second, sc.PositionStaleAfter)
	assert.Equal(t, "scan9", sc.ScanSerial, "file-only keys are kept")

	// Re-read from disk with a fresh viper: the file round-trips.
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "# node config")
	assert.Contains(t, string(data), "scan9 # keep")

	v2 := viper.New()
	v2.SetConfigFile(path)
	require.NoError(t, v2.ReadInConfig())

	sc2 := NewWithoutWatch(v2).GetScanner()
	assert.Equal(t, sc, sc2)

	require.Error(t, cfg.PersistScannerConfig(ScannerSettings{Mode: "sometimes"}))
	require.Error(t, NewWithoutWatch(viper.New()).PersistScannerConfig(ScannerSettings{Mode: ScannerModeAuto}))
}
