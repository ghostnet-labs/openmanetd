package chanmig_test

import (
	"crypto/ed25519"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

func TestSnapshot_freshNode(t *testing.T) {
	cfg := validConfig(t)
	cfg.Clock = &fakeClock{base: &simTime{now: planEpoch()}, synced: false}

	n, err := chanmig.New(cfg)
	require.NoError(t, err)
	require.NoError(t, n.Start(t.Context()))

	s := n.Snapshot()

	assert.Equal(t, planEpoch(), s.At)
	assert.Equal(t, chanmig.NodeID(1), s.ID)
	assert.Equal(t, uint64(testMeshID), s.MeshID)
	assert.Equal(t, chanmig.PhaseBooting, s.Phase)
	assert.False(t, s.ClockSynced)
	assert.False(t, s.Coordinator)
	assert.False(t, s.Coordinating)
	assert.Nil(t, s.CurrentPlan)
	assert.Nil(t, s.PreviousPlan)
	assert.Empty(t, s.Peers)
	assert.Zero(t, s.Acks)
	assert.True(t, s.Search.Started.IsZero())
	require.Len(t, s.TrustedKeys, 1)
	assert.True(t, s.TrustedKeys[0].Equal(testKey(1).Public().(ed25519.PublicKey)))

	require.Len(t, s.Bands, 3)

	for i, want := range []chanmig.Assignment{halow(1), wifi24(1), wifi5(36)} {
		b := s.Bands[i]
		assert.Equal(t, want, b.Operating, "band %d operating", i)
		assert.Equal(t, want, b.Target, "band %d target", i)
		assert.Equal(t, chanmig.BandSettled, b.Mode, "band %d mode", i)
		assert.Equal(t, planEpoch(), b.ModeSince, "band %d mode since", i)
		assert.Zero(t, b.Previous, "band %d previous", i)
		assert.True(t, b.SwitchAt.IsZero(), "band %d switch at", i)
		assert.Zero(t, b.SwitchTo, "band %d switch to", i)
		assert.True(t, b.Deadline.IsZero(), "band %d deadline", i)
		assert.True(t, b.LastPeer.IsZero(), "band %d last peer", i)
		assert.False(t, b.Failed, "band %d failed", i)
		assert.Zero(t, b.SearchCandidates, "band %d candidates", i)
	}
}

func TestSnapshot_pendingPlanAndPeers(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)
	sp := propose(t, s, nodeA, halow(3), wifi24(1), wifi5(44))

	s.run(2 * time.Second)

	coord := s.nodes[nodeA].node.Snapshot()
	assert.True(t, coord.Coordinator)
	assert.True(t, coord.Coordinating)
	assert.True(t, coord.ClockSynced)
	assert.Equal(t, 2, coord.Acks)

	b := s.nodes[nodeB].node.Snapshot()
	assert.False(t, b.Coordinator)
	assert.False(t, b.Coordinating)
	assert.Equal(t, chanmig.PhasePending, b.Phase)
	require.NotNil(t, b.CurrentPlan)
	assert.Equal(t, sp.Plan.Version, b.CurrentPlan.Version)
	assert.Equal(t, sp.Plan.Assignments, b.CurrentPlan.Assignments)
	assert.Equal(t, sp.Plan.ActivateAt, b.CurrentPlan.ActivateAt)
	assert.Nil(t, b.PreviousPlan)

	assert.Len(t, b.Peers, 2)
	assert.Contains(t, b.Peers, nodeA)
	assert.Contains(t, b.Peers, nodeC)

	halowBand, wifi2, wifi5Band := b.Bands[0], b.Bands[1], b.Bands[2]
	assert.Equal(t, halow(1), halowBand.Operating)
	assert.Equal(t, halow(3), halowBand.Target)
	assert.Equal(t, halow(3), halowBand.SwitchTo)
	assert.Equal(t, sp.Plan.ActivateAt, halowBand.SwitchAt)
	assert.False(t, halowBand.LastPeer.IsZero())

	assert.True(t, wifi2.SwitchAt.IsZero(), "unchanged band has no switch")
	assert.Zero(t, wifi2.SwitchTo)

	assert.Equal(t, wifi5(44), wifi5Band.SwitchTo)
	assert.Equal(t, sp.Plan.ActivateAt.Add(sp.Plan.Stagger), wifi5Band.SwitchAt, "second changed band is staggered")

	s.run(settle)
	require.True(t, s.allOnPlan(sp, nodeA, nodeB, nodeC))

	b = s.nodes[nodeB].node.Snapshot()
	assert.Equal(t, chanmig.PhaseStable, b.Phase)
	assert.Equal(t, halow(1), b.Bands[0].Previous)
	assert.Equal(t, halow(3), b.Bands[0].Operating)
	assert.True(t, b.Bands[0].SwitchAt.IsZero())

	sp2 := propose(t, s, nodeA, halow(4), wifi24(1), wifi5(44))
	s.run(time.Second)

	b = s.nodes[nodeB].node.Snapshot()
	require.NotNil(t, b.PreviousPlan)
	assert.Equal(t, sp.Plan.Version, b.PreviousPlan.Version)
	assert.Equal(t, sp2.Plan.Version, b.CurrentPlan.Version)
}

func TestSnapshot_verifyAndFallbackDeadlines(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)
	s.medium.jam(wifi5(44))
	sp := propose(t, s, nodeA, halow(1), wifi24(1), wifi5(44))

	s.run(sp.Plan.ActivateAt.Sub(s.now()) + time.Second)

	b := s.nodes[nodeB].node.Snapshot()
	w := b.Bands[2]
	assert.Equal(t, chanmig.PhaseMigrating, b.Phase)
	assert.Equal(t, chanmig.BandVerifying, w.Mode)
	assert.Equal(t, w.ModeSince.Add(chanmig.DefaultTiming().VerifyBound), w.Deadline)
	assert.Equal(t, wifi5(36), w.Previous)
	assert.Equal(t, wifi5(44), w.Operating)

	_, ok := s.runUntil(chanmig.DefaultTiming().VerifyBound, func() bool {
		w = s.nodes[nodeB].node.Snapshot().Bands[2]

		return w.Mode == chanmig.BandFallback
	})
	require.True(t, ok, "band never fell back")
	assert.Equal(t, wifi5(36), w.Operating)
	assert.Equal(t, w.ModeSince.Add(chanmig.DefaultTiming().FallbackBound), w.Deadline)

	s.run(settle)

	w = s.nodes[nodeB].node.Snapshot().Bands[2]
	assert.Equal(t, chanmig.BandSettled, w.Mode)
	assert.True(t, w.Deadline.IsZero(), "deadline only reported while verifying or falling back")
}

func TestSnapshot_searching(t *testing.T) {
	s := newMesh(t, nodeB)
	s.isolate(nodeB, true, allBands()...)

	timing := chanmig.DefaultTiming()
	s.run(timing.PeerLossTimeout + 2*time.Second)

	b := s.nodes[nodeB].node.Snapshot()
	require.Equal(t, chanmig.PhaseSearching, b.Phase)
	assert.Empty(t, b.Peers, "peers outside the window are not reported")
	assert.False(t, b.Search.Started.IsZero())
	assert.Positive(t, b.Search.Steps)
	assert.Equal(t, uint64(1), b.Stats.Searches)

	for i, band := range b.Bands {
		assert.Equal(t, chanmig.BandSearching, band.Mode, "band %d", i)
		assert.Positive(t, band.SearchCandidates, "band %d", i)
	}

	s.isolate(nodeB, false, allBands()...)
	_, ok := s.runUntil(s.nodes[nodeB].node.RecoveryBound(), func() bool {
		return s.status(nodeB).Phase == chanmig.PhaseStable
	})
	require.True(t, ok)

	b = s.nodes[nodeB].node.Snapshot()
	assert.Positive(t, b.LastSearch)
	assert.False(t, b.Search.Started.IsZero(), "most recent search is kept")
}

func TestSnapshot_returnsCopies(t *testing.T) {
	s := newMesh(t, nodeB)
	propose(t, s, nodeA, halow(3), wifi24(6), wifi5(44))
	s.run(time.Second)

	n := s.nodes[nodeB].node
	first := n.Snapshot()
	require.NotNil(t, first.CurrentPlan)

	first.CurrentPlan.Assignments[0] = halow(8)
	first.CurrentPlan.Version = 99
	first.Bands[0].Operating = halow(8)
	first.TrustedKeys[0] = nil

	for id := range first.Peers {
		delete(first.Peers, id)
	}

	second := n.Snapshot()
	assert.Equal(t, halow(3), second.CurrentPlan.Assignments[0])
	assert.Equal(t, uint64(1), second.CurrentPlan.Version)
	assert.Equal(t, halow(1), second.Bands[0].Operating)
	assert.NotNil(t, second.TrustedKeys[0])
	assert.NotEmpty(t, second.Peers)
}

func TestSnapshot_concurrentWithTraffic(t *testing.T) {
	s := newMesh(t, nodeB)
	sp := propose(t, s, nodeA, halow(2), wifi24(1), wifi5(36))
	frame := advertPayload(nodeC, sp.Raw, sp.Sig)
	b := s.nodes[nodeB].node

	var wg sync.WaitGroup

	for i := range 3 {
		wg.Go(func() {
			for range 200 {
				switch i {
				case 0:
					b.Tick(s.t.Context())
				case 1:
					b.HandleMessage(s.t.Context(), chanmig.BandHaLow, frame)
				default:
					_ = b.Snapshot()
				}
			}
		})
	}

	wg.Wait()
	assert.Equal(t, sp.Plan.Version, b.Snapshot().CurrentPlan.Version)
}

func TestBandModeString(t *testing.T) {
	for m, want := range map[chanmig.BandMode]string{
		chanmig.BandSettled:   "settled",
		chanmig.BandVerifying: "verifying",
		chanmig.BandFallback:  "fallback",
		chanmig.BandSearching: "searching",
		chanmig.BandMode(42):  "unknown",
	} {
		assert.Equal(t, want, m.String())
	}
}

func TestVerifierKeys_returnsCopy(t *testing.T) {
	k1 := testKey(1).Public().(ed25519.PublicKey)
	k2 := testKey(2).Public().(ed25519.PublicKey)

	v, err := chanmig.NewVerifier(testMeshID, k1, k2)
	require.NoError(t, err)

	keys := v.Keys()
	require.Len(t, keys, 2)
	assert.True(t, keys[0].Equal(k1))
	assert.True(t, keys[1].Equal(k2))

	keys[0] = nil

	assert.True(t, v.Keys()[0].Equal(k1))
}
