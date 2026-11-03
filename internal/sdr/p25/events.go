package p25

// Event is a decoded control channel occurrence delivered to the emit
// callback passed to NewDecoder. Concrete events are pointers to
// *GroupVoiceGrant, *SiteStatus, *AdjacentSite, *ChannelIdentifier and
// *SyncState.
//
// Events are owned by the Decoder and reused: an event is only valid for
// the duration of the emit call. Copy the pointed-to struct (g := *e) to
// keep it. This is what lets Decoder.Write run without allocating.
type Event interface {
	p25Event()
}

// GroupVoiceGrant reports a talkgroup call assigned to a voice channel,
// from GRP_V_CH_GRANT, GRP_V_CH_GRANT_UPDT and GRP_V_CH_GRANT_UPDT_EXP.
type GroupVoiceGrant struct {
	// FrequencyHz is the downlink frequency of Channel, or 0 when the
	// channel identifier has not been announced yet (no IDEN_UP seen).
	FrequencyHz uint64 `json:"frequency_hz"`
	// Source is the calling unit ID. It is 0 for grant updates, which do
	// not carry the source.
	Source    uint32 `json:"source"`
	Talkgroup uint16 `json:"talkgroup"`
	// Channel is the raw 16-bit channel: 4-bit identifier, 12-bit number.
	Channel uint16 `json:"channel"`
	NAC     uint16 `json:"nac"`
	// Slot is the TDMA slot (0 or 1) when the channel identifier
	// describes a Phase II TDMA carrier; always 0 for FDMA.
	Slot uint8 `json:"slot"`
	// Priority is the 3-bit service option priority (1 lowest .. 7).
	Priority uint8 `json:"priority"`
	// Encrypted is the service option "protected" bit: the call is
	// encrypted and its audio cannot be monitored.
	Encrypted bool `json:"encrypted"`
	Emergency bool `json:"emergency"`
	// Update is set for GRP_V_CH_GRANT_UPDT(_EXP) messages, which repeat
	// grants of calls already in progress.
	Update bool `json:"update"`
}

// SiteStatus describes the site whose control channel is being decoded. It
// merges RFSS_STS_BCST (RFSS, site, system) and NET_STS_BCST (WACN), so
// fields are zero until the corresponding broadcast has been seen.
type SiteStatus struct {
	ControlChannelHz uint64 `json:"control_channel_hz"`
	WACN             uint32 `json:"wacn"`
	SystemID         uint16 `json:"system_id"`
	NAC              uint16 `json:"nac"`
	Channel          uint16 `json:"channel"`
	RFSS             uint8  `json:"rfss"`
	Site             uint8  `json:"site"`
	LRA              uint8  `json:"lra"`
	ServiceClass     uint8  `json:"service_class"`
}

// AdjacentSite is a neighbor announced by ADJ_STS_BCST.
type AdjacentSite struct {
	ControlChannelHz uint64 `json:"control_channel_hz"`
	SystemID         uint16 `json:"system_id"`
	Channel          uint16 `json:"channel"`
	RFSS             uint8  `json:"rfss"`
	Site             uint8  `json:"site"`
	LRA              uint8  `json:"lra"`
	ServiceClass     uint8  `json:"service_class"`
	// Flags holds the C (conventional), F (failure), V (valid) and A
	// (active network connection) bits in its low nibble, C in bit 3.
	Flags uint8 `json:"flags"`
}

// ChannelIdentifier is one entry of the site's channel plan from IDEN_UP,
// IDEN_UP_VU or IDEN_UP_TDMA. Channel number n of this identifier is
// transmitted at BaseHz + (n / Slots) * SpacingHz.
type ChannelIdentifier struct {
	BaseHz      uint64 `json:"base_hz"`
	TxOffsetHz  int64  `json:"tx_offset_hz"`
	SpacingHz   uint32 `json:"spacing_hz"`
	BandwidthHz uint32 `json:"bandwidth_hz"`
	Identifier  uint8  `json:"identifier"`
	Slots       uint8  `json:"slots"`
	TDMA        bool   `json:"tdma"`
}

// SyncState reports acquisition (first valid NID) and loss of the control
// channel (no valid frame for one second of symbols).
type SyncState struct {
	NAC    uint16 `json:"nac"`
	Locked bool   `json:"locked"`
	// Inverted is set when the signal arrived with inverted deviation
	// polarity (spectrum-inverted receiver or mixing).
	Inverted bool `json:"inverted"`
}

func (*GroupVoiceGrant) p25Event()   {}
func (*SiteStatus) p25Event()        {}
func (*AdjacentSite) p25Event()      {}
func (*ChannelIdentifier) p25Event() {}
func (*SyncState) p25Event()         {}

// frequency maps a 16-bit channel field through the identifier table. It
// returns the downlink frequency (0 when the identifier is unknown) and
// the TDMA slot.
func frequency(idens *[16]ChannelIdentifier, ch uint16) (uint64, uint8) {
	id := &idens[ch>>12]
	if id.SpacingHz == 0 || id.Slots == 0 {
		return 0, 0
	}

	n := uint64(ch & 0xFFF)
	slots := uint64(id.Slots)

	return id.BaseHz + n/slots*uint64(id.SpacingHz), uint8(n % slots)
}
