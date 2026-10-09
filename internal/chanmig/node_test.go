package chanmig_test

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

// advertPayload builds an advert frame by hand, pinning the wire layout:
// type(1)=1 | from(8) | hasPlan(1) | rawLen(2) | raw | sig(64).
func advertPayload(from chanmig.NodeID, raw, sig []byte) []byte {
	b := []byte{1}
	b = binary.BigEndian.AppendUint64(b, uint64(from))
	b = append(b, 1)
	b = binary.BigEndian.AppendUint16(b, uint16(len(raw)))
	b = append(b, raw...)

	return append(b, sig...)
}

func validConfig(t *testing.T) chanmig.Config {
	t.Helper()

	return chanmig.Config{
		ID:        1,
		Log:       zerolog.Nop(),
		Clock:     &fakeClock{base: &simTime{now: planEpoch()}, synced: true},
		Transport: &fakeTransport{m: newFakeMedium(), id: 1},
		Radio:     newFakeRadio(),
		Store:     &fakeStore{},
		Verifier:  testVerifier(t),
		Bands:     testBands(),
		Timing:    chanmig.DefaultTiming(),
	}
}

func TestNew_validatesConfig(t *testing.T) {
	tests := []struct {
		mutate func(c *chanmig.Config)
		name   string
	}{
		{name: "missing clock", mutate: func(c *chanmig.Config) { c.Clock = nil }},
		{name: "missing transport", mutate: func(c *chanmig.Config) { c.Transport = nil }},
		{name: "missing radio", mutate: func(c *chanmig.Config) { c.Radio = nil }},
		{name: "missing store", mutate: func(c *chanmig.Config) { c.Store = nil }},
		{name: "missing verifier", mutate: func(c *chanmig.Config) { c.Verifier = nil }},
		{name: "no bands", mutate: func(c *chanmig.Config) { c.Bands = nil }},
		{name: "four bands", mutate: func(c *chanmig.Config) { c.Bands = append(c.Bands, c.Bands[0]) }},
		{name: "duplicate band", mutate: func(c *chanmig.Config) { c.Bands[1] = c.Bands[0] }},
		{name: "invalid initial", mutate: func(c *chanmig.Config) { c.Bands[0].Initial.WidthMHz = 3 }},
		{name: "foreign band in list", mutate: func(c *chanmig.Config) { c.Bands[0].Allowed = append(c.Bands[0].Allowed, wifi5(36)) }},
		{name: "invalid list entry", mutate: func(c *chanmig.Config) { c.Bands[0].Rendezvous = []chanmig.Assignment{halow(0)} }},
		{name: "search list too long", mutate: func(c *chanmig.Config) { c.Bands[0].Allowed = make([]chanmig.Assignment, 65) }},
		{name: "negative history", mutate: func(c *chanmig.Config) { c.HistoryLen = -1 }},
		{name: "history too long", mutate: func(c *chanmig.Config) { c.HistoryLen = 17 }},
		{name: "zero timer", mutate: func(c *chanmig.Config) { c.Timing.Dwell = 0 }},
		{name: "stagger out of range", mutate: func(c *chanmig.Config) { c.Timing.Stagger = 2 * time.Minute }},
		{name: "dwell below two adverts", mutate: func(c *chanmig.Config) { c.Timing.Dwell = 1999 * time.Millisecond }},
		{name: "peer window below two adverts", mutate: func(c *chanmig.Config) { c.Timing.PeerWindow = time.Second }},
		{name: "peer loss not above verify+fallback", mutate: func(c *chanmig.Config) { c.Timing.PeerLossTimeout = 16 * time.Second }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := validConfig(t)
			tc.mutate(&cfg)

			_, err := chanmig.New(cfg)
			assert.Error(t, err)
		})
	}

	t.Run("valid", func(t *testing.T) {
		_, err := chanmig.New(validConfig(t))
		assert.NoError(t, err)
	})
}

func TestRecoveryBound_defaults(t *testing.T) {
	n, err := chanmig.New(validConfig(t))
	require.NoError(t, err)

	// Longest list: HaLow = 2 + 4 history + 2 rendezvous + 8 allowed = 16
	// entries; E = 16 x 3 s = 48 s; bound = 20 s + 4 x 48 s + 3 s.
	assert.Equal(t, 215*time.Second, n.RecoveryBound())
}

func TestPropose_errors(t *testing.T) {
	n, err := chanmig.New(validConfig(t))
	require.NoError(t, err)

	_, err = n.Propose(t.Context(), []chanmig.Assignment{halow(2)}, lead)
	require.ErrorIs(t, err, chanmig.ErrNoSigner)

	cfg := validConfig(t)
	cfg.Signer, err = chanmig.NewSigner(1, testKey(1))
	require.NoError(t, err)

	n, err = chanmig.New(cfg)
	require.NoError(t, err)
	require.NoError(t, n.Start(t.Context()))

	_, err = n.Propose(t.Context(), []chanmig.Assignment{halow(20)}, lead)
	require.ErrorIs(t, err, chanmig.ErrInvalidPlan, "channel outside the allowed list")

	_, err = n.Propose(t.Context(), []chanmig.Assignment{halow(2)}, time.Hour)
	require.ErrorIs(t, err, chanmig.ErrInvalidPlan, "lead above MaxLead")

	_, err = n.Propose(t.Context(), []chanmig.Assignment{halow(2), halow(3)}, lead)
	require.ErrorIs(t, err, chanmig.ErrInvalidPlan, "duplicate band")

	sp, err := n.Propose(t.Context(), []chanmig.Assignment{wifi5(44), halow(2)}, lead)
	require.NoError(t, err)
	assert.Equal(t, []chanmig.Assignment{halow(2), wifi5(44)}, sp.Plan.Assignments, "assignments sorted by band")
}

func TestScenario_replayedAndForgedPlansRejected(t *testing.T) {
	s := newMesh(t, nodeB)

	v1 := propose(t, s, nodeA, halow(2), wifi24(1), wifi5(36))
	s.run(settle)
	v2 := propose(t, s, nodeA, halow(3), wifi24(1), wifi5(44))
	s.run(settle)
	require.True(t, s.allOnPlan(v2, nodeA, nodeB))

	b := s.nodes[nodeB].node
	signer, err := chanmig.NewSigner(nodeA, testKey(1))
	require.NoError(t, err)

	forger, err := chanmig.NewSigner(nodeA, testKey(2))
	require.NoError(t, err)

	forged, err := forger.Sign(makePlan(99, halow(8)))
	require.NoError(t, err)

	tampered, err := signer.Sign(makePlan(50, halow(8)))
	require.NoError(t, err)

	otherMesh := makePlan(51, halow(8))
	otherMesh.MeshID = 0xBEEF
	wrongMesh, err := signer.Sign(otherMesh)
	require.NoError(t, err)

	outside, err := signer.Sign(makePlan(52, halow(20)))
	require.NoError(t, err)

	tooEarly := makePlan(53, halow(8))
	tooEarly.ActivateAt = tooEarly.IssuedAt.Add(time.Hour)
	longLead, err := signer.Sign(tooEarly)
	require.NoError(t, err)

	tests := []struct {
		name      string
		payload   []byte
		rejected  uint64
		stale     uint64
		malformed uint64
	}{
		{name: "replayed older plan", payload: advertPayload(nodeC, v1.Raw, v1.Sig), stale: 1},
		{name: "forged with untrusted key", payload: advertPayload(nodeC, forged.Raw, forged.Sig), rejected: 1},
		{name: "tampered after signing", payload: advertPayload(nodeC, withByte(tampered.Raw, 20, 99), tampered.Sig), rejected: 1},
		{name: "other mesh", payload: advertPayload(nodeC, wrongMesh.Raw, wrongMesh.Sig), rejected: 1},
		{name: "channel outside allowed list", payload: advertPayload(nodeC, outside.Raw, outside.Sig), rejected: 1},
		{name: "lead above maximum", payload: advertPayload(nodeC, longLead.Raw, longLead.Sig), rejected: 1},
		{name: "truncated frame", payload: advertPayload(nodeC, v2.Raw, v2.Sig)[:30], malformed: 1},
		{name: "unknown type", payload: []byte{7, 0, 0}, malformed: 1},
		{name: "empty", payload: nil, malformed: 1},
		{name: "bad plan flag", payload: []byte{1, 0, 0, 0, 0, 0, 0, 0, 3, 2}, malformed: 1},
		{name: "ack wrong length", payload: []byte{2, 0, 1}, malformed: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			before := b.Status()
			b.HandleMessage(s.t.Context(), chanmig.BandHaLow, tc.payload)
			after := b.Status()

			assert.Equal(t, v2.Plan.Version, after.Version, "plan must not change")
			assert.Equal(t, tc.rejected, after.Stats.Rejected-before.Stats.Rejected)
			assert.Equal(t, tc.stale, after.Stats.Stale-before.Stats.Stale)
			assert.Equal(t, tc.malformed, after.Stats.Malformed-before.Stats.Malformed)
		})
	}

	// The persisted plan is unaffected by the rejected traffic.
	s.run(5 * time.Second)
	assert.True(t, s.onPlan(nodeB, v2))
}

func TestStart_persistedPlanSurvivesRestart(t *testing.T) {
	s := newMesh(t, nodeB)
	sp := propose(t, s, nodeA, halow(4), wifi24(1), wifi5(44))
	s.run(settle)
	require.True(t, s.onPlan(nodeB, sp))
	assert.Positive(t, s.nodes[nodeB].store.saveCount())

	s.powerOff(nodeB)
	s.powerOn(nodeB)

	st := s.status(nodeB)
	assert.Equal(t, sp.Plan.Version, st.Version)
	assert.Equal(t, chanmig.PhaseBooting, st.Phase)

	h, _ := s.nodes[nodeB].radio.on(chanmig.BandHaLow)
	assert.Equal(t, halow(4), h, "radios come up on the last-known plan")

	s.run(3 * time.Second)
	assert.True(t, s.onPlan(nodeB, sp))
}

func TestStart_corruptOrUnreadableStore(t *testing.T) {
	cfg := validConfig(t)
	cfg.Store = &fakeStore{loadErr: errors.New("flash read error")}

	n, err := chanmig.New(cfg)
	require.NoError(t, err)
	require.NoError(t, n.Start(t.Context()))
	assert.Equal(t, uint64(0), n.Status().Version)

	cfg = validConfig(t)
	cfg.Store = &fakeStore{records: [][]byte{{0}, {0, 200, 1}, {0, 1, 1}}}

	n, err = chanmig.New(cfg)
	require.NoError(t, err)
	require.NoError(t, n.Start(t.Context()))
	assert.Equal(t, uint64(0), n.Status().Version, "corrupt records are skipped")
	assert.Len(t, n.Status().Operating, 3)
}

func TestNode_sendAndSaveErrorsAreCounted(t *testing.T) {
	cfg := validConfig(t)
	cfg.Transport = &fakeTransport{fail: true}
	cfg.Store = &fakeStore{saveErr: errors.New("flash full")}
	cfg.Signer, _ = chanmig.NewSigner(1, testKey(1))

	n, err := chanmig.New(cfg)
	require.NoError(t, err)
	require.NoError(t, n.Start(t.Context()))

	n.Tick(t.Context())
	assert.Positive(t, n.Status().Stats.SendErrors)

	_, err = n.Propose(t.Context(), []chanmig.Assignment{halow(2)}, lead)
	require.NoError(t, err, "a failed save does not fail the proposal")
	assert.Equal(t, uint64(1), n.Status().Version)
}

func TestRun_stopsOnCancelAndClose(t *testing.T) {
	n, err := chanmig.New(validConfig(t))
	require.NoError(t, err)
	require.NoError(t, n.Start(t.Context()))

	ticks := make(chan time.Time)
	done := make(chan error, 1)

	go func() { done <- n.Run(t.Context(), ticks) }()

	ticks <- planEpoch()

	close(ticks)
	require.NoError(t, <-done)

	ctx, stop := context.WithCancel(t.Context())
	go func() { done <- n.Run(ctx, make(chan time.Time)) }()

	stop()
	require.NoError(t, <-done)
}

func TestPhaseString(t *testing.T) {
	for p, want := range map[chanmig.Phase]string{
		chanmig.PhaseBooting:   "booting",
		chanmig.PhaseStable:    "stable",
		chanmig.PhasePending:   "pending",
		chanmig.PhaseMigrating: "migrating",
		chanmig.PhaseSearching: "searching",
		chanmig.Phase(42):      "unknown",
	} {
		assert.Equal(t, want, p.String())
	}
}

func TestNode_concurrentUse(t *testing.T) {
	s := newMesh(t, nodeB)
	sp := propose(t, s, nodeA, halow(2), wifi24(1), wifi5(36))
	frame := advertPayload(nodeC, sp.Raw, sp.Sig)
	b := s.nodes[nodeB].node

	var wg sync.WaitGroup

	for i := range 4 {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for range 200 {
				switch i % 3 {
				case 0:
					b.Tick(s.t.Context())
				case 1:
					b.HandleMessage(s.t.Context(), chanmig.Band5G, frame)
				default:
					_ = b.Status()
				}
			}
		}()
	}

	wg.Wait()
	assert.Equal(t, sp.Plan.Version, b.Status().Version)
}
