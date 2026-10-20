package platform

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/openmanet/openmanetd/internal/chanmig"
)

// DefaultStorePath is where FileStore keeps accepted plans.
const DefaultStorePath = "/etc/openmanetd/chanmig/plans.bin"

// Store file layout, integers big-endian:
//
//	magic(4) "OMPS" | format(1) | count(1) |
//	count x { len(2) | record(len) } | crc32-IEEE(4) over all prior bytes
const (
	storeMagic      = "OMPS"
	storeFormat     = 1
	storeHeaderSize = 4 + 1 + 1
	storeCRCSize    = 4
	// maxStoreRecords is the current plan plus the Node's largest
	// history.
	maxStoreRecords = 17
	maxStoreRecord  = chanmig.MaxMessageSize
	maxStoreSize    = storeHeaderSize + maxStoreRecords*(2+maxStoreRecord) + storeCRCSize
)

// ErrCorruptStore reports a plan file that fails framing or CRC checks.
var ErrCorruptStore = errors.New("chanmig/platform: corrupt plan store")

// FileStore implements chanmig.Store with one small file. Writes go to a
// temporary file in the same directory that is fsynced and renamed over
// the target, so a power cut leaves either the old or the new file. Saves
// that would write identical bytes are skipped to spare flash.
type FileStore struct { //nolint:govet // fieldalignment: mu stays above the fields it guards per .claude/rules/concurrency.md
	path string

	mu   sync.Mutex // protects the fields below and serializes file I/O
	last []byte
}

// NewFileStore returns a store at path ("" selects DefaultStorePath).
func NewFileStore(path string) *FileStore {
	if path == "" {
		path = DefaultStorePath
	}

	return &FileStore{path: path}
}

// Load implements chanmig.Store. A missing file is an empty store; a
// corrupt or oversized file is an error (the Node then starts from its
// provisioned plan and the next Save replaces the file).
func (s *FileStore) Load(_ context.Context) ([][]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	f, err := os.Open(s.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("open plan store: %w", err)
	}

	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxStoreSize+1))
	if err != nil {
		return nil, fmt.Errorf("read plan store: %w", err)
	}

	records, err := decodeStore(data)
	if err != nil {
		return nil, err
	}

	s.last = data

	return records, nil
}

// Save implements chanmig.Store.
func (s *FileStore) Save(_ context.Context, records [][]byte) error {
	data, err := encodeStore(records)
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if bytes.Equal(data, s.last) {
		return nil
	}

	if err := writeFileAtomic(s.path, data); err != nil {
		return err
	}

	s.last = data

	return nil
}

func encodeStore(records [][]byte) ([]byte, error) {
	if len(records) > maxStoreRecords {
		return nil, fmt.Errorf("chanmig/platform: %d plan records exceed %d", len(records), maxStoreRecords)
	}

	size := storeHeaderSize + storeCRCSize

	for _, r := range records {
		if len(r) == 0 || len(r) > maxStoreRecord {
			return nil, fmt.Errorf("chanmig/platform: plan record of %d bytes", len(r))
		}

		size += 2 + len(r)
	}

	buf := make([]byte, 0, size)
	buf = append(buf, storeMagic...)
	buf = append(buf, storeFormat, byte(len(records)))

	for _, r := range records {
		buf = binary.BigEndian.AppendUint16(buf, uint16(len(r)))
		buf = append(buf, r...)
	}

	return binary.BigEndian.AppendUint32(buf, crc32.ChecksumIEEE(buf)), nil
}

func decodeStore(data []byte) ([][]byte, error) {
	if len(data) < storeHeaderSize+storeCRCSize || len(data) > maxStoreSize {
		return nil, fmt.Errorf("%w: size %d", ErrCorruptStore, len(data))
	}

	body := data[:len(data)-storeCRCSize]
	if crc32.ChecksumIEEE(body) != binary.BigEndian.Uint32(data[len(body):]) {
		return nil, fmt.Errorf("%w: checksum mismatch", ErrCorruptStore)
	}

	if string(body[:4]) != storeMagic || body[4] != storeFormat {
		return nil, fmt.Errorf("%w: bad magic or format", ErrCorruptStore)
	}

	count := int(body[5])
	if count > maxStoreRecords {
		return nil, fmt.Errorf("%w: %d records", ErrCorruptStore, count)
	}

	records := make([][]byte, 0, count)
	rest := body[storeHeaderSize:]

	for range count {
		if len(rest) < 2 {
			return nil, fmt.Errorf("%w: truncated record", ErrCorruptStore)
		}

		n := int(binary.BigEndian.Uint16(rest))
		rest = rest[2:]

		if n == 0 || n > maxStoreRecord || n > len(rest) {
			return nil, fmt.Errorf("%w: record length %d", ErrCorruptStore, n)
		}

		records = append(records, rest[:n:n])
		rest = rest[n:]
	}

	if len(rest) != 0 {
		return nil, fmt.Errorf("%w: trailing bytes", ErrCorruptStore)
	}

	return records, nil
}

// writeFileAtomic replaces path with data via a fsynced temporary file in
// the same directory and a rename, then fsyncs the directory.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create plan store directory: %w", err)
	}

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary plan store: %w", err)
	}

	tmpName := tmp.Name()
	committed := false

	defer func() {
		if !committed {
			_ = tmp.Close()        // already failing; the write error is what matters
			_ = os.Remove(tmpName) // best-effort cleanup of the partial file
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("write temporary plan store: %w", err)
	}

	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync temporary plan store: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close temporary plan store: %w", err)
	}

	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("replace plan store: %w", err)
	}

	committed = true

	return syncDir(dir)
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open directory %s: %w", dir, err)
	}
	defer d.Close()

	if err := d.Sync(); err != nil {
		return fmt.Errorf("sync directory %s: %w", dir, err)
	}

	return nil
}
