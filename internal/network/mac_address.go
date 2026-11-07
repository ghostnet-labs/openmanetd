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
// is visible in the mesh. Addresses inside a peer's DHCP pool are passed
// over too, so a node never takes an address a peer may lease to a phone. Gateways, and a MAC that does not parse, keep
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

	pools := peerDHCPRanges(nodes)

	start := macSlot(hw, nil)
	for i := range macAddrSlots {
		slot := (start + i) % macAddrSlots
		ip := slotIP(slot)

		if _, taken := reserved[ip]; taken || inRanges(slotOffset(slot), pools) {
			continue
		}

		return ip, nil
	}

	return "", fmt.Errorf("no available IP addresses in %s/16 range", DefaultNetworkAddress)
}

// dhcpSalt keeps a node's DHCP pool from hashing to the block that holds
// its own address.
var dhcpSalt = []byte("dhcp") //nolint:gochecknoglobals // read-only hash salt

// SelectDHCPStartForMAC picks where a node's DHCP pool (the addresses it
// hands to phones) starts, as an offset from 10.41.0.0, the form dnsmasq's
// `start` option takes.
//
// Pools are blocks of `limit` addresses inside the point range, never
// crossing a /24's .0 or .255. The MAC is hashed to a starting block, and
// blocks that overlap a peer's pool, a peer's address or ownIP are passed
// over, so two nodes that join at the same moment almost never hand
// phones the same addresses (the old first-free pick always did). A MAC
// that does not parse, or a pool wider than a /24, keeps the previous pick.
func SelectDHCPStartForMAC(nodes []models.MeshNode, mac, ownIP string, limit int) (int, error) {
	hw, err := net.ParseMAC(mac)

	perThird := 0
	if limit > 0 {
		perThird = macAddrFourths / limit
	}

	if err != nil || len(hw) == 0 || perThird == 0 {
		return CalculateAvailableDHCPStart(nodes, DefaultNetworkAddress, DefaultNetworkMask, limit)
	}

	pools := peerDHCPRanges(nodes)

	taken := make([]int, 0, len(nodes)+1)
	for _, node := range nodes {
		if off, ok := ipOffset(node.IpAddr); ok {
			taken = append(taken, off)
		}
	}

	if off, ok := ipOffset(ownIP); ok {
		taken = append(taken, off)
	}

	blocks := macAddrThirds * perThird
	start := macSlot(hw, dhcpSalt) % blocks

	for i := range blocks {
		b := (start + i) % blocks
		first := (macAddrFirstThird+b/perThird)*256 + 1 + (b%perThird)*limit
		last := first + limit - 1

		if poolBlocked(first, last, pools, taken) {
			continue
		}

		return first, nil
	}

	return 0, fmt.Errorf("no free %d-address DHCP pool in %s/16", limit, DefaultNetworkAddress)
}

func poolBlocked(first, last int, pools []DHCPRange, taken []int) bool {
	for _, r := range pools {
		if rangesOverlap(first, last, r.Start, r.End) {
			return true
		}
	}

	for _, off := range taken {
		if off >= first && off <= last {
			return true
		}
	}

	return false
}

// peerDHCPRanges returns the DHCP pools the peer rows advertise.
func peerDHCPRanges(nodes []models.MeshNode) []DHCPRange {
	ranges := make([]DHCPRange, 0, len(nodes))

	for _, node := range nodes {
		if !node.UciDhcpStart.Valid || !node.UciDhcpLimit.Valid {
			continue
		}

		start, limit := int(node.UciDhcpStart.Int64), int(node.UciDhcpLimit.Int64)
		if start > 0 && limit > 0 {
			ranges = append(ranges, DHCPRange{Start: start, End: start + limit - 1})
		}
	}

	return ranges
}

func inRanges(off int, ranges []DHCPRange) bool {
	for _, r := range ranges {
		if off >= r.Start && off <= r.End {
			return true
		}
	}

	return false
}

// ipOffset is an address's offset from 10.41.0.0, if it lies in 10.41.0.0/16.
func ipOffset(addr string) (int, bool) {
	ip := net.ParseIP(addr).To4()
	if ip == nil || ip[0] != 10 || ip[1] != 41 {
		return 0, false
	}

	return int(ip[2])*256 + int(ip[3]), true
}

// macSlot hashes a MAC (and an optional salt) to a slot index in
// [0, macAddrSlots).
func macSlot(hw net.HardwareAddr, salt []byte) int {
	h := fnv.New32a()
	_, _ = h.Write(hw)   // hash.Hash.Write never returns an error
	_, _ = h.Write(salt) // hash.Hash.Write never returns an error

	return int(h.Sum32() % macAddrSlots)
}

func slotThirdFourth(slot int) (int, int) {
	return macAddrFirstThird + slot/macAddrFourths, 1 + slot%macAddrFourths
}

func slotIP(slot int) string {
	third, fourth := slotThirdFourth(slot)

	return fmt.Sprintf("10.41.%d.%d", third, fourth)
}

func slotOffset(slot int) int {
	third, fourth := slotThirdFourth(slot)

	return third*256 + fourth
}
