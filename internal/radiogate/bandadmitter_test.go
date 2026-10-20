package radiogate_test

import (
	"errors"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/pathsel"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newAdmitter(src radiogate.ModelSource, mode radiogate.Mode) *radiogate.BandAdmitter {
	return &radiogate.BandAdmitter{Source: src, Mode: func() radiogate.Mode { return mode }}
}

func TestBandAdmitter_AdmitBand(t *testing.T) {
	unavail := allEvidence()
	unavail.UnavailablePHYs = []string{"phy2"}

	tests := []struct {
		name       string
		settings   radiogate.Settings
		band       pathsel.Band
		wantOK     bool
		wantReason string
	}{
		{name: "HaLow qualified", settings: allEvidence(), band: pathsel.BandHaLow, wantOK: true},
		{name: "2.4 qualified", settings: allEvidence(), band: pathsel.Band2G4, wantOK: true},
		{name: "5 qualified", settings: allEvidence(), band: pathsel.Band5G, wantOK: true},
		{
			name: "unqualified", settings: radiogate.Settings{}, band: pathsel.Band5G,
			wantReason: string(radiocap.ReasonUnqualifiedOnly),
		},
		{
			name: "HaLow unavailable", settings: unavail, band: pathsel.BandHaLow,
			wantReason: string(radiocap.ReasonPHYUnavailable),
		},
		{name: "unknown band", settings: allEvidence(), band: pathsel.Band("6"), wantReason: radiogate.ReasonUnknownBand},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			a := newAdmitter(&fakeSource{model: nodeModel(t, tc.settings)}, radiogate.ModeEnforce)

			ok, reason := a.AdmitBand(tc.band)
			assert.Equal(t, tc.wantOK, ok)
			assert.Equal(t, tc.wantReason, reason)
		})
	}
}

func TestBandAdmitter_neverDeniesOutsideEnforce(t *testing.T) {
	for _, mode := range []radiogate.Mode{radiogate.ModeOff, radiogate.ModeAdvisory} {
		t.Run(string(mode), func(t *testing.T) {
			src := &fakeSource{model: nodeModel(t, radiogate.Settings{})}
			a := newAdmitter(src, mode)

			ok, reason := a.AdmitBand(pathsel.Band5G)
			assert.True(t, ok)
			assert.Empty(t, reason)
			assert.Equal(t, 0, src.getCalls(), "no probe outside enforce")
		})
	}

	ok, _ := (&radiogate.BandAdmitter{Source: &fakeSource{model: nodeModel(t, radiogate.Settings{})}}).AdmitBand(pathsel.Band5G)
	assert.True(t, ok, "nil Mode is advisory")
}

func TestBandAdmitter_probeFailureAdmits(t *testing.T) {
	a := newAdmitter(&fakeSource{err: errors.New("iw list: not found")}, radiogate.ModeEnforce)

	ok, reason := a.AdmitBand(pathsel.BandHaLow)
	assert.True(t, ok)
	assert.Empty(t, reason)

	ok, _ = newAdmitter(nil, radiogate.ModeEnforce).AdmitBand(pathsel.BandHaLow)
	assert.True(t, ok, "no source admits")
}

func TestBandAdmitter_cachesPerModelAndPolicy(t *testing.T) {
	src := &fakeSource{model: nodeModel(t, radiogate.Settings{})}
	allow := false
	a := newAdmitter(src, radiogate.ModeEnforce)
	a.Policy = func() radiocap.Policy { return radiocap.Policy{AllowUnqualified: allow} }

	ok, _ := a.AdmitBand(pathsel.Band5G)
	require.False(t, ok)

	ok, _ = a.AdmitBand(pathsel.Band5G)
	assert.False(t, ok, "cached answer")

	allow = true
	ok, _ = a.AdmitBand(pathsel.Band5G)
	assert.True(t, ok, "a policy change invalidates the cache")

	allow = false

	src.set(nodeModel(t, allEvidence()), nil)

	ok, _ = a.AdmitBand(pathsel.Band5G)
	assert.True(t, ok, "a new model invalidates the cache")
}

// staticMetrics reports every band to one peer as healthy.
type staticMetrics struct{ now func() time.Time }

func (m staticMetrics) PathMetrics(_ string, _ pathsel.Band) (pathsel.Metrics, bool) {
	return pathsel.Metrics{
		UpdatedAt: m.now(), Latency: 10 * time.Millisecond, Jitter: 2 * time.Millisecond,
		CapacityKbps: 10000, Reachable: true,
	}, true
}

// TestBandAdmitter_drivesPathSelector wires the adapter into a real
// pathsel.Selector: an unavailable 5 GHz PHY makes 5 GHz ineligible for
// voice with the radiocap reason.
func TestBandAdmitter_drivesPathSelector(t *testing.T) {
	s := allEvidence()
	s.UnavailablePHYs = []string{"phy1"}

	g := &radiogate.Gate{
		Log:    zerolog.Nop(),
		Source: &fakeSource{model: nodeModel(t, s)},
		Mode:   func() radiogate.Mode { return radiogate.ModeEnforce },
	}

	clk := newFakeClock()
	sel, err := pathsel.New(pathsel.Config{}, staticMetrics{now: clk.Now},
		pathsel.WithClock(clk.Now), pathsel.WithCapacity(g.NewBandAdmitter()))
	require.NoError(t, err)

	d, err := sel.Select("peer", pathsel.ClassVoice)
	require.NoError(t, err)
	assert.NotEqual(t, pathsel.Band5G, d.Band)

	var found bool

	for _, c := range d.Candidates {
		if c.Band == pathsel.Band5G {
			found = true

			assert.Equal(t, pathsel.IneligibleCapacity, c.Ineligible)
			assert.Equal(t, string(radiocap.ReasonPHYUnavailable), c.Detail)
		}
	}

	assert.True(t, found, "5 GHz is a candidate")
}
