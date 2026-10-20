package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetRadio_defaults(t *testing.T) {
	r := NewWithoutWatch(viper.New()).GetRadio()

	assert.Equal(t, RadioCapacityGateAdvisory, r.CapacityGate, "advisory until PHYs are qualified on hardware")
	assert.Equal(t, DefaultRadioCapacityGate, r.CapacityGate)
	assert.Empty(t, r.CapacityGateRejected)
	assert.Empty(t, r.QualifiedPHYs)
	assert.Empty(t, r.SharingQualifiedPHYs)
	assert.Empty(t, r.UnavailablePHYs)
	assert.False(t, r.AllowUnqualified)
}

func TestGetRadio_capacityGateModes(t *testing.T) {
	tests := []struct {
		name         string
		value        string
		wantMode     string
		wantRejected string
	}{
		{name: "off", value: "off", wantMode: RadioCapacityGateOff},
		{name: "advisory", value: "advisory", wantMode: RadioCapacityGateAdvisory},
		{name: "enforce", value: "enforce", wantMode: RadioCapacityGateEnforce},
		{name: "case and space insensitive", value: "  Enforce ", wantMode: RadioCapacityGateEnforce},
		{name: "empty uses default", value: "", wantMode: DefaultRadioCapacityGate},
		{name: "unknown falls back", value: "strict", wantMode: DefaultRadioCapacityGate, wantRejected: "strict"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			v := viper.New()
			v.Set("radio.capacityGate", tc.value)

			r := NewWithoutWatch(v).GetRadio()
			assert.Equal(t, tc.wantMode, r.CapacityGate)
			assert.Equal(t, tc.wantRejected, r.CapacityGateRejected)
		})
	}
}

func TestGetRadio_overrides(t *testing.T) {
	v := viper.New()
	v.Set("radio.capacity.qualifiedPhys", []string{"phy0", "phy1"})
	v.Set("radio.capacity.sharingQualifiedPhys", []string{"phy0"})
	v.Set("radio.capacity.unavailablePhys", []string{"phy2"})
	v.Set("radio.capacity.allowUnqualified", true)

	cfg := NewWithoutWatch(v)

	r := cfg.GetRadio()
	assert.Equal(t, []string{"phy0", "phy1"}, r.QualifiedPHYs)
	assert.Equal(t, []string{"phy0"}, r.SharingQualifiedPHYs)
	assert.Equal(t, []string{"phy2"}, r.UnavailablePHYs)
	assert.True(t, r.AllowUnqualified)

	// The returned slices are copies.
	r.QualifiedPHYs[0] = "changed"
	assert.Equal(t, "phy0", cfg.GetRadio().QualifiedPHYs[0])
}

// TestGetRadio_fromYAML reads the keys through a real config file, the
// path the daemon uses (/etc/openmanetd/config.yml).
func TestGetRadio_fromYAML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yml")
	require.NoError(t, os.WriteFile(path, []byte(`radio:
  capacityGate: enforce
  capacity:
    qualifiedPhys: [phy0, phy1/radio0]
    sharingQualifiedPhys: [phy0]
    allowUnqualified: false
`), 0o600))

	v := viper.New()
	v.SetConfigFile(path)
	require.NoError(t, v.ReadInConfig())

	r := NewWithoutWatch(v).GetRadio()
	assert.Equal(t, RadioCapacityGateEnforce, r.CapacityGate)
	assert.Equal(t, []string{"phy0", "phy1/radio0"}, r.QualifiedPHYs)
	assert.Equal(t, []string{"phy0"}, r.SharingQualifiedPHYs)
	assert.False(t, r.AllowUnqualified)
}
