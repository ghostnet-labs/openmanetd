package platform_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
)

const (
	macA = "02:00:00:00:00:0a"
	macB = "02:00:00:00:00:0b"
)

type testEntry struct {
	payload []byte
	seq     uint32
	band    byte
}

// makeBundle builds a bundle by hand from the documented layout, so the
// tests pin the wire format independently of the encoder.
func makeBundle(boot uint32, entries ...testEntry) []byte {
	b := []byte("OMCM")
	b = append(b, 1)
	b = binary.BigEndian.AppendUint32(b, boot)
	b = append(b, byte(len(entries)))

	for _, e := range entries {
		b = binary.BigEndian.AppendUint32(b, e.seq)
		b = append(b, e.band)
		b = binary.BigEndian.AppendUint16(b, uint16(len(e.payload)))
		b = append(b, e.payload...)
	}

	return b
}

func newTransport(t *testing.T, a *fakeAlfred, mac string, boot uint32) (*platform.AlfredTransport, *fakeGossip) {
	t.Helper()

	g := a.client(t, mac)

	return platform.NewAlfredTransport(g, boot, time.Millisecond, zerolog.Nop()), g
}

func TestAlfredDataType_reserved(t *testing.T) {
	assert.Equal(t, uint8(104), platform.AlfredDataType)
	assert.Equal(t, uint8(1), platform.AlfredDataVersion)
}

func TestAlfredTransport_broadcastEncoding(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	tr, _ := newTransport(t, a, macA, 0xdeadbeef)

	require.NoError(t, tr.Broadcast(ctx, chanmig.BandHaLow, []byte("one")))
	require.NoError(t, tr.Broadcast(ctx, chanmig.Band5G, []byte("two")))

	want := makeBundle(0xdeadbeef,
		testEntry{seq: 1, band: 1, payload: []byte("one")},
		testEntry{seq: 2, band: 3, payload: []byte("two")},
	)
	assert.Equal(t, want, a.record(macA))
}

func TestAlfredTransport_ringKeepsNewestEight(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	tr, _ := newTransport(t, a, macA, 7)

	for i := range 11 {
		require.NoError(t, tr.Broadcast(ctx, chanmig.Band2G4, []byte{byte(i)}))
	}

	entries := make([]testEntry, 0, 8)
	for seq := uint32(4); seq <= 11; seq++ {
		entries = append(entries, testEntry{seq: seq, band: 2, payload: []byte{byte(seq - 1)}})
	}

	assert.Equal(t, makeBundle(7, entries...), a.record(macA))
}

func TestAlfredTransport_broadcastBounds(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	tr, g := newTransport(t, a, macA, 1)

	cases := []struct {
		name    string
		payload []byte
		band    chanmig.Band
	}{
		{name: "empty payload", band: chanmig.BandHaLow, payload: nil},
		{name: "oversize payload", band: chanmig.BandHaLow, payload: make([]byte, chanmig.MaxMessageSize+1)},
		{name: "band zero", band: 0, payload: []byte{1}},
		{name: "unknown band", band: 4, payload: []byte{1}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tr.Broadcast(ctx, tc.band, tc.payload)
			require.ErrorIs(t, err, platform.ErrPayload)
		})
	}

	assert.Equal(t, 0, g.publishCount(), "rejected payloads must not be published")

	require.NoError(t, tr.Broadcast(ctx, chanmig.Band5G, make([]byte, chanmig.MaxMessageSize)),
		"a maximum-size message must fit")

	g.setErrors(errInjected, nil)
	require.ErrorIs(t, tr.Broadcast(ctx, chanmig.Band5G, []byte{1}), errInjected)
}

func TestAlfredTransport_deliversOnlyFreshEntries(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	sender, _ := newTransport(t, a, macA, 100)
	receiver, _ := newTransport(t, a, macB, 200)

	var got sink

	require.NoError(t, sender.Broadcast(ctx, chanmig.BandHaLow, []byte("stale")))

	// First sight of a publisher sets the baseline only: alfred keeps a
	// departed node's record, so it is not evidence of a live peer.
	receiver.PollOnce(ctx, got.handle)
	assert.Empty(t, got.take())

	require.NoError(t, sender.Broadcast(ctx, chanmig.Band5G, []byte("fresh-1")))
	require.NoError(t, sender.Broadcast(ctx, chanmig.Band2G4, []byte("fresh-2")))

	receiver.PollOnce(ctx, got.handle)
	assert.Equal(t, []delivery{
		{band: chanmig.Band5G, payload: []byte("fresh-1")},
		{band: chanmig.Band2G4, payload: []byte("fresh-2")},
	}, got.take())

	receiver.PollOnce(ctx, got.handle)
	assert.Empty(t, got.take(), "an unchanged record is not redelivered")
}

func TestAlfredTransport_ignoresOwnRecord(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	tr, _ := newTransport(t, a, macA, 5)

	var got sink

	require.NoError(t, tr.Broadcast(ctx, chanmig.BandHaLow, []byte("x")))
	tr.PollOnce(ctx, got.handle)
	require.NoError(t, tr.Broadcast(ctx, chanmig.BandHaLow, []byte("y")))
	tr.PollOnce(ctx, got.handle)

	assert.Empty(t, got.take())
}

func TestAlfredTransport_publisherRestartResetsBaseline(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	receiver, _ := newTransport(t, a, macB, 2)

	var got sink

	a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 50, band: 1, payload: []byte("a")}))
	receiver.PollOnce(ctx, got.handle)
	a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 51, band: 1, payload: []byte("b")}))
	receiver.PollOnce(ctx, got.handle)
	require.Len(t, got.take(), 1)

	// Restart: new boot value, sequence numbers start over below 51.
	a.set(mustMAC(t, macA), makeBundle(11, testEntry{seq: 1, band: 1, payload: []byte("c")}))
	receiver.PollOnce(ctx, got.handle)
	assert.Empty(t, got.take(), "a restarted publisher is re-baselined")

	a.set(mustMAC(t, macA), makeBundle(11,
		testEntry{seq: 1, band: 1, payload: []byte("c")},
		testEntry{seq: 2, band: 1, payload: []byte("d")},
	))
	receiver.PollOnce(ctx, got.handle)
	assert.Equal(t, []delivery{{band: chanmig.BandHaLow, payload: []byte("d")}}, got.take())
}

func TestAlfredTransport_forgetsVanishedSources(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	receiver, _ := newTransport(t, a, macB, 2)

	var got sink

	a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 1, band: 1, payload: []byte("a")}))
	receiver.PollOnce(ctx, got.handle)

	a.drop(macA)
	receiver.PollOnce(ctx, got.handle)

	// Back after expiry: treated as a first sighting again.
	a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 2, band: 1, payload: []byte("b")}))
	receiver.PollOnce(ctx, got.handle)
	assert.Empty(t, got.take())
}

func TestAlfredTransport_fetchErrorDeliversNothing(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	receiver, g := newTransport(t, a, macB, 2)

	var got sink

	a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 1, band: 1, payload: []byte("a")}))
	receiver.PollOnce(ctx, got.handle)

	g.setErrors(nil, errInjected)
	a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 2, band: 1, payload: []byte("b")}))
	receiver.PollOnce(ctx, got.handle)
	assert.Empty(t, got.take())

	// The failed poll must not have forgotten the source.
	g.setErrors(nil, nil)
	receiver.PollOnce(ctx, got.handle)
	assert.Equal(t, []delivery{{band: chanmig.BandHaLow, payload: []byte("b")}}, got.take())
}

func TestAlfredTransport_malformedBundles(t *testing.T) {
	valid := makeBundle(10, testEntry{seq: 5, band: 1, payload: []byte("ok")})

	oversize := makeBundle(10, testEntry{seq: 5, band: 1, payload: make([]byte, chanmig.MaxMessageSize+1)})

	tooMany := make([]testEntry, 0, 9)
	for i := range 9 {
		tooMany = append(tooMany, testEntry{seq: uint32(5 + i), band: 1, payload: []byte{1}})
	}

	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: nil},
		{name: "short header", data: valid[:9]},
		{name: "bad magic", data: append([]byte("XXXX"), valid[4:]...)},
		{name: "bad format", data: append(append([]byte("OMCM"), 2), valid[5:]...)},
		{name: "truncated entry", data: valid[:len(valid)-1]},
		{name: "trailing bytes", data: append(bytes.Clone(valid), 0)},
		{name: "zero length entry", data: makeBundle(10, testEntry{seq: 5, band: 1})},
		{name: "band zero", data: makeBundle(10, testEntry{seq: 5, band: 0, payload: []byte{1}})},
		{name: "band four", data: makeBundle(10, testEntry{seq: 5, band: 4, payload: []byte{1}})},
		{name: "oversize entry", data: oversize},
		{name: "too many entries", data: makeBundle(10, tooMany...)},
		{name: "huge record", data: make([]byte, 64<<10)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			a := newFakeAlfred()
			receiver, _ := newTransport(t, a, macB, 2)

			var got sink

			a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 4, band: 1, payload: []byte("base")}))
			receiver.PollOnce(ctx, got.handle)

			a.set(mustMAC(t, macA), tc.data)
			receiver.PollOnce(ctx, got.handle)
			assert.Empty(t, got.take(), "a malformed bundle delivers nothing")

			a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 9, band: 2, payload: []byte("next")}))
			receiver.PollOnce(ctx, got.handle)
			assert.Equal(t, []delivery{{band: chanmig.Band2G4, payload: []byte("next")}}, got.take(),
				"state survives a malformed bundle")
		})
	}
}

func TestAlfredTransport_ignoresNonEthernetSource(t *testing.T) {
	ctx := context.Background()
	receiver := platform.NewAlfredTransport(&rawGossip{records: []gossipRecord{
		{Source: []byte{1, 2, 3}, Data: makeBundle(1, testEntry{seq: 1, band: 1, payload: []byte("x")})},
	}}, 2, time.Millisecond, zerolog.Nop())

	var got sink

	receiver.PollOnce(ctx, got.handle)
	receiver.PollOnce(ctx, got.handle)
	assert.Empty(t, got.take())
}

func TestAlfredTransport_runStopsOnCancel(t *testing.T) {
	a := newFakeAlfred()
	sender, _ := newTransport(t, a, macA, 1)
	receiver, _ := newTransport(t, a, macB, 2)

	ctx, cancel := context.WithCancel(context.Background())
	delivered := make(chan struct{}, 1)
	done := make(chan struct{})

	go func() {
		defer close(done)

		receiver.Run(ctx, func(context.Context, chanmig.Band, []byte) {
			select {
			case delivered <- struct{}{}:
			default:
			}
		})
	}()

	t.Cleanup(func() {
		cancel()
		<-done
	})

	require.NoError(t, sender.Broadcast(ctx, chanmig.BandHaLow, []byte("a")))

	// Keep publishing until the poll loop, which first baselines the
	// sender, delivers a fresh entry.
	tick := time.NewTicker(time.Millisecond)
	defer tick.Stop()

	for {
		select {
		case <-delivered:
			cancel()
			<-done

			return
		case <-tick.C:
			require.NoError(t, sender.Broadcast(ctx, chanmig.BandHaLow, []byte("b")))
		}
	}
}

func TestAlfredTransport_boundsTrackedSources(t *testing.T) {
	ctx := context.Background()
	a := newFakeAlfred()
	receiver, _ := newTransport(t, a, macB, 2)

	var got sink

	const publishers = 300

	macs := make([]net.HardwareAddr, 0, publishers)
	for i := range publishers {
		macs = append(macs, net.HardwareAddr{0x02, 0x10, 0, 0, byte(i >> 8), byte(i)})
	}

	for i, m := range macs {
		a.set(m, makeBundle(uint32(1000+i), testEntry{seq: 1, band: 1, payload: []byte{1}}))
	}

	receiver.PollOnce(ctx, got.handle)

	for i, m := range macs {
		a.set(m, makeBundle(uint32(1000+i), testEntry{seq: 2, band: 1, payload: []byte{2}}))
	}

	receiver.PollOnce(ctx, got.handle)
	assert.Len(t, got.take(), 256, "receive state is capped at 256 publishers")
}

func FuzzAlfredTransport_poll(f *testing.F) {
	f.Add(makeBundle(10, testEntry{seq: 2, band: 1, payload: []byte("x")}))
	f.Add([]byte("OMCM"))
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		ctx := context.Background()
		a := newFakeAlfred()
		receiver, _ := newTransport(t, a, macB, 2)

		a.set(mustMAC(t, macA), makeBundle(10, testEntry{seq: 1, band: 1, payload: []byte("base")}))
		receiver.PollOnce(ctx, func(context.Context, chanmig.Band, []byte) {})

		a.set(mustMAC(t, macA), data)
		receiver.PollOnce(ctx, func(_ context.Context, b chanmig.Band, p []byte) {
			if b < chanmig.BandHaLow || b > chanmig.Band5G || len(p) == 0 || len(p) > chanmig.MaxMessageSize {
				t.Fatalf("delivered out-of-bounds message: band %d, %d bytes", b, len(p))
			}
		})
	})
}

// rawGossip returns fixed records.
type rawGossip struct {
	records []gossipRecord
}

func (g *rawGossip) Publish([]byte) error { return nil }

func (g *rawGossip) Fetch() ([]gossipRecord, error) { return g.records, nil }

func mustMAC(t *testing.T, s string) net.HardwareAddr {
	t.Helper()

	hw, err := net.ParseMAC(s)
	require.NoError(t, err)

	return hw
}
