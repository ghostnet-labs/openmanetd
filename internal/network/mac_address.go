package network

import (
	"fmt"
	"hash/fnv"
	"net"

	"github.com/openmanet/openmanetd/internal/database/models"
)

// Point nodes take addresses from 10.41.1.1 to 10.41.252.254: third octet
// 1..252 (0 is for gateways, 253 and 254 are reserved), fourth 1..254.
const (
	macAddrFirstThird = 1
	macAddrThirds     = 252
	macAddrFourths    = 254
	macAddrSlots      = macAddrThirds * macAddrFourths
)

// SelectStaticIPForMAC picks a point node's mesh address from its MAC.
//
// The MAC is hashed to a starting slot in the point range, so every node
// lands on its own address without asking anyone and two nodes that join
// at the same moment almost never pick the same one (the old random and
// first-free picks could). When a peer already advertises that slot the
// search steps to the next one, so the answer also depends only on what
// is visible in the mesh. Gateways, and a MAC that does not parse, keep
// the previous selection.
func SelectStaticIPForMAC(nodes []models.MeshNode, mac string, gatewayMode bool) (string, error) {
	hw, err := net.ParseMAC(mac)
	if gatewayMode || err != nil || len(hw) == 0 {
		return SelectAvailableStaticIPFromNodeData(nodes, gatewayMode)
	}

	reserved := make(map[string]struct{}, len(nodes))
	for _, node := range nodes {
		if node.IpAddr != "" {
			reserved[node.IpAddr] = struct{}{}
		}
	}

	start := macSlot(hw)
	for i := range macAddrSlots {
		ip := slotIP((start + i) % macAddrSlots)
		if _, taken := reserved[ip]; !taken {
			return ip, nil
		}
	}

	return "", fmt.Errorf("no available IP addresses in %s/16 range", DefaultNetworkAddress)
}

// macSlot hashes a MAC to a slot index in [0, macAddrSlots).
func macSlot(hw net.HardwareAddr) int {
	h := fnv.New32a()
	_, _ = h.Write(hw) // hash.Hash.Write never returns an error

	return int(h.Sum32() % macAddrSlots)
}

func slotIP(slot int) string {
	third := macAddrFirstThird + slot/macAddrFourths
	fourth := 1 + slot%macAddrFourths

	return fmt.Sprintf("10.41.%d.%d", third, fourth)
}
