package openmanet

import (
	"context"

	"github.com/openmanet/openmanetd/internal/comms"
	"github.com/rs/zerolog"
)

// commsGuard implements hardware.CommsGuard over the live comms transmit
// state. The OpenVLM audio device sits on the HaLow USB hub, so the
// recovery ladder skips the hub reset while PTT is keyed rather than cut
// the transmission off mid-sentence.
//
// The guard holds no mutable state: transmitting resolves the comms
// service through an atomic pointer and reads an atomic flag, so the
// hardware step goroutine can call it while comms starts, stops or
// toggles PTT on its own goroutines.
type commsGuard struct {
	log zerolog.Logger
	// transmitting reports whether a PTT transmit is in progress. It must
	// be safe for concurrent use. Nil means comms is never present.
	transmitting func() bool
}

// newCommsGuard returns a guard backed by the process-wide comms service
// (comms.Default). It reports no transmit while comms is disabled or
// stopped, so the ladder then behaves as before.
func newCommsGuard(log zerolog.Logger) *commsGuard {
	return &commsGuard{
		log:          log,
		transmitting: func() bool { return comms.Default().Transmitting() },
	}
}

// TransmitActive implements hardware.CommsGuard.
func (g *commsGuard) TransmitActive() bool {
	return g.transmitting != nil && g.transmitting()
}

// BeforeHubReset implements hardware.CommsGuard. The pulse is 10 ms and
// comms handles the audio device dropping off the bus on its own, so the
// guard only logs the reset to give the resulting comms device-loss
// messages a visible cause.
func (g *commsGuard) BeforeHubReset(_ context.Context) {
	g.log.Warn().Msg("hardware: USB hub reset; the OpenVLM audio device will re-enumerate")
}
