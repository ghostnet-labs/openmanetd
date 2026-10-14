package hardware

import (
	"go/build"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestUSBDevFSResetFile_perArch pins which file defines usbdevfsReset on each
// architecture. A "_mips.go" filename carries an implicit GOARCH=mips
// constraint, so the MIPS file must not use an architecture suffix: it was
// silently dropped on mipsle and the package failed to build there.
func TestUSBDevFSResetFile_perArch(t *testing.T) {
	const (
		mipsFile  = "usbdevfs_mipsx.go"
		otherFile = "usbdevfs_other.go"
	)

	tests := []struct {
		goarch string
		want   string
		other  string
	}{
		{"mips", mipsFile, otherFile},
		{"mipsle", mipsFile, otherFile},
		{"mips64", mipsFile, otherFile},
		{"mips64le", mipsFile, otherFile},
		{"amd64", otherFile, mipsFile},
		{"arm64", otherFile, mipsFile},
		{"arm", otherFile, mipsFile},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.goarch, func(t *testing.T) {
			ctx := build.Default
			ctx.GOOS = "linux"
			ctx.GOARCH = tc.goarch
			ctx.CgoEnabled = false

			pkg, err := ctx.ImportDir(".", 0)
			require.NoError(t, err)
			assert.True(t, slices.Contains(pkg.GoFiles, tc.want), "%s should build on %s", tc.want, tc.goarch)
			assert.False(t, slices.Contains(pkg.GoFiles, tc.other), "%s should not build on %s", tc.other, tc.goarch)
		})
	}
}
