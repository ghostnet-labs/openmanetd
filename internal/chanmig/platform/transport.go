package platform

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"sync"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

// alfred data type reservation. The values 100..103 are taken by
// openmanet.network.v1.DataType (gateway, address reservation, node,
// mesh neighbors); 104 is reserved here for channel migration and must be
// added to proto/openmanet/network/v1/datatype.proto as
// DATA_TYPE_CHANNEL_MIGRATION when that file is next revised.
const (
	// AlfredDataType is the alfred data type carrying chanmig bundles.
	AlfredDataType uint8 = 104
	// AlfredDataVersion is the alfred record version for the bundle
	// format below. Bump it on any non-additive framing change.
	AlfredDataVersion uint8 = 1
)

// Bundle framing. alfred keeps exactly one record per (data type, source),
// so a node republishes a bundle of its last bundleSlots messages on every
// Broadcast. Layout, all integers big-endian:
//
//	magic(4) "OMCM" | format(1) | boot(4) | count(1) |
//	count x { seq(4) | band(1) | len(2) | payload(len) }
//
// boot is a per-process random value so receivers can tell a restarted
// publisher (whose seq starts over) from a replay. Entries are in send
// order, oldest first.
const (
	bundleMagic      = "OMCM"
	bundleFormat     = 1
	bundleHeaderSize = 4 + 1 + 4 + 1
	bundleEntryHead  = 4 + 1 + 2
	// bundleSlots bounds the bundle. One tick sends at most one advert
	// per band plus relayed acks, so eight slots cover a full tick with
	// room to spare while keeping the record near 1 KiB.
	bundleSlots   = 8
	maxBundleSize = bundleHeaderSize + bundleSlots*(bundleEntryHead+chanmig.MaxMessageSize)
	// maxSources bounds receive-side state; it matches the Node's peer
	// table bound.
	maxSources = 256
	// DefaultPollInterval is how often the transport asks alfred for
	// peer bundles. alfred itself only synchronizes every ~10 s, so
	// polling faster only shortens local latency.
	DefaultPollInterval = 2 * time.Second
)

// Errors returned by the transport.
var (
	// ErrBundle reports a bundle that cannot be decoded.
	ErrBundle = errors.New("chanmig/platform: malformed bundle")
	// ErrPayload reports a Broadcast payload the transport cannot carry.
	ErrPayload = errors.New("chanmig/platform: payload not transportable")
)

// GossipRecord is one alfred record: the publishing node's MAC and its
// data.
type GossipRecord struct {
	Source net.HardwareAddr
	Data   []byte
}

// Gossip is the alfred surface the transport needs, bound to one data
// type. The daemon adapts *alfred.Client to it.
type Gossip interface {
	Publish(payload []byte) error
	Fetch() ([]GossipRecord, error)
}

// Handler receives one decoded message. The payload is only valid for the
// duration of the call.
type Handler func(ctx context.Context, band chanmig.Band, payload []byte)

type slot struct {
	seq     uint32
	n       uint16
	band    chanmig.Band
	payload [chanmig.MaxMessageSize]byte
}

type sourceKey [6]byte

type sourceState struct {
	gen  uint64
	boot uint32
	seq  uint32
}

type bundleEntry struct {
	payload []byte
	seq     uint32
	band    chanmig.Band
}

// AlfredTransport implements chanmig.Transport over alfred gossip.
// Broadcast may be called from any goroutine; Run must be called from
// exactly one goroutine.
type AlfredTransport struct { //nolint:govet // fieldalignment: mutexes stay above the fields they guard per .claude/rules/concurrency.md
	gossip Gossip
	log    zerolog.Logger
	poll   time.Duration
	boot   uint32

	// sendMu serializes Broadcast. It is held across Publish on purpose
	// so bundles reach alfred in sequence order; nothing else takes it.
	sendMu sync.Mutex // protects the fields below
	ring   [bundleSlots]slot
	head   int
	count  int
	seq    uint32
	out    []byte

	// Receive state is owned by the Run goroutine.
	sources map[sourceKey]sourceState
	entries []bundleEntry
	gen     uint64
}

// NewAlfredTransport returns a transport publishing through g. boot must
// differ across daemon restarts (pass a random value); poll <= 0 selects
// DefaultPollInterval.
func NewAlfredTransport(g Gossip, boot uint32, poll time.Duration, log zerolog.Logger) *AlfredTransport {
	if poll <= 0 {
		poll = DefaultPollInterval
	}

	return &AlfredTransport{
		gossip:  g,
		log:     log,
		poll:    poll,
		boot:    boot,
		out:     make([]byte, 0, maxBundleSize),
		sources: make(map[sourceKey]sourceState, maxSources),
		entries: make([]bundleEntry, 0, bundleSlots),
	}
}

// Broadcast implements chanmig.Transport. alfred floods over batman-adv,
// which spans every band, so band travels as a tag: receivers hand the
// message to the Node as heard on that band.
func (t *AlfredTransport) Broadcast(_ context.Context, band chanmig.Band, payload []byte) error {
	if len(payload) == 0 || len(payload) > chanmig.MaxMessageSize {
		return fmt.Errorf("%w: length %d", ErrPayload, len(payload))
	}

	if band < chanmig.BandHaLow || band > chanmig.Band5G {
		return fmt.Errorf("%w: band %d", ErrPayload, band)
	}

	t.sendMu.Lock()
	defer t.sendMu.Unlock()

	t.seq++
	s := &t.ring[(t.head+t.count)%bundleSlots]

	if t.count == bundleSlots {
		t.head = (t.head + 1) % bundleSlots
	} else {
		t.count++
	}

	s.seq = t.seq
	s.band = band
	s.n = uint16(copy(s.payload[:], payload))

	t.out = t.appendBundle(t.out[:0])

	if err := t.gossip.Publish(t.out); err != nil {
		return fmt.Errorf("alfred publish: %w", err)
	}

	return nil
}

// appendBundle encodes the ring. The caller holds sendMu.
func (t *AlfredTransport) appendBundle(buf []byte) []byte {
	buf = append(buf, bundleMagic...)
	buf = append(buf, bundleFormat)
	buf = binary.BigEndian.AppendUint32(buf, t.boot)
	buf = append(buf, byte(t.count))

	for i := range t.count {
		s := &t.ring[(t.head+i)%bundleSlots]
		buf = binary.BigEndian.AppendUint32(buf, s.seq)
		buf = append(buf, byte(s.band))
		buf = binary.BigEndian.AppendUint16(buf, s.n)
		buf = append(buf, s.payload[:s.n]...)
	}

	return buf
}

// parseBundle validates b completely and returns its boot value and
// entries (appended to out, aliasing b). Nothing is returned for a bundle
// with any defect, so a corrupt record never delivers a partial message.
func parseBundle(b []byte, out []bundleEntry) (uint32, []bundleEntry, error) {
	if len(b) < bundleHeaderSize || len(b) > maxBundleSize {
		return 0, out, fmt.Errorf("%w: length %d", ErrBundle, len(b))
	}

	if string(b[:4]) != bundleMagic || b[4] != bundleFormat {
		return 0, out, fmt.Errorf("%w: bad magic or format", ErrBundle)
	}

	boot := binary.BigEndian.Uint32(b[5:])
	count := int(b[9])

	if count > bundleSlots {
		return 0, out, fmt.Errorf("%w: %d entries", ErrBundle, count)
	}

	start := len(out)
	rest := b[bundleHeaderSize:]

	for range count {
		if len(rest) < bundleEntryHead {
			return 0, out[:start], fmt.Errorf("%w: truncated entry", ErrBundle)
		}

		e := bundleEntry{
			seq:  binary.BigEndian.Uint32(rest),
			band: chanmig.Band(rest[4]),
		}
		n := int(binary.BigEndian.Uint16(rest[5:]))
		rest = rest[bundleEntryHead:]

		if n == 0 || n > chanmig.MaxMessageSize || n > len(rest) {
			return 0, out[:start], fmt.Errorf("%w: entry length %d", ErrBundle, n)
		}

		if e.band < chanmig.BandHaLow || e.band > chanmig.Band5G {
			return 0, out[:start], fmt.Errorf("%w: band %d", ErrBundle, e.band)
		}

		e.payload = rest[:n]
		rest = rest[n:]

		out = append(out, e)
	}

	if len(rest) != 0 {
		return 0, out[:start], fmt.Errorf("%w: trailing bytes", ErrBundle)
	}

	return boot, out, nil
}

// Run polls alfred every poll interval and hands new messages to h until
// ctx is done.
func (t *AlfredTransport) Run(ctx context.Context, h Handler) {
	ticker := time.NewTicker(t.poll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			t.PollOnce(ctx, h)
		}
	}
}

// PollOnce fetches every peer bundle once and delivers entries newer than
// the last seen sequence number for that publisher. The first bundle seen
// from a publisher (or from a restarted one) only sets the baseline: alfred
// keeps a departed node's record for minutes, so an entry is trusted as
// live only once the publisher has demonstrably published again. Sources
// that alfred no longer returns are forgotten. Must not be called
// concurrently with Run.
func (t *AlfredTransport) PollOnce(ctx context.Context, h Handler) {
	records, err := t.gossip.Fetch()
	if err != nil {
		t.log.Debug().Err(err).Msg("chanmig: alfred fetch failed")

		return
	}

	t.gen++

	for i := range records {
		t.receive(ctx, &records[i], h)
	}

	for k, st := range t.sources {
		if st.gen != t.gen {
			delete(t.sources, k)
		}
	}
}

func (t *AlfredTransport) receive(ctx context.Context, rec *GossipRecord, h Handler) {
	if len(rec.Source) != len(sourceKey{}) {
		return
	}

	var key sourceKey

	copy(key[:], rec.Source)

	prev, known := t.sources[key]

	boot, entries, err := parseBundle(rec.Data, t.entries[:0])
	t.entries = entries[:0]

	if err != nil {
		t.log.Debug().Err(err).Str("source", rec.Source.String()).Msg("chanmig: dropped alfred bundle")

		// Keep a known publisher's baseline: one bad record must not
		// turn its next good one into a first sighting.
		if known {
			prev.gen = t.gen
			t.sources[key] = prev
		}

		return
	}

	if boot == t.boot {
		return // our own record
	}

	if !known && len(t.sources) >= maxSources {
		return
	}

	last := latestSeq(entries)

	if !known || prev.boot != boot {
		t.sources[key] = sourceState{gen: t.gen, boot: boot, seq: last}

		return
	}

	t.sources[key] = sourceState{gen: t.gen, boot: boot, seq: max(last, prev.seq)}

	for _, e := range entries {
		if e.seq > prev.seq {
			h(ctx, e.band, e.payload)
		}
	}
}

func latestSeq(entries []bundleEntry) uint32 {
	var s uint32

	for _, e := range entries {
		s = max(s, e.seq)
	}

	return s
}
