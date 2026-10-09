package comms

import (
	"context"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/comms/control"
)

func (f *countingStartHA) cleanupCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.cleanups
}

func newBindingTestConfig(b *fakeAudioBinder, fake *countingStartHA) *CommsConfig {
	return &CommsConfig{
		Log:                  zerolog.Nop(),
		ControlSource:        defaultCtrlSrc,
		binder:               b,
		startHardwareAudioFn: fake.fn,
	}
}

// startAudio brings audio up the way Start does and installs the cleanup.
func startAudio(t *testing.T, cfg *CommsConfig, rt *CommsRuntime) {
	t.Helper()

	rt.audioCleanup = cfg.initAudioIO(context.Background(), rt)
	require.NotNil(t, rt.audioCleanup)
	require.NotNil(t, rt.Broadcast())
}

func TestPairedAudioGate_unboundSkipsHardwareAudio(t *testing.T) {
	fake := &countingStartHA{succeeded: make(chan struct{})}
	cfg := newBindingTestConfig(newFakeAudioBinder(false, 0), fake)
	rt := &CommsRuntime{}

	assert.Nil(t, cfg.initAudioIO(context.Background(), rt))
	assert.False(t, cfg.tryAudioRecovery(rt, 1))
	assert.Zero(t, fake.callCount(), "no device bound: never open the ALSA default card")
}

func TestPairedAudioGate_exemptions(t *testing.T) {
	tests := []struct {
		cfg  func(*CommsConfig, *fakeAudioBinder)
		name string
	}{
		{name: "operator ALSA_CARD override", cfg: func(_ *CommsConfig, b *fakeAudioBinder) { b.override = "2" }},
		{name: "explicit device spec", cfg: func(c *CommsConfig, _ *fakeAudioBinder) { c.BluetoothInputDevice = "hw:2" }},
		{name: "non-OpenVLM source", cfg: func(c *CommsConfig, _ *fakeAudioBinder) { c.binder = nil; c.ControlSource = "nanoptt" }},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeAudioBinder(false, 0)
			cfg := newBindingTestConfig(b, &countingStartHA{})
			tc.cfg(cfg, b)

			assert.NoError(t, cfg.pairedAudioGate())
		})
	}
}

func TestReconcileAudioBinding(t *testing.T) {
	tests := []struct {
		change       func(*fakeAudioBinder)
		name         string
		wantCleanups int
		wantCalls    int
		wantCard     int
		wantAudioUp  bool
	}{
		{name: "same card is a no-op", change: func(b *fakeAudioBinder) { b.bind(3) }, wantCalls: 1, wantCard: 3, wantAudioUp: true},
		{name: "loss tears audio down", change: func(b *fakeAudioBinder) { b.lose() }, wantCleanups: 1, wantCalls: 1},
		{name: "renumbered card restarts audio", change: func(b *fakeAudioBinder) { b.bind(6) }, wantCleanups: 1, wantCalls: 2, wantCard: 6, wantAudioUp: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			b := newFakeAudioBinder(true, 3)
			fake := &countingStartHA{succeeded: make(chan struct{})}
			cfg := newBindingTestConfig(b, fake)
			rt := &CommsRuntime{}

			startAudio(t, cfg, rt)
			assert.Equal(t, 3, rt.audioCard)

			tc.change(b)

			rec := audioRecovery{}
			cfg.reconcileAudioBinding(rt, b, &rec)

			assert.Equal(t, tc.wantCleanups, fake.cleanupCount())
			assert.Equal(t, tc.wantCalls, fake.callCount())
			assert.Equal(t, tc.wantAudioUp, rt.Broadcast() != nil)

			if tc.wantAudioUp {
				assert.Equal(t, tc.wantCard, rt.audioCard)
				assert.True(t, rt.audioCardSet)
			}
		})
	}
}

func TestReconcileAudioBinding_overrideIsIgnored(t *testing.T) {
	b := newFakeAudioBinder(true, 3)
	fake := &countingStartHA{succeeded: make(chan struct{})}
	cfg := newBindingTestConfig(b, fake)
	rt := &CommsRuntime{}

	startAudio(t, cfg, rt)

	b.override = "3"
	b.lose()

	cfg.reconcileAudioBinding(rt, b, &audioRecovery{})
	assert.Zero(t, fake.cleanupCount())
	assert.NotNil(t, rt.Broadcast())
}

func TestTeardownHardwareAudio_endsActiveTransmission(t *testing.T) {
	b := newFakeAudioBinder(true, 3)
	cfg := newBindingTestConfig(b, &countingStartHA{succeeded: make(chan struct{})})
	stream := &mockStream{}
	cleanups := 0
	rt := &CommsRuntime{audioCleanup: func() { cleanups++ }}
	rt.SetBroadcast(stream)
	rt.Broadcasting.Store(true)

	cfg.teardownHardwareAudio(rt)

	assert.False(t, rt.Broadcasting.Load())
	assert.Equal(t, 1, stream.txDisableCalls, "TX gate must close before the stream is freed")
	assert.Equal(t, 1, cleanups)
	assert.Nil(t, rt.Broadcast())
	assert.Nil(t, rt.audioCleanup)
}

// TestRun_followsPairedDeviceAcrossReplug drives the real Run loop through
// unplug → replug-with-renumbered-card and checks local audio follows.
func TestRun_followsPairedDeviceAcrossReplug(t *testing.T) {
	b := newFakeAudioBinder(true, 3)
	fake := &countingStartHA{succeeded: make(chan struct{})}
	cfg := newBindingTestConfig(b, fake)
	cfg.audioRecoveryInterval = time.Hour // only binder events may recover
	rt := &CommsRuntime{}

	startAudio(t, cfg, rt)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})

	go func() {
		cfg.Run(ctx, rt, &mockEventSource{ch: make(chan control.PTTEvent)})
		close(done)
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	waitFor := func(what string, cond func() bool) {
		t.Helper()

		tick := time.NewTicker(time.Millisecond)
		defer tick.Stop()

		deadline := time.After(2 * time.Second)

		for !cond() {
			select {
			case <-tick.C:
			case <-deadline:
				t.Fatalf("timed out waiting for %s", what)
			}
		}
	}

	b.lose()
	waitFor("teardown after unplug", func() bool { return fake.cleanupCount() == 1 })

	b.bind(7)
	waitFor("audio restart after replug", func() bool { return fake.callCount() == 2 })

	cancel()
	<-done

	assert.NotNil(t, rt.Broadcast())
	assert.Equal(t, 7, rt.audioCard)
}

func TestService_SnapshotDeviceBinding_unboundOutsidePairedSources(t *testing.T) {
	var snap CommsSnapshot

	var nilSvc *Service

	nilSvc.Snapshot(&snap)
	assert.Equal(t, -1, snap.DeviceBinding.ALSACard)
	assert.False(t, snap.DeviceBinding.Bound)

	svc := &Service{Cfg: &CommsConfig{ControlSource: "nanoptt"}}
	svc.Snapshot(&snap)
	assert.Equal(t, -1, snap.DeviceBinding.ALSACard)
	assert.False(t, snap.DeviceBinding.Bound)
}
