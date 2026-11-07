package batmanadv_test

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	batmanadv "github.com/openmanet/openmanetd/internal/batman-adv"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureDir walks up from this test file to the repo's testfixtures/batman-adv
// directory so fixture paths survive `go test ./...` no matter the cwd.
func fixtureDir() string {
	_, filename, _, _ := runtime.Caller(0)

	return filepath.Join(filepath.Dir(filename), "..", "..", "testfixtures", "batman-adv")
}

// fakeOrigProvider scripts a single GetOriginators response for tests.
type fakeOrigProvider struct {
	rows []batmanadv.Originator
	err  error
}

func (f *fakeOrigProvider) GetOriginators() ([]batmanadv.Originator, error) {
	return f.rows, f.err
}

// directRF + multiHopRF + directBLOS1 + multiHopBLOS1 + directBLOS2 scenario,
// matching testfixtures/batman-adv/originators.json but constructed inline so
// the test is self-contained.
func fiveEntrySnapshot() []batmanadv.Originator {
	return []batmanadv.Originator{
		// Direct RF neighbor (neigh_address == orig_address)
		{OrigAddress: "9c:ef:d5:f9:9e:02", HardIfname: "phy2-mesh0", BestNeigh: "9c:ef:d5:f9:9e:02", LastSeenMs: 120, TQ: 255, Best: true},
		// Non-best duplicate of the same originator — must be filtered out.
		{OrigAddress: "9c:ef:d5:f9:9e:02", HardIfname: "phy2-mesh0", BestNeigh: "9c:ef:d5:f9:9e:02", LastSeenMs: 120, TQ: 180, Best: false},
		// Multi-hop RF peer routed via the direct neighbor.
		{OrigAddress: "00:0a:52:0b:7d:ae", HardIfname: "phy2-mesh0", BestNeigh: "9c:ef:d5:f9:9e:02", LastSeenMs: 240, TQ: 210, Best: true},
		// Direct BLOS neighbor — first remote-segment gateway.
		{OrigAddress: "2c:cf:67:b8:88:bb", HardIfname: "vxlan0", BestNeigh: "2c:cf:67:b8:88:bb", LastSeenMs: 400, TQ: 230, Best: true},
		// Multi-hop BLOS peer routed via the first BLOS gateway.
		{OrigAddress: "aa:bb:cc:dd:01:01", HardIfname: "vxlan0", BestNeigh: "2c:cf:67:b8:88:bb", LastSeenMs: 450, TQ: 190, Best: true},
		// Second direct BLOS neighbor — distinct next hop ⇒ distinct remote segment.
		{OrigAddress: "aa:bb:cc:dd:02:02", HardIfname: "vxlan0", BestNeigh: "aa:bb:cc:dd:02:02", LastSeenMs: 500, TQ: 200, Best: true},
	}
}

func newProvider(rows []batmanadv.Originator, selfMAC string) *batmanadv.BatctlOriginatorTopologyProvider {
	return &batmanadv.BatctlOriginatorTopologyProvider{
		Originators:   &fakeOrigProvider{rows: rows},
		BatHostsPath:  filepath.Join(fixtureDir(), "bat-hosts"),
		SelfMACReader: func() (string, error) { return selfMAC, nil },
	}
}

// TestGetOriginatorTopology_FiltersNonBest ensures the provider drops every
// row where Best == false so the handler never sees non-forwarding entries.
func TestGetOriginatorTopology_FiltersNonBest(t *testing.T) {
	p := newProvider(fiveEntrySnapshot(), "0a:d7:37:78:2d:3e")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	require.NotNil(t, snap)
	// Five Best=true rows out of six; the non-best duplicate must be gone.
	assert.Len(t, snap.Originators, 5)
}

// TestGetOriginatorTopology_HostnameEnrichment confirms that bat-hosts
// entries round-trip onto both the originator and next-hop fields.
func TestGetOriginatorTopology_HostnameEnrichment(t *testing.T) {
	p := newProvider(fiveEntrySnapshot(), "0a:d7:37:78:2d:3e")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)

	byMAC := map[string]batmanadv.OriginatorEntry{}
	for _, e := range snap.Originators {
		byMAC[e.OrigMAC] = e
	}

	direct := byMAC["9c:ef:d5:f9:9e:02"]
	assert.Equal(t, "BCM2711-88ba_phy2-mesh0", direct.OrigHostname)
	assert.Equal(t, "BCM2711-88ba_phy2-mesh0", direct.NextHopHostname, "direct neighbor: orig == next hop")

	multiHopRF := byMAC["00:0a:52:0b:7d:ae"]
	assert.Equal(t, "BCM2711-1003_phy1-mesh0", multiHopRF.OrigHostname)
	assert.Equal(t, "BCM2711-88ba_phy2-mesh0", multiHopRF.NextHopHostname)

	gw1 := byMAC["2c:cf:67:b8:88:bb"]
	assert.Equal(t, "BLOS-GW1_vxlan0", gw1.OrigHostname)

	multiHopBLOS := byMAC["aa:bb:cc:dd:01:01"]
	assert.Equal(t, "Remote-Node1_vxlan0", multiHopBLOS.OrigHostname)
	assert.Equal(t, "BLOS-GW1_vxlan0", multiHopBLOS.NextHopHostname)

	// Self-hostname is derived from bat-hosts after stripping "_bat0".
	assert.Equal(t, "BCM2711-97d6", snap.SelfHostname)
	assert.Equal(t, "0a:d7:37:78:2d:3e", snap.SelfMAC)
}

// TestGetOriginatorTopology_HopDerivation checks that chain-walking produces
// the right hop counts: direct neighbors at 1 hop, multi-hop peers at 2+.
func TestGetOriginatorTopology_HopDerivation(t *testing.T) {
	p := newProvider(fiveEntrySnapshot(), "0a:d7:37:78:2d:3e")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)

	byMAC := map[string]batmanadv.OriginatorEntry{}
	for _, e := range snap.Originators {
		byMAC[e.OrigMAC] = e
	}

	assert.Equal(t, 1, byMAC["9c:ef:d5:f9:9e:02"].Hops, "direct RF neighbor")
	assert.Equal(t, 2, byMAC["00:0a:52:0b:7d:ae"].Hops, "multi-hop via direct neighbor")
	assert.Equal(t, 1, byMAC["2c:cf:67:b8:88:bb"].Hops, "direct BLOS neighbor")
	assert.Equal(t, 2, byMAC["aa:bb:cc:dd:01:01"].Hops, "multi-hop via BLOS gateway")
	assert.Equal(t, 1, byMAC["aa:bb:cc:dd:02:02"].Hops, "second direct BLOS neighbor")
}

// TestGetOriginatorTopology_AlgorithmBATMANIV labels the snapshot correctly
// when all rows carry TQ but no throughput.
func TestGetOriginatorTopology_AlgorithmBATMANIV(t *testing.T) {
	p := newProvider(fiveEntrySnapshot(), "0a:d7:37:78:2d:3e")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	assert.Equal(t, "BATMAN_IV", snap.Algorithm)
}

// TestGetOriginatorTopology_AlgorithmBATMANV flips the label when throughput
// values are present (the BATMAN_V case operators see on newer kernels).
func TestGetOriginatorTopology_AlgorithmBATMANV(t *testing.T) {
	rows := []batmanadv.Originator{
		{OrigAddress: "aa:bb:cc:dd:ee:01", HardIfname: "wlh0", BestNeigh: "aa:bb:cc:dd:ee:01", Throughput: 150000, Best: true},
	}
	p := newProvider(rows, "0a:d7:37:78:2d:3e")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	assert.Equal(t, "BATMAN_V", snap.Algorithm)
}

// TestGetOriginatorTopology_EmptySnapshot handles an empty mesh gracefully —
// no originators, no self-identity failure, no panic.
func TestGetOriginatorTopology_EmptySnapshot(t *testing.T) {
	p := newProvider(nil, "")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	assert.Empty(t, snap.Originators)
	assert.Equal(t, "", snap.Algorithm)
}

// TestGetOriginatorTopology_BatHostsMissing degrades hostnames to empty
// strings rather than failing the RPC when /tmp/bat-hosts can't be read.
func TestGetOriginatorTopology_BatHostsMissing(t *testing.T) {
	p := &batmanadv.BatctlOriginatorTopologyProvider{
		Originators:   &fakeOrigProvider{rows: fiveEntrySnapshot()},
		BatHostsPath:  filepath.Join(fixtureDir(), "does-not-exist"),
		SelfMACReader: func() (string, error) { return "0a:d7:37:78:2d:3e", nil },
	}

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err, "missing bat-hosts must not fail the provider")

	for _, e := range snap.Originators {
		assert.Empty(t, e.OrigHostname)
		assert.Empty(t, e.NextHopHostname)
	}

	assert.Empty(t, snap.SelfHostname)
	assert.Equal(t, "0a:d7:37:78:2d:3e", snap.SelfMAC, "self MAC is independent of bat-hosts")
}

// TestGetOriginatorTopology_SelfMACFailure tolerates netlink errors — the RPC
// still succeeds with an empty self-identity (frontend renders a fallback).
func TestGetOriginatorTopology_SelfMACFailure(t *testing.T) {
	p := &batmanadv.BatctlOriginatorTopologyProvider{
		Originators:   &fakeOrigProvider{rows: fiveEntrySnapshot()},
		BatHostsPath:  filepath.Join(fixtureDir(), "bat-hosts"),
		SelfMACReader: func() (string, error) { return "", errors.New("bat0 missing") },
	}

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	assert.Empty(t, snap.SelfMAC)
	assert.Empty(t, snap.SelfHostname)
}

// TestGetOriginatorTopology_ProviderError propagates non-exec errors so the
// handler can map them to connect.CodeInternal rather than the user-facing
// "unavailable" state.
func TestGetOriginatorTopology_ProviderError(t *testing.T) {
	p := &batmanadv.BatctlOriginatorTopologyProvider{
		Originators: &fakeOrigProvider{err: errors.New("corrupt JSON")},
	}

	_, err := p.GetOriginatorTopology()
	require.Error(t, err)
	assert.False(t, errors.Is(err, batmanadv.ErrOriginatorsUnavailable))
}

// TestGetOriginatorTopology_CycleSafety caps pathological chains rather than
// looping forever if the originator table ever reports a cycle.
func TestGetOriginatorTopology_CycleSafety(t *testing.T) {
	rows := []batmanadv.Originator{
		{OrigAddress: "aa:01", HardIfname: "wlh0", BestNeigh: "aa:02", TQ: 200, Best: true},
		{OrigAddress: "aa:02", HardIfname: "wlh0", BestNeigh: "aa:01", TQ: 200, Best: true},
	}
	p := newProvider(rows, "")

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	// Both should be marked with the unknown-hops sentinel.
	for _, e := range snap.Originators {
		assert.Equal(t, 99, e.Hops, "cyclic chains must cap at the unknown sentinel")
	}
}

// TestGetOriginatorTopology_MultiInterfaceNeighbor pins the hop count when
// the neighbor's link MAC is not its originator address: node2 has two
// mesh interfaces, its originator is its eth2 MAC, and node1 reaches it
// (and node3 behind it) through its eth1 MAC. Taken from the firmware QEMU
// mesh test, which saw both peers at the unknown sentinel.
func TestGetOriginatorTopology_MultiInterfaceNeighbor(t *testing.T) {
	dir := t.TempDir()
	hosts := filepath.Join(dir, "bat-hosts")
	require.NoError(t, os.WriteFile(hosts, []byte(
		"# Node 52:54:00:aa:02:02\n52:54:00:aa:02:01 node2_eth1\n52:54:00:aa:02:02 node2_eth2\n"+
			"# Node 52:54:00:aa:03:01\n52:54:00:aa:03:01 node3_eth1\n"), 0o600))

	rows := []batmanadv.Originator{
		{OrigAddress: "52:54:00:aa:02:02", HardIfname: "eth1", BestNeigh: "52:54:00:aa:02:01", Throughput: 1000, Best: true},
		{OrigAddress: "52:54:00:aa:03:01", HardIfname: "eth1", BestNeigh: "52:54:00:aa:02:01", Throughput: 800, Best: true},
	}
	p := &batmanadv.BatctlOriginatorTopologyProvider{
		Originators:   &fakeOrigProvider{rows: rows},
		BatHostsPath:  hosts,
		SelfMACReader: func() (string, error) { return "b6:93:05:a5:b3:d6", nil },
	}

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)

	hops := make(map[string]int, len(snap.Originators))
	for _, e := range snap.Originators {
		hops[e.OrigMAC] = e.Hops
	}

	assert.Equal(t, 1, hops["52:54:00:aa:02:02"], "node2 is a direct neighbor through its eth1")
	assert.Equal(t, 2, hops["52:54:00:aa:03:01"], "node3 is routed through node2")
}

// Without bat-hosts the owner of a link MAC is unknown, so the chain stays
// unknown rather than guessing.
func TestGetOriginatorTopology_MultiInterfaceNeighborNoBatHosts(t *testing.T) {
	rows := []batmanadv.Originator{
		{OrigAddress: "52:54:00:aa:02:02", HardIfname: "eth1", BestNeigh: "52:54:00:aa:02:01", Throughput: 1000, Best: true},
	}
	p := &batmanadv.BatctlOriginatorTopologyProvider{
		Originators:   &fakeOrigProvider{rows: rows},
		BatHostsPath:  filepath.Join(t.TempDir(), "none"),
		SelfMACReader: func() (string, error) { return "", nil },
	}

	snap, err := p.GetOriginatorTopology()
	require.NoError(t, err)
	require.Len(t, snap.Originators, 1)
	assert.Equal(t, 99, snap.Originators[0].Hops)
}
