package weather_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/weather"
)

// fakeClock is a settable clock. step, when set, advances it on every
// read so code waiting for a deadline sees time pass.
type fakeClock struct {
	mu   sync.Mutex // protects the fields below
	t    time.Time
	step time.Duration
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 11, 7, 18, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.t
	c.t = c.t.Add(c.step)

	return now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.t = c.t.Add(d)
}

// fakeModbusDevice answers Modbus-RTU read requests from a register
// table, like a SEN0657 on the other end of the RS485 adapter.
type fakeModbusDevice struct {
	mu sync.Mutex // protects the fields below
	// regs maps register address to value.
	regs map[uint16]uint16
	// exceptOn makes a request that covers this register fail with
	// exception 2 (illegal data address), as a device that rejects
	// undocumented registers would.
	exceptOn *uint16
	// replies overrides the reply to an exact request frame.
	replies map[string][]byte
	writes  [][]byte
	pending bytes.Buffer
	// chunk limits bytes per Read to exercise frame reassembly.
	chunk     int
	flushes   int
	readErr   error
	silent    bool
	corrupt   bool
	closed    bool
	addr      byte
	replyAddr byte
}

func newFakeModbusDevice(regs map[uint16]uint16) *fakeModbusDevice {
	return &fakeModbusDevice{regs: regs, addr: 1, replyAddr: 1, replies: map[string][]byte{}}
}

func (d *fakeModbusDevice) Write(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.writes = append(d.writes, bytes.Clone(b))

	if d.silent {
		return len(b), nil
	}

	if r, ok := d.replies[string(b)]; ok {
		d.pending.Write(r)

		return len(b), nil
	}

	if len(b) != 8 || b[0] != d.addr || b[1] != 0x03 {
		return len(b), nil
	}

	start := uint16(b[2])<<8 | uint16(b[3])
	count := uint16(b[4])<<8 | uint16(b[5])

	if d.exceptOn != nil && *d.exceptOn >= start && *d.exceptOn < start+count {
		d.pending.Write(withCRC([]byte{d.replyAddr, 0x83, 0x02}))

		return len(b), nil
	}

	frame := []byte{d.replyAddr, 0x03, byte(2 * count)}
	for r := start; r < start+count; r++ {
		v := d.regs[r]
		frame = append(frame, byte(v>>8), byte(v))
	}

	frame = withCRC(frame)
	if d.corrupt {
		frame[len(frame)-1] ^= 0xFF
	}

	d.pending.Write(frame)

	return len(b), nil
}

func (d *fakeModbusDevice) Read(b []byte) (int, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.readErr != nil {
		return 0, d.readErr
	}

	if d.pending.Len() == 0 {
		return 0, io.EOF
	}

	if d.chunk > 0 && len(b) > d.chunk {
		b = b[:d.chunk]
	}

	return d.pending.Read(b) //nolint:wrapcheck // fake port passes bytes.Buffer results through
}

func (d *fakeModbusDevice) Flush() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.flushes++
	d.pending.Reset()

	return nil
}

func (d *fakeModbusDevice) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.closed = true

	return nil
}

func (d *fakeModbusDevice) getWrites() [][]byte {
	d.mu.Lock()
	defer d.mu.Unlock()

	return append([][]byte(nil), d.writes...)
}

// withCRC appends the Modbus CRC (low byte first). It is written out
// independently of the package so tests pin the package's CRC against
// the datasheet frames rather than against itself.
func withCRC(b []byte) []byte {
	crc := uint16(0xFFFF)
	for _, v := range b {
		crc ^= uint16(v)
		for range 8 {
			if crc&1 == 1 {
				crc = (crc >> 1) ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}

	return append(b, byte(crc&0xFF), byte(crc>>8))
}

// fakeSensor returns scripted samples. reads signals each Read so tests
// can wait for the poll loop without sleeping.
type fakeSensor struct {
	mu      sync.Mutex // protects the fields below
	sample  weather.Sample
	err     error
	calls   int
	closed  bool
	readsCh chan struct{}
}

func (s *fakeSensor) Read(ctx context.Context) (weather.Sample, error) {
	s.mu.Lock()
	s.calls++
	sample, err, ch := s.sample, s.err, s.readsCh
	s.mu.Unlock()

	if ch != nil {
		select {
		case ch <- struct{}{}:
		default:
		}
	}

	if ctx.Err() != nil {
		return weather.Sample{}, ctx.Err() //nolint:wrapcheck // fake passes context errors through
	}

	return sample, err
}

func (s *fakeSensor) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.closed = true

	return nil
}

func (s *fakeSensor) set(sample weather.Sample, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.sample, s.err = sample, err
}

func (s *fakeSensor) isClosed() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	return s.closed
}

// fakeGossip is an in-memory alfred data type store.
type fakeGossip struct {
	mu         sync.Mutex // protects the fields below
	published  [][]byte
	records    []weather.GossipRecord
	publishErr error
	fetchErr   error
}

func (g *fakeGossip) Publish(payload []byte) error {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.publishErr != nil {
		return g.publishErr
	}

	g.published = append(g.published, bytes.Clone(payload))

	return nil
}

func (g *fakeGossip) Fetch() ([]weather.GossipRecord, error) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.fetchErr != nil {
		return nil, g.fetchErr
	}

	return append([]weather.GossipRecord(nil), g.records...), nil
}

func (g *fakeGossip) setRecords(recs ...weather.GossipRecord) {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.records = recs
}

func (g *fakeGossip) getPublished() [][]byte {
	g.mu.Lock()
	defer g.mu.Unlock()

	return append([][]byte(nil), g.published...)
}

var errInjected = errors.New("injected")
