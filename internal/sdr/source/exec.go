package source

import (
	"context"
	"fmt"
	"io"
	"os/exec"
	"time"

	"golang.org/x/sys/unix"
)

// stopGrace is how long a capture tool gets to release the USB device
// after SIGTERM before it is killed.
const stopGrace = 3 * time.Second

// ExecRunner starts capture tools with os/exec. Cancellation sends SIGTERM
// (rtl_sdr and airspy_rx close the device cleanly on it) and escalates to
// SIGKILL after a grace period.
type ExecRunner struct {
	// Stderr receives the tool's diagnostics; nil discards them.
	Stderr io.Writer
}

type execProcess struct {
	cmd    *exec.Cmd
	stdout io.Reader
}

func (p *execProcess) Stdout() io.Reader { return p.stdout }

func (p *execProcess) Wait() error {
	if err := p.cmd.Wait(); err != nil {
		return fmt.Errorf("source: wait: %w", err)
	}

	return nil
}

// Start implements Runner.
func (r *ExecRunner) Start(ctx context.Context, name string, args []string) (Process, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stderr = r.Stderr
	cmd.WaitDelay = stopGrace
	cmd.Cancel = func() error { return cmd.Process.Signal(unix.SIGTERM) }

	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("source: stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("source: start: %w", err)
	}

	return &execProcess{cmd: cmd, stdout: out}, nil
}
