package receivers_test

import (
	"errors"
	"io/fs"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/openmanet/openmanetd/internal/scanner/receivers"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// usbDevice adds a sysfs USB device directory to fsys.
func usbDevice(fsys fstest.MapFS, dir, vendor, product, serial string) {
	fsys[dir+"/idVendor"] = &fstest.MapFile{Data: []byte(vendor + "\n")}
	fsys[dir+"/idProduct"] = &fstest.MapFile{Data: []byte(product + "\n")}

	if serial != "" {
		fsys[dir+"/serial"] = &fstest.MapFile{Data: []byte(serial + "\n")}
	}
}

func sysfs() fstest.MapFS {
	fsys := fstest.MapFS{}
	usbDevice(fsys, "1-1.1", "0bda", "2838", "1090")
	usbDevice(fsys, "1-1.2", "0bda", "2832", "978")
	usbDevice(fsys, "1-1.3", "1d50", "60a1", "AIRSPY SN:44A061DC2E395E8F")
	usbDevice(fsys, "1-1.4", "0bda", "2838", "scan")
	usbDevice(fsys, "1-1.5", "0bda", "2838", "00000001")
	usbDevice(fsys, "1-1.6", "046d", "c52b", "keyboard")      // not a receiver
	usbDevice(fsys, "1-1.1:1.0", "0bda", "2838", "interface") // interface node
	fsys["usb1/idVendor"] = &fstest.MapFile{Data: []byte("1d6b\n")}

	return fsys
}

func TestScan(t *testing.T) {
	devs, err := receivers.Scan(sysfs())
	require.NoError(t, err)

	want := []receivers.Device{
		{Serial: "00000001", Dir: "1-1.5", Hardware: receivers.HardwareRTLSDR},
		{Serial: "1090", Dir: "1-1.1", Hardware: receivers.HardwareRTLSDR},
		{Serial: "978", Dir: "1-1.2", Hardware: receivers.HardwareRTLSDR},
		{Serial: "AIRSPY SN:44A061DC2E395E8F", Dir: "1-1.3", Hardware: receivers.HardwareAirspy},
		{Serial: "scan", Dir: "1-1.4", Hardware: receivers.HardwareRTLSDR},
	}
	assert.Equal(t, want, devs)
}

type errFS struct{}

func (errFS) Open(string) (fs.File, error) { return nil, fs.ErrPermission }

func TestScan_errors(t *testing.T) {
	_, err := receivers.Scan(errFS{})
	require.ErrorIs(t, err, fs.ErrPermission)

	devs, err := receivers.Scan(fstest.MapFS{})
	require.NoError(t, err)
	assert.Empty(t, devs)
}

func TestSerialMatches(t *testing.T) {
	airspy := receivers.Device{Serial: "AIRSPY SN:44A061DC2E395E8F", Hardware: receivers.HardwareAirspy}
	rtl := receivers.Device{Serial: "scan", Hardware: receivers.HardwareRTLSDR}

	tests := []struct {
		name    string
		claimed string
		dev     receivers.Device
		want    bool
	}{
		{name: "rtl exact", claimed: "scan", dev: rtl, want: true},
		{name: "rtl case differs", claimed: "SCAN", dev: rtl, want: false},
		{name: "empty never matches", claimed: "", dev: receivers.Device{}, want: false},
		{name: "airspy full", claimed: "AIRSPY SN:44A061DC2E395E8F", dev: airspy, want: true},
		{name: "airspy bare hex", claimed: "44a061dc2e395e8f", dev: airspy, want: true},
		{name: "airspy 0x", claimed: "0x44A061DC2E395E8F", dev: airspy, want: true},
		{name: "airspy other", claimed: "0x1234", dev: airspy, want: false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, receivers.SerialMatches(tc.claimed, tc.dev))
		})
	}
}

type fakeUCI struct {
	mu   sync.Mutex
	vals map[string]string
}

func (f *fakeUCI) Get(config, section, option string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v, ok := f.vals[config+"."+section+"."+option]
	if !ok {
		return nil, false
	}

	return []string{v}, true
}

func TestReadClaims(t *testing.T) {
	tests := []struct {
		name string
		vals map[string]string
		want receivers.Claims
	}{
		{
			name: "packaged defaults",
			vals: map[string]string{
				"dump1090.main.device_index": "1090", "dump1090.main.enabled": "1",
				"dump978-fa.main.serial": "978", "dump978-fa.main.enabled": "1",
			},
			want: receivers.Claims{ADSB1090: "1090", UAT978: "978", Wide: "w", Scan: "s", ADSBEnabled: true, UATEnabled: true},
		},
		{
			name: "dump1090 by index, dump978 disabled",
			vals: map[string]string{"dump1090.main.device_index": "0", "dump978-fa.main.serial": "978", "dump978-fa.main.enabled": "0"},
			want: receivers.Claims{ADSB1090: "0", UAT978: "978", Wide: "w", Scan: "s", ADSBByIndex: true, ADSBEnabled: true},
		},
		{
			name: "dump1090 disabled flag",
			vals: map[string]string{"dump1090.main.device_index": "1090", "dump1090.main.disabled": "1"},
			want: receivers.Claims{ADSB1090: "1090", Wide: "w", Scan: "s"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, receivers.ReadClaims(&fakeUCI{vals: tc.vals}, "w", "s"))
		})
	}

	assert.Equal(t, receivers.Claims{Wide: "w", Scan: "s"}, receivers.ReadClaims(nil, "w", "s"))
}

func TestAssess(t *testing.T) {
	devs, err := receivers.Scan(sysfs())
	require.NoError(t, err)

	now := time.Unix(1762150000, 0)
	claims := receivers.Claims{ADSB1090: "1090", UAT978: "978", Wide: "44a061dc2e395e8f", Scan: "scan"}
	act := map[receivers.Role]receivers.Activity{
		receivers.RoleADSB1090: {LastSample: now, Fresh: true},
		receivers.RoleUAT978:   {Idle: "dump978-fa is disabled in UCI"},
		receivers.RoleWide:     {Err: errors.New("tune failed")},
	}

	got := receivers.Assess(claims, devs, act)
	require.Len(t, got, 5)

	assert.Equal(t, receivers.Receiver{
		Role: receivers.RoleADSB1090, Serial: "1090", Owner: receivers.OwnerDump1090, Present: true,
		Hardware: receivers.HardwareRTLSDR, State: receivers.StateRunning, LastSample: now,
	}, got[0])
	assert.Equal(t, receivers.StateStopped, got[1].State)
	assert.Equal(t, "dump978-fa is disabled in UCI", got[1].Detail)
	assert.Equal(t, receivers.StateError, got[2].State)
	assert.Equal(t, receivers.HardwareAirspy, got[2].Hardware)
	assert.Equal(t, "tune failed", got[2].Detail)
	assert.Equal(t, receivers.StateStopped, got[3].State)
	assert.Equal(t, "no recent data", got[3].Detail)
	assert.Equal(t, receivers.Receiver{
		Role: receivers.RoleUnassigned, Serial: "00000001", Hardware: receivers.HardwareRTLSDR, Present: true,
		State: receivers.StateStopped, Detail: "not claimed by any role",
	}, got[4])
}

func TestAssess_problems(t *testing.T) {
	devs, err := receivers.Scan(sysfs())
	require.NoError(t, err)

	tests := []struct {
		name   string
		claims receivers.Claims
		role   int
		state  receivers.State
		detail string
	}{
		{name: "no serial", claims: receivers.Claims{}, role: 2, state: receivers.StateMissing, detail: "no serial configured"},
		{name: "unplugged", claims: receivers.Claims{Scan: "scan2"}, role: 3, state: receivers.StateMissing, detail: `no receiver with serial "scan2" plugged in`},
		{name: "by index", claims: receivers.Claims{ADSB1090: "0", ADSBByIndex: true}, role: 0, state: receivers.StateError, detail: "bus index"},
		{name: "reserved serial", claims: receivers.Claims{Scan: "1090"}, role: 3, state: receivers.StateError, detail: "reserved"},
		{name: "fallback-shaped serial", claims: receivers.Claims{Scan: "10901"}, role: 3, state: receivers.StateError, detail: "reserved"},
		{name: "duplicate", claims: receivers.Claims{UAT978: "scan", Scan: "scan"}, role: 1, state: receivers.StateError, detail: "also claimed"},
		{name: "wrong hardware", claims: receivers.Claims{Scan: "AIRSPY SN:44A061DC2E395E8F"}, role: 3, state: receivers.StateError, detail: "unsupported receiver type"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := receivers.Assess(tc.claims, devs, nil)
			assert.Equal(t, tc.state, got[tc.role].State)
			assert.Contains(t, got[tc.role].Detail, tc.detail)
		})
	}
}
