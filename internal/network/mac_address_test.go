package network

import (
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
	assert.Equal(t, slotIP((macSlot(hw)+1)%macAddrSlots), got)
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
