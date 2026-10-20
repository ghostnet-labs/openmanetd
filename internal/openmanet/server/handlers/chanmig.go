package handlers

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"slices"
	"time"

	"connectrpc.com/connect"
	chanmigv1 "github.com/openmanet/openmanetd/internal/api/openmanet/chanmig/v1"
	radiov1 "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1"
	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/rs/zerolog"
	"google.golang.org/protobuf/types/known/durationpb"
	"google.golang.org/protobuf/types/known/timestamppb"
)

var (
	// errChanmigDisabled is returned by ProposeChannelPlan when channel
	// migration does not run on this node.
	errChanmigDisabled = errors.New("channel migration is not running on this node")

	// errChanmigSessionRequired is returned by ProposeChannelPlan called
	// without a session while authentication is enabled.
	errChanmigSessionRequired = errors.New("a signed-in session is required to propose a channel plan")
)

// ChannelMigrator is the subset of *chanmig.Node the handler uses.
type ChannelMigrator interface {
	Snapshot() chanmig.Snapshot
	Propose(ctx context.Context, assignments []chanmig.Assignment, lead time.Duration) (chanmig.SignedPlan, error)
}

// ChannelMigrationService implements
// chanmigv1connect.ChannelMigrationServiceHandler. Both RPCs stay behind
// the API session middleware; ProposeChannelPlan additionally records the
// caller and, with auth enabled, refuses calls that carry no username.
type ChannelMigrationService struct {
	Log zerolog.Logger
	// Node is nil when channel migration is disabled or failed to start.
	Node ChannelMigrator
	// RequireSession refuses ProposeChannelPlan without an authenticated
	// username (set when auth is enabled).
	RequireSession bool
}

// GetChannelMigrationStatus maps the node snapshot. With channel migration
// disabled it succeeds with enabled false and every other field unset.
func (s *ChannelMigrationService) GetChannelMigrationStatus(
	_ context.Context,
	_ *chanmigv1.GetChannelMigrationStatusRequest,
) (*chanmigv1.GetChannelMigrationStatusResponse, error) {
	if s.Node == nil {
		return &chanmigv1.GetChannelMigrationStatusResponse{Status: &chanmigv1.ChannelMigrationStatus{}}, nil
	}

	snap := s.Node.Snapshot()

	return &chanmigv1.GetChannelMigrationStatusResponse{Status: chanmigStatusToProto(&snap)}, nil
}

// ProposeChannelPlan signs and starts distributing a plan on a coordinator
// node. The node enforces its allowed lists and maximum lead.
func (s *ChannelMigrationService) ProposeChannelPlan(
	ctx context.Context,
	req *chanmigv1.ProposeChannelPlanRequest,
) (*chanmigv1.ProposeChannelPlanResponse, error) {
	if s.Node == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errChanmigDisabled)
	}

	caller := auth.UsernameFromContext(ctx)
	if s.RequireSession && caller == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errChanmigSessionRequired)
	}

	assignments := make([]chanmig.Assignment, 0, len(req.GetAssignments()))

	for _, a := range req.GetAssignments() {
		ca, err := assignmentFromProto(a)
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}

		assignments = append(assignments, ca)
	}

	lead := req.GetLead().AsDuration()

	sp, err := s.Node.Propose(ctx, assignments, lead)
	if err != nil {
		s.Log.Warn().Err(err).Str("caller", caller).Dur("lead", lead).Msg("ProposeChannelPlan refused")

		return nil, mapChanmigError(err)
	}

	s.Log.Info().Str("caller", caller).Uint64("version", sp.Plan.Version).Time("activate_at", sp.Plan.ActivateAt).
		Int("assignments", len(sp.Plan.Assignments)).Msg("Channel plan proposed")

	return &chanmigv1.ProposeChannelPlanResponse{Plan: planToProto(&sp.Plan)}, nil
}

// mapChanmigError converts a Node.Propose error to a connect error.
func mapChanmigError(err error) error {
	switch {
	case errors.Is(err, chanmig.ErrNoSigner):
		return connect.NewError(connect.CodeFailedPrecondition, err)
	case errors.Is(err, chanmig.ErrInvalidPlan):
		return connect.NewError(connect.CodeInvalidArgument, err)
	default:
		return connect.NewError(connect.CodeInternal, err)
	}
}

// assignmentFromProto converts a validated wire assignment. The request
// validator already bounds every field; the checks here only keep the
// narrowing conversions safe.
func assignmentFromProto(a *chanmigv1.ChannelAssignment) (chanmig.Assignment, error) {
	band, ok := bandFromProto(a.GetBand())
	if !ok {
		return chanmig.Assignment{}, fmt.Errorf("band %s is not managed by channel migration", a.GetBand())
	}

	if a.GetChannel() > math.MaxUint16 || a.GetWidthMhz() > math.MaxUint16 {
		return chanmig.Assignment{}, fmt.Errorf("assignment %d/%d out of range", a.GetChannel(), a.GetWidthMhz())
	}

	return chanmig.Assignment{Band: band, Channel: uint16(a.GetChannel()), WidthMHz: uint16(a.GetWidthMhz())}, nil
}

func bandFromProto(b radiov1.Band) (chanmig.Band, bool) {
	switch b {
	case radiov1.Band_BAND_HALOW:
		return chanmig.BandHaLow, true
	case radiov1.Band_BAND_2_4_GHZ:
		return chanmig.Band2G4, true
	case radiov1.Band_BAND_5_GHZ:
		return chanmig.Band5G, true
	default:
		return 0, false
	}
}

func bandToProto(b chanmig.Band) radiov1.Band {
	switch b {
	case chanmig.BandHaLow:
		return radiov1.Band_BAND_HALOW
	case chanmig.Band2G4:
		return radiov1.Band_BAND_2_4_GHZ
	case chanmig.Band5G:
		return radiov1.Band_BAND_5_GHZ
	default:
		return radiov1.Band_BAND_UNSPECIFIED
	}
}

// assignmentToProto returns nil for the zero assignment (no band).
func assignmentToProto(a chanmig.Assignment) *chanmigv1.ChannelAssignment {
	if a.Band == 0 {
		return nil
	}

	return &chanmigv1.ChannelAssignment{
		Band:     bandToProto(a.Band),
		Channel:  uint32(a.Channel),
		WidthMhz: uint32(a.WidthMHz),
	}
}

func planToProto(p *chanmig.Plan) *chanmigv1.ChannelPlan {
	if p == nil {
		return nil
	}

	out := &chanmigv1.ChannelPlan{
		Version:     p.Version,
		SignerId:    uint64(p.SignerID),
		IssuedAt:    timestamppb.New(p.IssuedAt),
		ActivateAt:  timestamppb.New(p.ActivateAt),
		Stagger:     durationpb.New(p.Stagger),
		Assignments: make([]*chanmigv1.ChannelAssignment, 0, len(p.Assignments)),
	}

	for _, a := range p.Assignments {
		out.Assignments = append(out.Assignments, assignmentToProto(a))
	}

	return out
}

func migrationPhaseToProto(p chanmig.Phase) chanmigv1.MigrationPhase {
	switch p {
	case chanmig.PhaseBooting:
		return chanmigv1.MigrationPhase_MIGRATION_PHASE_BOOTING
	case chanmig.PhaseStable:
		return chanmigv1.MigrationPhase_MIGRATION_PHASE_STABLE
	case chanmig.PhasePending:
		return chanmigv1.MigrationPhase_MIGRATION_PHASE_PENDING
	case chanmig.PhaseMigrating:
		return chanmigv1.MigrationPhase_MIGRATION_PHASE_MIGRATING
	case chanmig.PhaseSearching:
		return chanmigv1.MigrationPhase_MIGRATION_PHASE_SEARCHING
	default:
		return chanmigv1.MigrationPhase_MIGRATION_PHASE_UNSPECIFIED
	}
}

func bandModeToProto(m chanmig.BandMode) chanmigv1.BandMode {
	switch m {
	case chanmig.BandSettled:
		return chanmigv1.BandMode_BAND_MODE_SETTLED
	case chanmig.BandVerifying:
		return chanmigv1.BandMode_BAND_MODE_VERIFYING
	case chanmig.BandFallback:
		return chanmigv1.BandMode_BAND_MODE_FALLBACK
	case chanmig.BandSearching:
		return chanmigv1.BandMode_BAND_MODE_SEARCHING
	default:
		return chanmigv1.BandMode_BAND_MODE_UNSPECIFIED
	}
}

func bandStatusToProto(b *chanmig.BandSnapshot) *chanmigv1.BandStatus {
	out := &chanmigv1.BandStatus{
		Band:             bandToProto(b.Operating.Band),
		Operating:        assignmentToProto(b.Operating),
		Target:           assignmentToProto(b.Target),
		Mode:             bandModeToProto(b.Mode),
		ModeSince:        optionalTimestamp(b.ModeSince),
		Deadline:         optionalTimestamp(b.Deadline),
		SwitchAt:         optionalTimestamp(b.SwitchAt),
		Previous:         assignmentToProto(b.Previous),
		LastPeerAt:       optionalTimestamp(b.LastPeer),
		RadioFailed:      b.Failed,
		SearchCandidates: clampUint32(b.SearchCandidates),
	}

	if !b.SwitchAt.IsZero() {
		out.SwitchTo = assignmentToProto(b.SwitchTo)
	}

	return out
}

// chanmigStatusToProto maps an enabled node's snapshot.
func chanmigStatusToProto(s *chanmig.Snapshot) *chanmigv1.ChannelMigrationStatus {
	out := &chanmigv1.ChannelMigrationStatus{
		GeneratedAt:  timestamppb.New(s.At),
		Enabled:      true,
		NodeId:       uint64(s.ID),
		MeshId:       s.MeshID,
		Coordinator:  s.Coordinator,
		Coordinating: s.Coordinating,
		ClockSynced:  s.ClockSynced,
		Phase:        migrationPhaseToProto(s.Phase),
		CurrentPlan:  planToProto(s.CurrentPlan),
		PreviousPlan: planToProto(s.PreviousPlan),
		Bands:        make([]*chanmigv1.BandStatus, 0, len(s.Bands)),
		Peers:        make([]*chanmigv1.PeerStatus, 0, len(s.Peers)),
		Acks:         clampUint32(s.Acks),
		TrustedKeys:  make([]*chanmigv1.TrustedKey, 0, len(s.TrustedKeys)),
		Stats: &chanmigv1.MigrationStats{
			Accepted:    s.Stats.Accepted,
			Rejected:    s.Stats.Rejected,
			Stale:       s.Stats.Stale,
			Malformed:   s.Stats.Malformed,
			Fallbacks:   s.Stats.Fallbacks,
			Searches:    s.Stats.Searches,
			Recoveries:  s.Stats.Recoveries,
			Aborts:      s.Stats.Aborts,
			RadioErrors: s.Stats.RadioErrors,
			SendErrors:  s.Stats.SendErrors,
		},
	}

	for i := range s.Bands {
		out.Bands = append(out.Bands, bandStatusToProto(&s.Bands[i]))
	}

	if s.Phase == chanmig.PhaseSearching {
		out.Search = &chanmigv1.SearchStatus{
			StartedAt: optionalTimestamp(s.Search.Started),
			Epoch:     clampUint32(s.Search.Epoch),
			Step:      clampUint32(s.Search.Step),
			Steps:     clampUint32(s.Search.Steps),
			Parking:   s.Search.Parking,
			Parks:     clampUint32(s.Search.Parks),
		}
	}

	if s.LastSearch > 0 {
		out.LastSearch = durationpb.New(s.LastSearch)
	}

	for id, seen := range s.Peers {
		out.Peers = append(out.Peers, &chanmigv1.PeerStatus{NodeId: uint64(id), LastSeen: timestamppb.New(seen)})
	}

	slices.SortFunc(out.Peers, func(a, b *chanmigv1.PeerStatus) int { return cmp.Compare(a.GetNodeId(), b.GetNodeId()) })

	for _, k := range s.TrustedKeys {
		sum := sha256.Sum256(k)
		out.TrustedKeys = append(out.TrustedKeys, &chanmigv1.TrustedKey{
			Fingerprint: hex.EncodeToString(sum[:]),
			PublicKey:   bytes.Clone(k),
		})
	}

	return out
}
