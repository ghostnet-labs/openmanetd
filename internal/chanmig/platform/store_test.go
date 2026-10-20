package platform_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"hash/crc32"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/chanmig/platform"
)

// storeBytes builds a store file by hand from the documented layout.
func storeBytes(records ...[]byte) []byte {
	b := []byte("OMPS")
	b = append(b, 1, byte(len(records)))

	for _, r := range records {
		b = binary.BigEndian.AppendUint16(b, uint16(len(r)))
		b = append(b, r...)
	}

	return binary.BigEndian.AppendUint32(b, crc32.ChecksumIEEE(b))
}

func newStore(t *testing.T) (*platform.FileStore, string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "chanmig", "plans.bin")

	return platform.NewFileStore(path), path
}

func tempFiles(t *testing.T, dir string) []string {
	t.Helper()

	entries, err := os.ReadDir(dir)
	require.NoError(t, err)

	var out []string

	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}

	return out
}

func TestFileStore_defaultPath(t *testing.T) {
	assert.Equal(t, "/etc/openmanetd/chanmig/plans.bin", platform.DefaultStorePath)
	assert.NotNil(t, platform.NewFileStore(""))
}

func TestFileStore_missingFileIsEmpty(t *testing.T) {
	s, _ := newStore(t)

	records, err := s.Load(context.Background())
	require.NoError(t, err)
	assert.Empty(t, records)
}

func TestFileStore_roundTrip(t *testing.T) {
	ctx := context.Background()
	s, path := newStore(t)
	records := [][]byte{[]byte("newest"), []byte("older"), bytes.Repeat([]byte{7}, chanmig.MaxMessageSize)}

	require.NoError(t, s.Save(ctx, records))

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Equal(t, storeBytes(records...), data, "file layout is pinned")

	fi, err := os.Stat(path)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), fi.Mode().Perm())
	assert.Empty(t, tempFiles(t, filepath.Dir(path)), "no temporary file is left behind")

	got, err := platform.NewFileStore(path).Load(ctx)
	require.NoError(t, err)
	assert.Equal(t, records, got)

	require.NoError(t, s.Save(ctx, nil))

	got, err = platform.NewFileStore(path).Load(ctx)
	require.NoError(t, err)
	assert.Empty(t, got)
}

func TestFileStore_identicalSaveSkipsWrite(t *testing.T) {
	ctx := context.Background()
	s, path := newStore(t)
	records := [][]byte{[]byte("plan")}

	require.NoError(t, s.Save(ctx, records))
	require.NoError(t, os.Remove(path))

	// Same bytes as the last write: flash is spared, nothing is written.
	require.NoError(t, s.Save(ctx, records))

	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist)

	require.NoError(t, s.Save(ctx, [][]byte{[]byte("plan2")}))

	_, err = os.Stat(path)
	require.NoError(t, err)
}

func TestFileStore_loadSeedsSkipCache(t *testing.T) {
	ctx := context.Background()
	_, path := newStore(t)
	records := [][]byte{[]byte("plan")}

	require.NoError(t, platform.NewFileStore(path).Save(ctx, records))

	s := platform.NewFileStore(path)
	_, err := s.Load(ctx)
	require.NoError(t, err)

	require.NoError(t, os.Remove(path))
	require.NoError(t, s.Save(ctx, records))

	_, err = os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "a save matching the loaded file is skipped")
}

func TestFileStore_corruptFiles(t *testing.T) {
	valid := storeBytes([]byte("a"), []byte("bc"))

	flipped := bytes.Clone(valid)
	flipped[7] ^= 0xff

	badMagic := []byte("XXXX")
	badMagic = append(badMagic, valid[4:len(valid)-4]...)
	badMagic = binary.BigEndian.AppendUint32(badMagic, crc32.ChecksumIEEE(badMagic))

	withCRC := func(body []byte) []byte {
		return binary.BigEndian.AppendUint32(body, crc32.ChecksumIEEE(body))
	}

	cases := []struct {
		name string
		data []byte
	}{
		{name: "empty", data: []byte{}},
		{name: "garbage", data: []byte("not a plan store at all")},
		{name: "truncated", data: valid[:len(valid)-1]},
		{name: "checksum mismatch", data: flipped},
		{name: "bad magic", data: badMagic},
		{name: "bad format", data: withCRC([]byte{'O', 'M', 'P', 'S', 2, 0})},
		{name: "too many records", data: withCRC([]byte{'O', 'M', 'P', 'S', 1, 18})},
		{name: "record overruns", data: withCRC([]byte{'O', 'M', 'P', 'S', 1, 1, 0, 9, 'x'})},
		{name: "zero length record", data: withCRC([]byte{'O', 'M', 'P', 'S', 1, 1, 0, 0})},
		{name: "trailing bytes", data: withCRC([]byte{'O', 'M', 'P', 'S', 1, 1, 0, 1, 'x', 'y'})},
		{name: "oversized file", data: make([]byte, 64<<10)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			s, path := newStore(t)
			require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
			require.NoError(t, os.WriteFile(path, tc.data, 0o600))

			records, err := s.Load(ctx)
			require.ErrorIs(t, err, platform.ErrCorruptStore)
			assert.Nil(t, records)

			// The next save replaces the corrupt file.
			require.NoError(t, s.Save(ctx, [][]byte{[]byte("fresh")}))

			records, err = s.Load(ctx)
			require.NoError(t, err)
			assert.Equal(t, [][]byte{[]byte("fresh")}, records)
		})
	}
}

func TestFileStore_saveRejectsUnboundedInput(t *testing.T) {
	ctx := context.Background()
	s, path := newStore(t)

	tooMany := make([][]byte, 18)
	for i := range tooMany {
		tooMany[i] = []byte{1}
	}

	require.Error(t, s.Save(ctx, tooMany))
	require.Error(t, s.Save(ctx, [][]byte{{}}))
	require.Error(t, s.Save(ctx, [][]byte{make([]byte, chanmig.MaxMessageSize+1)}))

	_, err := os.Stat(path)
	require.ErrorIs(t, err, os.ErrNotExist, "rejected saves write nothing")
}

func TestFileStore_failedReplaceKeepsOldFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	path := filepath.Join(dir, "plans.bin")

	// A non-empty directory at the target path makes the final rename
	// fail after the temporary file was fully written.
	require.NoError(t, os.MkdirAll(filepath.Join(path, "blocker"), 0o700))

	err := platform.NewFileStore(path).Save(ctx, [][]byte{[]byte("plan")})
	require.Error(t, err)
	assert.Empty(t, tempFiles(t, dir), "the temporary file is removed on failure")

	fi, statErr := os.Stat(path)
	require.NoError(t, statErr)
	assert.True(t, fi.IsDir(), "the existing target is untouched")
}

func TestFileStore_unreadablePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "plans.bin")
	require.NoError(t, os.Mkdir(path, 0o700))

	_, err := platform.NewFileStore(path).Load(context.Background())
	require.Error(t, err)
}
