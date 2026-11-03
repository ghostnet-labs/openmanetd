package source_test

import (
	"context"
	"errors"
	"io"
	"sync"

	"github.com/openmanet/openmanetd/internal/sdr/source"
)

var errKilled = errors.New("fake: killed")

type startCall struct {
	name string
	args []string
}

// fakeRunner starts fakeProcesses. Process i (0-based) streams chunk(i)
// repeatedly when loop is set, or once and then exits.
type fakeRunner struct {
	chunk    func(i int) []byte
	startErr error
	loop     bool

	mu     sync.Mutex // protects the fields below
	starts []startCall
	procs  []*fakeProcess
}

func (f *fakeRunner) Start(ctx context.Context, name string, args []string) (source.Process, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.startErr != nil {
		return nil, f.startErr
	}

	i := len(f.starts)
	f.starts = append(f.starts, startCall{name: name, args: append([]string(nil), args...)})

	pr, pw := io.Pipe()
	p := &fakeProcess{stdout: pr, done: make(chan struct{})}
	f.procs = append(f.procs, p)

	stop := context.AfterFunc(ctx, func() { pw.CloseWithError(errKilled) })
	data := f.chunk(i)
	loop := f.loop

	go func() {
		defer close(p.done)
		defer stop()

		for {
			if _, err := pw.Write(data); err != nil {
				p.setErr(errKilled)

				return
			}

			if !loop {
				_ = pw.Close()

				return
			}
		}
	}()

	return p, nil
}

func (f *fakeRunner) calls() []startCall {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]startCall(nil), f.starts...)
}

func (f *fakeRunner) processes() []*fakeProcess {
	f.mu.Lock()
	defer f.mu.Unlock()

	return append([]*fakeProcess(nil), f.procs...)
}

type fakeProcess struct {
	stdout io.Reader
	done   chan struct{}

	mu      sync.Mutex // protects the fields below
	err     error
	waited  bool
	exitErr error
}

func (p *fakeProcess) setErr(err error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.err = err
}

func (p *fakeProcess) Stdout() io.Reader { return p.stdout }

func (p *fakeProcess) Wait() error {
	<-p.done

	p.mu.Lock()
	defer p.mu.Unlock()

	p.waited = true
	p.exitErr = p.err

	return p.err
}

func (p *fakeProcess) wasWaited() bool {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.waited
}
