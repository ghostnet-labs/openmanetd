// Package onboard lets a phone that joins a node get ATAK and its
// settings from the node with no internet: it stores the ATAK APK an
// operator uploads and builds the ATAK data package (callsign, team,
// map sources) a phone imports with one confirm.
package onboard

import (
	"archive/zip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const (
	apkFile  = "atak.apk"
	metaFile = "atak.json"
	// copyBufSize is the buffer used to stream an upload to disk. 64 KiB
	// keeps syscalls low without holding much RAM on small nodes.
	copyBufSize = 64 << 10
)

var (
	// ErrNoAPK means no APK has been uploaded.
	ErrNoAPK = errors.New("no ATAK APK uploaded")
	// ErrTooLarge means the upload exceeded the configured size cap.
	ErrTooLarge = errors.New("APK exceeds the size limit")
	// ErrNotAPK means the upload is not an Android package.
	ErrNotAPK = errors.New("file is not an Android APK")
)

// APKInfo describes the stored APK.
type APKInfo struct {
	Uploaded time.Time `json:"uploaded"`
	Name     string    `json:"name"`
	SHA256   string    `json:"sha256"`
	Size     int64     `json:"size"`
}

// Store keeps one ATAK APK on disk. Uploads stream to a temporary file in
// the same directory and replace the live file with a rename, so a phone
// that is mid-download keeps reading the old file.
type Store struct {
	info     *APKInfo // guarded by mu
	dir      string
	mu       sync.RWMutex
	maxBytes int64
}

// NewStore opens (creating if needed) the store directory and loads any
// previously uploaded APK's metadata.
func NewStore(dir string, maxBytes int64) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create onboard dir: %w", err)
	}

	s := &Store{dir: dir, maxBytes: maxBytes}

	info, err := s.readMeta()
	if err == nil {
		s.info = info
	}

	return s, nil
}

func (s *Store) readMeta() (*APKInfo, error) {
	b, err := os.ReadFile(filepath.Join(s.dir, metaFile))
	if err != nil {
		return nil, fmt.Errorf("read meta: %w", err)
	}

	var info APKInfo
	if err = json.Unmarshal(b, &info); err != nil {
		return nil, fmt.Errorf("decode meta: %w", err)
	}

	st, err := os.Stat(filepath.Join(s.dir, apkFile))
	if err != nil || st.Size() != info.Size {
		return nil, ErrNoAPK
	}

	return &info, nil
}

// Info returns the stored APK's metadata, or false when none is stored.
func (s *Store) Info() (APKInfo, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.info == nil {
		return APKInfo{}, false
	}

	return *s.info, true
}

// Open returns the stored APK for reading. The caller closes the file.
func (s *Store) Open() (*os.File, APKInfo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.info == nil {
		return nil, APKInfo{}, ErrNoAPK
	}

	f, err := os.Open(filepath.Join(s.dir, apkFile))
	if err != nil {
		return nil, APKInfo{}, fmt.Errorf("open apk: %w", err)
	}

	return f, *s.info, nil
}

// Save streams r to disk, checks that it is an APK within the size cap,
// and makes it the live APK. name is the uploader's file name, kept for
// display only.
func (s *Store) Save(r io.Reader, name string) (APKInfo, error) {
	tmp, err := os.CreateTemp(s.dir, "upload-*.apk")
	if err != nil {
		return APKInfo{}, fmt.Errorf("create temp: %w", err)
	}

	tmpName := tmp.Name()
	keep := false

	defer func() {
		if !keep {
			_ = os.Remove(tmpName)
		}
	}()

	h := sha256.New()
	// Read one byte past the cap so an oversize upload is detected
	// without buffering it.
	limited := io.LimitReader(r, s.maxBytes+1)
	buf := make([]byte, copyBufSize)

	n, err := io.CopyBuffer(io.MultiWriter(tmp, h), limited, buf)
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}

	if err != nil {
		return APKInfo{}, fmt.Errorf("write upload: %w", err)
	}

	if n > s.maxBytes {
		return APKInfo{}, ErrTooLarge
	}

	if err = checkAPK(tmpName); err != nil {
		return APKInfo{}, err
	}

	info := APKInfo{
		Name:     cleanName(name),
		Size:     n,
		SHA256:   hex.EncodeToString(h.Sum(nil)),
		Uploaded: time.Now().UTC(),
	}

	meta, err := json.Marshal(info)
	if err != nil {
		return APKInfo{}, fmt.Errorf("encode meta: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Rename(tmpName, filepath.Join(s.dir, apkFile)); err != nil {
		return APKInfo{}, fmt.Errorf("install apk: %w", err)
	}

	keep = true

	if err := writeFileAtomic(filepath.Join(s.dir, metaFile), meta); err != nil {
		return APKInfo{}, err
	}

	s.info = &info

	return info, nil
}

// Remove deletes the stored APK.
func (s *Store) Remove() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	s.info = nil

	for _, f := range []string{metaFile, apkFile} {
		if err := os.Remove(filepath.Join(s.dir, f)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", f, err)
		}
	}

	return nil
}

// checkAPK confirms the file is a zip holding AndroidManifest.xml and at
// least one dex file, which every installable APK has.
func checkAPK(path string) error {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return ErrNotAPK
	}
	defer zr.Close()

	var manifest, dex bool

	for _, f := range zr.File {
		switch {
		case f.Name == "AndroidManifest.xml":
			manifest = true
		case strings.HasPrefix(f.Name, "classes") && strings.HasSuffix(f.Name, ".dex"):
			dex = true
		}
	}

	if !manifest || !dex {
		return ErrNotAPK
	}

	return nil
}

// cleanName keeps only the base name and caps its length.
func cleanName(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if name == "." || name == "/" {
		name = ""
	}

	const maxName = 128
	if len(name) > maxName {
		name = name[:maxName]
	}

	return name
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", filepath.Base(path), err)
	}

	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("install %s: %w", filepath.Base(path), err)
	}

	return nil
}
