package radiogate_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const nodeFixture = "node_mt7916_halow.txt"

// newProvider wires a Provider to the whole-node fixture with phy2 as
// the HaLow PHY.
func newProvider(t *testing.T, s radiogate.Settings) (*radiogate.Provider, *fakeExec, *fakeClock) {
	t.Helper()

	ex := &fakeExec{out: fixture(t, nodeFixture)}
	clk := newFakeClock()
	hl := &fakeHaLow{phys: []string{"phy2"}}

	return &radiogate.Provider{
		Exec:      ex.Run,
		HaLowPHYs: hl.PHYs,
		Settings:  func() radiogate.Settings { return s },
		Now:       clk.Now,
	}, ex, clk
}

func capacityOf(t *testing.T, m *radiocap.Model, phy string) radiocap.Capacity {
	t.Helper()

	for _, c := range m.Capacities() {
		if c.PHY == phy {
			return c
		}
	}

	t.Fatalf("no PHY %s", phy)

	return radiocap.Capacity{}
}

func TestProvider_Model_marksHaLowFromDriver(t *testing.T) {
	p, _, _ := newProvider(t, radiogate.Settings{})

	m, err := p.Model(context.Background())
	require.NoError(t, err)

	assert.Equal(t, []radiocap.Band{radiocap.Band2G4}, capacityOf(t, m, "phy0").Bands)
	assert.Equal(t, []radiocap.Band{radiocap.Band5G}, capacityOf(t, m, "phy1").Bands)
	assert.Equal(t, []radiocap.Band{radiocap.BandS1G}, capacityOf(t, m, "phy2").Bands,
		"iw reports Morse as 5 GHz; the driver type makes it S1G")
}

func TestProvider_Model_evidenceFromSettings(t *testing.T) {
	p, _, _ := newProvider(t, radiogate.Settings{})

	phys := p.PHYs()
	assert.Nil(t, phys, "no PHYs before the first probe")

	_, err := p.Model(context.Background())
	require.NoError(t, err)

	for _, phy := range p.PHYs() {
		assert.False(t, phy.Qualified, "enumeration is never qualification: %s", phy.Name)
		assert.False(t, phy.Available, "raw parsed PHYs carry no evidence: %s", phy.Name)
	}
}

func TestProvider_Model_qualifiedAvailableSharing(t *testing.T) {
	demand := func(id string, band radiocap.Band, center int, role radiocap.Role) radiocap.Demand {
		return radiocap.Demand{ID: id, Purpose: radiocap.PurposeMesh, Role: role,
			Channel: radiocap.Channel{Band: band, CenterKHz: center, WidthKHz: 20000}}
	}
	mesh := demand("mesh", radiocap.Band2G4, 2437000, radiocap.RoleMesh)
	ap := demand("ap", radiocap.Band2G4, 2437000, radiocap.RoleAP)

	tests := []struct {
		name     string
		settings radiogate.Settings
		want     radiocap.Reason
	}{
		{name: "no evidence", settings: radiogate.Settings{}, want: radiocap.ReasonNoEligiblePHY},
		{
			name:     "fits only unqualified",
			settings: radiogate.Settings{SharingQualifiedPHYs: []string{"phy0"}},
			want:     radiocap.ReasonUnqualifiedOnly,
		},
		{
			name:     "qualified but not sharing-qualified",
			settings: radiogate.Settings{QualifiedPHYs: []string{"phy0"}},
			want:     radiocap.ReasonCapacityShort,
		},
		{
			name:     "qualified and sharing",
			settings: radiogate.Settings{QualifiedPHYs: []string{"phy0"}, SharingQualifiedPHYs: []string{"phy0"}},
			want:     radiocap.ReasonFits,
		},
		{
			name: "operator-marked unavailable defers",
			settings: radiogate.Settings{
				QualifiedPHYs: []string{"phy0"}, SharingQualifiedPHYs: []string{"phy0"},
				UnavailablePHYs: []string{"phy0"},
			},
			want: radiocap.ReasonPHYUnavailable,
		},
		{
			name:     "allow unqualified",
			settings: radiogate.Settings{SharingQualifiedPHYs: []string{"phy0"}, AllowUnqualified: true},
			want:     radiocap.ReasonFits,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p, _, _ := newProvider(t, tc.settings)

			m, err := p.Model(context.Background())
			require.NoError(t, err)

			pol := radiocap.DefaultPolicy()
			pol.AllowUnqualified = tc.settings.AllowUnqualified

			dec, err := m.Admit([]radiocap.Demand{mesh}, []radiocap.Demand{ap}, pol)
			require.NoError(t, err)
			assert.Equal(t, tc.want, dec.Reason, dec.Explanation)
		})
	}
}

func TestProvider_Model_matchesWiphyForMultiRadio(t *testing.T) {
	p := &radiogate.Provider{
		Exec:     func(context.Context) ([]byte, error) { return fixture(t, "multiradio_owrt_iw.txt"), nil },
		Settings: func() radiogate.Settings { return radiogate.Settings{QualifiedPHYs: []string{"phy0"}} },
	}

	m, err := p.Model(context.Background())
	require.NoError(t, err)

	ch, ok := radiogate.UCIChannel(radiocap.Band5G, "36", 80, 0)
	require.True(t, ok)

	dec, err := m.Admit(nil, []radiocap.Demand{{ID: "m", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh, Channel: ch}},
		radiocap.DefaultPolicy())
	require.NoError(t, err)
	assert.Equal(t, radiocap.OutcomeAdmit, dec.Outcome, "qualifying the wiphy qualifies each of its radios: %s", dec.Explanation)
}

func TestProvider_Model_cachesWithinTTL(t *testing.T) {
	p, ex, clk := newProvider(t, radiogate.Settings{})
	ctx := context.Background()

	m1, err := p.Model(ctx)
	require.NoError(t, err)

	clk.Advance(radiogate.DefaultTTL - time.Second)

	m2, err := p.Model(ctx)
	require.NoError(t, err)
	assert.Same(t, m1, m2, "unchanged probe and settings reuse the model")
	assert.Equal(t, 1, ex.getCalls())

	clk.Advance(2 * time.Second)

	_, err = p.Model(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, ex.getCalls(), "an expired probe runs iw list again")
}

func TestProvider_Model_rebuildsOnSettingsChange(t *testing.T) {
	var (
		mu sync.Mutex
		s  radiogate.Settings
	)

	ex := &fakeExec{out: fixture(t, nodeFixture)}
	p := &radiogate.Provider{
		Exec: ex.Run,
		Settings: func() radiogate.Settings {
			mu.Lock()
			defer mu.Unlock()

			return s
		},
	}

	m1, err := p.Model(context.Background())
	require.NoError(t, err)

	mu.Lock()
	s = radiogate.Settings{QualifiedPHYs: []string{"phy1"}}
	mu.Unlock()

	m2, err := p.Model(context.Background())
	require.NoError(t, err)
	assert.NotSame(t, m1, m2)
	assert.Equal(t, 1, ex.getCalls(), "a settings change does not re-probe")
}

func TestProvider_InvalidateAndRefresh(t *testing.T) {
	p, ex, _ := newProvider(t, radiogate.Settings{})
	ctx := context.Background()

	_, err := p.Model(ctx)
	require.NoError(t, err)

	p.Invalidate()

	_, err = p.Model(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, ex.getCalls(), "Invalidate forces the next probe")

	_, err = p.Refresh(ctx)
	require.NoError(t, err)
	assert.Equal(t, 3, ex.getCalls(), "Refresh probes now")
}

func TestProvider_Model_probeFailureBacksOff(t *testing.T) {
	p, ex, clk := newProvider(t, radiogate.Settings{})
	ex.setErr(errors.New("exec: \"iw\": executable file not found"))

	ctx := context.Background()

	_, err := p.Model(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iw list")

	_, err = p.Model(ctx)
	require.Error(t, err)
	assert.Equal(t, 1, ex.getCalls(), "a failed probe is reused within the backoff")

	ex.setErr(nil)
	clk.Advance(radiogate.DefaultFailureBackoff)

	_, err = p.Model(ctx)
	require.NoError(t, err)
	assert.Equal(t, 2, ex.getCalls())
}

func TestProvider_Model_errors(t *testing.T) {
	tests := []struct {
		name  string
		out   string
		halow func(context.Context) ([]string, error)
		want  string
	}{
		{name: "no wiphy blocks", out: "nothing here\n", want: "parse iw list"},
		{
			name:  "HaLow identification fails",
			out:   "",
			halow: func(context.Context) ([]string, error) { return nil, errors.New("ubus down") },
			want:  "identify HaLow PHYs",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out := []byte(tc.out)
			if tc.out == "" {
				out = fixture(t, nodeFixture)
			}

			p := &radiogate.Provider{
				Exec:      func(context.Context) ([]byte, error) { return out, nil },
				HaLowPHYs: tc.halow,
			}

			_, err := p.Model(context.Background())
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

func TestProvider_Model_concurrentSingleFlight(t *testing.T) {
	p, ex, _ := newProvider(t, radiogate.Settings{})

	var wg sync.WaitGroup

	for range 8 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			_, err := p.Model(context.Background())
			assert.NoError(t, err)
		}()
	}

	wg.Wait()
	assert.Equal(t, 1, ex.getCalls(), "concurrent callers share one probe")
}

func TestProvider_Inventory(t *testing.T) {
	settings := radiogate.Settings{
		QualifiedPHYs:        []string{"phy0", "phy2"},
		SharingQualifiedPHYs: []string{"phy0"},
		UnavailablePHYs:      []string{"phy1"},
	}
	p, ex, clk := newProvider(t, settings)
	ctx := context.Background()
	probedAt := clk.Now()

	inv, err := p.Inventory(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, ex.getCalls(), "the first call probes")
	assert.Equal(t, probedAt, inv.UpdatedAt)
	require.Len(t, inv.PHYs, 3)

	byName := make(map[string]radiocap.PHY, len(inv.PHYs))
	for _, phy := range inv.PHYs {
		byName[phy.Name] = phy
	}

	assert.True(t, byName["phy0"].Qualified)
	assert.True(t, byName["phy0"].SharingQualified)
	assert.True(t, byName["phy0"].Available)
	assert.False(t, byName["phy1"].Qualified)
	assert.False(t, byName["phy1"].Available)
	assert.True(t, byName["phy2"].Qualified)
	assert.Equal(t, []radiocap.Band{radiocap.BandS1G}, byName["phy2"].Bands)

	inv.PHYs[0].Bands[0] = radiocap.Band60G

	clk.Advance(time.Second)

	again, err := p.Inventory(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, ex.getCalls(), "reused within the TTL")
	assert.Equal(t, probedAt, again.UpdatedAt)
	assert.NotEqual(t, radiocap.Band60G, again.PHYs[0].Bands[0], "callers get copies")
}

func TestProvider_Inventory_probeFailure(t *testing.T) {
	p, ex, _ := newProvider(t, radiogate.Settings{})
	ctx := context.Background()

	_, err := p.Inventory(ctx)
	require.NoError(t, err)

	ex.setErr(errors.New("exec: \"iw\": executable file not found"))
	p.Invalidate()

	_, err = p.Inventory(ctx)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "iw list")
}
