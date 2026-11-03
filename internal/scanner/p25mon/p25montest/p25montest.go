// Package p25montest provides a capture Runner that replays IQ bytes, for
// tests that drive p25mon end to end without SDR hardware.
package p25montest

import (
	"context"
	"errors"
	"io"
	"slices"
	"sync"

	"github.com/openmanet/openmanetd/internal/sdr/source"
)

// ErrKilled is the read error a replay process returns once its context
// is canceled, like a killed capture tool's closed pipe.
var ErrKilled = errors.New("p25montest: process killed")

// Start is one recorded Runner.Start call.
type Start struct {
	Name string
	Args []string
}

// Runner is a source.Runner whose processes write Data to stdout Passes
// times (at least once) and then exit. StartErr, when set, fails every
// start. It records every start.
type Runner struct { //nolint:govet // fieldalignment: mu stays above the field it guards per .claude/rules/concurrency.md
	StartErr error
	Data     []byte
	Passes   int

	mu     sync.Mutex // protects starts
	starts []Start
}

var _ source.Runner = (*Runner)(nil)

// Start implements source.Runner.
func (r *Runner) Start(ctx context.Context, name string, args []string) (source.Process, error) {
	r.mu.Lock()
	r.starts = append(r.starts, Start{Name: name, Args: slices.Clone(args)})
	r.mu.Unlock()

	if r.StartErr != nil {
		return nil, r.StartErr
	}

	return &process{ctx: ctx, data: r.Data, left: max(1, r.Passes)}, nil
}

// Starts returns the recorded starts.
func (r *Runner) Starts() []Start {
	r.mu.Lock()
	defer r.mu.Unlock()

	return slices.Clone(r.starts)
}

// process replays data; it is read only by source.Run's goroutine.
type process struct {
	ctx  context.Context //nolint:containedctx // the process lives exactly as long as its start context, like exec.CommandContext
	data []byte
	pos  int
	left int
}

func (p *process) Stdout() io.Reader { return p }

func (p *process) Wait() error { return nil }

// Read implements io.Reader over the repeated data.
func (p *process) Read(b []byte) (int, error) {
	if p.ctx.Err() != nil {
		return 0, ErrKilled
	}

	if p.left == 0 || len(p.data) == 0 {
		return 0, io.EOF
	}

	n := copy(b, p.data[p.pos:])
	p.pos += n

	if p.pos == len(p.data) {
		p.pos = 0
		p.left--
	}

	return n, nil
}
