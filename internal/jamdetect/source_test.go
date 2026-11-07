package jamdetect_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/openmanet/openmanetd/internal/jamdetect"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIWSource_radiosAndSurvey(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(root, "phy2-mesh0", "device"), 0o755))
	require.NoError(t, os.Symlink("../../../bus/usb/drivers/morse_usb", filepath.Join(root, "phy2-mesh0", "device", "driver")))

	var calls [][]string

	src := &jamdetect.IWSource{
		SysfsNet: root,
		Run: func(_ context.Context, args ...string) ([]byte, error) {
			calls = append(calls, args)
			if len(args) == 1 {
				return []byte("phy#2\n\tInterface phy2-mesh0\n\t\ttype mesh point\nphy#1\n\tInterface phy1-mesh0\n\t\ttype mesh point\n"), nil
			}

			return []byte(mt76Survey), nil
		},
	}

	radios, err := src.Radios(context.Background())
	require.NoError(t, err)
	assert.Equal(t, []jamdetect.Radio{
		{Ifname: "phy1-mesh0", PHY: "phy1"},
		{Ifname: "phy2-mesh0", PHY: "phy2", HaLow: true},
	}, radios)

	surveys, err := src.Survey(context.Background(), "phy1-mesh0")
	require.NoError(t, err)
	assert.Len(t, surveys, 3)
	assert.Equal(t, []string{"dev", "phy1-mesh0", "survey", "dump"}, calls[1])
}

func TestIWSource_haLowNameFallback(t *testing.T) {
	src := &jamdetect.IWSource{
		SysfsNet: t.TempDir(),
		Run: func(context.Context, ...string) ([]byte, error) {
			return []byte("phy#0\n\tInterface wlh0\n\t\ttype mesh point\n"), nil
		},
	}

	radios, err := src.Radios(context.Background())
	require.NoError(t, err)
	require.Len(t, radios, 1)
	assert.True(t, radios[0].HaLow)
}
