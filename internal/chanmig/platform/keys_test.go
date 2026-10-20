package platform_test

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/chanmig/platform"
)

func genKey(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)

	return pub, priv
}

func pemPublic(t *testing.T, pub any) string {
	t.Helper()

	der, err := x509.MarshalPKIXPublicKey(pub)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
}

func pemPrivate(t *testing.T, priv any) string {
	t.Helper()

	der, err := x509.MarshalPKCS8PrivateKey(priv)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}))
}

func writeKeyFile(t *testing.T, content string, mode os.FileMode) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "key")
	require.NoError(t, os.WriteFile(path, []byte(content), mode))
	require.NoError(t, os.Chmod(path, mode))

	return path
}

func TestKeyPathDefaults(t *testing.T) {
	assert.Equal(t, "/etc/openmanetd/chanmig/authority.pub", platform.DefaultPublicKeyPath)
	assert.Equal(t, "/etc/openmanetd/chanmig/authority.key", platform.DefaultPrivateKeyPath)
}

func TestLoadPublicKeys_formats(t *testing.T) {
	pub1, _ := genKey(t)
	pub2, _ := genKey(t)
	b64 := base64.StdEncoding.EncodeToString

	cases := []struct {
		name    string
		content string
		want    []ed25519.PublicKey
	}{
		{name: "one PEM", content: pemPublic(t, pub1), want: []ed25519.PublicKey{pub1}},
		{name: "two PEM for rotation", content: pemPublic(t, pub1) + pemPublic(t, pub2), want: []ed25519.PublicKey{pub1, pub2}},
		{name: "base64 line", content: b64(pub1) + "\n", want: []ed25519.PublicKey{pub1}},
		{
			name:    "base64 with comments and blanks",
			content: "# mesh authority\n\n" + b64(pub1) + "\n  " + b64(pub2) + "  \n",
			want:    []ed25519.PublicKey{pub1, pub2},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			keys, err := platform.LoadPublicKeys(writeKeyFile(t, tc.content, 0o644))
			require.NoError(t, err)
			assert.Equal(t, tc.want, keys)
		})
	}
}

func TestLoadPublicKeys_rejects(t *testing.T) {
	pub, _ := genKey(t)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	nine := strings.Repeat(base64.StdEncoding.EncodeToString(pub)+"\n", 9)

	cases := []struct {
		name    string
		content string
	}{
		{name: "empty", content: ""},
		{name: "comments only", content: "# nothing\n"},
		{name: "not base64", content: "not-a-key\n"},
		{name: "short key", content: base64.StdEncoding.EncodeToString(pub[:31]) + "\n"},
		{name: "non-ed25519 PEM", content: pemPublic(t, &ec.PublicKey)},
		{name: "corrupt PEM", content: "-----BEGIN PUBLIC KEY-----\nAAAA\n-----END PUBLIC KEY-----\n"},
		{name: "too many keys", content: nine},
		{name: "oversized file", content: strings.Repeat("#", 17<<10)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := platform.LoadPublicKeys(writeKeyFile(t, tc.content, 0o644))
			require.ErrorIs(t, err, platform.ErrKeyFormat)
		})
	}

	t.Run("missing file", func(t *testing.T) {
		_, err := platform.LoadPublicKeys(filepath.Join(t.TempDir(), "absent"))
		require.ErrorIs(t, err, os.ErrNotExist)
	})
}

func TestLoadPrivateKey_formats(t *testing.T) {
	_, priv := genKey(t)
	b64 := base64.StdEncoding.EncodeToString

	cases := []struct {
		name    string
		content string
	}{
		{name: "PKCS8 PEM", content: pemPrivate(t, priv)},
		{name: "base64 seed", content: b64(priv.Seed()) + "\n"},
		{name: "base64 full key", content: b64(priv)},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := platform.LoadPrivateKey(writeKeyFile(t, tc.content, 0o600))
			require.NoError(t, err)
			assert.True(t, priv.Equal(got))
		})
	}
}

func TestLoadPrivateKey_rejects(t *testing.T) {
	_, priv := genKey(t)
	_, other := genKey(t)
	ec, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	mismatched := append(append([]byte{}, priv.Seed()...), other.Public().(ed25519.PublicKey)...)

	cases := []struct {
		name    string
		content string
	}{
		{name: "not base64", content: "nope"},
		{name: "wrong length", content: base64.StdEncoding.EncodeToString(make([]byte, 40))},
		{name: "inconsistent 64-byte key", content: base64.StdEncoding.EncodeToString(mismatched)},
		{name: "non-ed25519 PKCS8", content: pemPrivate(t, ec)},
		{name: "wrong PEM type", content: pemPublic(t, priv.Public())},
		{name: "corrupt PEM", content: "-----BEGIN PRIVATE KEY-----\nAAAA\n-----END PRIVATE KEY-----\n"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := platform.LoadPrivateKey(writeKeyFile(t, tc.content, 0o600))
			require.ErrorIs(t, err, platform.ErrKeyFormat)
		})
	}

	t.Run("missing file means not a coordinator", func(t *testing.T) {
		_, err := platform.LoadPrivateKey(filepath.Join(t.TempDir(), "absent"))
		require.ErrorIs(t, err, platform.ErrNoPrivateKey)
	})

	for _, mode := range []os.FileMode{0o640, 0o604, 0o644} {
		t.Run("mode "+mode.String(), func(t *testing.T) {
			_, err := platform.LoadPrivateKey(writeKeyFile(t, pemPrivate(t, priv), mode))
			require.ErrorIs(t, err, platform.ErrKeyPermissions)
		})
	}
}

func TestTrustsKey(t *testing.T) {
	pub1, _ := genKey(t)
	pub2, _ := genKey(t)

	assert.True(t, platform.TrustsKey([]ed25519.PublicKey{pub2, pub1}, pub1))
	assert.False(t, platform.TrustsKey([]ed25519.PublicKey{pub2}, pub1))
	assert.False(t, platform.TrustsKey(nil, pub1))
}
