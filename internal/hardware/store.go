package hardware

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// Boot-loop guard parameters (design section 5).
const (
	// bootLoopWindow and bootLoopLimit: three recovery-caused resets within
	// one hour make the next boot telemetry-only.
	bootLoopWindow = time.Hour
	bootLoopLimit  = 3
	// hostResetWindow and hostResetLimit: software requests at most two
	// host resets per 24 h (fewer than three).
	hostResetWindow = 24 * time.Hour
	hostResetLimit  = 3
	// recordRetention bounds how long entries are kept, and recordMaxEntries
	// bounds the file however the clock behaves.
	recordRetention  = 24 * time.Hour
	recordMaxEntries = 32
	recordVersion    = 1
)

// RecoveryRecord is the persisted boot-loop guard state.
type RecoveryRecord struct {
	// RecoveryResets are node resets caused by recovery (software-requested
	// host resets today; hardware-attributed resets once reset-cause
	// evidence exists).
	RecoveryResets []time.Time `json:"recovery_resets"`
	// HostResetRequests are the times software stopped the watchdog
	// heartbeat to request a host reset.
	HostResetRequests []time.Time `json:"host_reset_requests"`
	Version           int         `json:"version"`
}

// countSince returns how many entries are at or after since.
func countSince(ts []time.Time, since time.Time) int {
	n := 0

	for _, t := range ts {
		if !t.Before(since) {
			n++
		}
	}

	return n
}

// prune drops entries older than the retention window and caps the count,
// keeping the newest. Entries are kept in append (chronological) order.
func prune(ts []time.Time, now time.Time) []time.Time {
	cutoff := now.Add(-recordRetention)
	out := ts[:0]

	for _, t := range ts {
		if !t.Before(cutoff) {
			out = append(out, t)
		}
	}

	if len(out) > recordMaxEntries {
		out = append(out[:0], out[len(out)-recordMaxEntries:]...)
	}

	return out
}

// RecoveryStore persists the boot-loop guard state across reboots.
type RecoveryStore interface {
	Load() (RecoveryRecord, error)
	Save(rec RecoveryRecord) error
}

// FileStore is a RecoveryStore backed by a JSON file, written atomically
// (temp file, fsync, rename).
type FileStore struct {
	Path string
}

// Load implements RecoveryStore. A missing file is an empty record.
func (s *FileStore) Load() (RecoveryRecord, error) {
	var rec RecoveryRecord

	data, err := os.ReadFile(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return rec, nil
	}

	if err != nil {
		return rec, fmt.Errorf("read recovery record: %w", err)
	}

	if err := json.Unmarshal(data, &rec); err != nil {
		return RecoveryRecord{}, fmt.Errorf("parse recovery record: %w", err)
	}

	return rec, nil
}

// Save implements RecoveryStore.
func (s *FileStore) Save(rec RecoveryRecord) error {
	rec.Version = recordVersion

	data, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode recovery record: %w", err)
	}

	dir := filepath.Dir(s.Path)
	if mkErr := os.MkdirAll(dir, 0o755); mkErr != nil {
		return fmt.Errorf("create recovery record dir: %w", mkErr)
	}

	tmp, err := os.CreateTemp(dir, ".hwrecovery-*.json")
	if err != nil {
		return fmt.Errorf("create recovery record temp: %w", err)
	}

	tmpName := tmp.Name()

	cleanup := func(cause error) error {
		_ = tmp.Close()        // already failing; report cause
		_ = os.Remove(tmpName) // best effort

		return cause
	}

	if _, err := tmp.Write(data); err != nil {
		return cleanup(fmt.Errorf("write recovery record: %w", err))
	}

	if err := tmp.Sync(); err != nil {
		return cleanup(fmt.Errorf("sync recovery record: %w", err))
	}

	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName) // best effort

		return fmt.Errorf("close recovery record: %w", err)
	}

	if err := os.Rename(tmpName, s.Path); err != nil {
		_ = os.Remove(tmpName) // best effort

		return fmt.Errorf("rename recovery record: %w", err)
	}

	return nil
}

// memoryStore is the RecoveryStore used when none is configured: the guard
// then only protects within one daemon run.
type memoryStore struct {
	rec RecoveryRecord
}

func (m *memoryStore) Load() (RecoveryRecord, error) { return m.rec, nil }

func (m *memoryStore) Save(rec RecoveryRecord) error {
	m.rec = rec

	return nil
}
