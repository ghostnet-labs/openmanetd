package weather

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

// Modbus-RTU function codes and frame sizes.
const (
	fcReadHolding  byte = 0x03
	exceptionFlag  byte = 0x80
	requestLen          = 8
	headerLen           = 3
	crcLen              = 2
	maxReadCount        = 125
	defaultTimeout      = time.Second
)

// ErrTimeout is returned when the sensor does not answer in time.
var ErrTimeout = errors.New("modbus: no response")

// ErrCRC is returned when a response fails its checksum.
var ErrCRC = errors.New("modbus: bad checksum")

// ExceptionError is a Modbus exception response, for example 2 (illegal
// data address).
type ExceptionError struct {
	Code byte
}

func (e *ExceptionError) Error() string {
	return fmt.Sprintf("modbus: exception %d", e.Code)
}

// Port is a serial line. Read may return (0, io.EOF) or (0, nil) when no
// byte arrived within the line's read timeout; the client treats both as
// "nothing yet" until its own deadline passes.
type Port interface {
	io.Reader
	io.Writer
}

// flusher is implemented by ports that can drop unread input, so a late
// reply to an earlier request cannot be taken for the next one.
type flusher interface {
	Flush() error
}

// rtuClient reads holding registers from one Modbus-RTU device. It is not
// safe for concurrent use; the Monitor's poll loop is its only caller.
type rtuClient struct {
	port    Port
	now     func() time.Time
	buf     []byte
	timeout time.Duration
	addr    byte
}

func newRTUClient(port Port, addr byte, timeout time.Duration, now func() time.Time) *rtuClient {
	if timeout <= 0 {
		timeout = defaultTimeout
	}

	if now == nil {
		now = time.Now
	}

	return &rtuClient{
		port:    port,
		addr:    addr,
		timeout: timeout,
		now:     now,
		buf:     make([]byte, headerLen+2*maxReadCount+crcLen),
	}
}

// readHolding reads count registers starting at start into dst, which it
// returns resliced. dst may be nil.
func (c *rtuClient) readHolding(ctx context.Context, start, count uint16, dst []uint16) ([]uint16, error) {
	if count == 0 || count > maxReadCount {
		return dst, fmt.Errorf("modbus: register count %d outside [1, %d]", count, maxReadCount)
	}

	if f, ok := c.port.(flusher); ok {
		if err := f.Flush(); err != nil {
			return dst, fmt.Errorf("modbus: flush: %w", err)
		}
	}

	var req [requestLen]byte

	req[0] = c.addr
	req[1] = fcReadHolding
	req[2], req[3] = byte(start>>8), byte(start)
	req[4], req[5] = byte(count>>8), byte(count)
	crc := crc16(req[:6])
	req[6], req[7] = byte(crc), byte(crc>>8)

	if _, err := c.port.Write(req[:]); err != nil {
		return dst, fmt.Errorf("modbus: write: %w", err)
	}

	deadline := c.now().Add(c.timeout)

	head := c.buf[:headerLen]
	if err := c.readFull(ctx, head, deadline); err != nil {
		return dst, err
	}

	if head[0] != c.addr {
		return dst, fmt.Errorf("modbus: reply from address %d, want %d", head[0], c.addr)
	}

	if head[1] == fcReadHolding|exceptionFlag {
		if err := c.readFull(ctx, c.buf[headerLen:headerLen+crcLen], deadline); err != nil {
			return dst, err
		}

		if !crcOK(c.buf[:headerLen+crcLen]) {
			return dst, ErrCRC
		}

		return dst, &ExceptionError{Code: head[2]}
	}

	if head[1] != fcReadHolding {
		return dst, fmt.Errorf("modbus: reply function 0x%02x, want 0x%02x", head[1], fcReadHolding)
	}

	n := int(head[2])
	if n != 2*int(count) {
		return dst, fmt.Errorf("modbus: reply has %d data bytes, want %d", n, 2*count)
	}

	frame := c.buf[:headerLen+n+crcLen]
	if err := c.readFull(ctx, frame[headerLen:], deadline); err != nil {
		return dst, err
	}

	if !crcOK(frame) {
		return dst, ErrCRC
	}

	dst = dst[:0]

	for i := range int(count) {
		hi, lo := frame[headerLen+2*i], frame[headerLen+2*i+1]
		dst = append(dst, uint16(hi)<<8|uint16(lo))
	}

	return dst, nil
}

// readFull fills b from the port, waiting until deadline.
func (c *rtuClient) readFull(ctx context.Context, b []byte, deadline time.Time) error {
	got := 0
	for got < len(b) {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("modbus: %w", err)
		}

		n, err := c.port.Read(b[got:])
		got += n

		switch {
		case err == nil, errors.Is(err, io.EOF):
		default:
			return fmt.Errorf("modbus: read: %w", err)
		}

		if got < len(b) && n == 0 && !c.now().Before(deadline) {
			return ErrTimeout
		}
	}

	return nil
}

// crc16 is the Modbus CRC (reflected 0x8005, initial 0xFFFF). It is sent
// low byte first.
func crc16(b []byte) uint16 {
	crc := uint16(0xFFFF)
	for _, v := range b {
		crc ^= uint16(v)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xA001
			} else {
				crc >>= 1
			}
		}
	}

	return crc
}

// crcOK checks a frame whose last two bytes are its CRC.
func crcOK(frame []byte) bool {
	if len(frame) < crcLen+1 {
		return false
	}

	body := frame[:len(frame)-crcLen]
	crc := crc16(body)

	return frame[len(frame)-2] == byte(crc) && frame[len(frame)-1] == byte(crc>>8)
}
