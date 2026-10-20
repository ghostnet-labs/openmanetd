package handlers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"connectrpc.com/connect"
	radiov1 "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1"
	"github.com/openmanet/openmanetd/internal/pathsel"
	"github.com/openmanet/openmanetd/internal/radiocap"
	"github.com/openmanet/openmanetd/internal/radiogate"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// errRadioCapacityUnavailable is returned when the daemon runs without a
// radio capacity gate.
var errRadioCapacityUnavailable = errors.New("radio capacity gate is not running")

// RadioCapacityView is the read side of the radio capacity gate (GHO-59);
// *radiogate.Gate implements it.
type RadioCapacityView interface {
	CurrentMode() radiogate.Mode
	Recent() []radiogate.Record
}

// RadioInventory supplies the PHY inventory with qualification evidence
// applied; *radiogate.Provider implements it.
type RadioInventory interface {
	Inventory(ctx context.Context) (radiogate.Inventory, error)
}

// PathDecisionLister lists the latest path selection decisions (GHO-51);
// *pathsel.Selector implements it. An empty dest or class matches all.
type PathDecisionLister interface {
	Decisions(dest string, class pathsel.Class, limit int) []pathsel.Decision
}

// RadioService implements radiov1connect.RadioServiceHandler: a read-only
// view of the radio capacity model, its admission decisions and the path
// selection decisions. Every RPC is session-gated by the API auth
// middleware like the other settings reads.
type RadioService struct {
	Log zerolog.Logger
	// Gate is nil when the daemon runs without a capacity gate.
	Gate RadioCapacityView
	// Inventory is optional; without it the capacity view has no PHYs.
	Inventory RadioInventory
	// Paths is optional. Path selection is not wired to live traffic
	// yet, so it is normally nil and ListPathDecisions returns nothing.
	Paths PathDecisionLister
	// Now is the clock for generated_at. nil uses time.Now.
	Now func() time.Time
}

func (s *RadioService) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}

	return time.Now()
}

// GetRadioCapacity returns the gate mode and the PHY inventory.
func (s *RadioService) GetRadioCapacity(ctx context.Context, _ *radiov1.GetRadioCapacityRequest) (*radiov1.GetRadioCapacityResponse, error) {
	if s.Gate == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errRadioCapacityUnavailable)
	}

	capacity := &radiov1.RadioCapacity{
		GeneratedAt: timestamppb.New(s.now()),
		GateMode:    gateModeToProto(s.Gate.CurrentMode()),
	}

	if s.Inventory != nil {
		inv, err := s.Inventory.Inventory(ctx)
		if err != nil {
			s.Log.Error().Err(err).Msg("Failed to read radio inventory")

			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("radio inventory: %w", err))
		}

		capacity.InventoryUpdatedAt = optionalTimestamp(inv.UpdatedAt)
		capacity.Phys = make([]*radiov1.Phy, 0, len(inv.PHYs))

		for i := range inv.PHYs {
			capacity.Phys = append(capacity.Phys, phyToProto(&inv.PHYs[i]))
		}
	}

	return &radiov1.GetRadioCapacityResponse{Capacity: capacity}, nil
}

// ListAdmissionDecisions returns up to limit recorded decisions, newest
// first. A record whose check could not run (probe failure, unidentified
// HaLow PHY) carries no decision and is skipped.
func (s *RadioService) ListAdmissionDecisions(_ context.Context, req *radiov1.ListAdmissionDecisionsRequest) (*radiov1.ListAdmissionDecisionsResponse, error) {
	if s.Gate == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errRadioCapacityUnavailable)
	}

	recs := s.Gate.Recent()
	limit := min(int(req.GetLimit()), len(recs))
	out := make([]*radiov1.AdmissionDecision, 0, limit)

	for i := range recs {
		if len(out) >= limit {
			break
		}

		if recs[i].Decision == nil {
			continue
		}

		out = append(out, admissionToProto(&recs[i]))
	}

	return &radiov1.ListAdmissionDecisionsResponse{Decisions: out}, nil
}

// ListPathDecisions returns the latest path decision per (destination,
// class) matching the filters, newest first. Without a selector it
// returns an empty list.
func (s *RadioService) ListPathDecisions(_ context.Context, req *radiov1.ListPathDecisionsRequest) (*radiov1.ListPathDecisionsResponse, error) {
	if s.Paths == nil || req.GetLimit() == 0 {
		return &radiov1.ListPathDecisionsResponse{}, nil
	}

	decs := s.Paths.Decisions(req.GetDestination(), classFromProto(req.GetTrafficClass()), int(req.GetLimit()))
	out := make([]*radiov1.PathDecision, 0, len(decs))

	for i := range decs {
		out = append(out, pathDecisionToProto(&decs[i]))
	}

	return &radiov1.ListPathDecisionsResponse{Decisions: out}, nil
}

// clampUint32 converts a non-negative count to uint32, saturating.
func clampUint32(n int) uint32 {
	switch {
	case n <= 0:
		return 0
	case uint64(n) > math.MaxUint32:
		return math.MaxUint32
	default:
		return uint32(n)
	}
}

func gateModeToProto(m radiogate.Mode) radiov1.CapacityGateMode {
	switch m {
	case radiogate.ModeOff:
		return radiov1.CapacityGateMode_CAPACITY_GATE_MODE_OFF
	case radiogate.ModeAdvisory:
		return radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ADVISORY
	case radiogate.ModeEnforce:
		return radiov1.CapacityGateMode_CAPACITY_GATE_MODE_ENFORCE
	default:
		return radiov1.CapacityGateMode_CAPACITY_GATE_MODE_UNSPECIFIED
	}
}

func capBandToProto(b radiocap.Band) radiov1.Band {
	switch b {
	case radiocap.BandS1G:
		return radiov1.Band_BAND_HALOW
	case radiocap.Band2G4:
		return radiov1.Band_BAND_2_4_GHZ
	case radiocap.Band5G:
		return radiov1.Band_BAND_5_GHZ
	case radiocap.Band6G:
		return radiov1.Band_BAND_6_GHZ
	case radiocap.Band60G:
		return radiov1.Band_BAND_60_GHZ
	default:
		return radiov1.Band_BAND_UNSPECIFIED
	}
}

func ifTypeToProto(t radiocap.IfType) radiov1.InterfaceType {
	switch t {
	case radiocap.IfTypeManaged:
		return radiov1.InterfaceType_INTERFACE_TYPE_MANAGED
	case radiocap.IfTypeAP:
		return radiov1.InterfaceType_INTERFACE_TYPE_AP
	case radiocap.IfTypeMesh:
		return radiov1.InterfaceType_INTERFACE_TYPE_MESH_POINT
	default:
		return radiov1.InterfaceType_INTERFACE_TYPE_UNSPECIFIED
	}
}

func phyToProto(p *radiocap.PHY) *radiov1.Phy {
	bands := make([]radiov1.Band, 0, len(p.Bands))
	for _, b := range p.Bands {
		bands = append(bands, capBandToProto(b))
	}

	c := p.Capacity()

	limits := make([]*radiov1.InterfaceTypeLimit, 0, len(c.MaxByType))
	for _, l := range c.MaxByType {
		limits = append(limits, &radiov1.InterfaceTypeLimit{Type: ifTypeToProto(l.Type), Max: clampUint32(l.Max)})
	}

	return &radiov1.Phy{
		Name:             p.Name,
		Wiphy:            p.Wiphy,
		RadioIndex:       p.Radio,
		Bands:            bands,
		Qualified:        p.Qualified,
		Available:        p.Available,
		SharingQualified: p.SharingQualified,
		IsHalow:          slices.Contains(p.Bands, radiocap.BandS1G),
		Capacity: &radiov1.PhyCapacity{
			MaxByType:     limits,
			MaxInterfaces: clampUint32(c.MaxInterfaces),
			MaxChannels:   clampUint32(c.MaxChannels),
		},
	}
}

func purposeToProto(p radiocap.Purpose) radiov1.DemandPurpose {
	switch p {
	case radiocap.PurposeMesh:
		return radiov1.DemandPurpose_DEMAND_PURPOSE_MESH
	case radiocap.PurposeMigration:
		return radiov1.DemandPurpose_DEMAND_PURPOSE_MESH_MIGRATION
	case radiocap.PurposeEUD:
		return radiov1.DemandPurpose_DEMAND_PURPOSE_EUD_ACCESS
	case radiocap.PurposeOther:
		return radiov1.DemandPurpose_DEMAND_PURPOSE_OTHER
	default:
		return radiov1.DemandPurpose_DEMAND_PURPOSE_UNSPECIFIED
	}
}

func roleToProto(r radiocap.Role) radiov1.DemandRole {
	switch r {
	case radiocap.RoleMesh:
		return radiov1.DemandRole_DEMAND_ROLE_MESH
	case radiocap.RoleAP:
		return radiov1.DemandRole_DEMAND_ROLE_AP
	case radiocap.RoleStation:
		return radiov1.DemandRole_DEMAND_ROLE_STATION
	default:
		return radiov1.DemandRole_DEMAND_ROLE_UNSPECIFIED
	}
}

func demandsToProto(ds []radiocap.Demand) []*radiov1.Demand {
	if len(ds) == 0 {
		return nil
	}

	out := make([]*radiov1.Demand, 0, len(ds))
	for i := range ds {
		d := &ds[i]
		out = append(out, &radiov1.Demand{
			Id:      d.ID,
			Purpose: purposeToProto(d.Purpose),
			Role:    roleToProto(d.Role),
			Channel: &radiov1.Channel{
				Band:      capBandToProto(d.Channel.Band),
				CenterKhz: clampUint32(d.Channel.CenterKHz),
				WidthKhz:  clampUint32(d.Channel.WidthKHz),
			},
		})
	}

	return out
}

func outcomeToProto(o radiocap.Outcome) radiov1.AdmissionOutcome {
	switch o {
	case radiocap.OutcomeAdmit:
		return radiov1.AdmissionOutcome_ADMISSION_OUTCOME_ADMIT
	case radiocap.OutcomeDefer:
		return radiov1.AdmissionOutcome_ADMISSION_OUTCOME_DEFER
	case radiocap.OutcomeReject:
		return radiov1.AdmissionOutcome_ADMISSION_OUTCOME_REJECT
	default:
		return radiov1.AdmissionOutcome_ADMISSION_OUTCOME_UNSPECIFIED
	}
}

func admissionReasonToProto(r radiocap.Reason) radiov1.AdmissionReason {
	switch r {
	case radiocap.ReasonFits:
		return radiov1.AdmissionReason_ADMISSION_REASON_FITS
	case radiocap.ReasonCapacityShort:
		return radiov1.AdmissionReason_ADMISSION_REASON_CAPACITY_SHORT
	case radiocap.ReasonNoEligiblePHY:
		return radiov1.AdmissionReason_ADMISSION_REASON_NO_ELIGIBLE_PHY
	case radiocap.ReasonUnqualifiedOnly:
		return radiov1.AdmissionReason_ADMISSION_REASON_UNQUALIFIED_ONLY
	case radiocap.ReasonPHYUnavailable:
		return radiov1.AdmissionReason_ADMISSION_REASON_PHY_UNAVAILABLE
	case radiocap.ReasonBoundedInterruption:
		return radiov1.AdmissionReason_ADMISSION_REASON_BOUNDED_INTERRUPTION_APPROVED
	case radiocap.ReasonSearchBudget:
		return radiov1.AdmissionReason_ADMISSION_REASON_SEARCH_BUDGET_EXCEEDED
	default:
		return radiov1.AdmissionReason_ADMISSION_REASON_UNSPECIFIED
	}
}

func alternativeKindToProto(k radiocap.AlternativeKind) radiov1.AlternativeKind {
	switch k {
	case radiocap.AltInterruptDemand:
		return radiov1.AlternativeKind_ALTERNATIVE_KIND_INTERRUPT_DEMAND
	case radiocap.AltRestorePHY:
		return radiov1.AlternativeKind_ALTERNATIVE_KIND_RESTORE_PHY
	case radiocap.AltQualifyPHY:
		return radiov1.AlternativeKind_ALTERNATIVE_KIND_QUALIFY_PHY
	default:
		return radiov1.AlternativeKind_ALTERNATIVE_KIND_UNSPECIFIED
	}
}

// admissionToProto maps a record that carries a decision. Records are
// shared with the gate's ring, so slices are copied, never aliased.
func admissionToProto(rec *radiogate.Record) *radiov1.AdmissionDecision {
	d := rec.Decision

	out := &radiov1.AdmissionDecision{
		DecidedAt:       optionalTimestamp(rec.Time),
		GateMode:        gateModeToProto(rec.Mode),
		Current:         demandsToProto(rec.CurrentDemands),
		Requested:       demandsToProto(rec.RequestedDemands),
		Outcome:         outcomeToProto(d.Outcome),
		Reason:          admissionReasonToProto(d.Reason),
		Explanation:     d.Explanation,
		Interrupt:       slices.Clone(d.Interrupt),
		Unplaceable:     slices.Clone(d.Unplaceable),
		UsesUnqualified: d.UsesUnqualified,
	}

	if d.PhysicalQualification == radiocap.NotEvaluated {
		out.PhysicalQualification = radiov1.PhysicalQualification_PHYSICAL_QUALIFICATION_NOT_EVALUATED
	}

	if d.MaxInterruption > 0 {
		out.MaxInterruption = durationpb.New(d.MaxInterruption)
	}

	if len(d.Placements) > 0 {
		out.Placements = make([]*radiov1.Placement, 0, len(d.Placements))
		for _, p := range d.Placements {
			out.Placements = append(out.Placements, &radiov1.Placement{Phy: p.PHY, DemandIds: slices.Clone(p.Demands)})
		}
	}

	if len(d.Alternatives) > 0 {
		out.Alternatives = make([]*radiov1.Alternative, 0, len(d.Alternatives))
		for _, a := range d.Alternatives {
			out.Alternatives = append(out.Alternatives, &radiov1.Alternative{
				Kind:     alternativeKindToProto(a.Kind),
				Subjects: slices.Clone(a.Subjects),
				Approved: a.Approved,
			})
		}
	}

	return out
}

func classFromProto(c radiov1.TrafficClass) pathsel.Class {
	switch c {
	case radiov1.TrafficClass_TRAFFIC_CLASS_CONTROL:
		return pathsel.ClassControl
	case radiov1.TrafficClass_TRAFFIC_CLASS_VOICE:
		return pathsel.ClassVoice
	case radiov1.TrafficClass_TRAFFIC_CLASS_BULK:
		return pathsel.ClassBulk
	default:
		return ""
	}
}

func classToProto(c pathsel.Class) radiov1.TrafficClass {
	switch c {
	case pathsel.ClassControl:
		return radiov1.TrafficClass_TRAFFIC_CLASS_CONTROL
	case pathsel.ClassVoice:
		return radiov1.TrafficClass_TRAFFIC_CLASS_VOICE
	case pathsel.ClassBulk:
		return radiov1.TrafficClass_TRAFFIC_CLASS_BULK
	default:
		return radiov1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED
	}
}

func pathBandToProto(b pathsel.Band) radiov1.Band {
	switch b {
	case pathsel.BandHaLow:
		return radiov1.Band_BAND_HALOW
	case pathsel.Band2G4:
		return radiov1.Band_BAND_2_4_GHZ
	case pathsel.Band5G:
		return radiov1.Band_BAND_5_GHZ
	default:
		return radiov1.Band_BAND_UNSPECIFIED
	}
}

func pathReasonToProto(r pathsel.Reason) radiov1.PathReason {
	switch r {
	case pathsel.ReasonInitial:
		return radiov1.PathReason_PATH_REASON_INITIAL
	case pathsel.ReasonDwell:
		return radiov1.PathReason_PATH_REASON_DWELL
	case pathsel.ReasonHoldMargin:
		return radiov1.PathReason_PATH_REASON_HOLD_MARGIN
	case pathsel.ReasonBetter:
		return radiov1.PathReason_PATH_REASON_BETTER
	case pathsel.ReasonFailover:
		return radiov1.PathReason_PATH_REASON_FAILOVER
	case pathsel.ReasonOverride:
		return radiov1.PathReason_PATH_REASON_OVERRIDE
	case pathsel.ReasonOverrideBlocked:
		return radiov1.PathReason_PATH_REASON_OVERRIDE_INELIGIBLE
	case pathsel.ReasonNoPath:
		return radiov1.PathReason_PATH_REASON_NO_ELIGIBLE_PATH
	default:
		return radiov1.PathReason_PATH_REASON_UNSPECIFIED
	}
}

func ineligibilityToProto(i pathsel.Ineligibility) radiov1.PathIneligibility {
	switch i {
	case pathsel.IneligibleNoMetrics:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_NO_METRICS
	case pathsel.IneligibleUnreach:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_UNREACHABLE
	case pathsel.IneligibleStale:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_STALE_METRICS
	case pathsel.IneligibleCapacity:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_RADIO_CAPACITY
	case pathsel.IneligibleLoss:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_LOSS_TOO_HIGH
	case pathsel.IneligibleLatency:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_LATENCY_TOO_HIGH
	case pathsel.IneligibleJitter:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_JITTER_TOO_HIGH
	case pathsel.IneligibleThroughput:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_INSUFFICIENT_CAPACITY
	case pathsel.IneligibleProtected:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_PROTECTED_FOR_PRIORITY
	default:
		return radiov1.PathIneligibility_PATH_INELIGIBILITY_UNSPECIFIED
	}
}

func pathDecisionToProto(d *pathsel.Decision) *radiov1.PathDecision {
	cands := make([]*radiov1.PathCandidate, 0, len(d.Candidates))

	for i := range d.Candidates {
		c := &d.Candidates[i]
		pc := &radiov1.PathCandidate{
			Band:          pathBandToProto(c.Band),
			Eligible:      c.Ineligible == pathsel.IneligibleNone,
			Ineligibility: ineligibilityToProto(c.Ineligible),
			Detail:        c.Detail,
			HasMetrics:    c.HasData,
			Score:         c.Score,
			Selected:      c.Selected,
		}

		if c.HasData {
			pc.Metrics = &radiov1.PathMetrics{
				UpdatedAt:    optionalTimestamp(c.Metrics.UpdatedAt),
				Latency:      durationpb.New(c.Metrics.Latency),
				Jitter:       durationpb.New(c.Metrics.Jitter),
				CapacityKbps: clampUint32(c.Metrics.CapacityKbps),
				LossPermille: clampUint32(c.Metrics.LossPermille),
				Reachable:    c.Metrics.Reachable,
			}
		}

		cands = append(cands, pc)
	}

	return &radiov1.PathDecision{
		Destination:   d.Destination,
		TrafficClass:  classToProto(d.Class),
		Band:          pathBandToProto(d.Band),
		PreviousBand:  pathBandToProto(d.Previous),
		DuplicateBand: pathBandToProto(d.Duplicate),
		OverrideBand:  pathBandToProto(d.Override),
		Reason:        pathReasonToProto(d.Reason),
		Explanation:   d.Explanation,
		Candidates:    cands,
		Since:         optionalTimestamp(d.Since),
		DecidedAt:     optionalTimestamp(d.DecidedAt),
	}
}
