package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// writeModule creates a throwaway module with one package holding src.
func writeModule(t *testing.T, src string) string {
	t.Helper()

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/m\n\ngo 1.22\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "m.go"), []byte(src), 0o600))

	return dir
}

func TestCheckTarget_intOverflowOn32Bit(t *testing.T) {
	// The bug that broke the mipsle firmware build: an untyped constant
	// above MaxInt32 in an int context.
	dir := writeModule(t, `package m

import "math"

func Clamp(n int) uint32 { return uint32(min(n, math.MaxUint32)) }
`)

	tests := []struct {
		target string
		want   int
	}{
		{target: "linux/mipsle", want: 1},
		{target: "linux/amd64", want: 0},
		{target: "linux/arm64", want: 0},
	}

	for _, tc := range tests {
		t.Run(tc.target, func(t *testing.T) {
			var out strings.Builder

			n, err := checkTarget(dir, tc.target, &out)
			require.NoError(t, err)
			assert.Equal(t, tc.want, n, out.String())

			if tc.want > 0 {
				assert.Contains(t, out.String(), "overflows int")
			}
		})
	}
}

func TestCheckTarget_fakesImportC(t *testing.T) {
	// Go code in a cgo file is checked; references to C are not errors.
	dir := writeModule(t, `package m

// #define ANSWER 42
import "C"

const Answer = C.ANSWER

func Half() int { return int(C.ANSWER) / 2 }

func Bad() int { return "not an int" }
`)

	var out strings.Builder

	n, err := checkTarget(dir, "linux/mipsle", &out)
	require.NoError(t, err)
	assert.Equal(t, 1, n, out.String())
	assert.Contains(t, out.String(), "not an int")
}

func TestCheckTarget_badTarget(t *testing.T) {
	_, err := checkTarget(".", "mipsle", &strings.Builder{})
	require.Error(t, err)

	_, err = checkTarget(".", "linux/nosucharch", &strings.Builder{})
	require.Error(t, err)
}
