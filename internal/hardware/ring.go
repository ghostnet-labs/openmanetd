package hardware

// eventRingSize bounds the recovery event history (design section 5).
const eventRingSize = 64

// eventRing is a fixed-capacity ring of the newest recovery events. It is
// not safe for concurrent use; the manager guards it with its mutex.
type eventRing struct {
	buf  [eventRingSize]Event
	next int
	n    int
}

func (r *eventRing) add(e Event) {
	r.buf[r.next] = e
	r.next = (r.next + 1) % eventRingSize

	if r.n < eventRingSize {
		r.n++
	}
}

// newest returns up to limit events, newest first, optionally filtered by
// radio (RadioUnspecified keeps every event). limit <= 0 means all.
func (r *eventRing) newest(limit int, radio Radio) []Event {
	if limit <= 0 || limit > r.n {
		limit = r.n
	}

	out := make([]Event, 0, limit)

	for i := 1; i <= r.n && len(out) < limit; i++ {
		e := r.buf[(r.next-i+eventRingSize)%eventRingSize]
		if radio != RadioUnspecified && e.Radio != radio {
			continue
		}

		out = append(out, e)
	}

	return out
}
