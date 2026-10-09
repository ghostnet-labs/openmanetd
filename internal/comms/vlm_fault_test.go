package comms

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/comms/gpio"
	"github.com/openmanet/openmanetd/internal/config"
)

// faultSeam records what startVLMFaultMonitor asked for and hands back a
// monitor over fake lines (or an open error).
type faultSeam struct {
	lines   *fakeFaultLines
	openErr error

	mu        sync.Mutex // protects the fields below
	built     int
	builtLine string
	openLine  string
	onEdge    func()
}

func (s *faultSeam) build(line string) *gpio.FaultMonitor {
	s.mu.Lock()
	s.built++
	s.builtLine = line
	s.mu.Unlock()

	return &gpio.FaultMonitor{
		Log:  zerolog.Nop(),
		Line: line,
		Open: func(name string, onEdge func()) (gpio.FaultLines, error) {
			s.mu.Lock()
			defer s.mu.Unlock()

			s.openLine = name
			s.onEdge = onEdge

			if s.openErr != nil {
				return nil, s.openErr
			}

			return s.lines, nil
		},
	}
}

func (s *faultSeam) builtCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.built
}

func (s *faultSeam) fireEdge() {
	s.mu.Lock()
	fire := s.onEdge
	s.mu.Unlock()

	fire()
}

func waitFaultRead(t *testing.T, f *fakeFaultLines) {
	t.Helper()

	select {
	case <-f.reads:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for a fault line read")
	}
}

func newFaultTestService(t *testing.T, enable, supported bool, seam *faultSeam) *Service {
	t.Helper()

	svc := newSelectTestService(t, 1)
	svc.Cfg.VLMUSBFaultEnable = enable
	svc.Cfg.VLMUSBFaultLine = "GPIO25"
	svc.Cfg.vlmFaultSupportedFn = func() bool { return supported }
	svc.Cfg.newVLMFaultMonitorFn = seam.build

	return svc
}

func TestStartVLMFaultMonitor_Gating(t *testing.T) {
	tests := []struct {
		name      string
		enable    bool
		supported bool
	}{
		{name: "disabled by default on a supported board", enable: false, supported: true},
		{name: "enabled on an unsupported board", enable: true, supported: false},
		{name: "disabled and unsupported", enable: false, supported: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			seam := &faultSeam{lines: newFakeFaultLines(1)}
			svc := newFaultTestService(t, tc.enable, tc.supported, seam)

			stop := svc.Cfg.startVLMFaultMonitor(context.Background(), svc)

			assert.Nil(t, stop)
			assert.Nil(t, svc.Rt.VLMFault.Load())
			assert.Zero(t, seam.builtCount(), "no line may be requested")
		})
	}
}

func TestStartVLMFaultMonitor_OpenFailureIsNonFatal(t *testing.T) {
	seam := &faultSeam{openErr: errors.New("gpio: find line \"GPIO25\": not found")}
	svc := newFaultTestService(t, true, true, seam)

	stop := svc.Cfg.startVLMFaultMonitor(context.Background(), svc)

	assert.Nil(t, stop)
	assert.Nil(t, svc.Rt.VLMFault.Load(), "failed monitor must not be published")
	assert.Equal(t, 1, seam.builtCount())

	var dst CommsSnapshot

	svc.Snapshot(&dst)
	assert.Equal(t, gpio.FaultSnapshot{}, dst.VLMUSBFault)
}

func TestStartVLMFaultMonitor_PublishesFaultAndStops(t *testing.T) {
	seam := &faultSeam{lines: newFakeFaultLines(0)} // fault present at boot
	svc := newFaultTestService(t, true, true, seam)
	svc.Cfg.VLMUSBFaultLine = "GPIO26"

	stop := svc.Cfg.startVLMFaultMonitor(context.Background(), svc)
	require.NotNil(t, stop)

	seam.mu.Lock()
	assert.Equal(t, "GPIO26", seam.builtLine, "configured line reaches the monitor")
	assert.Equal(t, "GPIO26", seam.openLine)
	seam.mu.Unlock()

	waitFaultRead(t, seam.lines) // initial read
	seam.fireEdge()              // barrier: consumed only after the initial apply
	waitFaultRead(t, seam.lines)

	var dst CommsSnapshot

	svc.Snapshot(&dst)
	assert.True(t, dst.VLMUSBFault.Monitored)
	assert.True(t, dst.VLMUSBFault.Asserted)
	assert.Equal(t, int64(1), dst.VLMUSBFault.Assertions)
	assert.NotZero(t, dst.VLMUSBFault.LastAssertUnixNano)

	stop() // cancels and joins the watch goroutine

	assert.True(t, seam.lines.isClosed(), "line released before stop returns")

	svc.Snapshot(&dst)
	assert.False(t, dst.VLMUSBFault.Monitored)
}

func TestStartVLMFaultMonitor_ParentCtxCancelStops(t *testing.T) {
	seam := &faultSeam{lines: newFakeFaultLines(1)}
	svc := newFaultTestService(t, true, true, seam)

	ctx, cancel := context.WithCancel(context.Background())

	stop := svc.Cfg.startVLMFaultMonitor(ctx, svc)
	require.NotNil(t, stop)

	cancel()
	stop() // must not block once the parent ctx is gone

	assert.True(t, seam.lines.isClosed())
}

func TestSnapshot_VLMUSBFaultZeroWithoutMonitor(t *testing.T) {
	t.Parallel()

	var nilSvc *Service

	dst := CommsSnapshot{VLMUSBFault: gpio.FaultSnapshot{Assertions: 3, Monitored: true}}

	nilSvc.Snapshot(&dst)
	assert.Equal(t, gpio.FaultSnapshot{}, dst.VLMUSBFault)

	dst.VLMUSBFault.Assertions = 3

	(&Service{Cfg: &CommsConfig{}}).Snapshot(&dst)
	assert.Equal(t, gpio.FaultSnapshot{}, dst.VLMUSBFault, "nil runtime reads as zero")

	dst.VLMUSBFault.Assertions = 3

	newSelectTestService(t, 1).Snapshot(&dst)
	assert.Equal(t, gpio.FaultSnapshot{}, dst.VLMUSBFault, "no monitor reads as zero")
}

func TestNewComms_CopiesVLMUSBFaultFields(t *testing.T) {
	t.Parallel()

	cfg := NewComms(CommsConfig{VLMUSBFaultEnable: true, VLMUSBFaultLine: "GPIO26"})

	assert.True(t, cfg.VLMUSBFaultEnable)
	assert.Equal(t, "GPIO26", cfg.VLMUSBFaultLine)
}

func TestBuildCommsConfig_VLMUSBFault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		set        map[string]any
		wantEnable bool
		wantLine   string
	}{
		{name: "defaults off with GPIO25", wantEnable: false, wantLine: gpio.DefaultVLMUSBFaultLine},
		{
			name:       "operator opt-in and custom line",
			set:        map[string]any{"comms.vlmUsbFault.enable": true, "comms.vlmUsbFault.line": "GPIO26"},
			wantEnable: true,
			wantLine:   "GPIO26",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			v := viper.New()
			for k, val := range tc.set {
				v.Set(k, val)
			}

			m := NewCommsManager(config.NewWithoutWatch(v), zerolog.Nop(), nil)
			cfg := m.buildCommsConfig()

			assert.Equal(t, tc.wantEnable, cfg.VLMUSBFaultEnable)
			assert.Equal(t, tc.wantLine, cfg.VLMUSBFaultLine)
		})
	}
}
