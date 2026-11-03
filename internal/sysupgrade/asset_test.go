package sysupgrade

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMatchAsset_ManifestPriority(t *testing.T) {
	manifest := &Manifest{Boards: map[string]string{
		"bcm2711,mm8108-usb": "openmanet-1.8.0-bcm27xx-bcm2711-rpi-4-squashfs-sysupgrade.img.gz",
	}}

	assets := []Asset{
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-rpi-4-squashfs-sysupgrade.img.gz", DownloadURL: "u1"},
		{Name: "openmanet-1.8.0-ipq40xx-generic-gw7100-squashfs-sysupgrade.bin", DownloadURL: "u2"},
		{Name: "manifest.json", DownloadURL: "u3"},
	}

	got, err := MatchAsset("bcm2711,mm8108-usb", "bcm27xx/bcm2711", manifest, assets)
	require.NoError(t, err)
	assert.Equal(t, assets[0].Name, got.Name)
}

func TestMatchAsset_ManifestPointsAtMissingAsset(t *testing.T) {
	manifest := &Manifest{Boards: map[string]string{
		"bcm2711,mm8108-usb": "missing.img.gz",
	}}

	assets := []Asset{
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-squashfs-sysupgrade.img.gz"},
	}

	_, err := MatchAsset("bcm2711,mm8108-usb", "bcm27xx/bcm2711", manifest, assets)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoMatchingAsset))
}

func TestMatchAsset_HeuristicHit(t *testing.T) {
	assets := []Asset{
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-squashfs-sysupgrade.img.gz", DownloadURL: "u1"},
		{Name: "openmanet-1.8.0-ipq40xx-generic-gw7100-squashfs-sysupgrade.bin", DownloadURL: "u2"},
	}

	got, err := MatchAsset("bcm2711,mm8108-usb", "bcm27xx/bcm2711", nil, assets)
	require.NoError(t, err)
	assert.Equal(t, assets[0].Name, got.Name)
}

func TestMatchAsset_HeuristicNoMatch(t *testing.T) {
	assets := []Asset{
		{Name: "openmanet-1.8.0-x86-64-generic-squashfs-sysupgrade.bin"},
	}

	_, err := MatchAsset("bcm2711,mm8108-usb", "bcm27xx/bcm2711", nil, assets)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoMatchingAsset))
}

func TestMatchAsset_HeuristicAmbiguous(t *testing.T) {
	assets := []Asset{
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-squashfs-sysupgrade.img.gz"},
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-squashfs-sysupgrade.bin"},
	}

	_, err := MatchAsset("bcm2711,mm8108-usb", "bcm27xx/bcm2711", nil, assets)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrAmbiguousAsset))
}

func TestMatchAsset_RejectNonSysupgrade(t *testing.T) {
	assets := []Asset{
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-squashfs-factory.bin"},
		{Name: "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-rootfs.tar.gz"},
	}

	_, err := MatchAsset("bcm2711,mm8108-usb", "bcm27xx/bcm2711", nil, assets)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrNoMatchingAsset))
}

func TestTokenizeBoardName(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{input: "bcm2711,mm8108-usb", want: []string{"bcm2711", "mm8108", "usb"}},
		{input: "gateworks,imx8mm-gw71xx-2x", want: []string{"gateworks", "imx8mm", "gw71xx", "2x"}},
		{input: "", want: nil},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := tokenizeBoardName(tt.input)
			assert.Equal(t, tt.want, got)
		})
	}
}

// x86ReleaseAssets is the x86-64 part of a release: BIOS and EFI combined
// images in squashfs and ext4, plus a non-x86 image and the checksums.
func x86ReleaseAssets() []Asset {
	return []Asset{
		{Name: "openmanet-1.9.0-x86-64-generic-squashfs-combined.img.gz"},
		{Name: "openmanet-1.9.0-x86-64-generic-squashfs-combined-efi.img.gz"},
		{Name: "openmanet-1.9.0-x86-64-generic-ext4-combined.img.gz"},
		{Name: "openmanet-1.9.0-x86-64-generic-ext4-combined-efi.img.gz"},
		{Name: "openmanet-1.9.0-bcm27xx-bcm2711-rpi-4-squashfs-sysupgrade.img.gz"},
		{Name: "sha256sums"},
	}
}

func TestMatchAssetFor_x86(t *testing.T) {
	tests := []struct {
		name    string
		hw      Hardware
		assets  []Asset
		want    string
		wantErr error
	}{
		{
			name:   "BIOS boot picks the combined image",
			hw:     Hardware{BoardName: "protectli-vp2430", Target: "x86/64"},
			assets: x86ReleaseAssets(),
			want:   "openmanet-1.9.0-x86-64-generic-squashfs-combined.img.gz",
		},
		{
			name:   "EFI boot picks the EFI combined image",
			hw:     Hardware{BoardName: "protectli-vp2430", Target: "x86/64", EFI: true},
			assets: x86ReleaseAssets(),
			want:   "openmanet-1.9.0-x86-64-generic-squashfs-combined-efi.img.gz",
		},
		{
			name:   "any DMI board name matches",
			hw:     Hardware{BoardName: "qemu-standard-pc-q35-ich9-2009", Target: "x86/64"},
			assets: x86ReleaseAssets(),
			want:   "openmanet-1.9.0-x86-64-generic-squashfs-combined.img.gz",
		},
		{
			name:    "ext4-only release offers nothing",
			hw:      Hardware{BoardName: "protectli-vp2430", Target: "x86/64"},
			assets:  []Asset{{Name: "openmanet-1.9.0-x86-64-generic-ext4-combined.img.gz"}},
			wantErr: ErrNoMatchingAsset,
		},
		{
			name:    "other subtarget's image is not offered",
			hw:      Hardware{BoardName: "pc-engines-apu2", Target: "x86/64"},
			assets:  []Asset{{Name: "openmanet-1.9.0-x86-generic-generic-squashfs-combined.img.gz"}},
			wantErr: ErrNoMatchingAsset,
		},
		{
			name: "two images for the same target are ambiguous",
			hw:   Hardware{BoardName: "protectli-vp2430", Target: "x86/64"},
			assets: []Asset{
				{Name: "openmanet-1.9.0-x86-64-generic-squashfs-combined.img.gz"},
				{Name: "openmanet-1.9.0-x86-64-test-squashfs-combined.img.gz"},
			},
			wantErr: ErrAmbiguousAsset,
		},
		{
			name:    "x86 image is not offered to a Pi",
			hw:      Hardware{BoardName: "bcm2711,mm8108-usb", Target: "bcm27xx/bcm2711"},
			assets:  x86ReleaseAssets()[:4],
			wantErr: ErrNoMatchingAsset,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := MatchAssetFor(tc.hw, nil, tc.assets)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Name)
		})
	}
}

func TestMatchAssetFor_x86ManifestWins(t *testing.T) {
	manifest := &Manifest{Boards: map[string]string{
		"protectli-vp2430": "openmanet-1.9.0-x86-64-generic-ext4-combined-efi.img.gz",
	}}

	got, err := MatchAssetFor(Hardware{BoardName: "protectli-vp2430", Target: "x86/64"}, manifest, x86ReleaseAssets())
	require.NoError(t, err)
	assert.Equal(t, "openmanet-1.9.0-x86-64-generic-ext4-combined-efi.img.gz", got.Name)
}
