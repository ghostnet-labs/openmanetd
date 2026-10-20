package handlers_test

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"testing"
	"time"

	"connectrpc.com/connect"
	chanmigv1 "github.com/openmanet/openmanetd/internal/api/openmanet/chanmig/v1"
	radiov1 "github.com/openmanet/openmanetd/internal/api/openmanet/radio/v1"
	"github.com/openmanet/openmanetd/internal/auth"
	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/durationpb"
)

// The production node satisfies the consumer interface.
var _ handlers.ChannelMigrator = (*chanmig.Node)(nil)

func newChannelMigrationService(node handlers.ChannelMigrator, requireSession bool) *handlers.ChannelMigrationService {
	return &handlers.ChannelMigrationService{Log: zerolog.Nop(), Node: node, RequireSession: requireSession}
}

func cmHaLow(ch uint16) chanmig.Assignment {
	return chanmig.Assignment{Band: chanmig.BandHaLow, Channel: ch, WidthMHz: 1}
}

func cm5G(ch uint16) chanmig.Assignment {
	return chanmig.Assignment{Band: chanmig.Band5G, Channel: ch, WidthMHz: 80}
}

func protoAssignment(band radiov1.Band, ch, w uint32) *chanmigv1.ChannelAssignment {
	return &chanmigv1.ChannelAssignment{Band: band, Channel: ch, WidthMhz: w}
}

func TestChannelMigrationService_GetStatus_disabled(t *testing.T) {
	svc := newChannelMigrationService(nil, true)

	resp, err := svc.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
	require.NoError(t, err)

	st := resp.GetStatus()
	require.NotNil(t, st)
	assert.False(t, st.GetEnabled())
	assert.Nil(t, st.GetGeneratedAt(), "every other field is unset when disabled")
	assert.Empty(t, st.GetBands())
	assert.Nil(t, st.GetStats())
	assert.Equal(t, chanmigv1.MigrationPhase_MIGRATION_PHASE_UNSPECIFIED, st.GetPhase())
}

func sampleChanmigSnapshot() chanmig.Snapshot {
	now := chanmigTestNow()
	pub, _ := chanmigTestKey().Public().(ed25519.PublicKey)

	current := &chanmig.Plan{
		Version:     4,
		SignerID:    7,
		IssuedAt:    now.Add(-time.Minute),
		ActivateAt:  now.Add(time.Minute),
		Stagger:     time.Minute,
		Assignments: []chanmig.Assignment{cmHaLow(2), cm5G(44)},
	}
	previous := &chanmig.Plan{
		Version:     3,
		SignerID:    7,
		IssuedAt:    now.Add(-time.Hour),
		ActivateAt:  now.Add(-time.Hour),
		Assignments: []chanmig.Assignment{cmHaLow(1)},
	}

	return chanmig.Snapshot{
		At: now,
		Peers: map[chanmig.NodeID]time.Time{
			9: now.Add(-2 * time.Second),
			3: now.Add(-time.Second),
		},
		CurrentPlan:  current,
		PreviousPlan: previous,
		Bands: []chanmig.BandSnapshot{
			{
				ModeSince: now.Add(-10 * time.Second),
				Deadline:  now.Add(50 * time.Second),
				SwitchAt:  now.Add(time.Minute),
				SwitchTo:  cmHaLow(3),
				LastPeer:  now.Add(-time.Second),
				Operating: cmHaLow(2),
				Target:    cmHaLow(2),
				Previous:  cmHaLow(1),
				Mode:      chanmig.BandVerifying,
			},
			{
				ModeSince:        now.Add(-time.Hour),
				Operating:        cm5G(36),
				Target:           cm5G(44),
				SwitchTo:         cm5G(149), // stale: no switch scheduled
				Mode:             chanmig.BandSettled,
				Failed:           true,
				SearchCandidates: 6,
			},
		},
		TrustedKeys: []ed25519.PublicKey{pub},
		Search:      chanmig.SearchSnapshot{Started: now.Add(-30 * time.Second), Epoch: 1, Step: 2, Steps: 6, Parks: 1, Parking: true},
		LastSearch:  90 * time.Second,
		Stats: chanmig.Stats{
			Accepted: 1, Rejected: 2, Stale: 3, Malformed: 4, Fallbacks: 5,
			Searches: 6, Recoveries: 7, Aborts: 8, RadioErrors: 9, SendErrors: 10,
		},
		MeshID:       0x0E1A,
		ID:           7,
		Acks:         2,
		Phase:        chanmig.PhaseMigrating,
		ClockSynced:  true,
		Coordinator:  true,
		Coordinating: true,
	}
}

func TestChannelMigrationService_GetStatus_mapsSnapshot(t *testing.T) {
	snap := sampleChanmigSnapshot()
	svc := newChannelMigrationService(&fakeChannelMigrator{snap: snap}, false)

	resp, err := svc.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
	require.NoError(t, err)

	st := resp.GetStatus()
	assert.True(t, st.GetEnabled())
	assert.Equal(t, chanmigTestNow(), st.GetGeneratedAt().AsTime())
	assert.Equal(t, uint64(7), st.GetNodeId())
	assert.Equal(t, uint64(0x0E1A), st.GetMeshId())
	assert.True(t, st.GetCoordinator())
	assert.True(t, st.GetCoordinating())
	assert.True(t, st.GetClockSynced())
	assert.Equal(t, chanmigv1.MigrationPhase_MIGRATION_PHASE_MIGRATING, st.GetPhase())
	assert.Equal(t, uint32(2), st.GetAcks())

	cur := st.GetCurrentPlan()
	assert.Equal(t, uint64(4), cur.GetVersion())
	assert.Equal(t, uint64(7), cur.GetSignerId())
	assert.Equal(t, snap.CurrentPlan.IssuedAt, cur.GetIssuedAt().AsTime())
	assert.Equal(t, snap.CurrentPlan.ActivateAt, cur.GetActivateAt().AsTime())
	assert.Equal(t, time.Minute, cur.GetStagger().AsDuration())
	require.Len(t, cur.GetAssignments(), 2)
	assert.Equal(t, radiov1.Band_BAND_HALOW, cur.GetAssignments()[0].GetBand())
	assert.Equal(t, uint32(2), cur.GetAssignments()[0].GetChannel())
	assert.Equal(t, uint32(1), cur.GetAssignments()[0].GetWidthMhz())
	assert.Equal(t, radiov1.Band_BAND_5_GHZ, cur.GetAssignments()[1].GetBand())
	assert.Equal(t, uint64(3), st.GetPreviousPlan().GetVersion())

	require.Len(t, st.GetBands(), 2)

	h := st.GetBands()[0]
	assert.Equal(t, radiov1.Band_BAND_HALOW, h.GetBand())
	assert.Equal(t, chanmigv1.BandMode_BAND_MODE_VERIFYING, h.GetMode())
	assert.Equal(t, uint32(2), h.GetOperating().GetChannel())
	assert.Equal(t, uint32(1), h.GetPrevious().GetChannel())
	assert.Equal(t, uint32(3), h.GetSwitchTo().GetChannel())
	assert.Equal(t, snap.Bands[0].SwitchAt, h.GetSwitchAt().AsTime())
	assert.Equal(t, snap.Bands[0].Deadline, h.GetDeadline().AsTime())
	assert.Equal(t, snap.Bands[0].LastPeer, h.GetLastPeerAt().AsTime())
	assert.Equal(t, snap.Bands[0].ModeSince, h.GetModeSince().AsTime())

	w := st.GetBands()[1]
	assert.Equal(t, radiov1.Band_BAND_5_GHZ, w.GetBand())
	assert.Equal(t, chanmigv1.BandMode_BAND_MODE_SETTLED, w.GetMode())
	assert.Equal(t, uint32(44), w.GetTarget().GetChannel())
	assert.True(t, w.GetRadioFailed())
	assert.Equal(t, uint32(6), w.GetSearchCandidates())
	assert.Nil(t, w.GetPrevious(), "zero previous assignment is unset")
	assert.Nil(t, w.GetSwitchAt())
	assert.Nil(t, w.GetSwitchTo(), "switch_to is unset without switch_at")
	assert.Nil(t, w.GetDeadline())
	assert.Nil(t, w.GetLastPeerAt())

	assert.Nil(t, st.GetSearch(), "search is reported only while searching")
	assert.Equal(t, 90*time.Second, st.GetLastSearch().AsDuration())

	require.Len(t, st.GetPeers(), 2)
	assert.Equal(t, uint64(3), st.GetPeers()[0].GetNodeId(), "peers sorted by node ID")
	assert.Equal(t, uint64(9), st.GetPeers()[1].GetNodeId())
	assert.Equal(t, snap.Peers[9], st.GetPeers()[1].GetLastSeen().AsTime())

	require.Len(t, st.GetTrustedKeys(), 1)

	sum := sha256.Sum256(snap.TrustedKeys[0])
	assert.Equal(t, hex.EncodeToString(sum[:]), st.GetTrustedKeys()[0].GetFingerprint())
	assert.Equal(t, []byte(snap.TrustedKeys[0]), st.GetTrustedKeys()[0].GetPublicKey())

	stats := st.GetStats()
	assert.Equal(t, []uint64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}, []uint64{
		stats.GetAccepted(), stats.GetRejected(), stats.GetStale(), stats.GetMalformed(), stats.GetFallbacks(),
		stats.GetSearches(), stats.GetRecoveries(), stats.GetAborts(), stats.GetRadioErrors(), stats.GetSendErrors(),
	})
}

func TestChannelMigrationService_GetStatus_phasesAndSearch(t *testing.T) {
	tests := []struct {
		phase chanmig.Phase
		want  chanmigv1.MigrationPhase
	}{
		{phase: chanmig.PhaseBooting, want: chanmigv1.MigrationPhase_MIGRATION_PHASE_BOOTING},
		{phase: chanmig.PhaseStable, want: chanmigv1.MigrationPhase_MIGRATION_PHASE_STABLE},
		{phase: chanmig.PhasePending, want: chanmigv1.MigrationPhase_MIGRATION_PHASE_PENDING},
		{phase: chanmig.PhaseMigrating, want: chanmigv1.MigrationPhase_MIGRATION_PHASE_MIGRATING},
		{phase: chanmig.PhaseSearching, want: chanmigv1.MigrationPhase_MIGRATION_PHASE_SEARCHING},
		{phase: chanmig.Phase(42), want: chanmigv1.MigrationPhase_MIGRATION_PHASE_UNSPECIFIED},
	}

	for _, tc := range tests {
		t.Run(tc.phase.String(), func(t *testing.T) {
			snap := sampleChanmigSnapshot()
			snap.Phase = tc.phase
			snap.Bands[0].Mode = chanmig.BandSearching
			snap.Bands[1].Mode = chanmig.BandFallback
			snap.LastSearch = 0
			snap.CurrentPlan, snap.PreviousPlan = nil, nil

			svc := newChannelMigrationService(&fakeChannelMigrator{snap: snap}, false)

			resp, err := svc.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
			require.NoError(t, err)

			st := resp.GetStatus()
			assert.Equal(t, tc.want, st.GetPhase())
			assert.Nil(t, st.GetCurrentPlan())
			assert.Nil(t, st.GetPreviousPlan())
			assert.Nil(t, st.GetLastSearch())
			assert.Equal(t, chanmigv1.BandMode_BAND_MODE_SEARCHING, st.GetBands()[0].GetMode())
			assert.Equal(t, chanmigv1.BandMode_BAND_MODE_FALLBACK, st.GetBands()[1].GetMode())

			if tc.phase != chanmig.PhaseSearching {
				assert.Nil(t, st.GetSearch())

				return
			}

			s := st.GetSearch()
			require.NotNil(t, s)
			assert.Equal(t, snap.Search.Started, s.GetStartedAt().AsTime())
			assert.Equal(t, uint32(1), s.GetEpoch())
			assert.Equal(t, uint32(2), s.GetStep())
			assert.Equal(t, uint32(6), s.GetSteps())
			assert.Equal(t, uint32(1), s.GetParks())
			assert.True(t, s.GetParking())
		})
	}
}

func TestChannelMigrationService_GetStatus_realNode(t *testing.T) {
	svc := newChannelMigrationService(newTestChanmigNode(t, false), false)

	resp, err := svc.GetChannelMigrationStatus(context.Background(), &chanmigv1.GetChannelMigrationStatusRequest{})
	require.NoError(t, err)

	st := resp.GetStatus()
	assert.True(t, st.GetEnabled())
	assert.False(t, st.GetCoordinator())
	assert.Equal(t, uint64(7), st.GetNodeId())
	assert.Equal(t, chanmigv1.MigrationPhase_MIGRATION_PHASE_BOOTING, st.GetPhase())
	require.Len(t, st.GetBands(), 2)
	assert.Equal(t, uint32(1), st.GetBands()[0].GetOperating().GetChannel())
	assert.Equal(t, uint32(36), st.GetBands()[1].GetTarget().GetChannel())
	assert.Len(t, st.GetTrustedKeys(), 1)
}

func proposeRequest(lead time.Duration, as ...*chanmigv1.ChannelAssignment) *chanmigv1.ProposeChannelPlanRequest {
	return &chanmigv1.ProposeChannelPlanRequest{Assignments: as, Lead: durationpb.New(lead)}
}

func TestChannelMigrationService_Propose_disabled(t *testing.T) {
	svc := newChannelMigrationService(nil, false)

	_, err := svc.ProposeChannelPlan(context.Background(),
		proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)))
	assertConnectCode(t, err, connect.CodeFailedPrecondition)
}

func TestChannelMigrationService_Propose_requiresSession(t *testing.T) {
	fake := &fakeChannelMigrator{}
	svc := newChannelMigrationService(fake, true)
	req := proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1))

	_, err := svc.ProposeChannelPlan(context.Background(), req)
	assertConnectCode(t, err, connect.CodeUnauthenticated)

	_, _, calls := fake.lastProposal()
	assert.Zero(t, calls, "no proposal without a session")

	_, err = svc.ProposeChannelPlan(auth.ContextWithUsername(context.Background(), "admin"), req)
	require.NoError(t, err)

	_, _, calls = fake.lastProposal()
	assert.Equal(t, 1, calls)
}

func TestChannelMigrationService_Propose_passesPlanAndMapsResponse(t *testing.T) {
	now := chanmigTestNow()
	fake := &fakeChannelMigrator{proposed: chanmig.SignedPlan{Plan: chanmig.Plan{
		Version:     5,
		SignerID:    7,
		IssuedAt:    now,
		ActivateAt:  now.Add(2 * time.Minute),
		Stagger:     time.Minute,
		Assignments: []chanmig.Assignment{cmHaLow(2), cm5G(44)},
	}}}
	svc := newChannelMigrationService(fake, false)

	resp, err := svc.ProposeChannelPlan(context.Background(), proposeRequest(2*time.Minute,
		protoAssignment(radiov1.Band_BAND_5_GHZ, 44, 80),
		protoAssignment(radiov1.Band_BAND_HALOW, 2, 1),
		protoAssignment(radiov1.Band_BAND_2_4_GHZ, 6, 20),
	))
	require.NoError(t, err)

	as, lead, _ := fake.lastProposal()
	assert.Equal(t, []chanmig.Assignment{
		cm5G(44), cmHaLow(2), {Band: chanmig.Band2G4, Channel: 6, WidthMHz: 20},
	}, as)
	assert.Equal(t, 2*time.Minute, lead)

	p := resp.GetPlan()
	assert.Equal(t, uint64(5), p.GetVersion())
	assert.Equal(t, now.Add(2*time.Minute), p.GetActivateAt().AsTime())
	assert.Len(t, p.GetAssignments(), 2)
}

func TestChannelMigrationService_Propose_errorMapping(t *testing.T) {
	tests := []struct {
		err  error
		name string
		want connect.Code
	}{
		{name: "no signer", err: chanmig.ErrNoSigner, want: connect.CodeFailedPrecondition},
		{name: "policy", err: errors.Join(chanmig.ErrInvalidPlan, errors.New("not in allowed list")), want: connect.CodeInvalidArgument},
		{name: "other", err: errors.New("boom"), want: connect.CodeInternal},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := newChannelMigrationService(&fakeChannelMigrator{proposeErr: tc.err}, false)

			_, err := svc.ProposeChannelPlan(context.Background(),
				proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)))
			assertConnectCode(t, err, tc.want)
		})
	}
}

func TestChannelMigrationService_Propose_unmanagedBand(t *testing.T) {
	fake := &fakeChannelMigrator{}
	svc := newChannelMigrationService(fake, false)

	_, err := svc.ProposeChannelPlan(context.Background(),
		proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_6_GHZ, 5, 80)))
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, err = svc.ProposeChannelPlan(context.Background(),
		proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 70000, 1)))
	assertConnectCode(t, err, connect.CodeInvalidArgument)

	_, _, calls := fake.lastProposal()
	assert.Zero(t, calls)
}

func TestChannelMigrationService_Propose_realNode(t *testing.T) {
	ctx := auth.ContextWithUsername(context.Background(), "admin")

	t.Run("not a coordinator", func(t *testing.T) {
		svc := newChannelMigrationService(newTestChanmigNode(t, false), true)

		_, err := svc.ProposeChannelPlan(ctx, proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)))
		assertConnectCode(t, err, connect.CodeFailedPrecondition)
	})

	t.Run("outside allowed list", func(t *testing.T) {
		svc := newChannelMigrationService(newTestChanmigNode(t, true), true)

		_, err := svc.ProposeChannelPlan(ctx, proposeRequest(time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 9, 1)))
		assertConnectCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("lead above max lead", func(t *testing.T) {
		svc := newChannelMigrationService(newTestChanmigNode(t, true), true)

		_, err := svc.ProposeChannelPlan(ctx, proposeRequest(11*time.Minute, protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)))
		assertConnectCode(t, err, connect.CodeInvalidArgument)
	})

	t.Run("accepted", func(t *testing.T) {
		node := newTestChanmigNode(t, true)
		svc := newChannelMigrationService(node, true)

		resp, err := svc.ProposeChannelPlan(ctx, proposeRequest(time.Minute,
			protoAssignment(radiov1.Band_BAND_5_GHZ, 44, 80), protoAssignment(radiov1.Band_BAND_HALOW, 2, 1)))
		require.NoError(t, err)

		p := resp.GetPlan()
		assert.Equal(t, uint64(1), p.GetVersion())
		assert.Equal(t, uint64(7), p.GetSignerId())
		assert.Equal(t, chanmigTestNow().Add(time.Minute), p.GetActivateAt().AsTime())
		require.Len(t, p.GetAssignments(), 2)
		assert.Equal(t, radiov1.Band_BAND_HALOW, p.GetAssignments()[0].GetBand(), "sorted by band")

		snap := node.Snapshot()
		require.NotNil(t, snap.CurrentPlan)
		assert.Equal(t, uint64(1), snap.CurrentPlan.Version)
		assert.True(t, snap.Coordinating)
	})
}
