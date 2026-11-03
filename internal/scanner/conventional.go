package scanner

import (
	"context"
	"errors"
	"time"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// The types below are the consumer-side contract with the conventional
// (analog FM / aviation AM) scanner that runs on the scan RTL-SDR
// (internal/scanner/conventional, wired in by the daemon).

// ErrTooManyAudioStreams is returned by SubscribeAudio when the
// conventional scanner's audio stream limit is reached.
var ErrTooManyAudioStreams = errors.New("too many scanner audio streams")

// AudioFrameSamples is the PCM length of one AudioFrame: 20 ms at 8 kHz.
const AudioFrameSamples = 160

// AudioSampleRate is the PCM rate of AudioFrame.
const AudioSampleRate = 8000

// AudioFrame is 20 ms of demodulated 8 kHz mono PCM from one channel. It
// is passed by value (no pooled buffers), so a subscriber may keep it.
// Only frames with the squelch open are delivered.
type AudioFrame struct {
	Time time.Time
	// ChannelKey is the sites.Channel key the audio came from.
	ChannelKey  string
	FrequencyHz uint64
	// Talkgroup is the P25 talkgroup for trunked voice; 0 for
	// conventional channels.
	Talkgroup uint32
	// Sequence counts frames per subscription, from 1.
	Sequence uint32
	// Level is the squelch metric (NFM: noise relative to deviation; AM:
	// carrier dBFS).
	Level float32
	PCM   [AudioFrameSamples]int16
}

// ConventionalChannel is a channel selected for scanning.
type ConventionalChannel struct {
	Channel *sites.Channel
	// DistanceKm is NaN when the channel has no position.
	DistanceKm float64
	AlwaysOn   bool
}

// ConventionalPlan tells the conventional scanner which receiver to use
// and what to scan. The zero plan stops it and releases the receiver.
type ConventionalPlan struct {
	// Serial is the scan RTL-SDR's USB serial.
	Serial string
	// Channels are the selected channels sorted by frequency.
	Channels []ConventionalChannel
	// UsableHz is the usable bandwidth of one capture window.
	UsableHz uint64
}

// ConventionalState is the conventional scanner state.
type ConventionalState uint8

// ConventionalState values.
const (
	ConventionalUnknown ConventionalState = iota
	// ConventionalUnavailable: no scanner wired or the capture tool is not
	// installed.
	ConventionalUnavailable
	// ConventionalIdle: no plan (scanner disabled or no channels).
	ConventionalIdle
	// ConventionalReceiverBusy: the scan RTL-SDR decodes the P25 control
	// channel because no wide receiver is configured.
	ConventionalReceiverBusy
	// ConventionalScanning: stepping through windows.
	ConventionalScanning
	// ConventionalHolding: a channel's squelch is open (or was within the
	// hold time) and the scanner stays on its window.
	ConventionalHolding
	// ConventionalError: the capture failed; it is retried with backoff.
	ConventionalError
)

// ConventionalChannelStatus is one scanned channel's live state.
type ConventionalChannelStatus struct {
	LastHeard time.Time
	ConventionalChannel
	// Level is the last squelch metric measured (see AudioFrame.Level).
	Level float64
	// Window is the 1-based capture window holding the channel, 0 when it
	// fits no window (it is listed but not scanned).
	Window int
	// Active is true while the squelch is open.
	Active bool
}

// ConventionalStatus is the conventional scanner's status.
type ConventionalStatus struct {
	Detail   string
	Channels []ConventionalChannelStatus
	// CenterHz is the current capture center (0 when not capturing).
	CenterHz uint64
	// Window is the 1-based window being captured; Windows the count.
	Window     int
	Windows    int
	SampleRate int
	State      ConventionalState
}

// ConventionalScanner is the conventional scanner as the manager uses it.
// Scan hands over a plan without blocking on the capture; the zero plan
// stops it. WaitIdle blocks until no capture process holds the receiver.
// SubscribeAudio streams PCM frames of channelKey ("" for every channel);
// the channel closes when cancel is called or the scanner stops. A
// subscription for a channel also pins the scan to that channel's window.
type ConventionalScanner interface {
	Scan(plan ConventionalPlan) error
	WaitIdle(ctx context.Context) error
	Status() ConventionalStatus
	SubscribeAudio(channelKey string) (<-chan AudioFrame, func(), error)
}

// ReceiverReleaser is implemented by a ControlChannelMonitor that can
// report when it has released a receiver, so the manager can hand the
// scan RTL-SDR to the conventional scanner without both opening it.
type ReceiverReleaser interface {
	WaitReleased(ctx context.Context, serial string) error
}
