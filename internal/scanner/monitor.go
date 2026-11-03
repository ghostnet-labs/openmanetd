package scanner

import "context"

// The types below are the consumer-side contract with the P25
// control-channel decoder (internal/sdr, wired in by the daemon). The
// decoder's adapter sends values of these types on Events(); anything else
// is ignored.

// VoiceGrant is a group voice channel grant or grant update decoded from
// the control channel.
type VoiceGrant struct {
	// FrequencyHz is the voice channel frequency the grant points at.
	FrequencyHz uint64
	// Talkgroup is the 16-bit P25 talkgroup ID.
	Talkgroup uint32
	// Source is the 24-bit talker unit ID; zero when not carried (grant
	// updates omit it).
	Source uint32
	// Channel is the 16-bit P25 channel (4-bit identifier, 12-bit number).
	Channel uint32
	// Encrypted and Emergency are the grant's service-option bits.
	Encrypted bool
	Emergency bool
}

// SiteStatus is the decoded identity of the site being monitored (RFSS
// status / network status broadcasts).
type SiteStatus struct {
	ControlChannelHz uint64
	WACN             uint32
	SystemID         uint32
	RFSS             uint32
	Site             uint32
	NAC              uint32
}

// AdjacentSite is a neighbor site announced on the control channel. The
// manager counts it as decoder activity only.
type AdjacentSite struct {
	ControlChannelHz uint64
	SystemID         uint32
	RFSS             uint32
	Site             uint32
}

// SyncState reports whether the decoder holds frame sync.
type SyncState struct {
	Locked bool
}

// DecoderStatus reports the decoder's health between decodes: which
// control channel it is hunting on, or why it is not running. A nil Err
// means the capture is running on FrequencyHz.
type DecoderStatus struct {
	// Err is why the capture is not running (process exited, bad
	// receiver configuration).
	Err error
	// FrequencyHz is the control channel being decoded or hunted.
	FrequencyHz uint64
	// Unavailable is set with Err when the capture tool is not
	// installed: the decoder cannot run at all on this node.
	Unavailable bool
}

// Tuner names the receiver a TunePlan is for.
type Tuner uint8

// Tuner values.
const (
	// TunerNone is the zero plan: stop the decoder.
	TunerNone Tuner = iota
	// TunerWide is the Airspy wide receiver (receivers.wideSerial).
	TunerWide
	// TunerScan is the RTL-SDR scan receiver (receivers.scanSerial), used
	// for the control channel when no wide receiver is configured.
	TunerScan
)

// TunePlan tells the decoder where to put the wide tuner and which control
// channels to hunt. The zero TunePlan stops the decoder.
type TunePlan struct {
	// SiteKey is the sites.Site key being monitored.
	SiteKey string
	// Serial is the USB serial of the receiver to capture with.
	Serial string
	// ControlHz are the site's control channels in hunt order: those
	// inside the tuner window first (source order), then the rest. The
	// decoder hunts across them until it locks, moving the tuner for a
	// channel outside the window.
	ControlHz []uint64
	// VoiceHz are the voice channels inside the window.
	VoiceHz []uint64
	// CenterHz is the tuner center frequency.
	CenterHz uint64
	// UsableHz is the usable bandwidth the plan was built for.
	UsableHz uint64
	// Tuner is the receiver kind Serial names.
	Tuner Tuner
}

// ControlChannelMonitor is the P25 control-channel decoder as the manager
// uses it. Tune is called from the manager goroutine whenever the selected
// site changes (and with a zero plan on shutdown); it should return once
// the tuner is retuned, not block until lock. Events delivers VoiceGrant,
// SiteStatus, AdjacentSite, SyncState and DecoderStatus values; a closed
// channel means the decoder stopped.
type ControlChannelMonitor interface {
	Tune(ctx context.Context, plan TunePlan) error
	Events() <-chan any
}
