package jamdetect

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Source reads radios and their channel surveys.
type Source interface {
	Radios(ctx context.Context) ([]Radio, error)
	Survey(ctx context.Context, ifname string) ([]Survey, error)
}

const (
	// iwTimeout bounds one iw call. Survey dumps are answered from the
	// driver's counters and return in milliseconds.
	iwTimeout = 3 * time.Second
	// maxIWBytes bounds iw output. A survey dump of every 2.4 + 5 + 6 GHz
	// channel is under 16 KiB.
	maxIWBytes = 64 << 10
)

// ErrOutputTooLarge reports iw output over maxIWBytes.
var ErrOutputTooLarge = errors.New("jamdetect: iw output too large")

// IWSource reads radios with `iw dev` and surveys with
// `iw dev <if> survey dump`.
type IWSource struct {
	// Run overrides command execution in tests. nil runs iw.
	Run func(ctx context.Context, args ...string) ([]byte, error)
	// SysfsNet is the /sys/class/net root used to recognize HaLow
	// radios by driver. Empty means /sys/class/net.
	SysfsNet string
}

// Radios implements Source.
func (s *IWSource) Radios(ctx context.Context) ([]Radio, error) {
	out, err := s.run(ctx, "dev")
	if err != nil {
		return nil, err
	}

	return ParseIWDev(out, s.isHaLow), nil
}

// Survey implements Source.
func (s *IWSource) Survey(ctx context.Context, ifname string) ([]Survey, error) {
	out, err := s.run(ctx, "dev", ifname, "survey", "dump")
	if err != nil {
		return nil, err
	}

	return ParseSurvey(out), nil
}

func (s *IWSource) run(ctx context.Context, args ...string) ([]byte, error) {
	if s.Run != nil {
		return s.Run(ctx, args...)
	}

	ctx, cancel := context.WithTimeout(ctx, iwTimeout)
	defer cancel()

	out := &limitedBuffer{limit: maxIWBytes}

	cmd := exec.CommandContext(ctx, "iw", args...)
	cmd.Stdout = out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run iw %s: %w", strings.Join(args, " "), err)
	}

	if out.truncated {
		return nil, ErrOutputTooLarge
	}

	return out.buf.Bytes(), nil
}

// isHaLow recognizes a Morse Micro radio by its kernel driver, falling
// back to the interface names OpenMANET gives HaLow radios.
func (s *IWSource) isHaLow(ifname string) bool {
	root := s.SysfsNet
	if root == "" {
		root = "/sys/class/net"
	}

	if target, err := os.Readlink(filepath.Join(root, ifname, "device", "driver")); err == nil {
		if strings.Contains(strings.ToLower(filepath.Base(target)), "morse") {
			return true
		}
	}

	return strings.HasPrefix(ifname, "morse") || strings.HasPrefix(ifname, "wlh")
}

// limitedBuffer keeps at most limit bytes and records truncation. It
// reports full writes so exec keeps draining the pipe.
type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)

	room := b.limit - b.buf.Len()
	if room <= 0 {
		b.truncated = true

		return n, nil
	}

	if len(p) > room {
		p = p[:room]
		b.truncated = true
	}

	b.buf.Write(p)

	return n, nil
}
