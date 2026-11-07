//go:build meshsim

package meshsim_test

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openmanet/go-alfred"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	meshtopov1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_topology/v1"
	batmanadv "github.com/openmanet/openmanetd/internal/batman-adv"
	"github.com/openmanet/openmanetd/internal/meshsim"
	"github.com/openmanet/openmanetd/internal/mgmt"
	"github.com/openmanet/openmanetd/internal/openmanet/server/handlers"
)

// startAlfred runs an alfred primary and a batadv-vis server on every node,
// each with its own unix socket, as OpenMANET's init scripts do on bat0.
func startAlfred(t *testing.T, m *meshsim.Mesh, names ...string) {
	t.Helper()

	for _, bin := range []string{"alfred", "batadv-vis"} {
		if _, err := exec.LookPath(bin); err != nil {
			if os.Getenv("MESHSIM_REQUIRE") == "1" {
				t.Fatalf("%s not installed: %v", bin, err)
			}

			t.Skipf("%s not installed: %v", bin, err)
		}
	}

	// alfred binds bat0's IPv6 link-local address at startup and exits
	// ("can't bind: Cannot assign requested address") while that address
	// is still tentative. OpenMANET's procd respawns it; here, wait for
	// duplicate address detection to finish before starting it.
	for _, n := range names {
		m.Eventually(convergeTimeout, n+" bat0 link-local ready", linkLocalReady(m, n))
		m.Start(n, "alfred", "-i", meshsim.BatIface, "-b", meshsim.BatIface, "-m", "-p", "1", "-u", alfredSock(m, n))
	}

	// Wait until each alfred accepts connections (a leftover socket file
	// is not enough) before starting the vis server.
	for _, n := range names {
		sock := alfredSock(m, n)
		m.Eventually(convergeTimeout, n+" alfred is up", func() error {
			conn, err := net.Dial("unix", sock)
			if err != nil {
				return fmt.Errorf("alfred socket: %w", err)
			}

			return conn.Close()
		})
		m.Start(n, "batadv-vis", "-i", meshsim.BatIface, "-s", "-u", sock)
	}
}

// linkLocalReady reports whether the node's bat0 has an IPv6 link-local
// address that has finished duplicate address detection.
func linkLocalReady(m *meshsim.Mesh, node string) func() error {
	return func() error {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()

		out, err := exec.CommandContext(ctx, "ip", "-n", m.Node(node).NS, "-6", "addr", "show",
			"dev", meshsim.BatIface, "scope", "link", "-tentative").CombinedOutput()
		if err != nil {
			return fmt.Errorf("ip addr: %w: %s", err, out)
		}

		if !strings.Contains(string(out), "inet6 fe80:") {
			return errors.New("no non-tentative link-local address yet")
		}

		return nil
	}
}

func alfredSock(m *meshsim.Mesh, node string) string {
	return filepath.Join(m.Dir(), node+"-alfred.sock")
}

// topologyService wires MeshTopologyService the way server.go does, minus
// the snapshot cache, so every call reads live batctl and batadv-vis
// output from inside the node's namespace.
func topologyService(m *meshsim.Mesh, node string, neighbors batmanadv.MeshNeighborsProvider) *handlers.MeshTopologyService {
	return &handlers.MeshTopologyService{
		Log:               zerolog.Nop(),
		VisProvider:       batmanadv.BatadvVisProvider{Args: []string{"-u", alfredSock(m, node)}},
		OrigProvider:      &batmanadv.BatctlOriginatorTopologyProvider{BatHostsPath: m.BatHostsPath()},
		NeighborsProvider: neighbors,
		ParseBatHosts:     batmanadv.ParseBatHostsFile,
		GetMeshGateways:   batmanadv.GetMeshGateways,
		BatHostsPath:      m.BatHostsPath(),
		BatInterface:      meshsim.BatIface,
	}
}

// alfredClient opens a go-alfred client on the node's alfred socket.
func alfredClient(t *testing.T, m *meshsim.Mesh, node string) *alfred.Client {
	t.Helper()

	c, err := alfred.NewClient(alfred.WithSocketPath(alfredSock(m, node)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = c.Close() }) // best effort at test end

	return c
}

// gossipPublishers builds openmanetd's mesh-neighbors publisher for every
// node, each bound to that node's alfred. On BATMAN_V this gossip is the
// only adjacency source: batadv-vis reads just the BATMAN_IV TQ attribute
// and publishes no neighbors.
func gossipPublishers(t *testing.T, m *meshsim.Mesh, names ...string) map[string]*mgmt.MeshNeighborsWorker {
	t.Helper()

	out := make(map[string]*mgmt.MeshNeighborsWorker, len(names))
	for _, n := range names {
		out[n] = &mgmt.MeshNeighborsWorker{
			Config: &mgmt.ManagementConfig{Log: zerolog.Nop(), BatInterface: meshsim.BatIface},
			Client: alfredClient(t, m, n),
		}
	}

	return out
}

// publishGossip publishes each node's neighbor table from inside its
// namespace, as the daemon's ticker does on a real node.
func publishGossip(m *meshsim.Mesh, workers map[string]*mgmt.MeshNeighborsWorker) error {
	for name, w := range workers {
		if err := m.Do(name, w.PublishOnce); err != nil {
			return fmt.Errorf("%s publish mesh-neighbors: %w", name, err)
		}
	}

	return nil
}

// gossipReader starts the daemon's mesh-neighbors snapshotter on the
// node's alfred, as openmanetd wires it for the topology handler.
func gossipReader(t *testing.T, m *meshsim.Mesh, node string) *batmanadv.MeshNeighborsSnapshotter {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	s := &batmanadv.MeshNeighborsSnapshotter{
		Log:      zerolog.Nop(),
		Client:   alfredClient(t, m, node),
		Interval: 250 * time.Millisecond,
	}
	s.Start(ctx)

	return s
}

func getTopology(m *meshsim.Mesh, node string, neighbors batmanadv.MeshNeighborsProvider) (*meshtopov1.MeshTopology, error) {
	var topo *meshtopov1.MeshTopology

	err := m.Do(node, func() error {
		res, err := topologyService(m, node, neighbors).GetMeshTopology(context.Background(), &emptypb.Empty{})
		if err != nil {
			return err
		}

		topo = res.GetTopology()

		return nil
	})

	return topo, err
}

// describe renders a topology compactly for failure messages.
func describe(topo *meshtopov1.MeshTopology) string {
	var b strings.Builder

	b.WriteString("self=" + topo.GetSelfMac() + " nodes=[")

	for _, n := range topo.GetNodes() {
		fmt.Fprintf(&b, " %s/%s hops=%d self=%t", n.GetMac(), n.GetHostname(), n.GetHopsFromSelf(), n.GetIsSelf())
	}

	b.WriteString(" ] edges=[")

	for _, e := range topo.GetEdges() {
		fmt.Fprintf(&b, " %s-%s", e.GetFromMac(), e.GetToMac())
	}

	b.WriteString(" ]")

	return b.String()
}

func hasEdge(topo *meshtopov1.MeshTopology, a, b string) bool {
	for _, e := range topo.GetEdges() {
		from, to := strings.ToLower(e.GetFromMac()), strings.ToLower(e.GetToMac())
		if (from == a && to == b) || (from == b && to == a) {
			return true
		}
	}

	return false
}

// TestMesh_topologyRPC serves GetMeshTopology from one end of a four-node
// line, with real alfred gossip carrying batadv-vis data between nodes,
// and checks the graph the UI would draw.
func TestMesh_topologyRPC(t *testing.T) {
	forEachAlgo(t, func(t *testing.T, m *meshsim.Mesh) {
		names := []string{"a", "b", "c", "d"}
		chain(t, m, names...)
		m.Eventually(convergeTimeout, "a learns 3 originators", originatorCount(m, "a", 3))
		startAlfred(t, m, names...)

		publishers := gossipPublishers(t, m, names...)
		neighbors := gossipReader(t, m, "a")

		var topo *meshtopov1.MeshTopology

		// Nodes render under their canonical MAC: bat0's for the serving
		// node (from bat-hosts grouping), the originator MAC for peers.
		// Look them up by hostname, as the UI labels them.
		macOf := func(name string) string {
			for _, n := range topo.GetNodes() {
				if n.GetHostname() == name {
					return strings.ToLower(n.GetMac())
				}
			}

			return "missing:" + name
		}

		m.Eventually(purgeTimeout, "topology shows the whole line", func() error {
			var err error

			if err := publishGossip(m, publishers); err != nil {
				return err
			}

			topo, err = getTopology(m, "a", neighbors)
			if err != nil {
				return err
			}

			if got := len(topo.GetNodes()); got != len(names) {
				return fmt.Errorf("%d nodes, want %d: %s", got, len(names), describe(topo))
			}

			for _, pair := range [][2]string{{"a", "b"}, {"b", "c"}, {"c", "d"}} {
				if !hasEdge(topo, macOf(pair[0]), macOf(pair[1])) {
					return fmt.Errorf("no edge %s-%s: %s", pair[0], pair[1], describe(topo))
				}
			}

			// While batman-adv converges a snapshot can still carry a
			// transient extra edge or hop count, so wait for the steady
			// line rather than checking once after the first full one.
			for _, pair := range [][2]string{{"a", "c"}, {"a", "d"}, {"b", "d"}} {
				if hasEdge(topo, macOf(pair[0]), macOf(pair[1])) {
					return fmt.Errorf("edge %s-%s, but they cannot hear each other: %s", pair[0], pair[1], describe(topo))
				}
			}

			if got := len(topo.GetEdges()); got != 3 {
				return fmt.Errorf("%d edges, want 3: %s", got, describe(topo))
			}

			for i, name := range names {
				for _, n := range topo.GetNodes() {
					if n.GetHostname() == name && n.GetHopsFromSelf() != int32(i) {
						return fmt.Errorf("%d hops from a to %s, want %d: %s", n.GetHopsFromSelf(), name, i, describe(topo))
					}
				}
			}

			return nil
		})

		assert.Equal(t, m.Algo(), topo.GetAlgorithm())
		assert.Equal(t, m.Node("a").BatMAC, macOf("a"), "self renders under bat0's MAC")

		for i, name := range names {
			var node *meshtopov1.MeshNode

			for _, n := range topo.GetNodes() {
				if n.GetHostname() == name {
					node = n
				}
			}

			require.NotNil(t, node, "node %s missing: %s", name, describe(topo))
			assert.Equal(t, name == "a", node.GetIsSelf(), "is_self of %s", name)
			assert.Equal(t, int32(i), node.GetHopsFromSelf(), "hops from a to %s", name)

			if name != "a" {
				assert.Equal(t, m.Node(name).MAC, macOf(name), "%s renders under its originator MAC", name)
			}
		}
	})
}
