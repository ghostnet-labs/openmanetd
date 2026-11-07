//go:build linux

package weather

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// readTimeoutDeciseconds is the line's read timeout (VTIME, tenths of a
// second): a read with no byte returns after it so the Modbus client can
// check its own deadline and the context.
const readTimeoutDeciseconds = 2

// serialBauds maps supported rates to termios speed bits.
var serialBauds = map[int]uint32{ //nolint:gochecknoglobals // read-only lookup table
	1200:   unix.B1200,
	2400:   unix.B2400,
	4800:   unix.B4800,
	9600:   unix.B9600,
	19200:  unix.B19200,
	38400:  unix.B38400,
	57600:  unix.B57600,
	115200: unix.B115200,
}

// serialPort is a raw 8N1 serial line opened in blocking mode with a
// short VTIME, so reads return within readTimeoutDeciseconds.
type serialPort struct {
	f *os.File
}

// OpenSerial opens device as a raw 8N1 line at baud.
func OpenSerial(device string, baud int) (Port, error) {
	speed, ok := serialBauds[baud]
	if !ok {
		return nil, fmt.Errorf("serial: unsupported baud %d", baud)
	}

	// Blocking (no O_NONBLOCK) so os.File does not hand the fd to the
	// poller, which would ignore VTIME and wait forever for a byte.
	fd, err := unix.Open(device, unix.O_RDWR|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("serial: open %s: %w", device, err)
	}

	if err := configureRaw(fd, speed); err != nil {
		_ = unix.Close(fd) //nolint:errcheck // already returning the configure error

		return nil, fmt.Errorf("serial: configure %s: %w", device, err)
	}

	return &serialPort{f: os.NewFile(uintptr(fd), device)}, nil //nolint:gosec // fd is a valid non-negative descriptor
}

func configureRaw(fd int, speed uint32) error {
	t, err := unix.IoctlGetTermios(fd, unix.TCGETS)
	if err != nil {
		return fmt.Errorf("get termios: %w", err)
	}

	t.Iflag &^= unix.IGNBRK | unix.BRKINT | unix.PARMRK | unix.ISTRIP | unix.INLCR | unix.IGNCR | unix.ICRNL | unix.IXON | unix.IXOFF | unix.IXANY
	t.Oflag &^= unix.OPOST
	t.Lflag &^= unix.ECHO | unix.ECHONL | unix.ICANON | unix.ISIG | unix.IEXTEN
	t.Cflag &^= unix.CSIZE | unix.PARENB | unix.CSTOPB | unix.CRTSCTS | unix.CBAUD
	t.Cflag |= unix.CS8 | unix.CREAD | unix.CLOCAL | speed
	t.Cc[unix.VMIN] = 0
	t.Cc[unix.VTIME] = readTimeoutDeciseconds

	if err := unix.IoctlSetTermios(fd, unix.TCSETS, t); err != nil {
		return fmt.Errorf("set termios: %w", err)
	}

	return nil
}

func (p *serialPort) Read(b []byte) (int, error) {
	n, err := p.f.Read(b)
	if err != nil {
		return n, fmt.Errorf("serial read: %w", err)
	}

	return n, nil
}

func (p *serialPort) Write(b []byte) (int, error) {
	n, err := p.f.Write(b)
	if err != nil {
		return n, fmt.Errorf("serial write: %w", err)
	}

	return n, nil
}

// Flush drops unread input.
func (p *serialPort) Flush() error {
	if err := unix.IoctlSetInt(int(p.f.Fd()), unix.TCFLSH, unix.TCIFLUSH); err != nil { //nolint:gosec // Fd fits in int
		return fmt.Errorf("serial flush: %w", err)
	}

	return nil
}

func (p *serialPort) Close() error {
	if err := p.f.Close(); err != nil {
		return fmt.Errorf("serial close: %w", err)
	}

	return nil
}
