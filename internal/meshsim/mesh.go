//go:build meshsim

package meshsim

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vishvananda/netns"
)

// Routing algorithms batman-adv accepts for a new mesh interface.
const (
	AlgoIV = "BATMAN_IV"
	AlgoV  = "BATMAN_V"
)

// Interface names inside every node, matching what openmanetd reads.
const (
	BatIface   = "bat0"
	RadioIface = "radio0"
)

// LossPercent is the frame loss applied to a pair marked lossy with
// SetLossy: bad enough that batman-adv should route around it, good enough
// that the link stays up.
const LossPercent = 40

// Timer settings that keep convergence in seconds. Production uses
// batman-adv's defaults (1000 ms OGM, 500 ms ELP); only the cadence changes,
// not the routing logic under test.
const (
	origIntervalMs = 200
	elpIntervalMs  = 100
	cmdTimeout     = 10 * time.Second
	pollInterval   = 250 * time.Millisecond
)

// requireEnvVar makes a missing prerequisite a failure instead of a skip.
const requireEnvVar = "MESHSIM_REQUIRE"

// Node is one virtual OpenMANET node.
type Node struct {
	Name string
	// NS is the named network namespace (see `ip netns list`).
	NS string
	// MAC is the node's originator address: the MAC of radio0, its only
	// (and so primary) batman-adv hard interface.
	MAC string
	// BatMAC is bat0's own MAC, distinct from the originator address as on
	// a real node.
	BatMAC string
	// IP is the node's IPv4 address on bat0 (10.99.0.<index>/24).
	IP string

	port  string
	procs []*exec.Cmd
	index int
}

// Mesh is a set of nodes on a shared simulated radio medium.
//
// The medium is a Linux bridge in its own namespace ("air"). Every node has
// one veth, radio0, plugged into it, so each node has exactly one batman-adv
// hard interface and broadcasts reach every peer it can hear, as on HaLow.
// Which pairs can hear each other is an nftables allow-list on the bridge:
// Link and Unlink add or remove a pair, SetLossy drops LossPercent of the
// frames between a pair. Nothing is allowed by default.
type Mesh struct {
	t         testing.TB
	algo      string
	prefix    string
	air       string
	hostsPath string
	dir       string

	// nodes, order and next are guarded by mu.
	nodes map[string]*Node
	order []*Node
	mu    sync.Mutex
	next  int
}

// Require skips (or, with MESHSIM_REQUIRE=1, fails) the test when the host
// cannot run a virtual mesh: not root, missing tools, or no batman_adv module.
func Require(t testing.TB) {
	t.Helper()

	if err := checkHost(); err != nil {
		if os.Getenv(requireEnvVar) == "1" {
			t.Fatalf("meshsim prerequisites missing: %v", err)
		}

		t.Skipf("meshsim prerequisites missing (set %s=1 to fail instead): %v", requireEnvVar, err)
	}
}

func checkHost() error {
	if os.Geteuid() != 0 {
		return errors.New("must run as root")
	}

	for _, bin := range []string{"ip", "batctl", "nft", "ping"} {
		if _, err := exec.LookPath(bin); err != nil {
			return fmt.Errorf("%s not found: %w", bin, err)
		}
	}

	if _, err := os.Stat("/sys/module/batman_adv"); err == nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	if out, err := exec.CommandContext(ctx, "modprobe", "batman_adv").CombinedOutput(); err != nil {
		return fmt.Errorf("modprobe batman_adv: %w: %s", err, bytes.TrimSpace(out))
	}

	return nil
}

// RequireAlgo skips the test when the running kernel's batman-adv was built
// without the routing algorithm (Ubuntu's Azure kernels, which GitHub-hosted
// runners use, lack CONFIG_BATMAN_ADV_BATMAN_V). Setting
// MESHSIM_REQUIRE_ALGOS to a comma-separated list turns the skip into a
// failure for those algorithms.
func RequireAlgo(t testing.TB, algo string) {
	t.Helper()
	Require(t)

	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	probe := "ip link add msprobe0 type batadv ra " + algo

	out, err := exec.CommandContext(ctx, "unshare", "--net", "sh", "-c", probe).CombinedOutput()
	if err == nil {
		return
	}

	for _, want := range strings.Split(os.Getenv("MESHSIM_REQUIRE_ALGOS"), ",") {
		if strings.TrimSpace(want) == algo {
			t.Fatalf("kernel batman-adv cannot create a %s mesh: %v: %s", algo, err, bytes.TrimSpace(out))
		}
	}

	t.Skipf("kernel batman-adv cannot create a %s mesh (CONFIG_BATMAN_ADV_BATMAN_V?): %s", algo, bytes.TrimSpace(out))
}

// New creates an empty mesh using the given routing algorithm. Every
// namespace and helper process is removed in t.Cleanup.
func New(t testing.TB, algo string) *Mesh {
	t.Helper()
	RequireAlgo(t, algo)

	dir := t.TempDir()
	// One prefix per test so tests can run in parallel without their
	// namespace names colliding.
	h := fnv.New32a()
	_, _ = h.Write([]byte(t.Name())) // hash.Hash never returns an error
	prefix := fmt.Sprintf("ms%07x-", h.Sum32()&0xfffffff)
	m := &Mesh{
		t:         t,
		algo:      algo,
		prefix:    prefix,
		air:       prefix + "air",
		hostsPath: filepath.Join(dir, "bat-hosts"),
		dir:       dir,
		nodes:     make(map[string]*Node, 8),
		order:     make([]*Node, 0, 8),
	}

	t.Cleanup(m.close)

	m.must("ip", "netns", "add", m.air)
	m.must("ip", "netns", "exec", m.air, "sysctl", "-qw", "net.ipv6.conf.all.disable_ipv6=1")
	m.must("ip", "netns", "exec", m.air, "sysctl", "-qw", "net.ipv6.conf.default.disable_ipv6=1")
	m.must("ip", "-n", m.air, "link", "add", "br0", "type", "bridge", "stp_state", "0", "mcast_snooping", "0")
	m.must("ip", "-n", m.air, "link", "set", "br0", "up")

	ruleset := fmt.Sprintf(`table bridge air {
	set links { type ifname . ifname; }
	set lossy { type ifname . ifname; }
	chain forward {
		type filter hook forward priority 0; policy drop;
		iifname . oifname @lossy numgen random mod 100 < %d drop
		iifname . oifname @links accept
	}
}
`, LossPercent)

	rulesPath := filepath.Join(dir, "air.nft")
	if err := os.WriteFile(rulesPath, []byte(ruleset), 0o600); err != nil {
		t.Fatalf("meshsim: write ruleset: %v", err)
	}

	m.must("ip", "netns", "exec", m.air, "nft", "-f", rulesPath)

	return m
}

// Algo returns the routing algorithm the mesh was built with.
func (m *Mesh) Algo() string { return m.algo }

// BatHostsPath returns the bat-hosts file listing every node, in the format
// OpenMANET writes to /tmp/bat-hosts.
func (m *Mesh) BatHostsPath() string { return m.hostsPath }

// Dir returns a scratch directory that lives as long as the mesh.
func (m *Mesh) Dir() string { return m.dir }

// Node returns a node by name, failing the test if it does not exist.
func (m *Mesh) Node(name string) *Node {
	m.t.Helper()

	m.mu.Lock()
	n, ok := m.nodes[name]
	m.mu.Unlock()

	if !ok {
		m.t.Fatalf("meshsim: no node %q", name)
	}

	return n
}

// AddNode creates a node whose radio is on the medium but cannot hear
// anyone until Link is called.
func (m *Mesh) AddNode(name string) *Node {
	m.t.Helper()

	m.mu.Lock()
	m.next++
	idx := m.next
	m.mu.Unlock()

	n := &Node{
		Name:   name,
		NS:     m.prefix + name,
		MAC:    fmt.Sprintf("02:00:00:00:00:%02x", idx),
		BatMAC: fmt.Sprintf("02:ba:00:00:00:%02x", idx),
		IP:     "10.99.0." + strconv.Itoa(idx),
		port:   "p-" + name,
		index:  idx,
	}

	m.must("ip", "netns", "add", n.NS)

	m.mu.Lock()
	m.nodes[name] = n
	m.order = append(m.order, n)
	m.mu.Unlock()

	m.must("ip", "-n", n.NS, "link", "set", "lo", "up")
	m.must("ip", "-n", n.NS, "link", "add", BatIface, "address", n.BatMAC, "type", "batadv", "ra", m.algo)
	m.must("ip", "-n", n.NS, "link", "add", RadioIface, "address", n.MAC,
		"type", "veth", "peer", "name", n.port, "netns", m.air)
	m.must("ip", "-n", m.air, "link", "set", n.port, "master", "br0")
	m.must("ip", "-n", m.air, "link", "set", n.port, "up")
	m.must("ip", "-n", n.NS, "link", "set", RadioIface, "master", BatIface)
	m.must("ip", "-n", n.NS, "link", "set", RadioIface, "up")
	m.must("ip", "-n", n.NS, "addr", "add", n.IP+"/24", "dev", BatIface)
	m.must("ip", "-n", n.NS, "link", "set", BatIface, "up")
	m.batctl(n, "meshif", BatIface, "orig_interval", strconv.Itoa(origIntervalMs))

	if m.algo == AlgoV {
		m.batctl(n, "hardif", RadioIface, "elp_interval", strconv.Itoa(elpIntervalMs))
	}

	m.writeBatHosts()

	return n
}

// Link lets two nodes hear each other.
func (m *Mesh) Link(a, b string) {
	m.t.Helper()
	m.pairSet("add", "links", a, b)
}

// Unlink makes two nodes unable to hear each other, the way a radio link
// fades out: frames stop, interfaces stay up, and batman-adv has to notice
// on its own.
func (m *Mesh) Unlink(a, b string) {
	m.t.Helper()
	m.pairSet("delete", "links", a, b)
}

// SetLossy drops (or stops dropping) LossPercent of the frames between two
// linked nodes.
func (m *Mesh) SetLossy(a, b string, lossy bool) {
	m.t.Helper()

	op := "delete"
	if lossy {
		op = "add"
	}

	m.pairSet(op, "lossy", a, b)
}

// SetRadio brings a node's radio interface up or down, as when the radio
// driver resets: batman-adv sees the hard interface go away immediately.
func (m *Mesh) SetRadio(name string, up bool) {
	m.t.Helper()

	state := "down"
	if up {
		state = "up"
	}

	m.must("ip", "-n", m.Node(name).NS, "link", "set", RadioIface, state)
}

// RemoveNode powers a node off: its helper processes stop and its namespace
// and radio disappear. Peers are not told; they must time it out.
func (m *Mesh) RemoveNode(name string) {
	m.t.Helper()

	n := m.Node(name)
	n.stopProcs()
	m.must("ip", "netns", "del", n.NS)

	m.mu.Lock()
	delete(m.nodes, name)

	for i, o := range m.order {
		if o == n {
			m.order = append(m.order[:i], m.order[i+1:]...)

			break
		}
	}
	m.mu.Unlock()

	m.writeBatHosts()
}

// SetGatewayMode runs `batctl gw_mode` on a node ("server", "client" or
// "off").
func (m *Mesh) SetGatewayMode(name, mode string) {
	m.t.Helper()
	m.batctl(m.Node(name), "meshif", BatIface, "gw_mode", mode)
}

// Ping sends one ICMP echo over bat0 from one node to another.
func (m *Mesh) Ping(from, to string) error {
	m.t.Helper()

	src, dst := m.Node(from), m.Node(to)

	return m.run("ip", "netns", "exec", src.NS, "ping", "-c", "1", "-W", "1", "-n", dst.IP)
}

// Start launches a long-running helper (alfred, batadv-vis) inside a node's
// namespace. Its output goes to <Dir>/<node>-<binary>.log. It is killed when
// the node is removed or the mesh is closed.
func (m *Mesh) Start(name string, args ...string) {
	m.t.Helper()

	n := m.Node(name)
	full := append([]string{"netns", "exec", n.NS}, args...)
	cmd := exec.Command("ip", full...) //nolint:noctx // lives until cleanup kills it
	logPath := filepath.Join(m.dir, n.Name+"-"+filepath.Base(args[0])+".log")

	logFile, err := os.Create(logPath) //nolint:gosec // path is under t.TempDir
	if err != nil {
		m.t.Fatalf("meshsim: create %s: %v", logPath, err)
	}

	cmd.Stdout = logFile
	cmd.Stderr = logFile

	startErr := cmd.Start()
	_ = logFile.Close() // the child holds its own descriptor

	if startErr != nil {
		m.t.Fatalf("meshsim: start %v in %s: %v", args, n.Name, startErr)
	}

	n.procs = append(n.procs, cmd)
}

// Do runs fn with the calling goroutine's thread moved into the node's
// network namespace. Commands fn starts (batctl, batadv-vis) inherit the
// namespace, so openmanetd's production providers see that node's mesh.
func (m *Mesh) Do(name string, fn func() error) error {
	m.t.Helper()

	n := m.Node(name)

	runtime.LockOSThread()

	orig, err := netns.Get()
	if err != nil {
		runtime.UnlockOSThread()

		return fmt.Errorf("get current netns: %w", err)
	}
	defer orig.Close()

	target, err := netns.GetFromName(n.NS)
	if err != nil {
		runtime.UnlockOSThread()

		return fmt.Errorf("open netns %s: %w", n.NS, err)
	}
	defer target.Close()

	if err := netns.Set(target); err != nil {
		runtime.UnlockOSThread()

		return fmt.Errorf("enter netns %s: %w", n.NS, err)
	}

	fnErr := fn()

	// If the thread cannot go back it stays locked, and the runtime
	// discards it when the goroutine exits instead of reusing it.
	if err := netns.Set(orig); err != nil {
		return errors.Join(fnErr, fmt.Errorf("restore netns: %w", err))
	}

	runtime.UnlockOSThread()

	return fnErr
}

// Eventually polls cond until it returns nil or the timeout expires, then
// fails the test with the last error. It returns how long it took.
func (m *Mesh) Eventually(timeout time.Duration, what string, cond func() error) time.Duration {
	m.t.Helper()

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()

	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	start := time.Now()

	for {
		err := cond()
		if err == nil {
			took := time.Since(start).Round(time.Millisecond)
			m.t.Logf("meshsim: %s after %s", what, took)

			return took
		}

		select {
		case <-deadline.C:
			m.dumpState()
			m.t.Fatalf("meshsim: %s: not reached within %s: %v", what, timeout, err)
		case <-tick.C:
		}
	}
}

func (n *Node) stopProcs() {
	for _, p := range n.procs {
		if p.Process != nil {
			_ = p.Process.Kill() // best effort; it may already have exited
			_ = p.Wait()         // reap; a "killed" exit status is expected
		}
	}

	n.procs = nil
}

func (m *Mesh) pairSet(op, set, a, b string) {
	m.t.Helper()

	pa, pb := m.Node(a).port, m.Node(b).port
	elems := fmt.Sprintf(`{ "%s" . "%s", "%s" . "%s" }`, pa, pb, pb, pa)
	m.must("ip", "netns", "exec", m.air, "nft", op, "element", "bridge", "air", set, elems)
}

func (m *Mesh) batctl(n *Node, args ...string) {
	m.t.Helper()
	m.must(append([]string{"ip", "netns", "exec", n.NS, "batctl"}, args...)...)
}

func (m *Mesh) must(args ...string) {
	m.t.Helper()

	if err := m.run(args...); err != nil {
		m.t.Fatalf("meshsim: %v", err)
	}
}

func (m *Mesh) run(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
	defer cancel()

	out, err := exec.CommandContext(ctx, args[0], args[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w: %s", strings.Join(args, " "), err, bytes.TrimSpace(out))
	}

	return nil
}

// writeBatHosts renders every node in OpenMANET's /tmp/bat-hosts format
// (see testfixtures/batman-adv/bat-hosts).
func (m *Mesh) writeBatHosts() {
	m.t.Helper()

	var b strings.Builder

	b.WriteString("### generated by meshsim\n")

	// Same shape as alfred-bat-hosts output on a node: a header per node,
	// then one "<mac> <host>_<iface>" line per interface.
	m.mu.Lock()
	for _, n := range m.order {
		fmt.Fprintf(&b, "# Node %s\n%s %s_%s\n%s %s_%s\n\n", n.BatMAC, n.MAC, n.Name, RadioIface, n.BatMAC, n.Name, BatIface)
	}
	m.mu.Unlock()

	if err := os.WriteFile(m.hostsPath, []byte(b.String()), 0o600); err != nil {
		m.t.Fatalf("meshsim: write bat-hosts: %v", err)
	}
}

// dumpState logs every node's batctl tables so a failed wait shows what the
// kernel actually had.
func (m *Mesh) dumpState() {
	m.mu.Lock()
	nodes := append([]*Node(nil), m.order...)
	m.mu.Unlock()

	for _, n := range nodes {
		for _, sub := range []string{"n", "o", "gwl"} {
			ctx, cancel := context.WithTimeout(context.Background(), cmdTimeout)
			out, _ := exec.CommandContext(ctx, "ip", "netns", "exec", n.NS, "batctl", "meshif", BatIface, sub).CombinedOutput()

			cancel()
			m.t.Logf("meshsim: %s batctl %s:\n%s", n.Name, sub, out)
		}
	}
}

func (m *Mesh) close() {
	m.mu.Lock()
	nodes := append([]*Node(nil), m.order...)
	m.order = nil
	m.nodes = make(map[string]*Node)
	m.mu.Unlock()

	for _, n := range nodes {
		n.stopProcs()

		if err := m.run("ip", "netns", "del", n.NS); err != nil {
			m.t.Logf("meshsim: cleanup: %v", err)
		}
	}

	if err := m.run("ip", "netns", "del", m.air); err != nil {
		m.t.Logf("meshsim: cleanup: %v", err)
	}
}
