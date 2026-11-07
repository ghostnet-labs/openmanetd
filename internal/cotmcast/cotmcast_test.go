package cotmcast_test

import (
	"errors"
	"net"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/cotmcast"
)

const upMulticast = net.FlagUp | net.FlagMulticast

type fakeWriter struct {
	mu   sync.Mutex
	fail map[string]bool
	got  []string
	dst  []*net.UDPAddr
}

func (f *fakeWriter) write(ifi *net.Interface, dst *net.UDPAddr, _ []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.fail[ifi.Name] {
		return errors.New("rejected")
	}

	f.got = append(f.got, ifi.Name)
	f.dst = append(f.dst, dst)

	return nil
}

func (f *fakeWriter) names() []string {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]string(nil), f.got...)
}

func ifaces() []net.Interface {
	return []net.Interface{
		{Name: "lo", Flags: net.FlagUp | net.FlagLoopback | net.FlagMulticast},
		{Name: "eth0", Flags: upMulticast},
		{Name: "wlan0", Flags: net.FlagMulticast}, // down
		{Name: "br-lan", Flags: upMulticast},
	}
}

func newSender(w *fakeWriter, mesh *net.Interface) *cotmcast.Sender {
	return &cotmcast.Sender{
		InterfaceByName: func(name string) (*net.Interface, error) {
			if mesh != nil && name == mesh.Name {
				return mesh, nil
			}

			return nil, errors.New("no such interface")
		},
		Interfaces: func() ([]net.Interface, error) { return ifaces(), nil },
		Write:      w.write,
	}
}

func TestSend_prefersMeshBridge(t *testing.T) {
	w := &fakeWriter{}
	s := newSender(w, &net.Interface{Name: "br-ahwlan", Flags: upMulticast})

	require.NoError(t, s.Send([]byte("x")))

	assert.Equal(t, []string{"br-ahwlan"}, w.names())
	assert.Equal(t, "239.2.3.1:6969", w.dst[0].String())
}

func TestSend_configuredMeshName(t *testing.T) {
	w := &fakeWriter{}
	s := newSender(w, &net.Interface{Name: "br-mesh", Flags: upMulticast})
	s.MeshInterface = func() string { return "br-mesh" }

	require.NoError(t, s.Send([]byte("x")))
	assert.Equal(t, []string{"br-mesh"}, w.names())
}

func TestSend_fallsBackToEveryUpMulticastInterface(t *testing.T) {
	w := &fakeWriter{}
	s := newSender(w, &net.Interface{Name: "br-ahwlan", Flags: net.FlagMulticast}) // bridge down

	require.NoError(t, s.Send([]byte("x")))
	assert.Equal(t, []string{"eth0", "br-lan"}, w.names())
}

func TestSend_succeedsWhenOneInterfaceAccepts(t *testing.T) {
	w := &fakeWriter{fail: map[string]bool{"eth0": true}}

	require.NoError(t, newSender(w, nil).Send([]byte("x")))
	assert.Equal(t, []string{"br-lan"}, w.names())
}

func TestSend_failsWhenEveryInterfaceRejects(t *testing.T) {
	w := &fakeWriter{fail: map[string]bool{"eth0": true, "br-lan": true}}

	err := newSender(w, nil).Send([]byte("x"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "eth0")
	assert.Contains(t, err.Error(), "br-lan")
}

func TestSend_noInterfaces(t *testing.T) {
	s := &cotmcast.Sender{
		InterfaceByName: func(string) (*net.Interface, error) { return nil, errors.New("none") },
		Interfaces:      func() ([]net.Interface, error) { return nil, errors.New("netlink down") },
		Write:           (&fakeWriter{}).write,
	}

	assert.Error(t, s.Send([]byte("x")))
}

func TestCapable(t *testing.T) {
	tests := []struct {
		name  string
		flags net.Flags
		want  bool
	}{
		{"up multicast", upMulticast, true},
		{"down", net.FlagMulticast, false},
		{"no multicast", net.FlagUp, false},
		{"loopback", upMulticast | net.FlagLoopback, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, cotmcast.Capable(&net.Interface{Flags: tc.flags}))
		})
	}
}
