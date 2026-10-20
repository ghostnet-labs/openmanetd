package platform

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

// Default key locations. The public key file is provisioned on every node;
// the private key file only on coordinators.
const (
	DefaultPublicKeyPath  = "/etc/openmanetd/chanmig/authority.pub"
	DefaultPrivateKeyPath = "/etc/openmanetd/chanmig/authority.key"
)

const (
	maxKeyFileSize = 16 << 10
	// maxPublicKeys bounds the trusted set; more than a couple of keys
	// only ever exist during a rotation.
	maxPublicKeys = 8
)

// Key loading errors.
var (
	// ErrNoPrivateKey reports that no private key file exists; the node
	// is then not a coordinator.
	ErrNoPrivateKey = errors.New("chanmig/platform: no plan-authority private key")
	// ErrKeyFormat reports a key file that cannot be parsed.
	ErrKeyFormat = errors.New("chanmig/platform: bad key file")
	// ErrKeyPermissions reports a private key readable by group or others.
	ErrKeyPermissions = errors.New("chanmig/platform: private key file must not be group or world accessible")
)

// LoadPublicKeys reads the trusted plan-authority public keys from path.
// The file holds either PEM "PUBLIC KEY" blocks (as written by
// `openssl pkey -pubout` for an Ed25519 key) or one standard-base64 raw
// 32-byte key per line; blank lines and lines starting with # are
// ignored. Several keys allow rotation.
func LoadPublicKeys(path string) ([]ed25519.PublicKey, error) {
	data, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}

	var keys []ed25519.PublicKey

	if bytes.Contains(data, []byte("-----BEGIN")) {
		keys, err = parsePEMPublicKeys(data)
	} else {
		keys, err = parseBase64PublicKeys(data)
	}

	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: %s holds no key", ErrKeyFormat, path)
	}

	return keys, nil
}

func parsePEMPublicKeys(data []byte) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, 1)

	for {
		var block *pem.Block

		block, data = pem.Decode(data)
		if block == nil {
			return keys, nil
		}

		if block.Type != "PUBLIC KEY" {
			continue
		}

		pub, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrKeyFormat, err)
		}

		k, ok := pub.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("%w: public key is %T, not Ed25519", ErrKeyFormat, pub)
		}

		if len(keys) == maxPublicKeys {
			return nil, fmt.Errorf("%w: more than %d keys", ErrKeyFormat, maxPublicKeys)
		}

		keys = append(keys, k)
	}
}

func parseBase64PublicKeys(data []byte) ([]ed25519.PublicKey, error) {
	keys := make([]ed25519.PublicKey, 0, 1)
	sc := bufio.NewScanner(bytes.NewReader(data))

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		raw, err := base64.StdEncoding.DecodeString(line)
		if err != nil || len(raw) != ed25519.PublicKeySize {
			return nil, fmt.Errorf("%w: line is not a base64 32-byte key", ErrKeyFormat)
		}

		if len(keys) == maxPublicKeys {
			return nil, fmt.Errorf("%w: more than %d keys", ErrKeyFormat, maxPublicKeys)
		}

		keys = append(keys, ed25519.PublicKey(raw))
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKeyFormat, err)
	}

	return keys, nil
}

// LoadPrivateKey reads the plan-authority private key from path. A
// missing file returns ErrNoPrivateKey. The file must not be group or
// world accessible and holds either a PEM "PRIVATE KEY" (PKCS#8, as
// written by `openssl genpkey -algorithm ed25519`) or a standard-base64
// 32-byte seed or 64-byte private key.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	fi, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNoPrivateKey
	}

	if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}

	if fi.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: %s has mode %s", ErrKeyPermissions, path, fi.Mode().Perm())
	}

	data, err := readKeyFile(path)
	if err != nil {
		return nil, err
	}

	key, err := parsePrivateKey(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	return key, nil
}

func parsePrivateKey(data []byte) (ed25519.PrivateKey, error) {
	if block, _ := pem.Decode(data); block != nil {
		if block.Type != "PRIVATE KEY" {
			return nil, fmt.Errorf("%w: PEM type %q", ErrKeyFormat, block.Type)
		}

		priv, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrKeyFormat, err)
		}

		k, ok := priv.(ed25519.PrivateKey)
		if !ok {
			return nil, fmt.Errorf("%w: private key is %T, not Ed25519", ErrKeyFormat, priv)
		}

		return k, nil
	}

	raw, err := base64.StdEncoding.DecodeString(string(bytes.TrimSpace(data)))
	if err != nil {
		return nil, fmt.Errorf("%w: not PEM or base64", ErrKeyFormat)
	}

	switch len(raw) {
	case ed25519.SeedSize:
		return ed25519.NewKeyFromSeed(raw), nil
	case ed25519.PrivateKeySize:
		k := ed25519.PrivateKey(raw)
		// Reject a 64-byte blob whose public half does not match its seed.
		if !bytes.Equal(ed25519.NewKeyFromSeed(k.Seed()), k) {
			return nil, fmt.Errorf("%w: inconsistent private key", ErrKeyFormat)
		}

		return k, nil
	default:
		return nil, fmt.Errorf("%w: %d-byte key", ErrKeyFormat, len(raw))
	}
}

// TrustsKey reports whether pub is one of keys.
func TrustsKey(keys []ed25519.PublicKey, pub ed25519.PublicKey) bool {
	for _, k := range keys {
		if k.Equal(pub) {
			return true
		}
	}

	return false
}

func readKeyFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open key file: %w", err)
	}
	defer f.Close()

	data, err := io.ReadAll(io.LimitReader(f, maxKeyFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("read key file: %w", err)
	}

	if len(data) > maxKeyFileSize {
		return nil, fmt.Errorf("%w: %s larger than %d bytes", ErrKeyFormat, path, maxKeyFileSize)
	}

	return data, nil
}
