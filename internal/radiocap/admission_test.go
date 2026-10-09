package radiocap_test

import (
	"encoding/json"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makePHY mirrors phy() in the offline model's tests: a qualified, available
// 5 GHz radio with one channel, <=2 AP/mesh and <=1 station, total <=3.
func makePHY(name string, bands ...radiocap.Band) radiocap.PHY {
	if len(bands) == 0 {
		bands = []radiocap.Band{radiocap.Band5G}
	}

	return radiocap.PHY{
		Name: name, Bands: bands, Qualified: true, Available: true, SharingQualified: true,
		Combinations: []radiocap.Combination{{
			MaxInterfaces: 3, MaxChannels: 1,
			Limits: []radiocap.Limit{
				{Types: []radiocap.IfType{radiocap.IfTypeMesh, radiocap.IfTypeAP}, Max: 2},
				{Types: []radiocap.IfType{radiocap.IfTypeManaged}, Max: 1},
			},
		}},
	}
}

// makeDemand mirrors radio() in the offline model's tests: a 20 MHz channel.
func makeDemand(id string, purpose radiocap.Purpose, role radiocap.Role, mhz int) radiocap.Demand {
	return radiocap.Demand{
		ID: id, Purpose: purpose, Role: role,
		Channel: radiocap.Channel{Band: radiocap.BandForMHz(float64(mhz)), CenterKHz: mhz * 1000, WidthKHz: 20000},
	}
}

func oldMesh(mhz int) radiocap.Demand {
	return makeDemand("mesh-old", radiocap.PurposeMesh, radiocap.RoleMesh, mhz)
}

func newMesh(mhz int) radiocap.Demand {
	return makeDemand("mesh-new", radiocap.PurposeMigration, radiocap.RoleMesh, mhz)
}

func eudAP(mhz int) radiocap.Demand {
	return makeDemand("eud-ap", radiocap.PurposeEUD, radiocap.RoleAP, mhz)
}

func newModel(t *testing.T, phys ...radiocap.PHY) *radiocap.Model {
	t.Helper()

	m, err := radiocap.NewModel(phys)
	require.NoError(t, err)

	return m
}

// qualify marks parsed PHYs as qualified and available, as a controller
// would after checking qualification records and liveness.
func qualify(phys []radiocap.PHY) []radiocap.PHY {
	for i := range phys {
		phys[i].Qualified = true
		phys[i].Available = true
	}

	return phys
}

func TestAdmit_offlineModelScenarios(t *testing.T) {
	sharingOff := makePHY("p")
	sharingOff.SharingQualified = false

	oneAPOrMesh := makePHY("p")
	oneAPOrMesh.Combinations[0].Limits[0].Max = 1

	wide := eudAP(5180)
	wide.Channel.WidthKHz = 40000

	unqualified := makePHY("p")
	unqualified.Qualified = false

	dual := makePHY("single", radiocap.Band5G, radiocap.Band2G4)

	tests := []struct {
		name      string
		phys      []radiocap.PHY
		current   []radiocap.Demand
		requested []radiocap.Demand
		policy    radiocap.Policy
		outcome   radiocap.Outcome
		reason    radiocap.Reason
	}{
		{
			name:      "three independent channels cannot fit two PHYs",
			phys:      []radiocap.PHY{makePHY("p0"), makePHY("p1")},
			current:   []radiocap.Demand{oldMesh(5180), eudAP(5220)},
			requested: []radiocap.Demand{newMesh(5200)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
		},
		{
			name:      "three independent channels fit three PHYs",
			phys:      []radiocap.PHY{makePHY("p0"), makePHY("p1"), makePHY("p2")},
			current:   []radiocap.Demand{oldMesh(5180), eudAP(5220)},
			requested: []radiocap.Demand{newMesh(5200)},
			outcome:   radiocap.OutcomeAdmit, reason: radiocap.ReasonFits,
		},
		{
			name:      "same-channel sharing fits a permitted combination",
			phys:      []radiocap.PHY{makePHY("p")},
			current:   []radiocap.Demand{oldMesh(5180)},
			requested: []radiocap.Demand{eudAP(5180)},
			outcome:   radiocap.OutcomeAdmit, reason: radiocap.ReasonFits,
		},
		{
			name:      "same-channel sharing needs the combination to allow it",
			phys:      []radiocap.PHY{oneAPOrMesh},
			current:   []radiocap.Demand{oldMesh(5180)},
			requested: []radiocap.Demand{eudAP(5180)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
		},
		{
			name:      "same-channel sharing needs qualification",
			phys:      []radiocap.PHY{sharingOff},
			current:   []radiocap.Demand{oldMesh(5180)},
			requested: []radiocap.Demand{eudAP(5180)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
		},
		{
			name:      "channel width is part of the channel",
			phys:      []radiocap.PHY{makePHY("p")},
			current:   []radiocap.Demand{oldMesh(5180)},
			requested: []radiocap.Demand{wide},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
		},
		{
			name:      "unqualified PHY fails closed",
			phys:      []radiocap.PHY{unqualified},
			requested: []radiocap.Demand{oldMesh(5180)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonUnqualifiedOnly,
		},
		{
			name:      "unqualified PHY admitted only by candidate policy",
			phys:      []radiocap.PHY{unqualified},
			requested: []radiocap.Demand{oldMesh(5180)},
			policy:    radiocap.Policy{AllowUnqualified: true},
			outcome:   radiocap.OutcomeAdmit, reason: radiocap.ReasonFits,
		},
		{
			name:      "two band names on one PHY are not two radios",
			phys:      []radiocap.PHY{dual},
			current:   []radiocap.Demand{oldMesh(5180)},
			requested: []radiocap.Demand{makeDemand("b", radiocap.PurposeOther, radiocap.RoleMesh, 2412)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
		},
		{
			name:      "unsupported band has no eligible PHY",
			phys:      []radiocap.PHY{makePHY("p")},
			requested: []radiocap.Demand{makeDemand("b", radiocap.PurposeOther, radiocap.RoleMesh, 2412)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonNoEligiblePHY,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			d, err := newModel(t, tc.phys...).Admit(tc.current, tc.requested, tc.policy)
			require.NoError(t, err)
			assert.Equal(t, tc.outcome, d.Outcome, d.Explanation)
			assert.Equal(t, tc.reason, d.Reason, d.Explanation)
			assert.Equal(t, radiocap.NotEvaluated, d.PhysicalQualification)
			assert.NotEmpty(t, d.Explanation)

			if !tc.policy.AllowBoundedInterruption {
				assert.Empty(t, d.Interrupt, "default policy must never interrupt")
			}

			if tc.outcome == radiocap.OutcomeAdmit {
				assert.NotEmpty(t, d.Placements)
			} else {
				assert.Empty(t, d.Placements)
			}
		})
	}
}

func TestAdmit_candidateUseIsReported(t *testing.T) {
	p := makePHY("p")
	p.Qualified = false

	d, err := newModel(t, p).Admit(nil, []radiocap.Demand{oldMesh(5180)}, radiocap.Policy{AllowUnqualified: true})
	require.NoError(t, err)
	assert.True(t, d.UsesUnqualified)

	d, err = newModel(t, p).Admit(nil, []radiocap.Demand{oldMesh(5180)}, radiocap.DefaultPolicy())
	require.NoError(t, err)
	require.Len(t, d.Alternatives, 1)
	assert.Equal(t, radiocap.Alternative{Kind: radiocap.AltQualifyPHY, Subjects: []string{"p"}}, d.Alternatives[0])
}

func TestAdmit_unavailablePHYIsNeverUsed(t *testing.T) {
	p := makePHY("p")
	p.Qualified = false
	p.Available = false

	// The offline model reports this infeasible; the controller defers
	// instead, because the shortage is a missing PHY, not missing capacity.
	d, err := newModel(t, p).Admit(nil, []radiocap.Demand{oldMesh(5180)}, radiocap.Policy{AllowUnqualified: true})
	require.NoError(t, err)
	assert.Equal(t, radiocap.OutcomeDefer, d.Outcome)
	assert.Equal(t, radiocap.ReasonPHYUnavailable, d.Reason)
	assert.Empty(t, d.Placements)
}

func TestAdmit_backtracksWhenGreedyChoiceBlocksLaterDemand(t *testing.T) {
	a := makePHY("A", radiocap.Band5G)
	b := makePHY("B", radiocap.Band5G, radiocap.Band6G)
	c := makePHY("C", radiocap.Band6G)

	for _, p := range []*radiocap.PHY{&a, &b, &c} {
		p.Combinations[0].MaxInterfaces = 1
	}

	reqs := []radiocap.Demand{
		makeDemand("a", radiocap.PurposeOther, radiocap.RoleMesh, 5180),
		makeDemand("b", radiocap.PurposeOther, radiocap.RoleMesh, 5975),
		makeDemand("c", radiocap.PurposeOther, radiocap.RoleAP, 5200),
	}

	d, err := newModel(t, a, b, c).Admit(nil, reqs, radiocap.DefaultPolicy())
	require.NoError(t, err)
	require.Equal(t, radiocap.OutcomeAdmit, d.Outcome, d.Explanation)
	assert.Equal(t, []radiocap.Placement{
		{PHY: "A", Demands: []string{"a"}},
		{PHY: "B", Demands: []string{"c"}},
		{PHY: "C", Demands: []string{"b"}},
	}, d.Placements)
}

func TestAdmit_searchBudgetIsIndeterminateAndRejects(t *testing.T) {
	phys := make([]radiocap.PHY, 0, 31)

	for i := range 31 {
		p := makePHY("p" + strconv.Itoa(i))
		p.Combinations[0].MaxInterfaces = 1
		phys = append(phys, p)
	}

	reqs := make([]radiocap.Demand, 0, 32)
	for i := range 32 {
		reqs = append(reqs, makeDemand("d"+strconv.Itoa(i), radiocap.PurposeOther, radiocap.RoleMesh, 5180))
	}

	d, err := newModel(t, phys...).Admit(nil, reqs, radiocap.DefaultPolicy())
	require.NoError(t, err)
	assert.Equal(t, radiocap.OutcomeReject, d.Outcome)
	assert.Equal(t, radiocap.ReasonSearchBudget, d.Reason)
}

// TestAdmit_migrationOnMT7916 runs GHO-59's migration scenarios against the
// parsed MT7916 DBDC fixture: one 2.4 GHz PHY and one 5 GHz PHY.
func TestAdmit_migrationOnMT7916(t *testing.T) {
	bounded := radiocap.Policy{
		AllowBoundedInterruption: true,
		MaxInterruption:          30 * time.Second,
		InterruptiblePurposes:    []radiocap.Purpose{radiocap.PurposeEUD},
	}

	tests := []struct {
		name      string
		sharing   bool
		current   []radiocap.Demand
		requested []radiocap.Demand
		policy    radiocap.Policy
		outcome   radiocap.Outcome
		reason    radiocap.Reason
		interrupt []string
		alts      []radiocap.Alternative
	}{
		{
			name:      "old and new mesh plus distinct EUD channel in one band are rejected",
			current:   []radiocap.Demand{oldMesh(5180), eudAP(5745)},
			requested: []radiocap.Demand{newMesh(5200)},
			policy:    bounded,
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
		},
		{
			name:      "same-band migration on two PHYs needs the old mesh interrupted",
			current:   []radiocap.Demand{oldMesh(5180), eudAP(2437)},
			requested: []radiocap.Demand{newMesh(5200)},
			policy:    bounded,
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
			alts: []radiocap.Alternative{
				{Kind: radiocap.AltInterruptDemand, Subjects: []string{"mesh-old"}},
			},
		},
		{
			name:      "cross-band migration onto the EUD band is rejected by default",
			current:   []radiocap.Demand{oldMesh(5180), eudAP(2437)},
			requested: []radiocap.Demand{newMesh(2412)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
			alts: []radiocap.Alternative{
				{Kind: radiocap.AltInterruptDemand, Subjects: []string{"eud-ap"}},
			},
		},
		{
			name:      "approved bounded interruption pauses only the EUD AP",
			current:   []radiocap.Demand{oldMesh(5180), eudAP(2437)},
			requested: []radiocap.Demand{newMesh(2412)},
			policy:    bounded,
			outcome:   radiocap.OutcomeAdmit, reason: radiocap.ReasonBoundedInterruption,
			interrupt: []string{"eud-ap"},
			alts: []radiocap.Alternative{
				{Kind: radiocap.AltInterruptDemand, Subjects: []string{"eud-ap"}, Approved: true},
			},
		},
		{
			name:      "same-channel EUD beside the new mesh fits when sharing is qualified",
			sharing:   true,
			current:   []radiocap.Demand{oldMesh(5180), eudAP(2412)},
			requested: []radiocap.Demand{newMesh(2412)},
			outcome:   radiocap.OutcomeAdmit, reason: radiocap.ReasonFits,
		},
		{
			name:      "same-channel EUD beside the new mesh is refused when sharing is unqualified",
			current:   []radiocap.Demand{oldMesh(5180), eudAP(2412)},
			requested: []radiocap.Demand{newMesh(2412)},
			outcome:   radiocap.OutcomeReject, reason: radiocap.ReasonCapacityShort,
			alts: []radiocap.Alternative{
				{Kind: radiocap.AltInterruptDemand, Subjects: []string{"eud-ap"}},
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			phys := qualify(parseFixture(t, "mt7916_dbdc.txt"))
			for i := range phys {
				phys[i].SharingQualified = tc.sharing
			}

			d, err := newModel(t, phys...).Admit(tc.current, tc.requested, tc.policy)
			require.NoError(t, err)
			assert.Equal(t, tc.outcome, d.Outcome, d.Explanation)
			assert.Equal(t, tc.reason, d.Reason, d.Explanation)
			assert.Equal(t, tc.interrupt, d.Interrupt)
			assert.Equal(t, tc.alts, d.Alternatives)

			if len(tc.interrupt) > 0 {
				assert.Equal(t, tc.policy.MaxInterruption, d.MaxInterruption)
				assert.Contains(t, d.Explanation, "30s")

				for _, pl := range d.Placements {
					assert.NotContains(t, pl.Demands, tc.interrupt[0], "interrupted demand must not be placed")
				}
			}
		})
	}
}

// TestAdmit_haLowFailureAndFallback covers a failed/stale HaLow PHY and the
// fallback of mesh to a Wi-Fi PHY.
func TestAdmit_haLowFailureAndFallback(t *testing.T) {
	build := func(t *testing.T, haLowAvailable bool) *radiocap.Model {
		t.Helper()

		halow := parseFixture(t, "morse_halow.txt")[0]
		halow.SetHaLow()
		halow.Name = "phy-halow"
		halow.Qualified = true
		halow.Available = haLowAvailable

		return newModel(t, append(qualify(parseFixture(t, "mt7916_dbdc.txt")), halow)...)
	}

	haLowMesh := radiocap.Demand{
		ID: "mesh-halow", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh,
		Channel: radiocap.Channel{Band: radiocap.BandS1G, CenterKHz: 923000, WidthKHz: 2000},
	}
	eud := []radiocap.Demand{eudAP(5180)}

	t.Run("healthy HaLow PHY carries HaLow mesh", func(t *testing.T) {
		d, err := build(t, true).Admit(eud, []radiocap.Demand{haLowMesh}, radiocap.DefaultPolicy())
		require.NoError(t, err)
		assert.Equal(t, radiocap.OutcomeAdmit, d.Outcome)
		assert.Contains(t, d.Placements, radiocap.Placement{PHY: "phy-halow", Demands: []string{"mesh-halow"}})
	})

	t.Run("failed HaLow PHY defers HaLow mesh", func(t *testing.T) {
		d, err := build(t, false).Admit(eud, []radiocap.Demand{haLowMesh}, radiocap.DefaultPolicy())
		require.NoError(t, err)
		assert.Equal(t, radiocap.OutcomeDefer, d.Outcome)
		assert.Equal(t, radiocap.ReasonPHYUnavailable, d.Reason)
		assert.Equal(t, []string{"mesh-halow"}, d.Unplaceable)
		assert.Contains(t, d.Alternatives,
			radiocap.Alternative{Kind: radiocap.AltRestorePHY, Subjects: []string{"phy-halow"}})
	})

	t.Run("fallback mesh on 2.4 GHz fits beside the EUD AP", func(t *testing.T) {
		d, err := build(t, false).Admit(eud, []radiocap.Demand{oldMesh(2412)}, radiocap.DefaultPolicy())
		require.NoError(t, err)
		assert.Equal(t, radiocap.OutcomeAdmit, d.Outcome)
		assert.Equal(t, []radiocap.Placement{
			{PHY: "phy0", Demands: []string{"mesh-old"}},
			{PHY: "phy1", Demands: []string{"eud-ap"}},
		}, d.Placements)
	})
}

func TestAdmit_invalidInput(t *testing.T) {
	m := newModel(t, makePHY("p"))

	negWidth := oldMesh(5180)
	negWidth.Channel.WidthKHz = -1

	zeroCenter := oldMesh(5180)
	zeroCenter.Channel.CenterKHz = 0

	badRole := oldMesh(5180)
	badRole.Role = "relay"

	noBand := oldMesh(5180)
	noBand.Channel.Band = ""

	tests := []struct {
		name      string
		current   []radiocap.Demand
		requested []radiocap.Demand
		policy    radiocap.Policy
	}{
		{name: "no requested demands"},
		{name: "negative width", requested: []radiocap.Demand{negWidth}},
		{name: "zero center", requested: []radiocap.Demand{zeroCenter}},
		{name: "unknown role", requested: []radiocap.Demand{badRole}},
		{name: "missing band", requested: []radiocap.Demand{noBand}},
		{name: "empty id", requested: []radiocap.Demand{{Role: radiocap.RoleMesh}}},
		{name: "duplicate id", current: []radiocap.Demand{oldMesh(5180)}, requested: []radiocap.Demand{oldMesh(5200)}},
		{
			name: "interruption without bound", requested: []radiocap.Demand{oldMesh(5180)},
			policy: radiocap.Policy{AllowBoundedInterruption: true, InterruptiblePurposes: []radiocap.Purpose{radiocap.PurposeEUD}},
		},
		{
			name: "interruption without purposes", requested: []radiocap.Demand{oldMesh(5180)},
			policy: radiocap.Policy{AllowBoundedInterruption: true, MaxInterruption: time.Second},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := m.Admit(tc.current, tc.requested, tc.policy)
			assert.ErrorIs(t, err, radiocap.ErrInvalid)
		})
	}
}

func TestNewModel_invalidPHYs(t *testing.T) {
	noBands := makePHY("p")
	noBands.Bands = nil

	noCombos := makePHY("p")
	noCombos.Combinations = nil

	zeroChannels := makePHY("p")
	zeroChannels.Combinations[0].MaxChannels = 0

	dupType := makePHY("p")
	dupType.Combinations[0].Limits[1].Types = []radiocap.IfType{radiocap.IfTypeAP}

	zeroLimit := makePHY("p")
	zeroLimit.Combinations[0].Limits[0].Max = 0

	tests := []struct {
		name string
		phys []radiocap.PHY
	}{
		{name: "duplicate name", phys: []radiocap.PHY{makePHY("p"), makePHY("p")}},
		{name: "empty name", phys: []radiocap.PHY{makePHY("")}},
		{name: "no bands", phys: []radiocap.PHY{noBands}},
		{name: "no combinations", phys: []radiocap.PHY{noCombos}},
		{name: "zero channels", phys: []radiocap.PHY{zeroChannels}},
		{name: "type listed twice", phys: []radiocap.PHY{dupType}},
		{name: "zero limit", phys: []radiocap.PHY{zeroLimit}},
		{name: "too many", phys: make([]radiocap.PHY, radiocap.MaxEntries+1)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := radiocap.NewModel(tc.phys)
			assert.True(t, errors.Is(err, radiocap.ErrInvalid), "got %v", err)
		})
	}
}

func TestNewModel_copiesInput(t *testing.T) {
	phys := []radiocap.PHY{makePHY("p")}
	m := newModel(t, phys...)

	phys[0].Combinations[0].Limits[0].Max = 0
	phys[0].Bands[0] = radiocap.Band2G4

	d, err := m.Admit(nil, []radiocap.Demand{oldMesh(5180)}, radiocap.DefaultPolicy())
	require.NoError(t, err)
	assert.Equal(t, radiocap.OutcomeAdmit, d.Outcome)

	caps := m.Capacities()
	require.Len(t, caps, 1)
	assert.Equal(t, []radiocap.Band{radiocap.Band5G}, caps[0].Bands)
}

func TestEvaluate(t *testing.T) {
	m := newModel(t, makePHY("p0"), makePHY("p1"))

	r, err := m.Evaluate([]radiocap.Demand{oldMesh(5180), newMesh(5200)}, false)
	require.NoError(t, err)
	assert.Equal(t, radiocap.StatusFeasible, r.Status)

	r, err = m.Evaluate([]radiocap.Demand{oldMesh(5180), newMesh(5200), eudAP(5220)}, false)
	require.NoError(t, err)
	assert.Equal(t, radiocap.StatusInfeasible, r.Status)

	_, err = m.Evaluate(nil, false)
	assert.ErrorIs(t, err, radiocap.ErrInvalid)
}

func TestDecision_JSONIsMachineReadable(t *testing.T) {
	d, err := newModel(t, makePHY("p0"), makePHY("p1")).Admit(
		[]radiocap.Demand{oldMesh(5180), eudAP(5220)}, []radiocap.Demand{newMesh(5200)}, radiocap.DefaultPolicy())
	require.NoError(t, err)

	raw, err := json.Marshal(d)
	require.NoError(t, err)

	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))
	assert.Equal(t, "reject", got["outcome"])
	assert.Equal(t, "capacity_short", got["reason"])
	assert.Equal(t, "not_evaluated", got["physical_qualification"])
	assert.NotContains(t, got, "interrupt")
}

func TestModel_concurrentAdmit(t *testing.T) {
	m := newModel(t, makePHY("p0"), makePHY("p1"))
	done := make(chan radiocap.Outcome, 8)

	for range 8 {
		go func() {
			d, err := m.Admit([]radiocap.Demand{oldMesh(5180)}, []radiocap.Demand{newMesh(5200)}, radiocap.DefaultPolicy())
			if err != nil {
				done <- ""

				return
			}

			done <- d.Outcome
		}()
	}

	for range 8 {
		assert.Equal(t, radiocap.OutcomeAdmit, <-done)
	}
}
