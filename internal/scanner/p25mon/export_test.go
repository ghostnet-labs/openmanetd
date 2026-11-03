package p25mon

import "time"

// BackoffForTest exposes the restart delay for failures consecutive failures.
func (m *Monitor) BackoffForTest(failures int) time.Duration { return m.backoff(failures) }
