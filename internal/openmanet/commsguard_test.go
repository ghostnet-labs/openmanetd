package openmanet

import (
	"bytes"
	"context"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/openmanet/openmanetd/internal/comms"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

var _ hardware.CommsGuard = (*commsGuard)(nil)

func TestCommsGuard_TransmitActive(t *testing.T) {
	var keyed atomic.Bool

	g := &commsGuard{log: zerolog.Nop(), transmitting: keyed.Load}

	assert.False(t, g.TransmitActive(), "idle")

	keyed.Store(true)
	assert.True(t, g.TransmitActive(), "PTT keyed")

	keyed.Store(false)
	assert.False(t, g.TransmitActive(), "PTT released")
}

func TestCommsGuard_TransmitActive_noSource(t *testing.T) {
	g := &commsGuard{log: zerolog.Nop()}

	assert.False(t, g.TransmitActive())
}

// TestNewCommsGuard_followsCommsService drives the production guard through
// the real comms service: no service, a service that is idle, keyed and
// released, and comms stopping again.
func TestNewCommsGuard_followsCommsService(t *testing.T) {
	comms.SetDefault(nil)
	t.Cleanup(func() { comms.SetDefault(nil) })

	g := newCommsGuard(zerolog.Nop())
	assert.False(t, g.TransmitActive(), "comms not running")

	rt := &comms.CommsRuntime{}
	comms.SetDefault(&comms.Service{Rt: rt})
	assert.False(t, g.TransmitActive(), "comms running, PTT idle")

	rt.Broadcasting.Store(true)
	assert.True(t, g.TransmitActive(), "PTT keyed")

	rt.Broadcasting.Store(false)
	assert.False(t, g.TransmitActive(), "PTT released")

	rt.Broadcasting.Store(true)
	comms.SetDefault(nil)
	assert.False(t, g.TransmitActive(), "comms stopped")
}

// TestNewCommsGuard_concurrent runs the guard against concurrent PTT
// toggles and comms restarts; run with -race.
func TestNewCommsGuard_concurrent(t *testing.T) {
	comms.SetDefault(nil)
	t.Cleanup(func() { comms.SetDefault(nil) })

	g := newCommsGuard(zerolog.Nop())
	rt := &comms.CommsRuntime{}

	var wg sync.WaitGroup

	wg.Add(1)

	go func() {
		defer wg.Done()

		for i := range 1000 {
			if i%100 == 0 {
				comms.SetDefault(&comms.Service{Rt: rt})
			}

			rt.Broadcasting.Store(i%2 == 0)
		}
	}()

	for range 1000 {
		_ = g.TransmitActive()
	}

	wg.Wait()
}

func TestCommsGuard_BeforeHubResetLogs(t *testing.T) {
	var buf bytes.Buffer

	g := newCommsGuard(zerolog.New(&buf))
	g.BeforeHubReset(context.Background())

	assert.Contains(t, buf.String(), "USB hub reset")
	assert.Contains(t, buf.String(), `"level":"warn"`)
}
