package weather

// ring is a fixed-capacity FIFO that overwrites its oldest entry when
// full. It is not safe for concurrent use; the Monitor guards it.
type ring[T any] struct {
	buf   []T
	start int
	n     int
}

func newRing[T any](capacity int) ring[T] {
	return ring[T]{buf: make([]T, max(capacity, 1))}
}

func (r *ring[T]) push(v T) {
	if r.n < len(r.buf) {
		r.buf[(r.start+r.n)%len(r.buf)] = v
		r.n++

		return
	}

	r.buf[r.start] = v
	r.start = (r.start + 1) % len(r.buf)
}

// at returns the i-th oldest entry, 0 <= i < len.
func (r *ring[T]) at(i int) T {
	return r.buf[(r.start+i)%len(r.buf)]
}

func (r *ring[T]) len() int { return r.n }

// last returns the newest entry; ok is false when empty.
func (r *ring[T]) last() (T, bool) {
	var zero T
	if r.n == 0 {
		return zero, false
	}

	return r.at(r.n - 1), true
}
