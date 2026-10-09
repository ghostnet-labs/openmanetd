package chanmig_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

const (
	nodeA chanmig.NodeID = 1
	nodeB chanmig.NodeID = 2
	nodeC chanmig.NodeID = 3
	nodeD chanmig.NodeID = 4
	nodeX chanmig.NodeID = 9

	lead = 30 * time.Second
)

// settle covers activation, all staggered band switches and verification.
const settle = lead + 3*5*time.Second + 10*time.Second

// newMesh starts coordinator A plus the given members and lets them hear
// each other.
func newMesh(t *testing.T, members ...chanmig.NodeID) *sim {
	t.Helper()

	s := newSim(t)
	s.add(nodeA, true)

	for _, id := range members {
		s.add(id, false)
	}

	s.run(3 * time.Second)

	for _, id := range s.order {
		require.Equal(t, chanmig.PhaseStable, s.status(id).Phase, "node %d not stable at start", id)
	}

	return s
}

func propose(t *testing.T, s *sim, by chanmig.NodeID, as ...chanmig.Assignment) chanmig.SignedPlan {
	t.Helper()

	sp, err := s.nodes[by].node.Propose(s.t.Context(), as, lead)
	require.NoError(t, err)

	return sp
}

func TestScenario_stagedSwitch(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)
	sp := propose(t, s, nodeA, halow(3), wifi24(1), wifi5(44))

	s.run(2 * time.Second)

	for _, id := range []chanmig.NodeID{nodeA, nodeB, nodeC} {
		st := s.status(id)
		assert.Equal(t, uint64(1), st.Version, "node %d", id)
		assert.Equal(t, chanmig.PhasePending, st.Phase, "node %d", id)
		a, _ := s.nodes[id].radio.on(chanmig.BandHaLow)
		assert.Equal(t, halow(1), a, "node %d must not switch before activation", id)
	}

	assert.Equal(t, 2, s.status(nodeA).Acks, "coordinator collects both acks")

	// One second after activation HaLow has moved but 5 GHz still holds
	// the old path (stagger keeps an alternate path up).
	s.run(sp.Plan.ActivateAt.Sub(s.now()) + time.Second)

	for _, id := range []chanmig.NodeID{nodeA, nodeB, nodeC} {
		h, _ := s.nodes[id].radio.on(chanmig.BandHaLow)
		w, _ := s.nodes[id].radio.on(chanmig.Band5G)
		assert.Equal(t, halow(3), h, "node %d halow", id)
		assert.Equal(t, wifi5(36), w, "node %d 5 GHz", id)
	}

	s.run(settle)

	assert.True(t, s.allOnPlan(sp, nodeA, nodeB, nodeC))

	for _, id := range []chanmig.NodeID{nodeA, nodeB, nodeC} {
		assert.Zero(t, s.status(id).Stats.Fallbacks, "node %d", id)
		assert.Zero(t, s.status(id).Stats.Searches, "node %d", id)
	}
}

func TestScenario_fallbackWhenNewChannelSilent(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)
	s.medium.jam(wifi5(44))
	propose(t, s, nodeA, halow(1), wifi24(1), wifi5(44))

	s.run(settle)

	for _, id := range []chanmig.NodeID{nodeA, nodeB, nodeC} {
		st := s.status(id)
		w, _ := s.nodes[id].radio.on(chanmig.Band5G)
		assert.Equal(t, wifi5(36), w, "node %d falls back to the previous 5 GHz channel", id)
		assert.Equal(t, uint64(1), st.Stats.Fallbacks, "node %d", id)
		assert.Equal(t, chanmig.PhaseStable, st.Phase, "node %d", id)
	}

	// Fallback holds while peers are present on the previous channel.
	s.run(time.Minute)

	w, _ := s.nodes[nodeB].radio.on(chanmig.Band5G)
	assert.Equal(t, wifi5(36), w)
}

func TestScenario_missedSwitchRecoversWithinBound(t *testing.T) {
	s := newMesh(t, nodeB, nodeC, nodeD)
	s.isolate(nodeD, true, allBands()...)

	sp := propose(t, s, nodeA, halow(5), wifi24(6), wifi5(149))
	s.run(settle)
	require.True(t, s.allOnPlan(sp, nodeA, nodeB, nodeC))
	assert.Equal(t, uint64(0), s.status(nodeD).Version, "D missed the announcement")

	s.isolate(nodeD, false, allBands()...)

	took, ok := s.runUntil(2*s.nodes[nodeD].node.RecoveryBound(), func() bool { return s.onPlan(nodeD, sp) })
	require.True(t, ok, "D never recovered")
	assert.LessOrEqual(t, took, s.nodes[nodeD].node.RecoveryBound())
	assert.Positive(t, s.status(nodeD).Stats.Recoveries)
}

func TestScenario_multipleMissedSwitchesAfterColdBoot(t *testing.T) {
	s := newMesh(t, nodeB, nodeC, nodeD)

	v1 := propose(t, s, nodeA, halow(2), wifi24(1), wifi5(44))
	s.run(settle)
	require.True(t, s.allOnPlan(v1, nodeA, nodeB, nodeC, nodeD))

	s.powerOff(nodeD)

	v2 := propose(t, s, nodeA, halow(6), wifi24(11), wifi5(157))
	s.run(settle)
	v3 := propose(t, s, nodeA, halow(7), wifi24(6), wifi5(149))
	s.run(settle)
	require.True(t, s.allOnPlan(v3, nodeA, nodeB, nodeC))
	require.NotEqual(t, v2.Plan.Version, v3.Plan.Version)

	s.powerOn(nodeD)
	assert.Equal(t, v1.Plan.Version, s.status(nodeD).Version, "D boots from its persisted plan")

	bound := s.nodes[nodeD].node.RecoveryBound()
	took, ok := s.runUntil(2*bound, func() bool { return s.onPlan(nodeD, v3) })
	require.True(t, ok, "D never recovered after cold boot")
	assert.LessOrEqual(t, took, bound)
}

func TestScenario_rendezvousLoss(t *testing.T) {
	s := newMesh(t, nodeB, nodeD)

	for _, a := range []chanmig.Assignment{halow(9), halow(10), chanmig.Assignment{Band: chanmig.Band5G, Channel: 165, WidthMHz: 20}} {
		s.medium.jam(a)
	}

	s.powerOff(nodeD)

	propose(t, s, nodeA, halow(4), wifi24(6), wifi5(44))
	s.run(settle)
	v2 := propose(t, s, nodeA, halow(8), wifi24(11), wifi5(157))
	s.run(settle)
	require.True(t, s.allOnPlan(v2, nodeA, nodeB))

	s.powerOn(nodeD)

	bound := s.nodes[nodeD].node.RecoveryBound()
	took, ok := s.runUntil(2*bound, func() bool { return s.onPlan(nodeD, v2) })
	require.True(t, ok, "D never recovered with every rendezvous channel jammed")
	assert.LessOrEqual(t, took, bound)
}

func TestScenario_searchingNodesMeetOnRendezvous(t *testing.T) {
	s := newSim(t)

	// Both nodes boot with no plan, the provisioned channels are jammed on
	// every band and the first HaLow rendezvous channel is jammed too.
	for _, a := range []chanmig.Assignment{halow(1), wifi24(1), wifi5(36), halow(9)} {
		s.medium.jam(a)
	}

	s.add(nodeB, false)
	// Start C out of phase by two and a half dwells so the two seek
	// sweeps never line up on their own.
	s.run(7500 * time.Millisecond)
	s.add(nodeC, false)

	bound := 10 * s.nodes[nodeB].node.RecoveryBound()
	_, ok := s.runUntil(bound, func() bool {
		return s.status(nodeB).Phase == chanmig.PhaseStable && s.status(nodeC).Phase == chanmig.PhaseStable &&
			s.status(nodeB).Peers > 0 && s.status(nodeC).Peers > 0
	})
	require.True(t, ok, "two searching nodes never met")
}

func TestScenario_haLowOnlyCoverage(t *testing.T) {
	s := newMesh(t, nodeB, nodeD)
	s.powerOff(nodeD)

	sp := propose(t, s, nodeA, halow(4), wifi24(11), wifi5(149))
	s.run(settle)
	require.True(t, s.allOnPlan(sp, nodeA, nodeB))

	// D returns where only HaLow reaches the mesh.
	s.isolate(nodeD, true, chanmig.Band2G4, chanmig.Band5G)
	s.powerOn(nodeD)

	bound := s.nodes[nodeD].node.RecoveryBound()
	took, ok := s.runUntil(2*bound, func() bool { return s.onPlan(nodeD, sp) })
	require.True(t, ok, "D never recovered over HaLow alone")
	assert.LessOrEqual(t, took, bound)

	// Wi-Fi radios follow the learned plan even with no Wi-Fi peers.
	s.run(time.Minute)
	assert.True(t, s.onPlan(nodeD, sp))
}

func TestScenario_partitionAndMerge(t *testing.T) {
	s := newSim(t)
	s.add(nodeA, true)
	s.add(nodeB, false)
	s.add(nodeX, true)
	s.add(nodeC, false)

	left := []chanmig.NodeID{nodeA, nodeB}
	right := []chanmig.NodeID{nodeX, nodeC}
	s.partition(left, right, true)
	s.run(3 * time.Second)

	pa := propose(t, s, nodeA, halow(1), wifi24(1), wifi5(44))
	px := propose(t, s, nodeX, halow(1), wifi24(1), wifi5(149))
	require.Equal(t, pa.Plan.Version, px.Plan.Version, "both partitions issue the same version")

	s.run(settle)
	require.True(t, s.allOnPlan(pa, left...))
	require.True(t, s.allOnPlan(px, right...))

	s.partition(left, right, false)

	// Equal versions: the higher signer ID (X) wins everywhere.
	_, ok := s.runUntil(2*time.Minute, func() bool { return s.allOnPlan(px, nodeA, nodeB, nodeX, nodeC) })
	require.True(t, ok, "partitions did not merge onto the winning plan")

	// A later plan from either side supersedes the merged one.
	next := propose(t, s, nodeA, halow(2), wifi24(1), wifi5(149))
	assert.Equal(t, px.Plan.Version+1, next.Plan.Version)

	s.run(settle)
	assert.True(t, s.allOnPlan(next, nodeA, nodeB, nodeX, nodeC))
}

func TestScenario_coordinatorLoss(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)
	sp := propose(t, s, nodeA, halow(3), wifi24(6), wifi5(44))

	s.run(5 * time.Second)
	s.powerOff(nodeA)

	s.run(settle)

	assert.True(t, s.allOnPlan(sp, nodeB, nodeC), "members finish the switch without the coordinator")

	for _, id := range []chanmig.NodeID{nodeB, nodeC} {
		assert.Zero(t, s.status(id).Stats.Fallbacks, "node %d", id)
		assert.Zero(t, s.status(id).Stats.Searches, "node %d", id)
	}

	// The coordinator returns later and rejoins from its persisted plan.
	s.powerOn(nodeA)
	_, ok := s.runUntil(time.Minute, func() bool { return s.onPlan(nodeA, sp) })
	assert.True(t, ok)
}

func TestScenario_unsyncedClockSwitchesOnRelativeTime(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)
	s.nodes[nodeC].clock.set(-time.Hour, false)

	sp := propose(t, s, nodeA, halow(3), wifi24(1), wifi5(36))

	_, ok := s.runUntil(sp.Plan.ActivateAt.Sub(s.now())+2*time.Second, func() bool {
		h, _ := s.nodes[nodeC].radio.on(chanmig.BandHaLow)

		return h == halow(3)
	})
	require.True(t, ok, "node with a lost clock must still switch at activation")

	s.run(settle)
	assert.True(t, s.allOnPlan(sp, nodeA, nodeB, nodeC))
	assert.Zero(t, s.status(nodeC).Stats.Fallbacks)
}

func TestScenario_abortWhenAcksMissing(t *testing.T) {
	s := newMesh(t, nodeB, nodeC)

	// Members still reach A, but nothing A sends reaches them, so no plan
	// and no acks propagate.
	for _, id := range []chanmig.NodeID{nodeB, nodeC} {
		for _, b := range allBands() {
			s.medium.setCut(nodeA, id, b, true)
		}
	}

	sp := propose(t, s, nodeA, halow(5), wifi24(6), wifi5(44))
	s.run(settle)

	st := s.status(nodeA)
	assert.Equal(t, uint64(1), st.Stats.Aborts)
	assert.Equal(t, sp.Plan.Version+1, st.Version, "abort issues a superseding revert plan")

	for _, b := range allBands() {
		got, _ := s.nodes[nodeA].radio.on(b)
		want, _ := revertTarget(b)
		assert.Equal(t, want, got, "coordinator stays on the pre-plan %s channel", b)
	}

	// Once the link heals, members learn the revert and stay put.
	for _, id := range []chanmig.NodeID{nodeB, nodeC} {
		for _, b := range allBands() {
			s.medium.setCut(nodeA, id, b, false)
		}
	}

	s.run(10 * time.Second)

	for _, id := range []chanmig.NodeID{nodeB, nodeC} {
		assert.Equal(t, sp.Plan.Version+1, s.status(id).Version, "node %d", id)
		h, _ := s.nodes[id].radio.on(chanmig.BandHaLow)
		assert.Equal(t, halow(1), h, "node %d", id)
	}
}

func revertTarget(b chanmig.Band) (chanmig.Assignment, bool) {
	for _, bc := range testBands() {
		if bc.Initial.Band == b {
			return bc.Initial, true
		}
	}

	return chanmig.Assignment{}, false
}

func TestScenario_radioFailureDuringSwitch(t *testing.T) {
	s := newMesh(t, nodeB, nodeD)
	sp := propose(t, s, nodeA, halow(3), wifi24(1), wifi5(149))

	s.run(10 * time.Second)
	s.nodes[nodeD].radio.setFailing(chanmig.Band5G, true)
	s.run(settle)

	st := s.status(nodeD)
	assert.Positive(t, st.Stats.RadioErrors)

	h, _ := s.nodes[nodeD].radio.on(chanmig.BandHaLow)
	assert.Equal(t, halow(3), h, "the working band still migrates")
	assert.Len(t, st.Operating, 2, "the failed radio is not reported as operating")

	s.nodes[nodeD].radio.setFailing(chanmig.Band5G, false)

	_, ok := s.runUntil(time.Minute, func() bool { return s.onPlan(nodeD, sp) })
	assert.True(t, ok, "the repaired radio rejoins on the plan's channel")
}

// TestScenario_recoveryBoundAcrossReturnTimes restores coverage at many
// points of the absent node's search schedule (seek and park epochs) and
// checks every recovery against the advertised bound.
func TestScenario_recoveryBoundAcrossReturnTimes(t *testing.T) {
	var worst time.Duration

	for offset := time.Duration(0); offset <= 260*time.Second; offset += 13 * time.Second {
		t.Run(offset.String(), func(t *testing.T) {
			s := newMesh(t, nodeB, nodeD)
			s.isolate(nodeD, true, allBands()...)

			sp := propose(t, s, nodeA, halow(6), wifi24(11), wifi5(157))
			s.run(settle + offset)
			s.isolate(nodeD, false, allBands()...)

			bound := s.nodes[nodeD].node.RecoveryBound()
			took, ok := s.runUntil(2*bound, func() bool { return s.onPlan(nodeD, sp) })
			require.True(t, ok)
			assert.LessOrEqual(t, took, bound)

			worst = max(worst, took)
		})
	}

	t.Logf("worst observed recovery %s", worst)
}
