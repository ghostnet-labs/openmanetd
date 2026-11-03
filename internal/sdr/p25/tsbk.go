package p25

import "encoding/binary"

// Outbound TSBK opcodes (TIA-102.AABC) with the standard MFID 0x00.
const (
	opGroupVoiceGrant       = 0x00
	opGroupVoiceGrantUpdate = 0x02
	opGroupVoiceGrantUpdExp = 0x03
	opIdenUpTDMA            = 0x33
	opIdenUpVU              = 0x34
	opRFSSStatus            = 0x3A
	opNetStatus             = 0x3B
	opAdjacentStatus        = 0x3C
	opIdenUp                = 0x3D
)

// Service option bits (TIA-102.AABC 2.2.1).
const (
	svcEmergency = 0x80
	svcProtected = 0x40
	svcPriority  = 0x07
)

// tdmaSlots maps the IDEN_UP_TDMA channel type to slots per carrier.
func tdmaSlots(channelType uint8) uint8 {
	switch channelType {
	case 0, 1, 2:
		return 1
	case 3, 5:
		return 2
	case 4:
		return 4
	default:
		return 0
	}
}

// vuBandwidth decodes the 4-bit IDEN_UP_VU bandwidth code.
func vuBandwidth(code uint8) uint32 {
	switch code {
	case 0x4:
		return 6250
	case 0x5:
		return 12500
	default:
		return 0
	}
}

// handleTSBK parses one CRC-valid trunking signaling block and emits the
// resulting events.
func (d *Decoder) handleTSBK(b *[12]byte) {
	if b[0]&0x40 != 0 || b[1] != 0 {
		// Protected (encrypted) TSBK or manufacturer-specific opcode.
		return
	}

	args := binary.BigEndian.Uint64(b[2:10])

	switch b[0] & 0x3F {
	case opGroupVoiceGrant:
		d.emitGrant(uint8(args>>56), uint16(args>>40), uint16(args>>24), uint32(args&0xFFFFFF), false)
	case opGroupVoiceGrantUpdate:
		d.emitGrant(0, uint16(args>>48), uint16(args>>32), 0, true)

		chB, grpB := uint16(args>>16), uint16(args)
		if chB != uint16(args>>48) || grpB != uint16(args>>32) {
			d.emitGrant(0, chB, grpB, 0, true)
		}
	case opGroupVoiceGrantUpdExp:
		// Service options, reserved, channel T (downlink), channel R, group.
		d.emitGrant(uint8(args>>56), uint16(args>>32), uint16(args), 0, true)
	case opIdenUp:
		d.handleIdenUp(args)
	case opIdenUpVU, opIdenUpTDMA:
		d.handleIdenUpVU(args, b[0]&0x3F == opIdenUpTDMA)
	case opRFSSStatus:
		s := &d.site
		s.LRA = uint8(args >> 56)
		s.SystemID = uint16(args>>40) & 0xFFF
		s.RFSS = uint8(args >> 32)
		s.Site = uint8(args >> 24)
		s.Channel = uint16(args >> 8)
		s.ServiceClass = uint8(args)

		d.emitSite()
	case opNetStatus:
		s := &d.site
		s.LRA = uint8(args >> 56)
		s.WACN = uint32(args>>36) & 0xFFFFF
		s.SystemID = uint16(args>>24) & 0xFFF
		s.Channel = uint16(args >> 8)
		s.ServiceClass = uint8(args)

		d.emitSite()
	case opAdjacentStatus:
		a := &d.adjacent
		a.LRA = uint8(args >> 56)
		a.Flags = uint8(args>>52) & 0xF
		a.SystemID = uint16(args>>40) & 0xFFF
		a.RFSS = uint8(args >> 32)
		a.Site = uint8(args >> 24)
		a.Channel = uint16(args >> 8)
		a.ServiceClass = uint8(args)
		a.ControlChannelHz, _ = frequency(&d.idens, a.Channel)
		d.emit(a)
	}
}

func (d *Decoder) emitGrant(svc uint8, ch, group uint16, src uint32, update bool) {
	g := &d.grant
	*g = GroupVoiceGrant{
		Source:    src,
		Talkgroup: group,
		Channel:   ch,
		NAC:       d.nac,
		Priority:  svc & svcPriority,
		Encrypted: svc&svcProtected != 0,
		Emergency: svc&svcEmergency != 0,
		Update:    update,
	}
	g.FrequencyHz, g.Slot = frequency(&d.idens, ch)
	d.emit(g)
}

func (d *Decoder) emitSite() {
	d.site.NAC = d.nac
	d.site.ControlChannelHz, _ = frequency(&d.idens, d.site.Channel)
	d.emit(&d.site)
}

// handleIdenUp decodes IDEN_UP: identifier(4) bandwidth(9) tx offset(9,
// sign bit then 8 bits of 250 kHz) spacing(10, 125 Hz) base(32, 5 Hz).
func (d *Decoder) handleIdenUp(args uint64) {
	id := &d.idens[args>>60]
	*id = ChannelIdentifier{
		Identifier:  uint8(args >> 60),
		BandwidthHz: uint32(args>>51&0x1FF) * 125,
		SpacingHz:   uint32(args>>32&0x3FF) * 125,
		BaseHz:      (args & 0xFFFFFFFF) * 5,
		Slots:       1,
	}

	off := int64(args>>42&0xFF) * 250_000
	if args>>50&1 == 0 {
		off = -off
	}

	id.TxOffsetHz = off
	d.emit(id)
}

// handleIdenUpVU decodes IDEN_UP_VU and IDEN_UP_TDMA, which share a layout:
// identifier(4) bandwidth or channel type(4) tx offset sign(1) tx
// offset(13, in channel spacings) spacing(10) base(32).
func (d *Decoder) handleIdenUpVU(args uint64, tdma bool) {
	code := uint8(args >> 56 & 0xF)
	id := &d.idens[args>>60]
	*id = ChannelIdentifier{
		Identifier: uint8(args >> 60),
		SpacingHz:  uint32(args>>32&0x3FF) * 125,
		BaseHz:     (args & 0xFFFFFFFF) * 5,
		Slots:      1,
		TDMA:       tdma,
	}

	if tdma {
		id.Slots = tdmaSlots(code)
		id.BandwidthHz = 12500
	} else {
		id.BandwidthHz = vuBandwidth(code)
	}

	off := int64(args>>42&0x1FFF) * int64(id.SpacingHz)
	if args>>55&1 == 0 {
		off = -off
	}

	id.TxOffsetHz = off
	d.emit(id)
}
