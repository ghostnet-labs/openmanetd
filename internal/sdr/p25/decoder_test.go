package p25_test

import (
	"math/rand/v2"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/sdr/p25"
	"github.com/openmanet/openmanetd/internal/sdr/p25/p25test"
)

const (
	testNAC  = 0x293
	testWACN = 0xBEE00
	testSys  = 0x3A1
	iden800  = 1
	idenUHF  = 2
	idenTDMA = 3
	base800  = 851_006_250
	baseUHF  = 450_000_000
)

// siteFrames is a representative control channel sequence: channel plan,
// site identity, a clear grant, an encrypted emergency grant, updates,
// an adjacent site and a Phase II TDMA grant.
func siteFrames() [][]uint8 {
	return [][]uint8{
		p25test.TSDU(testNAC,
			p25test.IdenUp(iden800, 12500, -45_000_000, 6250, base800),
			p25test.NetStatus(1, testWACN, testSys, p25test.Channel(iden800, 0x123), 0x70),
			p25test.RFSSStatus(1, testSys, 1, 7, p25test.Channel(iden800, 0x123), 0x70)),
		p25test.TSDU(testNAC, p25test.Grant(0, p25test.Channel(iden800, 0x2AB), 8001, 1234567)),
		p25test.TSDU(testNAC,
			p25test.IdenUpVU(idenUHF, 0x5, 5_000_000, 12500, baseUHF),
			p25test.Grant(p25test.SvcEncrypted|p25test.SvcEmergency|3, p25test.Channel(idenUHF, 10), 100, 42),
			p25test.GrantUpdate(p25test.Channel(iden800, 5), 200, p25test.Channel(iden800, 6), 300)),
		p25test.TSDU(testNAC,
			p25test.AdjacentStatus(1, 0x3, testSys, 1, 8, p25test.Channel(iden800, 0x200), 0x70),
			p25test.GrantUpdateExplicit(p25test.SvcEncrypted, p25test.Channel(idenUHF, 20), p25test.Channel(idenUHF, 20), 400),
			p25test.IdenUpTDMA(idenTDMA, 3, -45_000_000, 12500, base800)),
		p25test.TSDU(testNAC, p25test.Grant(0, p25test.Channel(idenTDMA, 17), 500, 7)),
	}
}

// expectedGrants are the grants siteFrames must produce, in order.
func expectedGrants() []p25.GroupVoiceGrant {
	return []p25.GroupVoiceGrant{
		{Talkgroup: 8001, Source: 1234567, Channel: 0x12AB, FrequencyHz: base800 + 0x2AB*6250, NAC: testNAC},
		{Talkgroup: 100, Source: 42, Channel: 0x200A, FrequencyHz: baseUHF + 10*12500, NAC: testNAC,
			Encrypted: true, Emergency: true, Priority: 3},
		{Talkgroup: 200, Channel: 0x1005, FrequencyHz: base800 + 5*6250, NAC: testNAC, Update: true},
		{Talkgroup: 300, Channel: 0x1006, FrequencyHz: base800 + 6*6250, NAC: testNAC, Update: true},
		{Talkgroup: 400, Channel: 0x2014, FrequencyHz: baseUHF + 20*12500, NAC: testNAC, Encrypted: true, Update: true},
		{Talkgroup: 500, Source: 7, Channel: 0x3011, FrequencyHz: base800 + 8*12500, Slot: 1, NAC: testNAC},
	}
}

// signal builds IQ for frames with a random preamble and tail.
func signal(m p25test.Modulator, frames [][]uint8, preamble int) []complex64 {
	rng := rand.New(rand.NewPCG(m.Seed, 99))
	dibits := p25test.RandomDibits(rng, preamble)

	for _, f := range frames {
		dibits = append(dibits, f...)
	}

	dibits = append(dibits, p25test.RandomDibits(rng, 24)...)

	return m.Modulate(dibits)
}

// recorder copies every event (events are only valid during emit).
type recorder struct {
	grants   []p25.GroupVoiceGrant
	sites    []p25.SiteStatus
	adjacent []p25.AdjacentSite
	idens    []p25.ChannelIdentifier
	syncs    []p25.SyncState
}

func (r *recorder) emit(e p25.Event) {
	switch ev := e.(type) {
	case *p25.GroupVoiceGrant:
		r.grants = append(r.grants, *ev)
	case *p25.SiteStatus:
		r.sites = append(r.sites, *ev)
	case *p25.AdjacentSite:
		r.adjacent = append(r.adjacent, *ev)
	case *p25.ChannelIdentifier:
		r.idens = append(r.idens, *ev)
	case *p25.SyncState:
		r.syncs = append(r.syncs, *ev)
	}
}

func decodeAll(t *testing.T, rate int, offset float64, iqs []complex64, block int) (*recorder, *p25.Decoder) {
	t.Helper()

	r := &recorder{}
	d, err := p25.NewDecoder(rate, r.emit)
	require.NoError(t, err)
	d.SetFrequencyOffset(offset)

	for i := 0; i < len(iqs); i += block {
		d.Write(iqs[i:min(i+block, len(iqs))])
	}

	return r, d
}

func assertSiteEvents(t *testing.T, r *recorder) {
	t.Helper()

	require.NotEmpty(t, r.syncs)
	assert.True(t, r.syncs[0].Locked)
	assert.Equal(t, uint16(testNAC), r.syncs[0].NAC)

	assert.Equal(t, expectedGrants(), r.grants)

	require.Len(t, r.sites, 2)
	assert.Equal(t, p25.SiteStatus{
		ControlChannelHz: base800 + 0x123*6250, WACN: testWACN, SystemID: testSys, NAC: testNAC,
		Channel: 0x1123, RFSS: 1, Site: 7, LRA: 1, ServiceClass: 0x70,
	}, r.sites[1])

	require.Len(t, r.adjacent, 1)
	assert.Equal(t, p25.AdjacentSite{
		ControlChannelHz: base800 + 0x200*6250, SystemID: testSys, Channel: 0x1200,
		RFSS: 1, Site: 8, LRA: 1, ServiceClass: 0x70, Flags: 0x3,
	}, r.adjacent[0])

	require.Len(t, r.idens, 3)
	assert.Equal(t, p25.ChannelIdentifier{
		BaseHz: base800, TxOffsetHz: -45_000_000, SpacingHz: 6250, BandwidthHz: 12500, Identifier: iden800, Slots: 1,
	}, r.idens[0])
	assert.Equal(t, p25.ChannelIdentifier{
		BaseHz: baseUHF, TxOffsetHz: 5_000_000, SpacingHz: 12500, BandwidthHz: 12500, Identifier: idenUHF, Slots: 1,
	}, r.idens[1])
	assert.Equal(t, p25.ChannelIdentifier{
		BaseHz: base800, TxOffsetHz: -45_000_000, SpacingHz: 12500, BandwidthHz: 12500, Identifier: idenTDMA,
		Slots: 2, TDMA: true,
	}, r.idens[2])
}

func TestDecoder_endToEnd(t *testing.T) {
	cases := []struct {
		name  string
		mod   p25test.Modulator
		block int
	}{
		{"clean_48k", p25test.Modulator{SampleRate: 48000, Amplitude: 0.5}, 480},
		{"rtl_1024k_offset_noise", p25test.Modulator{
			SampleRate: 1_024_000, OffsetHz: 100_000, FreqErrorHz: 400, Amplitude: 0.25, CNRdB: 22, TimingOffset: 0.37, Seed: 1,
		}, 16384},
		{"rtl_2400k_inverted_clock", p25test.Modulator{
			SampleRate: 2_400_000, OffsetHz: -300_000, FreqErrorHz: -900, Amplitude: 0.25, CNRdB: 20,
			ClockPPM: 80, Invert: true, Seed: 2,
		}, 7919},
		{"airspy_2500k", p25test.Modulator{
			SampleRate: 2_500_000, OffsetHz: 625_000, FreqErrorHz: 150, Amplitude: 0.25, CNRdB: 20, TimingOffset: 0.8, Seed: 3,
		}, 65536},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			iqs := signal(tc.mod, siteFrames(), 96)
			r, d := decodeAll(t, int(tc.mod.SampleRate), tc.mod.OffsetHz, iqs, tc.block)

			assertSiteEvents(t, r)
			assert.Equal(t, tc.mod.Invert, r.syncs[0].Inverted)

			st := d.Stats()
			assert.Equal(t, uint64(11), st.TSBKs, "%+v", st)
			assert.Zero(t, st.TSBKBadCRC)
		})
	}
}

func TestDecoder_lossOfSync(t *testing.T) {
	m := p25test.Modulator{SampleRate: 48000, Amplitude: 0.5}
	rng := rand.New(rand.NewPCG(7, 7))
	dibits := append(p25test.RandomDibits(rng, 50), siteFrames()[1]...)
	dibits = append(dibits, p25test.RandomDibits(rng, 6000)...)

	r, _ := decodeAll(t, 48000, 0, m.Modulate(dibits), 1000)

	require.Len(t, r.syncs, 2)
	assert.True(t, r.syncs[0].Locked)
	assert.False(t, r.syncs[1].Locked)
}

func TestDecoder_noiseOnlyEmitsNothing(t *testing.T) {
	r, d := decodeAll(t, 1_024_000, 0, noise(1_024_000, 0.2), 16384)

	assert.Empty(t, r.grants)
	assert.Empty(t, r.syncs)
	assert.Zero(t, d.Stats().TSBKs)
}

func noise(n int, sigma float64) []complex64 {
	rng := rand.New(rand.NewPCG(12, 34))
	out := make([]complex64, n)

	for i := range out {
		out[i] = complex(float32(rng.NormFloat64()*sigma), float32(rng.NormFloat64()*sigma))
	}

	return out
}

// TestDecoder_zeroAllocs runs a repeating control channel through Write
// with 16k-sample blocks and asserts the steady state allocates nothing,
// including while events are being emitted.
func TestDecoder_zeroAllocs(t *testing.T) {
	m := p25test.Modulator{SampleRate: 1_024_000, OffsetHz: 50_000, Amplitude: 0.25, CNRdB: 25, Seed: 5}
	iqs := signal(m, siteFrames(), 48)

	events := 0
	d, err := p25.NewDecoder(1_024_000, func(p25.Event) { events++ })
	require.NoError(t, err)
	d.SetFrequencyOffset(50_000)
	d.Write(iqs) // warm up

	const block = 16384

	pos := 0
	allocs := testing.AllocsPerRun(50, func() {
		if pos+block > len(iqs) {
			pos = 0
		}

		d.Write(iqs[pos : pos+block])
		pos += block
	})

	assert.Zero(t, allocs)
	assert.Positive(t, events)
}

func BenchmarkDecoder_Write16k(b *testing.B) {
	m := p25test.Modulator{SampleRate: 1_024_000, OffsetHz: 50_000, Amplitude: 0.25, CNRdB: 25, Seed: 5}
	iqs := signal(m, siteFrames(), 48)
	d, err := p25.NewDecoder(1_024_000, func(p25.Event) {})
	require.NoError(b, err)
	d.SetFrequencyOffset(50_000)

	const block = 16384

	b.ReportAllocs()
	b.SetBytes(block * 8)

	pos := 0
	for b.Loop() {
		if pos+block > len(iqs) {
			pos = 0
		}

		d.Write(iqs[pos : pos+block])
		pos += block
	}
}
