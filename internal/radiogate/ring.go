package radiogate

import (
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/radiocap"
)

// RingSize bounds the admission decision history.
const RingSize = 32

// Record is one admission evaluation. Records are immutable once added:
// Recent returns shallow copies whose slices must not be modified.
type Record struct {
	Time   time.Time `json:"time"`
	Origin string    `json:"origin"`
	Mode   Mode      `json:"mode"`
	// Error is set when the check could not run (probe failure,
	// unidentified HaLow PHY, invalid demands); the change proceeded.
	Error string `json:"error,omitempty"`
	// Decision is the model's answer, nil when Error is set.
	Decision *radiocap.Decision `json:"decision,omitempty"`
	// Radios are the UCI wifi-device sections the change touched.
	Radios []string `json:"radios"`
	// Current and Requested count the demands evaluated.
	Current   int `json:"current"`
	Requested int `json:"requested"`
	// Blocked is true when enforce mode refused the change.
	Blocked bool `json:"blocked"`
}

// Ring is a fixed-size, mutex-protected history of Records. The zero
// value is empty and ready to use.
type Ring struct { //nolint:govet // fieldalignment: mu must sit directly above the fields it guards (.claude/rules/concurrency.md); the pointer-scan-optimal layout would separate them.
	mu   sync.Mutex // protects the fields below
	buf  [RingSize]Record
	next int // index the next Add writes
	n    int // number of valid entries, at most RingSize
}

// Add appends r, overwriting the oldest record when full.
func (r *Ring) Add(rec Record) {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.buf[r.next] = rec
	r.next = (r.next + 1) % RingSize

	if r.n < RingSize {
		r.n++
	}
}

// Recent returns the records, newest first.
func (r *Ring) Recent() []Record {
	r.mu.Lock()
	defer r.mu.Unlock()

	out := make([]Record, r.n)
	for i := range r.n {
		out[i] = r.buf[(r.next-1-i+RingSize)%RingSize]
	}

	return out
}

// Len returns the number of records held.
func (r *Ring) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	return r.n
}
