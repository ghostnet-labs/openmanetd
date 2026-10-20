package handlers_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	radiov1 "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/openmanet/openmanetd/internal/pathsel"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The production types satisfy the consumer interfaces.
var (
	_ handlers.RadioCapacityView  = (*radiogate.Gate)(nil)
	_ handlers.RadioInventory     = (*radiogate.Provider)(nil)
	_ handlers.PathDecisionLister = (*pathsel.Selector)(nil)
)

// radioTestNow is the fixed clock of the RadioService tests.
func radioTestNow() time.Time { return time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC) }

func newRadioService(gate handlers.RadioCapacityView, inv handlers.RadioInventory, paths handlers.PathDecisionLister) *handlers.RadioService {
	return &handlers.RadioService{
		Log:       zerolog.Nop(),
		Gate:      gate,
		Inventory: inv,
		Paths:     paths,
		Now:       radioTestNow,
	}
}

// makePHY returns a PHY whose one combination allows a mesh point and an
// AP on one channel.
func makePHY(name string, bands ...radiocap.Band) radiocap.PHY {
	return radiocap.PHY{
		Name:  name,
		Wiphy: name,
		Bands: bands,
		Combinations: []radiocap.Combination{{
			Limits: []radiocap.Limit{
				{Types: []radiocap.IfType{radiocap.IfTypeMesh}, Max: 1},
				{Types: []radiocap.IfType{radiocap.IfTypeAP, radiocap.IfTypeManaged}, Max: 2},
			},
			MaxInterfaces: 3,
			MaxChannels:   1,
		}},
		Available: true,
	}
}

func makeDemand(id string, purpose radiocap.Purpose, role radiocap.Role, band radiocap.Band, centerKHz int) radiocap.Demand {
	return radiocap.Demand{ID: id, Purpose: purpose, Role: role,
		Channel: radiocap.Channel{Band: band, CenterKHz: centerKHz, WidthKHz: 20000}}
}

func TestRadioService_GetRadioCapacity_noGate(t *testing.T) {
	svc := newRadioService(nil, &fakeRadioInventory{}, nil)

	_, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestRadioService_GetRadioCapacity_mapsInventory(t *testing.T) {
	updated := radioTestNow().Add(-30 * time.Second)

	halow := makePHY("phy2", radiocap.BandS1G)
	halow.Qualified = true
	halow.SharingQualified = true

	multi := makePHY("phy0/radio1", radiocap.Band5G, radiocap.Band6G)
	multi.Wiphy = "phy0"
	multi.Radio = "1"
	multi.Available = false

	inv := &fakeRadioInventory{inv: radiogate.Inventory{UpdatedAt: updated, PHYs: []radiocap.PHY{halow, multi}}}
	svc := newRadioService(&fakeRadioCapacityView{mode: radiogate.ModeEnforce}, inv, nil)

	resp, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	require.NoError(t, err)

	c := resp.GetCapacity()
	assert.Equal(t, radioTestNow(), c.GetGeneratedAt().AsTime())
	assert.Equal(t, updated, c.GetInventoryUpdatedAt().AsTime())
	assert.Equal(t, radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ENFORCE, c.GetGateMode())
	require.Len(t, c.GetPhys(), 2)

	h := c.GetPhys()[0]
	assert.Equal(t, "phy2", h.GetName())
	assert.Equal(t, "phy2", h.GetWiphy())
	assert.Empty(t, h.GetRadioIndex())
	assert.Equal(t, []radiov1.Band{radiov1.Band_BAND_HALOW}, h.GetBands())
	assert.True(t, h.GetIsHalow())
	assert.True(t, h.GetQualified())
	assert.True(t, h.GetSharingQualified())
	assert.True(t, h.GetAvailable())
	assert.Equal(t, uint32(3), h.GetCapacity().GetMaxInterfaces())
	assert.Equal(t, uint32(1), h.GetCapacity().GetMaxChannels())

	maxByType := make(map[radiov1.InterfaceType]uint32, 3)
	for _, l := range h.GetCapacity().GetMaxByType() {
		maxByType[l.GetType()] = l.GetMax()
	}

	assert.Equal(t, map[radiov1.InterfaceType]uint32{
		radiov1.InterfaceType_INTERFACE_TYPE_MESH_POINT: 1,
		radiov1.InterfaceType_INTERFACE_TYPE_AP:         2,
		radiov1.InterfaceType_INTERFACE_TYPE_MANAGED:    2,
	}, maxByType)

	m := c.GetPhys()[1]
	assert.Equal(t, "phy0/radio1", m.GetName())
	assert.Equal(t, "phy0", m.GetWiphy())
	assert.Equal(t, "1", m.GetRadioIndex())
	assert.Equal(t, []radiov1.Band{radiov1.Band_BAND_5_GHZ, radiov1.Band_BAND_6_GHZ}, m.GetBands())
	assert.False(t, m.GetIsHalow())
	assert.False(t, m.GetQualified())
	assert.False(t, m.GetAvailable())
}

func TestRadioService_GetRadioCapacity_gateModes(t *testing.T) {
	tests := []struct {
		mode radiogate.Mode
		want radiov1.CapacityGateMode
	}{
		{mode: radiogate.ModeOff, want: radiov1.CapacityGateMode_CAPACITY_GATE_MODE_OFF},
		{mode: radiogate.ModeAdvisory, want: radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ADVISORY},
		{mode: radiogate.ModeEnforce, want: radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ENFORCE},
		{mode: "bogus", want: radiov1.CapacityGateMode_CAPACITY_GATE_MODE_UNSPECIFIED},
	}

	for _, tc := range tests {
		t.Run(string(tc.mode), func(t *testing.T) {
			svc := newRadioService(&fakeRadioCapacityView{mode: tc.mode}, nil, nil)

			resp, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, resp.GetCapacity().GetGateMode())
		})
	}
}

func TestRadioService_GetRadioCapacity_noInventory(t *testing.T) {
	svc := newRadioService(&fakeRadioCapacityView{mode: radiogate.ModeAdvisory}, nil, nil)

	resp, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	require.NoError(t, err)
	assert.Empty(t, resp.GetCapacity().GetPhys())
	assert.Nil(t, resp.GetCapacity().GetInventoryUpdatedAt(), "never read")
}

func TestRadioService_GetRadioCapacity_neverProbedHasNoTimestamp(t *testing.T) {
	inv := &fakeRadioInventory{}
	svc := newRadioService(&fakeRadioCapacityView{mode: radiogate.ModeAdvisory}, inv, nil)

	resp, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	require.NoError(t, err)
	assert.Nil(t, resp.GetCapacity().GetInventoryUpdatedAt())
	assert.Equal(t, 1, inv.getCalls())
}

func TestRadioService_GetRadioCapacity_inventoryError(t *testing.T) {
	inv := &fakeRadioInventory{inventoryErr: errors.New("radiogate: iw list: exec: not found")}
	svc := newRadioService(&fakeRadioCapacityView{mode: radiogate.ModeAdvisory}, inv, nil)

	_, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	assertConnectCode(t, err, connect.CodeInternal)
	assert.Contains(t, err.Error(), "iw list")
}

func TestRadioService_ListAdmissionDecisions_noGate(t *testing.T) {
	svc := newRadioService(nil, nil, nil)

	_, err := svc.ListAdmissionDecisions(context.Background(), &radiov1.ListAdmissionDecisionsRequest{Limit: 8})
	assertConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestRadioService_ListAdmissionDecisions_mapsRecord(t *testing.T) {
	decided := radioTestNow().Add(-time.Minute)
	mesh := makeDemand("mesh0", radiocap.PurposeMesh, radiocap.RoleMesh, radiocap.BandS1G, 915000)
	ap := makeDemand("ap0", radiocap.PurposeEUD, radiocap.RoleAP, radiocap.Band2G4, 2437000)
	sta := makeDemand("sta0", radiocap.PurposeOther, radiocap.RoleStation, radiocap.Band5G, 5180000)
	migr := makeDemand("mesh1", radiocap.PurposeMigration, radiocap.RoleMesh, radiocap.Band6G, 5975000)

	view := &fakeRadioCapacityView{records: []radiogate.Record{{
		Time:             decided,
		Mode:             radiogate.ModeAdvisory,
		CurrentDemands:   []radiocap.Demand{mesh, sta},
		RequestedDemands: []radiocap.Demand{ap, migr},
		Decision: &radiocap.Decision{
			Outcome:               radiocap.OutcomeAdmit,
			Reason:                radiocap.ReasonBoundedInterruption,
			PhysicalQualification: radiocap.NotEvaluated,
			Explanation:           "admitted by pausing sta0",
			Placements:            []radiocap.Placement{{PHY: "phy0", Demands: []string{"ap0", "mesh1"}}},
			Interrupt:             []string{"sta0"},
			Unplaceable:           []string{"x"},
			Alternatives: []radiocap.Alternative{
				{Kind: radiocap.AltInterruptDemand, Subjects: []string{"sta0"}, Approved: true},
				{Kind: radiocap.AltRestorePHY, Subjects: []string{"phy1"}},
				{Kind: radiocap.AltQualifyPHY, Subjects: []string{"phy2"}},
			},
			MaxInterruption: 3 * time.Second,
			UsesUnqualified: true,
		},
	}}}
	svc := newRadioService(view, nil, nil)

	resp, err := svc.ListAdmissionDecisions(context.Background(), &radiov1.ListAdmissionDecisionsRequest{Limit: 64})
	require.NoError(t, err)
	require.Len(t, resp.GetDecisions(), 1)

	d := resp.GetDecisions()[0]
	assert.Equal(t, decided, d.GetDecidedAt().AsTime())
	assert.Equal(t, radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ADVISORY, d.GetGateMode())
	assert.Equal(t, radiov1.AdmissionOutcome_ADMISSION_OUTCOME_ADMIT, d.GetOutcome())
	assert.Equal(t, radiov1.AdmissionReason_ADMISSION_REASON_BOUNDED_INTERRUPTION_APPROVED, d.GetReason())
	assert.Equal(t, "admitted by pausing sta0", d.GetExplanation())
	assert.Equal(t, radiov1.PhysicalQualification_PHYSICAL_QUALIFICATION_NOT_EVALUATED, d.GetPhysicalQualification())
	assert.Equal(t, 3*time.Second, d.GetMaxInterruption().AsDuration())
	assert.True(t, d.GetUsesUnqualified())
	assert.Equal(t, []string{"sta0"}, d.GetInterrupt())
	assert.Equal(t, []string{"x"}, d.GetUnplaceable())

	require.Len(t, d.GetPlacements(), 1)
	assert.Equal(t, "phy0", d.GetPlacements()[0].GetPhy())
	assert.Equal(t, []string{"ap0", "mesh1"}, d.GetPlacements()[0].GetDemandIds())

	require.Len(t, d.GetAlternatives(), 3)
	assert.Equal(t, radiov1.AlternativeKind_ALTERNATIVE_KIND_INTERRUPT_DEMAND, d.GetAlternatives()[0].GetKind())
	assert.True(t, d.GetAlternatives()[0].GetApproved())
	assert.Equal(t, []string{"sta0"}, d.GetAlternatives()[0].GetSubjects())
	assert.Equal(t, radiov1.AlternativeKind_ALTERNATIVE_KIND_RESTORE_PHY, d.GetAlternatives()[1].GetKind())
	assert.Equal(t, radiov1.AlternativeKind_ALTERNATIVE_KIND_QUALIFY_PHY, d.GetAlternatives()[2].GetKind())

	require.Len(t, d.GetCurrent(), 2)
	cur := d.GetCurrent()[0]
	assert.Equal(t, "mesh0", cur.GetId())
	assert.Equal(t, radiov1.DemandPurpose_DEMAND_PURPOSE_MESH, cur.GetPurpose())
	assert.Equal(t, radiov1.DemandRole_DEMAND_ROLE_MESH, cur.GetRole())
	assert.Equal(t, radiov1.Band_BAND_HALOW, cur.GetChannel().GetBand())
	assert.Equal(t, uint32(915000), cur.GetChannel().GetCenterKhz())
	assert.Equal(t, uint32(20000), cur.GetChannel().GetWidthKhz())
	assert.Equal(t, radiov1.DemandPurpose_DEMAND_PURPOSE_OTHER, d.GetCurrent()[1].GetPurpose())
	assert.Equal(t, radiov1.DemandRole_DEMAND_ROLE_STATION, d.GetCurrent()[1].GetRole())
	assert.Equal(t, radiov1.Band_BAND_5_GHZ, d.GetCurrent()[1].GetChannel().GetBand())

	require.Len(t, d.GetRequested(), 2)
	assert.Equal(t, radiov1.DemandPurpose_DEMAND_PURPOSE_EUD_ACCESS, d.GetRequested()[0].GetPurpose())
	assert.Equal(t, radiov1.DemandRole_DEMAND_ROLE_AP, d.GetRequested()[0].GetRole())
	assert.Equal(t, radiov1.Band_BAND_2_4_GHZ, d.GetRequested()[0].GetChannel().GetBand())
	assert.Equal(t, radiov1.DemandPurpose_DEMAND_PURPOSE_MESH_MIGRATION, d.GetRequested()[1].GetPurpose())
	assert.Equal(t, radiov1.Band_BAND_6_GHZ, d.GetRequested()[1].GetChannel().GetBand())
}

func TestRadioService_ListAdmissionDecisions_outcomesAndReasons(t *testing.T) {
	tests := []struct {
		outcome     radiocap.Outcome
		reason      radiocap.Reason
		wantOutcome radiov1.AdmissionOutcome
		wantReason  radiov1.AdmissionReason
	}{
		{radiocap.OutcomeAdmit, radiocap.ReasonFits,
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_ADMIT, radiov1.AdmissionReason_ADMISSION_REASON_FITS},
		{radiocap.OutcomeReject, radiocap.ReasonCapacityShort,
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT, radiov1.AdmissionReason_ADMISSION_REASON_CAPACITY_SHORT},
		{radiocap.OutcomeReject, radiocap.ReasonNoEligiblePHY,
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT, radiov1.AdmissionReason_ADMISSION_REASON_NO_ELIGIBLE_PHY},
		{radiocap.OutcomeReject, radiocap.ReasonUnqualifiedOnly,
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT, radiov1.AdmissionReason_ADMISSION_REASON_UNQUALIFIED_ONLY},
		{radiocap.OutcomeDefer, radiocap.ReasonPHYUnavailable,
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_DEFER, radiov1.AdmissionReason_ADMISSION_REASON_PHY_UNAVAILABLE},
		{radiocap.OutcomeReject, radiocap.ReasonSearchBudget,
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT, radiov1.AdmissionReason_ADMISSION_REASON_SEARCH_BUDGET_EXCEEDED},
		{"unknown", "unknown",
			radiov1.AdmissionOutcome_ADMISSION_OUTCOME_UNSPECIFIED, radiov1.AdmissionReason_ADMISSION_REASON_UNSPECIFIED},
	}

	for _, tc := range tests {
		t.Run(string(tc.reason), func(t *testing.T) {
			view := &fakeRadioCapacityView{records: []radiogate.Record{{
				Time:     radioTestNow(),
				Mode:     radiogate.ModeEnforce,
				Decision: &radiocap.Decision{Outcome: tc.outcome, Reason: tc.reason},
			}}}

			resp, err := newRadioService(view, nil, nil).ListAdmissionDecisions(context.Background(),
				&radiov1.ListAdmissionDecisionsRequest{Limit: 1})
			require.NoError(t, err)
			require.Len(t, resp.GetDecisions(), 1)

			d := resp.GetDecisions()[0]
			assert.Equal(t, tc.wantOutcome, d.GetOutcome())
			assert.Equal(t, tc.wantReason, d.GetReason())
			assert.Nil(t, d.GetMaxInterruption(), "unset when no interruption")
			assert.Equal(t, radiov1.PhysicalQualification_PHYSICAL_QUALIFICATION_UNSPECIFIED, d.GetPhysicalQualification())
			assert.Empty(t, d.GetCurrent())
			assert.Empty(t, d.GetPlacements())
		})
	}
}

func TestRadioService_ListAdmissionDecisions_limitAndSkip(t *testing.T) {
	rec := func(sec int, withDecision bool) radiogate.Record {
		r := radiogate.Record{Time: radioTestNow().Add(-time.Duration(sec) * time.Second), Mode: radiogate.ModeAdvisory}
		if withDecision {
			r.Decision = &radiocap.Decision{Outcome: radiocap.OutcomeAdmit, Reason: radiocap.ReasonFits}
		} else {
			r.Error = "radiogate: iw list: exec: not found"
		}

		return r
	}

	// Newest first, as Gate.Recent returns them.
	view := &fakeRadioCapacityView{records: []radiogate.Record{
		rec(1, true), rec(2, false), rec(3, true), rec(4, true),
	}}
	svc := newRadioService(view, nil, nil)

	tests := []struct {
		name  string
		limit uint32
		want  []int
	}{
		{name: "one", limit: 1, want: []int{1}},
		{name: "skips records without a decision", limit: 2, want: []int{1, 3}},
		{name: "more than held", limit: 64, want: []int{1, 3, 4}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			resp, err := svc.ListAdmissionDecisions(context.Background(), &radiov1.ListAdmissionDecisionsRequest{Limit: tc.limit})
			require.NoError(t, err)
			require.Len(t, resp.GetDecisions(), len(tc.want))

			for i, sec := range tc.want {
				assert.Equal(t, radioTestNow().Add(-time.Duration(sec)*time.Second), resp.GetDecisions()[i].GetDecidedAt().AsTime())
			}
		})
	}

	assert.Equal(t, len(tests), view.getRecentCalls())
}

func TestRadioService_ListAdmissionDecisions_empty(t *testing.T) {
	resp, err := newRadioService(&fakeRadioCapacityView{}, nil, nil).ListAdmissionDecisions(context.Background(),
		&radiov1.ListAdmissionDecisionsRequest{Limit: 64})
	require.NoError(t, err)
	assert.Empty(t, resp.GetDecisions())
}

// TestRadioService_realGate runs a real gate over the node fixture and
// reads its decision and inventory back through the service.
func TestRadioService_realGate(t *testing.T) {
	gate := newCapacityGate(t, radiogate.ModeAdvisory, radiogate.Settings{QualifiedPHYs: []string{"phy0"}}, nil)
	provider, ok := gate.Source.(*radiogate.Provider)
	require.True(t, ok)

	req := radiogate.Request{
		Origin: "test",
		Radios: []string{"radio3"},
		Requested: []radiocap.Demand{{
			ID: "default_radio3", Purpose: radiocap.PurposeMesh, Role: radiocap.RoleMesh, Channel: s1gChannel(42, 1000),
		}},
	}
	require.NoError(t, gate.Evaluate(context.Background(), req))

	svc := &handlers.RadioService{Log: zerolog.Nop(), Gate: gate, Inventory: provider}

	decs, err := svc.ListAdmissionDecisions(context.Background(), &radiov1.ListAdmissionDecisionsRequest{Limit: 10})
	require.NoError(t, err)
	require.Len(t, decs.GetDecisions(), 1)

	d := decs.GetDecisions()[0]
	assert.Equal(t, radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT, d.GetOutcome())
	assert.Equal(t, radiov1.AdmissionReason_ADMISSION_REASON_UNQUALIFIED_ONLY, d.GetReason())
	assert.NotEmpty(t, d.GetExplanation())
	require.Len(t, d.GetRequested(), 1)
	assert.Equal(t, "default_radio3", d.GetRequested()[0].GetId())
	assert.Equal(t, radiov1.Band_BAND_HALOW, d.GetRequested()[0].GetChannel().GetBand())

	capResp, err := svc.GetRadioCapacity(context.Background(), &radiov1.GetRadioCapacityRequest{})
	require.NoError(t, err)

	c := capResp.GetCapacity()
	assert.Equal(t, radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ADVISORY, c.GetGateMode())
	assert.NotNil(t, c.GetInventoryUpdatedAt())
	require.Len(t, c.GetPhys(), 3)

	byName := make(map[string]*radiov1.Phy, 3)
	for _, p := range c.GetPhys() {
		byName[p.GetName()] = p
	}

	assert.True(t, byName["phy0"].GetQualified())
	assert.False(t, byName["phy2"].GetQualified())
	assert.True(t, byName["phy2"].GetIsHalow())
	assert.Equal(t, []radiov1.Band{radiov1.Band_BAND_HALOW}, byName["phy2"].GetBands())
}

func TestRadioService_ListPathDecisions_noSelector(t *testing.T) {
	svc := newRadioService(nil, nil, nil)

	resp, err := svc.ListPathDecisions(context.Background(), &radiov1.ListPathDecisionsRequest{Limit: 10})
	require.NoError(t, err, "path selection not wired is not an error")
	assert.Empty(t, resp.GetDecisions())
}

func TestRadioService_ListPathDecisions_filters(t *testing.T) {
	tests := []struct {
		name      string
		req       *radiov1.ListPathDecisionsRequest
		wantDest  string
		wantClass pathsel.Class
		wantLimit int
	}{
		{name: "all", req: &radiov1.ListPathDecisionsRequest{Limit: 64}, wantLimit: 64},
		{name: "destination", req: &radiov1.ListPathDecisionsRequest{Limit: 5, Destination: "node-7"},
			wantDest: "node-7", wantLimit: 5},
		{name: "control", req: &radiov1.ListPathDecisionsRequest{Limit: 1, TrafficClass: radiov1.TrafficClass_TRAFFIC_CLASS_CONTROL},
			wantClass: pathsel.ClassControl, wantLimit: 1},
		{name: "voice", req: &radiov1.ListPathDecisionsRequest{Limit: 1, TrafficClass: radiov1.TrafficClass_TRAFFIC_CLASS_VOICE},
			wantClass: pathsel.ClassVoice, wantLimit: 1},
		{name: "bulk", req: &radiov1.ListPathDecisionsRequest{Limit: 1, TrafficClass: radiov1.TrafficClass_TRAFFIC_CLASS_BULK},
			wantClass: pathsel.ClassBulk, wantLimit: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			paths := &fakePathDecisionLister{}

			_, err := newRadioService(nil, nil, paths).ListPathDecisions(context.Background(), tc.req)
			require.NoError(t, err)

			dest, class, limit, calls := paths.lastArgs()
			assert.Equal(t, 1, calls)
			assert.Equal(t, tc.wantDest, dest)
			assert.Equal(t, tc.wantClass, class)
			assert.Equal(t, tc.wantLimit, limit)
		})
	}
}

func TestRadioService_ListPathDecisions_mapsDecision(t *testing.T) {
	since := radioTestNow().Add(-time.Minute)
	measured := radioTestNow().Add(-time.Second)

	paths := &fakePathDecisionLister{decisions: []pathsel.Decision{{
		Since:       since,
		DecidedAt:   radioTestNow(),
		Destination: "node-7",
		Class:       pathsel.ClassVoice,
		Band:        pathsel.Band5G,
		Previous:    pathsel.Band2G4,
		Duplicate:   pathsel.BandHaLow,
		Override:    pathsel.Band5G,
		Reason:      pathsel.ReasonFailover,
		Explanation: "5 GHz: 2.4 GHz became unreachable",
		Candidates: []pathsel.Candidate{
			{Band: pathsel.Band5G, Score: 91.5, Selected: true, HasData: true, Metrics: pathsel.Metrics{
				UpdatedAt: measured, Latency: 5 * time.Millisecond, Jitter: 2 * time.Millisecond,
				CapacityKbps: 60000, LossPermille: 20, Reachable: true,
			}},
			{Band: pathsel.Band2G4, Ineligible: pathsel.IneligibleUnreach, Detail: "no route", HasData: true},
			{Band: pathsel.BandHaLow, Ineligible: pathsel.IneligibleNoMetrics},
		},
	}}}

	resp, err := newRadioService(nil, nil, paths).ListPathDecisions(context.Background(),
		&radiov1.ListPathDecisionsRequest{Limit: 64})
	require.NoError(t, err)
	require.Len(t, resp.GetDecisions(), 1)

	d := resp.GetDecisions()[0]
	assert.Equal(t, "node-7", d.GetDestination())
	assert.Equal(t, radiov1.TrafficClass_TRAFFIC_CLASS_VOICE, d.GetTrafficClass())
	assert.Equal(t, radiov1.Band_BAND_5_GHZ, d.GetBand())
	assert.Equal(t, radiov1.Band_BAND_2_4_GHZ, d.GetPreviousBand())
	assert.Equal(t, radiov1.Band_BAND_HALOW, d.GetDuplicateBand())
	assert.Equal(t, radiov1.Band_BAND_5_GHZ, d.GetOverrideBand())
	assert.Equal(t, radiov1.PathReason_PATH_REASON_FAILOVER, d.GetReason())
	assert.Equal(t, "5 GHz: 2.4 GHz became unreachable", d.GetExplanation())
	assert.Equal(t, since, d.GetSince().AsTime())
	assert.Equal(t, radioTestNow(), d.GetDecidedAt().AsTime())
	require.Len(t, d.GetCandidates(), 3)

	sel := d.GetCandidates()[0]
	assert.True(t, sel.GetEligible())
	assert.True(t, sel.GetSelected())
	assert.InDelta(t, 91.5, sel.GetScore(), 1e-9)
	assert.Equal(t, radiov1.PathIneligibility_PATH_INELIGIBILITY_UNSPECIFIED, sel.GetIneligibility())
	require.True(t, sel.GetHasMetrics())
	assert.Equal(t, measured, sel.GetMetrics().GetUpdatedAt().AsTime())
	assert.Equal(t, 5*time.Millisecond, sel.GetMetrics().GetLatency().AsDuration())
	assert.Equal(t, 2*time.Millisecond, sel.GetMetrics().GetJitter().AsDuration())
	assert.Equal(t, uint32(60000), sel.GetMetrics().GetCapacityKbps())
	assert.Equal(t, uint32(20), sel.GetMetrics().GetLossPermille())
	assert.True(t, sel.GetMetrics().GetReachable())

	unreach := d.GetCandidates()[1]
	assert.False(t, unreach.GetEligible())
	assert.Equal(t, radiov1.PathIneligibility_PATH_INELIGIBILITY_UNREACHABLE, unreach.GetIneligibility())
	assert.Equal(t, "no route", unreach.GetDetail())
	assert.Nil(t, unreach.GetMetrics().GetUpdatedAt(), "zero time is unset")

	none := d.GetCandidates()[2]
	assert.False(t, none.GetHasMetrics())
	assert.Nil(t, none.GetMetrics(), "metrics unset without data")
	assert.Equal(t, radiov1.PathIneligibility_PATH_INELIGIBILITY_NO_METRICS, none.GetIneligibility())
}

func TestRadioService_ListPathDecisions_enums(t *testing.T) {
	reasons := map[pathsel.Reason]radiov1.PathReason{
		pathsel.ReasonInitial:         radiov1.PathReason_PATH_REASON_INITIAL,
		pathsel.ReasonDwell:           radiov1.PathReason_PATH_REASON_DWELL,
		pathsel.ReasonHoldMargin:      radiov1.PathReason_PATH_REASON_HOLD_MARGIN,
		pathsel.ReasonBetter:          radiov1.PathReason_PATH_REASON_BETTER,
		pathsel.ReasonFailover:        radiov1.PathReason_PATH_REASON_FAILOVER,
		pathsel.ReasonOverride:        radiov1.PathReason_PATH_REASON_OVERRIDE,
		pathsel.ReasonOverrideBlocked: radiov1.PathReason_PATH_REASON_OVERRIDE_INELIGIBLE,
		pathsel.ReasonNoPath:          radiov1.PathReason_PATH_REASON_NO_ELIGIBLE_PATH,
		"unknown":                     radiov1.PathReason_PATH_REASON_UNSPECIFIED,
	}

	inelig := map[pathsel.Ineligibility]radiov1.PathIneligibility{
		pathsel.IneligibleStale:      radiov1.PathIneligibility_PATH_INELIGIBILITY_STALE_METRICS,
		pathsel.IneligibleCapacity:   radiov1.PathIneligibility_PATH_INELIGIBILITY_RADIO_CAPACITY,
		pathsel.IneligibleLoss:       radiov1.PathIneligibility_PATH_INELIGIBILITY_LOSS_TOO_HIGH,
		pathsel.IneligibleLatency:    radiov1.PathIneligibility_PATH_INELIGIBILITY_LATENCY_TOO_HIGH,
		pathsel.IneligibleJitter:     radiov1.PathIneligibility_PATH_INELIGIBILITY_JITTER_TOO_HIGH,
		pathsel.IneligibleThroughput: radiov1.PathIneligibility_PATH_INELIGIBILITY_INSUFFICIENT_CAPACITY,
		pathsel.IneligibleProtected:  radiov1.PathIneligibility_PATH_INELIGIBILITY_PROTECTED_FOR_PRIORITY,
	}

	decisions := make([]pathsel.Decision, 0, len(reasons))
	for r := range reasons {
		decisions = append(decisions, pathsel.Decision{Destination: string(r), Reason: r, Class: "unknown"})
	}

	cands := make([]pathsel.Candidate, 0, len(inelig))
	for i := range inelig {
		cands = append(cands, pathsel.Candidate{Band: pathsel.Band2G4, Ineligible: i, Detail: string(i)})
	}

	decisions[0].Candidates = cands

	resp, err := newRadioService(nil, nil, &fakePathDecisionLister{decisions: decisions}).ListPathDecisions(
		context.Background(), &radiov1.ListPathDecisionsRequest{Limit: 64})
	require.NoError(t, err)
	require.Len(t, resp.GetDecisions(), len(reasons))

	for _, d := range resp.GetDecisions() {
		assert.Equal(t, reasons[pathsel.Reason(d.GetDestination())], d.GetReason(), d.GetDestination())
		assert.Equal(t, radiov1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED, d.GetTrafficClass())
		assert.Equal(t, radiov1.Band_BAND_UNSPECIFIED, d.GetBand(), "no band is unspecified")

		for _, c := range d.GetCandidates() {
			assert.Equal(t, inelig[pathsel.Ineligibility(c.GetDetail())], c.GetIneligibility(), c.GetDetail())
		}
	}
}
