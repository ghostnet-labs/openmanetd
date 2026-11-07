package takfeed_test

import (
	"bufio"
	"bytes"
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coreywagehoft/go-tak/pkg/cot"
	"github.com/coreywagehoft/go-tak/pkg/cotproto"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/emptypb"

	meshtopov1 "github.com/openmanet/openmanetd/internal/api/openmanet/mesh_topology/v1"
	weatherv1 "github.com/openmanet/openmanetd/internal/api/openmanet/weather/v1"
	"github.com/openmanet/openmanetd/internal/config"
	"github.com/openmanet/openmanetd/internal/database/models"
	"github.com/openmanet/openmanetd/internal/scanner/aircraft"
	"github.com/openmanet/openmanetd/internal/system"
	"github.com/openmanet/openmanetd/internal/takfeed"
)

// fakeSender records every datagram and decodes it on read.
type fakeSender struct {
	mu      sync.Mutex
	packets [][]byte
	err     error
}

func (f *fakeSender) Send(data []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return f.err
	}

	f.packets = append(f.packets, bytes.Clone(data))

	return nil
}

func (f *fakeSender) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.packets = nil
}

func (f *fakeSender) events(t *testing.T) []*cotproto.CotEvent {
	t.Helper()

	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]*cotproto.CotEvent, 0, len(f.packets))

	for _, p := range f.packets {
		msg, _, err := cot.ReadProtoMesh(bufio.NewReader(bytes.NewReader(p)))
		require.NoError(t, err)

		out = append(out, msg.GetCotEvent())
	}

	return out
}

// byType returns the events of one CoT type.
func byType(evs []*cotproto.CotEvent, typ string) []*cotproto.CotEvent {
	var out []*cotproto.CotEvent

	for _, e := range evs {
		if e.GetType() == typ {
			out = append(out, e)
		}
	}

	return out
}

type fakePosition struct {
	mu  sync.Mutex
	pos takfeed.Position
	ok  bool
}

func (f *fakePosition) Position() (takfeed.Position, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.pos, f.ok
}

func (f *fakePosition) set(p takfeed.Position, ok bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.pos, f.ok = p, ok
}

type fakeTopology struct {
	mu   sync.Mutex
	topo *meshtopov1.MeshTopology
	err  error
}

func (f *fakeTopology) GetMeshTopology(_ context.Context, _ *emptypb.Empty) (*meshtopov1.GetMeshTopologyResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.err != nil {
		return nil, f.err
	}

	return &meshtopov1.GetMeshTopologyResponse{Topology: f.topo}, nil
}

func (f *fakeTopology) set(topo *meshtopov1.MeshTopology) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.topo = topo
}

type fakeNodes struct {
	mu    sync.Mutex
	nodes []models.MeshNode
	err   error
}

func (f *fakeNodes) ListMeshNodes(_ context.Context) ([]models.MeshNode, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.nodes, f.err
}

type fakeBattery struct {
	st  *system.BatteryStatus
	err error
}

func (f *fakeBattery) GetBatteryStatus() (*system.BatteryStatus, error) { return f.st, f.err }

type fakeAircraft struct {
	mu   sync.Mutex
	list []aircraft.Aircraft
}

func (f *fakeAircraft) Aircraft(dst []aircraft.Aircraft, _ bool) ([]aircraft.Aircraft, []aircraft.FeedStatus, time.Time) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append(dst, f.list...), nil, time.Time{}
}

type fakeJams struct {
	mu   sync.Mutex
	jams []takfeed.Jam
}

func (f *fakeJams) ActiveJams() []takfeed.Jam {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]takfeed.Jam(nil), f.jams...)
}

func (f *fakeJams) set(j []takfeed.Jam) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.jams = j
}

// fakeWeather serves a scripted local weather report.
type fakeWeather struct {
	mu  sync.Mutex
	rep *weatherv1.WeatherReport
}

func (f *fakeWeather) Local() *weatherv1.WeatherReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.rep
}

func (f *fakeWeather) set(rep *weatherv1.WeatherReport) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.rep = rep
}

// fakeConfig serves a real *config.Config built from YAML so tests see
// exactly the defaults and validation production uses.
type fakeConfig struct {
	mu  sync.Mutex
	cfg *config.Config
}

func newFakeConfig(t *testing.T, yaml string) *fakeConfig {
	t.Helper()

	f := &fakeConfig{}
	f.load(t, yaml)

	return f
}

func (f *fakeConfig) load(t *testing.T, yaml string) {
	t.Helper()

	v := viper.New()
	v.SetConfigType("yaml")
	require.NoError(t, v.ReadConfig(strings.NewReader(yaml)))

	f.mu.Lock()
	defer f.mu.Unlock()

	f.cfg = config.NewWithoutWatch(v)
}

func (f *fakeConfig) GetTAK() config.TAKConfig {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.cfg.GetTAK()
}

// fakeStatus returns a fixed node status and counts reads.
type fakeStatus struct {
	mu    sync.Mutex
	st    takfeed.Status
	calls int
}

func (f *fakeStatus) NodeStatus(_ context.Context) takfeed.Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.calls++

	return f.st
}

func (f *fakeStatus) getCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.calls
}

// fakeSignals returns a fixed station list.
type fakeSignals struct {
	mu      sync.Mutex
	signals map[string]takfeed.StationSignal
}

func (f *fakeSignals) StationSignals(_ context.Context) map[string]takfeed.StationSignal {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.signals
}
