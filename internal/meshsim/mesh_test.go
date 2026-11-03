//go:build meshsim

package meshsim_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	batmanadv "github.com/openmanet/openmanetd/internal/batman-adv"
	"github.com/openmanet/openmanetd/internal/meshsim"
)

// Deadlines. Convergence with the shortened timers takes a few seconds;
// a silent peer is only dropped when batman-adv's purge timeout (200 s,
// fixed in the kernel) expires, so fade and power-off cases wait longer.
const (
	convergeTimeout = 45 * time.Second
	purgeTimeout    = 260 * time.Second
)

// forEachAlgo runs fn once per routing algorithm. OpenMANET configures
// BATMAN_V; BATMAN_IV is batman-adv's default and still in the field.
func forEachAlgo(t *testing.T, fn func(t *testing.T, m *meshsim.Mesh)) {
	t.Helper()
	meshsim.Require(t)

	for _, algo := range []string{meshsim.AlgoV, meshsim.AlgoIV} {
		t.Run(algo, func(t *testing.T) {
			t.Parallel()
			fn(t, meshsim.New(t, algo))
		})
	}
}

// topology reads node's originator table through openmanetd's production
// provider (batctl oj + bat-hosts + bat0 MAC), from inside node's namespace.
func topology(m *meshsim.Mesh, node string) (*batmanadv.OriginatorTopology, error) {
	var topo *batmanadv.OriginatorTopology

	err := m.Do(node, func() error {
		p := &batmanadv.BatctlOriginatorTopologyProvider{BatHostsPath: m.BatHostsPath()}

		var err error

		topo, err = p.GetOriginatorTopology()

		return err
	})

	return topo, err
}

func entryFor(topo *batmanadv.OriginatorTopology, mac string) *batmanadv.OriginatorEntry {
	for i := range topo.Originators {
		if strings.EqualFold(topo.Originators[i].OrigMAC, mac) {
			return &topo.Originators[i]
		}
	}

	return nil
}

// routeVia returns a condition that holds once from's best route to dest
// goes through via ("" accepts any next hop).
func routeVia(m *meshsim.Mesh, from, dest, via string) func() error {
	return func() error {
		topo, err := topology(m, from)
		if err != nil {
			return err
		}

		e := entryFor(topo, m.Node(dest).MAC)
		if e == nil {
			return fmt.Errorf("%s has no route to %s", from, dest)
		}

		if via != "" && !strings.EqualFold(e.NextHopMAC, m.Node(via).MAC) {
			return fmt.Errorf("%s reaches %s via %s (%s), want %s", from, dest, e.NextHopMAC, e.NextHopHostname, via)
		}

		return nil
	}
}

// noRoute holds once from's originator table no longer lists dest.
func noRoute(m *meshsim.Mesh, from, destMAC string) func() error {
	return func() error {
		topo, err := topology(m, from)
		if err != nil {
			return err
		}

		if e := entryFor(topo, destMAC); e != nil {
			return fmt.Errorf("%s still routes to %s via %s (last seen %d ms ago)", from, destMAC, e.NextHopMAC, e.LastSeenMs)
		}

		return nil
	}
}

func originatorCount(m *meshsim.Mesh, node string, want int) func() error {
	return func() error {
		topo, err := topology(m, node)
		if err != nil {
			return err
		}

		if got := len(topo.Originators); got != want {
			return fmt.Errorf("%s sees %d originators, want %d", node, got, want)
		}

		return nil
	}
}

func neighbors(m *meshsim.Mesh, node string) (batmanadv.Neighbors, error) {
	var out batmanadv.Neighbors

	err := m.Do(node, func() error {
		n, err := batmanadv.GetMeshNeighbors()
		if err != nil {
			return fmt.Errorf("batctl nj: %w", err)
		}

		out = *n

		return nil
	})

	return out, err
}

// hasNeighbor holds when node's one-hop table does (want=true) or does not
// list peer.
func hasNeighbor(m *meshsim.Mesh, node, peerMAC string, want bool) func() error {
	return func() error {
		ns, err := neighbors(m, node)
		if err != nil {
			return err
		}

		found := false

		for _, n := range ns {
			if strings.EqualFold(n.NeighAddress, peerMAC) {
				found = true

				break
			}
		}

		if found != want {
			return fmt.Errorf("%s neighbor %s present=%t, want %t", node, peerMAC, found, want)
		}

		return nil
	}
}

func pings(m *meshsim.Mesh, from, to string) func() error {
	return func() error { return m.Ping(from, to) }
}

func chain(t *testing.T, m *meshsim.Mesh, names ...string) {
	t.Helper()

	for _, n := range names {
		m.AddNode(n)
	}

	for i := 1; i < len(names); i++ {
		m.Link(names[i-1], names[i])
	}
}

// diamond builds a-b-d and a-c-d: two equal two-hop paths from a to d.
func diamond(t *testing.T, m *meshsim.Mesh) {
	t.Helper()

	for _, n := range []string{"a", "b", "c", "d"} {
		m.AddNode(n)
	}

	m.Link("a", "b")
	m.Link("a", "c")
	m.Link("b", "d")
	m.Link("c", "d")
}

// TestMesh_chainConvergence builds a four-node line and checks every node
// learns every other one, and that openmanetd's originator topology reports
// the right next hop, hostnames, self MAC and routing algorithm.
func TestMesh_chainConvergence(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		chain(t, m, "a", "b", "c", "d")

		for _, n := range []string{"a", "b", "c", "d"} {
			m.Eventually(convergeTimeout, n+" learns 3 originators", originatorCount(m, n, 3))
		}

		m.Eventually(convergeTimeout, "a routes to d via b", routeVia(m, "a", "d", "b"))
		m.Eventually(convergeTimeout, "d routes to a via c", routeVia(m, "d", "a", "c"))

		topo, err := topology(m, "a")
		require.NoError(t, err)

		assert.Equal(t, m.Algo(), topo.Algorithm, "algorithm label")
		assert.Equal(t, m.Node("a").BatMAC, topo.SelfMAC, "self MAC is bat0's")

		b := entryFor(topo, m.Node("b").MAC)
		require.NotNil(t, b)
		assert.Equal(t, "b_radio0", b.OrigHostname)
		assert.Equal(t, "radio0", b.HardIfname)
		assert.Equal(t, 1, b.Hops, "direct neighbor")

		d := entryFor(topo, m.Node("d").MAC)
		require.NotNil(t, d)
		assert.Equal(t, "d_radio0", d.OrigHostname)
		assert.Equal(t, "b_radio0", d.NextHopHostname)
		// The originator table only names the first hop, so this provider
		// cannot tell two hops from three: anything not direct is 2. The
		// topology RPC test checks exact hop counts, which need vis data.
		assert.Equal(t, 2, d.Hops, "multi-hop originator")

		c := entryFor(topo, m.Node("c").MAC)
		require.NotNil(t, c)
		assert.Equal(t, 2, c.Hops, "a-b-c is two hops")
		assert.Equal(t, "b_radio0", c.NextHopHostname)

		m.Eventually(convergeTimeout, "a pings d over three hops", pings(m, "a", "d"))
	})
}

// TestMesh_lossyLinkAvoided gives a two equal-length paths, one lossy, and
// expects batman-adv to route over the clean one.
func TestMesh_lossyLinkAvoided(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		diamond(t, m)
		m.SetLossy("a", "b", true)
		m.SetLossy("b", "d", true)

		m.Eventually(convergeTimeout, "a routes to d via clean c", routeVia(m, "a", "d", "c"))
		m.Eventually(convergeTimeout, "a pings d", pings(m, "a", "d"))
	})
}

// TestMesh_linkFadeFailover cuts the link a is using to reach d without
// taking any interface down, and expects the route to move to the other
// path once batman-adv notices.
func TestMesh_linkFadeFailover(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		diamond(t, m)
		m.SetLossy("a", "c", true)
		m.SetLossy("c", "d", true)
		m.Eventually(convergeTimeout, "a routes to d via b", routeVia(m, "a", "d", "b"))

		// c is now the only way through; clear its loss so it wins once b
		// is gone.
		m.SetLossy("a", "c", false)
		m.SetLossy("c", "d", false)
		m.Unlink("a", "b")

		took := m.Eventually(purgeTimeout, "a fails over to c", routeVia(m, "a", "d", "c"))
		t.Logf("link fade failover (%s): %s", m.Algo(), took)

		m.Eventually(convergeTimeout, "a pings d after failover", pings(m, "a", "d"))
		m.Eventually(purgeTimeout, "a drops b as a neighbor", hasNeighbor(m, "a", m.Node("b").MAC, false))

		// Restore the link: b comes back as a neighbor.
		m.Link("a", "b")
		m.Eventually(convergeTimeout, "a hears b again", hasNeighbor(m, "a", m.Node("b").MAC, true))
	})
}

// TestMesh_radioResetFailover takes the relay's radio interface down. The
// relay's peers do not see an interface event, but the relay itself loses
// its hard interface at once.
func TestMesh_radioResetFailover(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		chain(t, m, "a", "b", "c")
		m.Eventually(convergeTimeout, "a routes to c via b", routeVia(m, "a", "c", "b"))

		// With its only hard interface down, batctl refuses to dump b's
		// originator table; openmanetd must report that as
		// ErrOriginatorsUnavailable rather than as an empty mesh.
		m.SetRadio("b", false)
		m.Eventually(convergeTimeout, "b reports originators unavailable", func() error {
			_, err := topology(m, "b")
			if errors.Is(err, batmanadv.ErrOriginatorsUnavailable) {
				return nil
			}

			return fmt.Errorf("want ErrOriginatorsUnavailable, got %w", err)
		})

		m.SetRadio("b", true)
		m.Eventually(convergeTimeout, "b relays again", routeVia(m, "a", "c", "b"))
		m.Eventually(convergeTimeout, "a pings c after radio reset", pings(m, "a", "c"))
	})
}

// TestMesh_nodeJoinLeave adds a node at the edge of a running mesh, checks
// everyone learns it, then powers it off and checks it ages out.
func TestMesh_nodeJoinLeave(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		chain(t, m, "a", "b", "c")
		m.Eventually(convergeTimeout, "a learns 2 originators", originatorCount(m, "a", 2))

		d := m.AddNode("d")
		m.Link("c", "d")

		took := m.Eventually(convergeTimeout, "a learns joining d via b", routeVia(m, "a", "d", "b"))
		t.Logf("join propagation (%s): %s", m.Algo(), took)
		m.Eventually(convergeTimeout, "c lists d as a neighbor", hasNeighbor(m, "c", d.MAC, true))
		m.Eventually(convergeTimeout, "a pings d", pings(m, "a", "d"))

		m.RemoveNode("d")

		took = m.Eventually(purgeTimeout, "c ages out powered-off d", hasNeighbor(m, "c", d.MAC, false))
		t.Logf("neighbor age-out (%s): %s", m.Algo(), took)
		m.Eventually(purgeTimeout, "a ages out powered-off d", noRoute(m, "a", d.MAC))
		m.Eventually(convergeTimeout, "a still pings c", pings(m, "a", "c"))
	})
}

// TestMesh_gatewayAnnouncement checks a gateway announced on one node shows
// up in openmanetd's gateway list on another, and disappears when withdrawn.
func TestMesh_gatewayAnnouncement(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		chain(t, m, "a", "b", "c")
		m.SetGatewayMode("a", batmanadv.GwModeClient)
		m.SetGatewayMode("c", batmanadv.GwModeServer)

		gwMAC := m.Node("c").MAC
		gateways := func(want bool) func() error {
			return func() error {
				var gws *batmanadv.Gateways

				err := m.Do("a", func() error {
					var err error

					gws, err = batmanadv.GetMeshGateways(meshsim.BatIface)

					return err
				})
				if err != nil {
					return err
				}

				found := false

				for _, gw := range *gws {
					if strings.EqualFold(gw.OrigAddress, gwMAC) {
						found = true
					}
				}

				if found != want {
					return fmt.Errorf("gateway c listed=%t, want %t (%d gateways)", found, want, len(*gws))
				}

				return nil
			}
		}

		m.Eventually(convergeTimeout, "a sees gateway c", gateways(true))

		err := m.Do("a", func() error {
			cfg, err := batmanadv.GetMeshConfig(meshsim.BatIface)
			if err != nil {
				return err
			}

			if !cfg.IsClientMode() {
				return errors.New("a is not reported as gateway client: " + cfg.GwMode)
			}

			return nil
		})
		require.NoError(t, err)

		m.SetGatewayMode("c", batmanadv.GwModeOff)
		m.Eventually(convergeTimeout, "gateway c withdrawn", gateways(false))
	})
}
