package platform_test

import (
	"context"
	"crypto/ed25519"
	"net"
	"path/filepath"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
)

const testMeshID = 77

func TestParseAssignment(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		want    chanmig.Assignment
		band    chanmig.Band
		wantErr bool
	}{
		{name: "halow", band: chanmig.BandHaLow, in: "28/2", want: chanmig.Assignment{Band: chanmig.BandHaLow, Channel: 28, WidthMHz: 2}},
		{name: "spaces", band: chanmig.Band5G, in: " 36 / 80 ", want: chanmig.Assignment{Band: chanmig.Band5G, Channel: 36, WidthMHz: 80}},
		{name: "max channel", band: chanmig.Band2G4, in: "65535/20", want: chanmig.Assignment{Band: chanmig.Band2G4, Channel: 65535, WidthMHz: 20}},
		{name: "no slash", band: chanmig.Band5G, in: "36", wantErr: true},
		{name: "empty", band: chanmig.Band5G, in: "", wantErr: true},
		{name: "channel zero", band: chanmig.Band5G, in: "0/20", wantErr: true},
		{name: "negative", band: chanmig.Band5G, in: "-1/20", wantErr: true},
		{name: "channel overflow", band: chanmig.Band5G, in: "65536/20", wantErr: true},
		{name: "hex", band: chanmig.Band5G, in: "0x24/20", wantErr: true},
		{name: "width illegal for band", band: chanmig.Band2G4, in: "6/80", wantErr: true},
		{name: "halow width 3", band: chanmig.BandHaLow, in: "28/3", wantErr: true},
		{name: "width text", band: chanmig.Band5G, in: "36/wide", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := platform.ParseAssignment(tc.band, tc.in)
			if tc.wantErr {
				require.Error(t, err)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestBandSpec_BandConfig(t *testing.T) {
	bc, err := platform.BandSpec{
		Band:       chanmig.Band5G,
		Initial:    "36/80",
		Rendezvous: []string{"149/80"},
		Allowed:    []string{"36/80", "52/80", "149/80"},
	}.BandConfig()
	require.NoError(t, err)

	a := func(ch uint16) chanmig.Assignment {
		return chanmig.Assignment{Band: chanmig.Band5G, Channel: ch, WidthMHz: 80}
	}
	assert.Equal(t, chanmig.BandConfig{
		Initial:    a(36),
		Rendezvous: []chanmig.Assignment{a(149)},
		Allowed:    []chanmig.Assignment{a(36), a(52), a(149)},
	}, bc)

	for name, spec := range map[string]platform.BandSpec{
		"bad initial":    {Band: chanmig.Band5G, Initial: "x"},
		"bad rendezvous": {Band: chanmig.Band5G, Initial: "36/80", Rendezvous: []string{"1/7"}},
		"bad allowed":    {Band: chanmig.Band5G, Initial: "36/80", Allowed: []string{"36"}},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := spec.BandConfig()
			require.Error(t, err)
		})
	}
}

func TestNodeIDFromMAC(t *testing.T) {
	id, err := platform.NodeIDFromMAC(net.HardwareAddr{0x02, 0, 0, 0, 0x12, 0x34})
	require.NoError(t, err)
	assert.Equal(t, chanmig.NodeID(0x020000001234), id)

	_, err = platform.NodeIDFromMAC(net.HardwareAddr{1, 2, 3})
	require.Error(t, err)

	_, err = platform.NodeIDFromMAC(make(net.HardwareAddr, 6))
	require.Error(t, err)
}

func TestSystemClock(t *testing.T) {
	before := time.Now()
	assert.False(t, platform.SystemClock{}.Now().Before(before))

	assert.True(t, platform.SystemClock{Probe: func() bool { return true }}.Synced())
	assert.False(t, platform.SystemClock{Probe: func() bool { return false }}.Synced())

	// The kernel probe must not panic in an unprivileged test process.
	_ = platform.SystemClock{}.Synced()
}

// testNode wires a full node from the production adapters (alfred
// transport, file store, UCI radio) over fakes for alfred and UCI.
type testNode struct {
	node      *chanmig.Node
	transport *platform.AlfredTransport
	uci       *fakeUCI
	reloader  *fakeReloader
	store     *platform.FileStore
}

func newTestNode(t *testing.T, a *fakeAlfred, clock chanmig.Clock, mac string, id chanmig.NodeID,
	pub ed25519.PublicKey, priv ed25519.PrivateKey,
) *testNode {
	t.Helper()

	verifier, err := chanmig.NewVerifier(testMeshID, pub)
	require.NoError(t, err)

	var signer *chanmig.Signer

	if priv != nil {
		signer, err = chanmig.NewSigner(id, priv)
		require.NoError(t, err)
	}

	uci := newFakeUCI()
	uci.addDevice("radio0", "s1g", "28", "2 MHz")

	rl := &fakeReloader{}
	transport := platform.NewAlfredTransport(a.client(t, mac), uint32(id), time.Millisecond, zerolog.Nop())
	store := platform.NewFileStore(filepath.Join(t.TempDir(), "plans.bin"))

	bc, err := platform.BandSpec{
		Band: chanmig.BandHaLow, Initial: "28/2", Allowed: []string{"28/2", "30/4", "12/1"},
	}.BandConfig()
	require.NoError(t, err)

	node, err := chanmig.New(chanmig.Config{
		Clock:     clock,
		Transport: transport,
		Radio:     &platform.UCIRadio{Reader: uci, Reload: rl.reload, Log: zerolog.Nop()},
		Store:     store,
		Verifier:  verifier,
		Signer:    signer,
		Log:       zerolog.Nop(),
		Bands:     []chanmig.BandConfig{bc},
		Timing:    platform.AlfredTiming(),
		ID:        id,
	})
	require.NoError(t, err)

	return &testNode{node: node, transport: transport, uci: uci, reloader: rl, store: store}
}

func TestAlfredTiming_isValid(t *testing.T) {
	a := newFakeAlfred()
	pub, _ := genKey(t)
	n := newTestNode(t, a, &stepClock{now: time.Unix(1_700_000_000, 0)}, macA, 1, pub, nil)

	assert.Positive(t, n.node.RecoveryBound())
}

// TestEndToEnd_planMigratesOverAlfred drives two nodes built from the
// production adapters: the coordinator proposes a plan, it crosses the
// alfred transport, both nodes switch the HaLow radio in UCI at the
// activation time and persist the plan to their file stores.
func TestEndToEnd_planMigratesOverAlfred(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	clock := &stepClock{now: time.Unix(1_700_000_000, 0), synced: true}
	pub, priv := genKey(t)

	coord := newTestNode(t, a, clock, macA, 1, pub, priv)
	member := newTestNode(t, a, clock, macB, 2, pub, nil)
	nodes := []*testNode{coord, member}

	for _, n := range nodes {
		require.NoError(t, n.node.Start(ctx))
		assert.Equal(t, "28", n.uci.option("radio0", "channel"))
		assert.Zero(t, n.reloader.count(), "booting on the configured channel does not reload")
	}

	step := func(d time.Duration) {
		clock.advance(d)

		for _, n := range nodes {
			n.node.Tick(ctx)
		}

		for _, n := range nodes {
			n.transport.PollOnce(ctx, n.node.HandleMessage)
		}
	}

	// Let both sides see each other's adverts.
	for range 4 {
		step(5 * time.Second)
	}

	sp, err := coord.node.Propose(ctx, []chanmig.Assignment{{Band: chanmig.BandHaLow, Channel: 30, WidthMHz: 4}}, 2*time.Minute)
	require.NoError(t, err)

	for range 6 {
		step(5 * time.Second)
	}

	assert.Equal(t, sp.Plan.Version, member.node.Status().Version, "the member accepted the plan over alfred")
	assert.Equal(t, "28", member.uci.option("radio0", "channel"), "nothing moves before activation")

	for range 40 {
		step(5 * time.Second)
	}

	for i, n := range nodes {
		assert.Equal(t, "30", n.uci.option("radio0", "channel"), "node %d", i)
		assert.Equal(t, "4 MHz", n.uci.option("radio0", "htmode"), "node %d", i)
		assert.Equal(t, 1, n.reloader.count(), "node %d reloads exactly once", i)
		assert.Equal(t, chanmig.PhaseStable, n.node.Status().Phase, "node %d", i)

		records, err := n.store.Load(ctx)
		require.NoError(t, err)
		require.NotEmpty(t, records, "node %d persisted the plan", i)
	}
}

func TestService_runAndStop(t *testing.T) {
	a := newFakeAlfred()
	pub, _ := genKey(t)
	n := newTestNode(t, a, platform.SystemClock{Probe: func() bool { return false }}, macA, 1, pub, nil)

	svc := platform.NewService(n.node, n.transport, time.Millisecond, zerolog.Nop())
	assert.Same(t, n.node, svc.Node())

	ctx, cancel := context.WithCancel(context.Background())
	go svc.Run(ctx)

	t.Cleanup(func() {
		cancel()
		<-svc.Done()
	})

	// The node starts publishing adverts through alfred.
	require.Eventually(t, func() bool { return len(a.record(macA)) > 0 }, 5*time.Second, time.Millisecond)

	cancel()

	select {
	case <-svc.Done():
	case <-time.After(5 * time.Second):
		require.Fail(t, "service did not stop")
	}
}
