// Package cotmcast sends Cursor-on-Target datagrams to the ATAK
// Situational Awareness multicast group (239.2.3.1:6969) with the egress
// interface pinned explicitly.
//
// Pinning matters on a mesh node: an unpinned send to 239.2.3.1 follows
// the unicast routing table, so a node with no uplink and no batman-adv
// gateway has no default route and the send fails with "network is
// unreachable", while a node with a WAN uplink sends the datagram out the
// WAN instead of the mesh bridge. The mesh bridge (br-ahwlan) carries the
// datagram to every node, because batman-adv floods multicast and every
// AP client is bridged onto the same broadcast domain, so ATAK on any
// phone on the mesh receives it.
package cotmcast

import (
	"errors"
	"fmt"
	"net"

	"golang.org/x/net/ipv4"
)

const (
	// SAAddress is the ATAK Situational Awareness multicast group. ATAK
	// listens on it out of the box ("Default SA Multicast" input).
	SAAddress = "239.2.3.1"
	// SAPort is the ATAK SA multicast UDP port.
	SAPort = 6969
	// TTL is the IP TTL on every datagram. The mesh is one L2 domain so a
	// TTL of 1 would do; 64 keeps the datagram alive across a routed hop
	// (BLOS overlays) without relying on that.
	TTL = 64
)

// Sender sends datagrams to the ATAK SA group. The zero value sends on
// the default mesh bridge with the real network stack; the function
// fields exist for tests. A Sender is safe for concurrent use as long as
// its fields are not changed after first use.
type Sender struct {
	// MeshInterface returns the preferred egress interface name. Nil or
	// an empty result means "br-ahwlan".
	MeshInterface func() string
	// InterfaceByName, Interfaces and Write override the network stack.
	InterfaceByName func(string) (*net.Interface, error)
	Interfaces      func() ([]net.Interface, error)
	Write           func(ifi *net.Interface, dst *net.UDPAddr, data []byte) error
}

// DefaultMeshInterface is the bridge that carries the mesh and every AP
// client on an OpenMANET node.
const DefaultMeshInterface = "br-ahwlan"

// Send writes data to the ATAK SA group. The mesh interface is used when
// it is up; otherwise the datagram goes out every up, multicast-capable,
// non-loopback interface. It succeeds if at least one interface accepted
// the datagram.
func (s *Sender) Send(data []byte) error {
	byName := s.InterfaceByName
	if byName == nil {
		byName = net.InterfaceByName
	}

	list := s.Interfaces
	if list == nil {
		list = net.Interfaces
	}

	write := s.Write
	if write == nil {
		write = WriteOn
	}

	mesh := DefaultMeshInterface

	if s.MeshInterface != nil {
		if name := s.MeshInterface(); name != "" {
			mesh = name
		}
	}

	candidates := SelectInterfaces(mesh, byName, list)
	if len(candidates) == 0 {
		return errors.New("no multicast-capable interface available for CoT send")
	}

	dst := &net.UDPAddr{IP: net.IPv4(239, 2, 3, 1), Port: SAPort}

	var (
		errs []error
		sent bool
	)

	for i := range candidates {
		if err := write(&candidates[i], dst, data); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", candidates[i].Name, err))

			continue
		}

		sent = true
	}

	if !sent {
		return errors.Join(errs...)
	}

	return nil
}

// SelectInterfaces picks the interfaces a CoT multicast datagram is sent
// on. The named mesh interface wins when it resolves and is up; otherwise
// every up, multicast-capable, non-loopback interface is a candidate. An
// empty result means no usable interface exists.
func SelectInterfaces(
	meshName string,
	byName func(string) (*net.Interface, error),
	list func() ([]net.Interface, error),
) []net.Interface {
	if ifi, err := byName(meshName); err == nil && Capable(ifi) {
		return []net.Interface{*ifi}
	}

	ifaces, err := list()
	if err != nil {
		return nil
	}

	out := make([]net.Interface, 0, len(ifaces))

	for i := range ifaces {
		if Capable(&ifaces[i]) {
			out = append(out, ifaces[i])
		}
	}

	return out
}

// Capable reports whether ifi is up, multicast-capable and not loopback.
func Capable(ifi *net.Interface) bool {
	return ifi.Flags&net.FlagUp != 0 &&
		ifi.Flags&net.FlagMulticast != 0 &&
		ifi.Flags&net.FlagLoopback == 0
}

// WriteOn sends data to dst with the socket's multicast egress forced to
// ifi.
func WriteOn(ifi *net.Interface, dst *net.UDPAddr, data []byte) error {
	conn, err := net.ListenUDP("udp4", nil)
	if err != nil {
		return fmt.Errorf("open multicast socket: %w", err)
	}
	defer conn.Close()

	pconn := ipv4.NewPacketConn(conn)

	if err := pconn.SetMulticastInterface(ifi); err != nil {
		return fmt.Errorf("set multicast interface: %w", err)
	}

	if err := pconn.SetMulticastTTL(TTL); err != nil {
		return fmt.Errorf("set multicast TTL: %w", err)
	}

	if _, err := pconn.WriteTo(data, nil, dst); err != nil {
		return fmt.Errorf("write: %w", err)
	}

	return nil
}
