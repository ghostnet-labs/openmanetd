package network

import (
	"database/sql"
	"net"
	"testing"

	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSelectStaticIPForMAC_SameMACSameAddress(t *testing.T) {
	a, err := SelectStaticIPForMAC(nil, "52:54:00:aa:01:01", false)
	require.NoError(t, err)
	b, err := SelectStaticIPForMAC(nil, "52:54:00:AA:01:01", false)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestSelectStaticIPForMAC_InPointRange(t *testing.T) {
	for _, mac := range []string{"52:54:00:aa:01:01", "52:54:00:aa:02:01", "00:00:00:00:00:00", "ff:ff:ff:ff:ff:ff"} {
		got, err := SelectStaticIPForMAC(nil, mac, false)
		require.NoError(t, err)

		ip := net.ParseIP(got).To4()
		require.NotNil(t, ip, got)
		assert.Equal(t, byte(10), ip[0])
		assert.Equal(t, byte(41), ip[1])
		assert.True(t, ip[2] >= 1 && ip[2] <= 252, got)
		assert.True(t, ip[3] >= 1 && ip[3] <= 254, got)
	}
}

func TestSelectStaticIPForMAC_NeighboursDiffer(t *testing.T) {
	// Nodes that boot together have near-identical MACs; they must not
	// land on the same address.
	seen := make(map[string]string, 64)

	for i := range 64 {
		mac := net.HardwareAddr{0x52, 0x54, 0x00, 0xaa, byte(i), 0x01}.String()
		got, err := SelectStaticIPForMAC(nil, mac, false)
		require.NoError(t, err)

		_, dup := seen[got]
		assert.False(t, dup, "%s and %s both got %s", seen[got], mac, got)
		seen[got] = mac
	}
}

func TestSelectStaticIPForMAC_StepsPastTakenAddress(t *testing.T) {
	mac := "52:54:00:aa:01:01"
	first, err := SelectStaticIPForMAC(nil, mac, false)
	require.NoError(t, err)

	got, err := SelectStaticIPForMAC([]models.MeshNode{{IpAddr: first}}, mac, false)
	require.NoError(t, err)
	assert.NotEqual(t, first, got)

	hw, err := net.ParseMAC(mac)
	require.NoError(t, err)
	assert.Equal(t, slotIP((macSlot(hw, nil)+1)%macAddrSlots), got)
}

func TestSelectStaticIPForMAC_WrapsAtEndOfRange(t *testing.T) {
	assert.Equal(t, "10.41.252.254", slotIP(macAddrSlots-1))
	assert.Equal(t, "10.41.1.1", slotIP(0))
	assert.Equal(t, "10.41.2.1", slotIP(macAddrFourths))
}

func TestSelectStaticIPForMAC_GatewayKeepsSequentialPick(t *testing.T) {
	got, err := SelectStaticIPForMAC([]models.MeshNode{{IpAddr: "10.41.0.1"}}, "52:54:00:aa:01:01", true)
	require.NoError(t, err)
	assert.Equal(t, "10.41.0.2", got)
}

func TestSelectStaticIPForMAC_BadMACFallsBack(t *testing.T) {
	for _, mac := range []string{"", "not-a-mac"} {
		got, err := SelectStaticIPForMAC(nil, mac, false)
		require.NoError(t, err)

		ip := net.ParseIP(got).To4()
		require.NotNil(t, ip, got)
		assert.True(t, ip[2] >= 1 && ip[2] <= 252, got)
	}
}

func dhcpPeer(ip string, start, limit int64) models.MeshNode {
	return models.MeshNode{
		IpAddr:       ip,
		UciDhcpStart: sql.NullInt64{Int64: start, Valid: true},
		UciDhcpLimit: sql.NullInt64{Int64: limit, Valid: true},
	}
}

func poolFirstIP(start int) string {
	return net.IPv4(10, 41, byte(start/256), byte(start%256)).String()
}

func TestSelectStaticIPForMAC_SkipsPeerDHCPPool(t *testing.T) {
	mac := "52:54:00:aa:01:01"
	want, err := SelectStaticIPForMAC(nil, mac, false)
	require.NoError(t, err)

	off, ok := ipOffset(want)
	require.True(t, ok)

	got, err := SelectStaticIPForMAC([]models.MeshNode{dhcpPeer("10.41.0.9", int64(off), 16)}, mac, false)
	require.NoError(t, err)
	assert.NotEqual(t, want, got, "the address is in a peer's DHCP pool")
}

func TestSelectDHCPStartForMAC_SameMACSamePool(t *testing.T) {
	a, err := SelectDHCPStartForMAC(nil, "52:54:00:aa:01:01", "", 16)
	require.NoError(t, err)
	b, err := SelectDHCPStartForMAC(nil, "52:54:00:AA:01:01", "", 16)
	require.NoError(t, err)
	assert.Equal(t, a, b)
}

func TestSelectDHCPStartForMAC_PoolStaysInsideOneThirdOctet(t *testing.T) {
	for i := range 256 {
		mac := net.HardwareAddr{0x52, 0x54, 0x00, 0xaa, byte(i), 0x01}.String()
		start, err := SelectDHCPStartForMAC(nil, mac, "", 16)
		require.NoError(t, err)

		third, first, last := start/256, start%256, start%256+15
		assert.True(t, third >= 1 && third <= 252, "pool %s in third octet %d", poolFirstIP(start), third)
		assert.True(t, first >= 1 && last <= 254, "pool %s crosses .0 or .255", poolFirstIP(start))
	}
}

func TestSelectDHCPStartForMAC_NeighboursGetSeparatePools(t *testing.T) {
	// 3,780 pools: a handful of nodes switched on together should not share
	// one (a few dozen would, by the birthday bound, and then the gossip
	// steers the later ones apart).
	var nodes []models.MeshNode

	for i := range 8 {
		mac := net.HardwareAddr{0x52, 0x54, 0x00, 0xaa, byte(i), 0x01}.String()
		// As two nodes joining together would: neither sees the other yet.
		start, err := SelectDHCPStartForMAC(nil, mac, "", 16)
		require.NoError(t, err)

		for _, n := range nodes {
			s := int(n.UciDhcpStart.Int64)
			assert.False(t, rangesOverlap(start, start+15, s, s+15), "%s pool overlaps another node's", mac)
		}

		nodes = append(nodes, dhcpPeer("", int64(start), 16))
	}
}

func TestSelectDHCPStartForMAC_AvoidsPeersAndOwnAddress(t *testing.T) {
	mac := "52:54:00:aa:01:01"
	first, err := SelectDHCPStartForMAC(nil, mac, "", 16)
	require.NoError(t, err)

	inPool := poolFirstIP(first + 3)

	for name, tc := range map[string]struct {
		nodes []models.MeshNode
		own   string
	}{
		"peer pool":    {nodes: []models.MeshNode{dhcpPeer("", int64(first+8), 16)}},
		"peer address": {nodes: []models.MeshNode{{IpAddr: inPool}}},
		"own address":  {own: inPool},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := SelectDHCPStartForMAC(tc.nodes, mac, tc.own, 16)
			require.NoError(t, err)
			assert.NotEqual(t, first, got)
		})
	}
}

func TestSelectDHCPStartForMAC_FallsBack(t *testing.T) {
	got, err := SelectDHCPStartForMAC(nil, "not-a-mac", "", 16)
	require.NoError(t, err)
	assert.Equal(t, 100, got, "a MAC that does not parse keeps the first-free pick")

	got, err = SelectDHCPStartForMAC(nil, "52:54:00:aa:01:01", "", 300)
	require.NoError(t, err)
	assert.Equal(t, 100, got, "a pool wider than a /24 keeps the first-free pick")
}
