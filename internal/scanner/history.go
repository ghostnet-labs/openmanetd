package scanner

import "time"

// grantMergeWindow is how long after a grant's last update a grant for the
// same talkgroup on the same frequency is still the same call. P25 control
// channels repeat grant updates roughly every second during a call.
const grantMergeWindow = 3 * time.Second

// grantMergeScan bounds how many recent entries are searched for a call to
// merge into.
const grantMergeScan = 16

// Grant is one call in the talkgroup history.
type Grant struct {
	Start       time.Time
	Last        time.Time
	SiteKey     string
	FrequencyHz uint64
	Talkgroup   uint32
	Source      uint32
	Channel     uint32
	Grants      uint32
	Encrypted   bool
	Emergency   bool
}

// history is a fixed-capacity ring of calls, oldest overwritten first. It
// is not safe for concurrent use; the manager guards it with its mutex.
type history struct {
	buf   []Grant
	head  int // next write position
	count int
}

func newHistory(capacity int) *history {
	return &history{buf: make([]Grant, max(capacity, 1))}
}

// add merges g into a recent matching call or appends it, returning the
// stored entry.
func (h *history) add(g VoiceGrant, siteKey string, now time.Time) Grant {
	for i := range min(h.count, grantMergeScan) {
		e := &h.buf[h.index(i)]
		if e.Talkgroup == g.Talkgroup && e.FrequencyHz == g.FrequencyHz && e.SiteKey == siteKey &&
			now.Sub(e.Last) <= grantMergeWindow {
			e.Last = now
			e.Grants++
			e.Encrypted = e.Encrypted || g.Encrypted
			e.Emergency = e.Emergency || g.Emergency

			if g.Source != 0 {
				e.Source = g.Source
			}

			return *e
		}
	}

	h.buf[h.head] = Grant{
		Start: now, Last: now, SiteKey: siteKey, FrequencyHz: g.FrequencyHz, Talkgroup: g.Talkgroup,
		Source: g.Source, Channel: g.Channel, Grants: 1, Encrypted: g.Encrypted, Emergency: g.Emergency,
	}
	out := h.buf[h.head]

	h.head = (h.head + 1) % len(h.buf)
	if h.count < len(h.buf) {
		h.count++
	}

	return out
}

// index maps i (0 = newest written) to a buffer position.
func (h *history) index(i int) int {
	return (h.head - 1 - i + 2*len(h.buf)) % len(h.buf)
}

// newest returns up to limit calls (all when limit <= 0) ordered by Last,
// newest first.
func (h *history) newest(limit int) []Grant {
	n := h.count
	if limit > 0 && limit < n {
		n = limit
	}

	// Merged calls can update an older slot, so order by Last rather than
	// by write position.
	all := make([]Grant, 0, h.count)
	for i := range h.count {
		all = append(all, h.buf[h.index(i)])
	}

	sortGrantsNewestFirst(all)

	return all[:n]
}

func (h *history) capacity() int { return len(h.buf) }

func sortGrantsNewestFirst(g []Grant) {
	// Insertion sort: the slice is already nearly ordered.
	for i := 1; i < len(g); i++ {
		for j := i; j > 0 && g[j].Last.After(g[j-1].Last); j-- {
			g[j], g[j-1] = g[j-1], g[j]
		}
	}
}
