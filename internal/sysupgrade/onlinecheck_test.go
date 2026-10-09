package sysupgrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const onlineCheckTestAsset = "openmanet-1.8.0-bcm27xx-bcm2711-mm8108-usb-squashfs-sysupgrade.img.gz"

// disableOnlineCheck is a makeManagerWith hook that turns the online
// release check off.
func disableOnlineCheck(o *Options) { o.DisableOnlineCheck = true }

func TestManager_ListAvailableUpdates_OnlineCheck(t *testing.T) {
	releases := []Release{
		{Tag: "v1.8.0", Assets: []Asset{makeAsset(onlineCheckTestAsset, "u", 1)}},
	}

	tests := []struct {
		name         string
		mutate       func(*Options)
		forceRefresh bool
		wantUpdates  int
		wantFetches  int
	}{
		{name: "enabled by default fetches releases", mutate: nil, forceRefresh: true, wantUpdates: 1, wantFetches: 1},
		{name: "disabled returns empty list without fetching", mutate: disableOnlineCheck, forceRefresh: false, wantUpdates: 0, wantFetches: 0},
		{name: "disabled ignores force refresh", mutate: disableOnlineCheck, forceRefresh: true, wantUpdates: 0, wantFetches: 0},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			fetcher := &fakeReleasesFetcher{releases: releases}
			mgr := makeManagerWith(t, fetcher, &fakeRunner{}, "1.7.0", tc.mutate)

			updates, fetchedAt, err := mgr.ListAvailableUpdates(context.Background(), tc.forceRefresh, true)
			require.NoError(t, err)
			assert.Len(t, updates, tc.wantUpdates)
			assert.Equal(t, tc.wantFetches, fetcher.callCount())

			if tc.wantFetches == 0 {
				assert.True(t, fetchedAt.IsZero(), "no fetch means no fetch timestamp")
			}
		})
	}
}

func TestManager_OnlineCheckDisabled_IgnoresStaleCache(t *testing.T) {
	cache := &inMemoryCache{
		releases:  []Release{{Tag: "v1.8.0", Assets: []Asset{makeAsset(onlineCheckTestAsset, "u", 1)}}},
		fetchedAt: time.Now().UTC(),
	}
	fetcher := &fakeReleasesFetcher{}
	mgr := makeManagerWith(t, fetcher, &fakeRunner{}, "1.7.0", func(o *Options) {
		o.DisableOnlineCheck = true
		o.Cache = cache
	})

	updates, _, err := mgr.ListAvailableUpdates(context.Background(), false, true)
	require.NoError(t, err)
	assert.Empty(t, updates, "cached offers must not surface while the online check is off")

	_, err = mgr.GetReleaseDetail(context.Background(), "v1.8.0")
	require.ErrorIs(t, err, ErrOnlineCheckDisabled)
	assert.Equal(t, 0, fetcher.callCount())
}

func TestManager_OnlineCheckDisabled_StartUpgradeRefused(t *testing.T) {
	fetcher := &fakeReleasesFetcher{}
	runner := &fakeRunner{}
	mgr := makeManagerWith(t, fetcher, runner, "1.7.0", disableOnlineCheck)

	err := mgr.StartUpgrade(context.Background(), "v1.8.0", onlineCheckTestAsset, SysupgradeOptions{}, false)
	require.ErrorIs(t, err, ErrOnlineCheckDisabled)
	assert.Equal(t, 0, fetcher.callCount())
	assert.Equal(t, 0, runner.callCount())
	assert.Equal(t, PhaseIdle, mgr.GetUpgradeStatus(context.Background()).Phase)
}

func TestManager_OnlineCheckDisabled_ManualUploadStillWorks(t *testing.T) {
	fetcher := &fakeReleasesFetcher{}
	runner := &fakeRunner{pid: 4242}
	mgr := makeManagerWith(t, fetcher, runner, "1.7.0", disableOnlineCheck)

	staged, err := mgr.StoreStagedImage(context.Background(), strings.NewReader("payload"), "openmanet-bcm27xx-bcm2711.img")
	require.NoError(t, err)
	require.True(t, staged.PreflightOK)

	ch, unsub := mgr.Subscribe(context.Background())
	t.Cleanup(unsub)

	require.NoError(t, mgr.StartLocalUpgrade(context.Background(), SysupgradeOptions{}, false, false))

	sawUpgrading := false
	for i := 0; i < 20 && !sawUpgrading; i++ {
		ev, ok := <-ch
		if !ok {
			break
		}

		sawUpgrading = ev.Phase == PhaseUpgrading
	}

	assert.True(t, sawUpgrading, "expected local upgrade to reach PhaseUpgrading")
	assert.Equal(t, 1, runner.callCount())
	assert.Equal(t, 0, fetcher.callCount(), "manual upload must not contact GitHub")
}
