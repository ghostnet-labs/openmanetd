package comms

import (
	"errors"
	"time"

	"github.com/openmanet/openmanetd/internal/comms/device"
)

// errNoPairedCard is returned instead of opening hardware audio when the
// OpenVLM control source has no bound CM108 device. Opening the ALSA
// default device in that state would capture/play on whatever card happens
// to be card 0 (HDMI, an onboard codec, another USB headset) — the
// wrong-device audio the pairing exists to prevent.
var errNoPairedCard = errors.New("comms: no paired OpenVLM/CM108 audio card bound")

// audioBinder is the slice of *device.Binder the comms runtime needs to
// follow the paired ALSA card across unplug, replug and renumbering.
type audioBinder interface {
	Changed() <-chan struct{}
	Current() (device.Selection, bool)
	CardOverride() (string, bool)
}

// audioBinding returns the binder that governs hardware audio, or nil when
// the control source does not pair HID and audio (nanoptt, roip, web,
// bluetooth). Only the OpenVLM source reads the same USB device's HID
// interface, so only it can tell the runtime when that device goes away.
func (cfg *CommsConfig) audioBinding() audioBinder {
	if cfg.binder != nil {
		return cfg.binder
	}

	if cfg.ControlSource != defaultCtrlSrc {
		return nil
	}

	return device.DefaultBinder()
}

// followedBinder returns the binder hardware audio follows, or nil when
// audio is not tied to the paired device: no OpenVLM binder, an operator
// ALSA_CARD override, or explicitly named capture/playback devices.
func (cfg *CommsConfig) followedBinder() audioBinder {
	b := cfg.audioBinding()
	if b == nil || cfg.BluetoothInputDevice != "" || cfg.BluetoothOutputDevice != "" {
		return nil
	}

	if _, override := b.CardOverride(); override {
		return nil
	}

	return b
}

// pairedAudioGate reports whether hardware audio may be opened now. It
// refuses only when audio follows the paired device and none is bound.
func (cfg *CommsConfig) pairedAudioGate() error {
	b := cfg.followedBinder()
	if b == nil {
		return nil
	}

	if _, bound := b.Current(); !bound {
		return errNoPairedCard
	}

	return nil
}

// recordAudioCard remembers which bound card the just-opened hardware
// audio uses, so a later loss or rebind to a different (renumbered) card
// is detected. Audio that does not follow the binding is left unrecorded
// and is never restarted by reconcileAudioBinding. Called on the
// Start/Run goroutine only.
func (cfg *CommsConfig) recordAudioCard(rt *CommsRuntime) {
	rt.audioCardSet = false

	b := cfg.followedBinder()
	if b == nil {
		return
	}

	if sel, bound := b.Current(); bound {
		rt.audioCard = sel.Device.ALSACardIdx
		rt.audioCardSet = true
	}
}

// teardownHardwareAudio stops the local capture/playback streams in-run,
// leaving the comms runtime (RTP relay, receive loops, control source)
// alive. Any active transmission is ended first so the TX gate closes on a
// live encoder. Called on the Run goroutine only.
func (cfg *CommsConfig) teardownHardwareAudio(rt *CommsRuntime) {
	if rt.Broadcasting.Load() {
		cfg.endTransmission(rt)
	}

	rt.SetBroadcast(nil)

	if rt.audioCleanup != nil {
		rt.audioCleanup()
		rt.audioCleanup = nil
	}

	rt.audioCardSet = false
}

// reconcileAudioBinding runs on the Run goroutine whenever the binder
// signals a change. It restarts hardware audio when the paired device it
// was opened on is gone or its card was renumbered, and tries to bring
// audio up immediately when a device has just been bound. Audio that does
// not follow the binding (see followedBinder / recordAudioCard) is left
// alone.
func (cfg *CommsConfig) reconcileAudioBinding(rt *CommsRuntime, b audioBinder, rec *audioRecovery) {
	if cfg.followedBinder() == nil {
		return
	}

	sel, bound := b.Current()

	if rt.Broadcast() != nil {
		if !rt.audioCardSet || (bound && rt.audioCard == sel.Device.ALSACardIdx) {
			return
		}

		ev := cfg.Log.Warn().Bool("bound", bound).Int("from_card", rt.audioCard)
		if bound {
			ev = ev.Int("to_card", sel.Device.ALSACardIdx).Str("sys_path", sel.Device.SysPath)
		}

		ev.Msg("comms: paired audio device changed; restarting hardware audio")

		cfg.teardownHardwareAudio(rt)
	}

	if !bound {
		rec.arm()

		return
	}

	rec.attempts++

	if cfg.tryAudioRecovery(rt, rec.attempts) {
		rec.disarm()

		return
	}

	rec.arm()
}

// audioRecovery is the Run loop's in-run hardware audio retry ticker. It is
// owned by the Run goroutine. A nil ticker (c == nil) is inert in select.
type audioRecovery struct {
	tick     *time.Ticker
	c        <-chan time.Time
	interval time.Duration
	attempts int
}

// arm starts the ticker if recovery is enabled and not already running.
func (r *audioRecovery) arm() {
	if r.interval <= 0 || r.tick != nil {
		return
	}

	r.tick = time.NewTicker(r.interval)
	r.c = r.tick.C
}

// disarm stops the ticker and resets the attempt count (which throttles
// failure logging). Safe to call when not armed.
func (r *audioRecovery) disarm() {
	if r.tick == nil {
		return
	}

	r.tick.Stop()
	r.tick = nil
	r.c = nil
	r.attempts = 0
}
