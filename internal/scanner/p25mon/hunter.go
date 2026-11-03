package p25mon

import (
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/sdr/p25"
	"github.com/openmanet/openmanetd/internal/sdr/source"
)

// hunter walks a plan's control channels. It survives capture restarts
// for the same plan so a failing tool does not reset the hunt. Every
// method except newHunter runs on the capture goroutine (inside
// source.Run via consume and the decoder's emit callback), so it needs no
// locking.
type hunter struct {
	mon        *Monitor
	dec        *p25.Decoder
	src        *source.Source
	plan       scanner.TunePlan
	center     uint64 // current tuner center
	idx        int    // index into plan.ControlHz being decoded
	since      int    // samples consumed on idx without sync
	limit      int    // samples per channel before stepping (0: never step)
	locked     bool
	everLocked bool // any sync since the session started
}

func newHunter(plan scanner.TunePlan, limit int) *hunter {
	h := &hunter{plan: plan, limit: limit}
	h.center = h.centerFor(h.freq())

	return h
}

// freq is the control channel being decoded.
func (h *hunter) freq() uint64 {
	if len(h.plan.ControlHz) == 0 {
		return 0
	}

	return h.plan.ControlHz[h.idx]
}

// centerFor returns the tuner center that receives f: the plan center
// when f is inside its window and clear of DC, otherwise a center
// dcClearHz below f.
func (h *hunter) centerFor(f uint64) uint64 {
	if h.plan.CenterHz != 0 && h.plan.UsableHz != 0 {
		off := absDiff(f, h.plan.CenterHz)
		if off >= minOffsetHz && off+edgeGuardHz <= h.plan.UsableHz/2 {
			return h.plan.CenterHz
		}
	}

	return f - dcClearHz
}

// attach binds a new decoder and source for a capture session.
func (h *hunter) attach(m *Monitor, dec *p25.Decoder, src *source.Source) {
	h.mon, h.dec, h.src = m, dec, src
	h.since, h.locked = 0, false
	h.dec.SetFrequencyOffset(float64(int64(h.freq()) - int64(h.center)))
}

// consume is the source.Consumer: decode, then step the hunt when the
// current channel has had its share of samples without sync.
func (h *hunter) consume(samples []complex64) {
	h.dec.Write(samples)

	if h.locked || h.limit <= 0 || len(h.plan.ControlHz) < 2 {
		return
	}

	h.since += len(samples)
	if h.since >= h.limit {
		h.step()
	}
}

// step moves to the next control channel, retuning the receiver when the
// channel needs a different center, and reports the channel now hunted.
func (h *hunter) step() {
	h.idx = (h.idx + 1) % len(h.plan.ControlHz)
	h.since = 0

	f := h.freq()
	if c := h.centerFor(f); c != h.center {
		h.center = c
		h.src.Retune(c)
	}

	h.dec.SetFrequencyOffset(float64(int64(f) - int64(h.center)))
	h.mon.send(scanner.DecoderStatus{FrequencyHz: f})
}

// emitFunc returns the decoder callback. p25 events are reused after the
// callback returns, so each is copied into a scanner value before it is
// queued.
func (h *hunter) emitFunc() func(p25.Event) {
	return func(ev p25.Event) {
		switch e := ev.(type) {
		case *p25.SyncState:
			h.locked, h.since = e.Locked, 0
			h.everLocked = h.everLocked || e.Locked
			h.mon.send(scanner.SyncState{Locked: e.Locked})

			if e.Locked {
				h.mon.send(scanner.DecoderStatus{FrequencyHz: h.freq()})
			}
		case *p25.GroupVoiceGrant:
			h.mon.send(scanner.VoiceGrant{
				FrequencyHz: e.FrequencyHz,
				Talkgroup:   uint32(e.Talkgroup),
				Source:      e.Source,
				Channel:     uint32(e.Channel),
				Encrypted:   e.Encrypted,
				Emergency:   e.Emergency,
			})
		case *p25.SiteStatus:
			h.mon.send(scanner.SiteStatus{
				ControlChannelHz: e.ControlChannelHz,
				WACN:             e.WACN,
				SystemID:         uint32(e.SystemID),
				RFSS:             uint32(e.RFSS),
				Site:             uint32(e.Site),
				NAC:              uint32(e.NAC),
			})
		case *p25.AdjacentSite:
			h.mon.send(scanner.AdjacentSite{
				ControlChannelHz: e.ControlChannelHz,
				SystemID:         uint32(e.SystemID),
				RFSS:             uint32(e.RFSS),
				Site:             uint32(e.Site),
			})
		}
	}
}

func absDiff(a, b uint64) uint64 {
	if a > b {
		return a - b
	}

	return b - a
}
