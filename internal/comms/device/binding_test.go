package device_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/openmanet/openmanetd/internal/comms/device"
)

func TestSelectCM108(t *testing.T) {
	openvlm := device.CM108Descriptor{SysPath: "bus/usb/devices/1-1.3", HIDPath: hidraw(3), ALSACardIdx: 2, PID: 0x0012}
	openvlm2 := device.CM108Descriptor{SysPath: "bus/usb/devices/1-1.4", HIDPath: hidraw(4), ALSACardIdx: 5, PID: 0x0012}
	generic := device.CM108Descriptor{SysPath: "bus/usb/devices/1-1.1", HIDPath: hidraw(1), ALSACardIdx: 1, PID: 0x013c}
	generic2 := device.CM108Descriptor{SysPath: "bus/usb/devices/1-1.2", HIDPath: hidraw(2), ALSACardIdx: 0, PID: 0x013c}
	noAudio := device.CM108Descriptor{SysPath: "bus/usb/devices/1-1.0", HIDPath: hidraw(0), ALSACardIdx: device.NoCard}
	noHID := device.CM108Descriptor{SysPath: "bus/usb/devices/1-1.0", ALSACardIdx: 7}

	tests := []struct {
		name          string
		descs         []device.CM108Descriptor
		strapped      []string
		failing       []string
		pin           int
		wantErr       error
		wantPath      string
		wantOpenVLM   bool
		wantAmbiguous bool
		wantPinned    bool
	}{
		{name: "none", pin: device.NoCard, wantErr: device.ErrNoCM108},
		{name: "missing audio and HID children", descs: []device.CM108Descriptor{noAudio, noHID}, pin: device.NoCard, wantErr: device.ErrCM108Incomplete},
		{
			name: "OpenVLM preferred over generic on lower path", descs: []device.CM108Descriptor{generic, openvlm},
			strapped: []string{hidraw(3)}, pin: device.NoCard, wantPath: openvlm.SysPath, wantOpenVLM: true,
		},
		{
			name: "incomplete OpenVLM is skipped", descs: []device.CM108Descriptor{noAudio, openvlm},
			strapped: []string{hidraw(0), hidraw(3)}, pin: device.NoCard, wantPath: openvlm.SysPath, wantOpenVLM: true,
		},
		{
			name: "two OpenVLMs choose lowest path and flag ambiguity", descs: []device.CM108Descriptor{openvlm2, openvlm},
			strapped: []string{hidraw(3), hidraw(4)}, pin: device.NoCard,
			wantPath: openvlm.SysPath, wantOpenVLM: true, wantAmbiguous: true,
		},
		{
			name: "generic fallback when every probe answered", descs: []device.CM108Descriptor{generic},
			pin: device.NoCard, wantPath: generic.SysPath,
		},
		{
			name: "several generics are ambiguous", descs: []device.CM108Descriptor{generic2, generic},
			pin: device.NoCard, wantPath: generic.SysPath, wantAmbiguous: true,
		},
		{
			name: "failed probe blocks generic fallback", descs: []device.CM108Descriptor{generic, openvlm},
			failing: []string{hidraw(3)}, pin: device.NoCard, wantErr: device.ErrIdentityUnknown,
		},
		{
			name: "failed probe on generic still binds confirmed OpenVLM", descs: []device.CM108Descriptor{generic, openvlm},
			strapped: []string{hidraw(3)}, failing: []string{hidraw(1)}, pin: device.NoCard,
			wantPath: openvlm.SysPath, wantOpenVLM: true,
		},
		{
			name: "operator card pin wins", descs: []device.CM108Descriptor{generic, openvlm},
			strapped: []string{hidraw(3)}, pin: 1, wantPath: generic.SysPath, wantPinned: true,
		},
		{
			name: "pin naming no CM108 falls back to ranking", descs: []device.CM108Descriptor{generic, openvlm},
			strapped: []string{hidraw(3)}, pin: 9, wantPath: openvlm.SysPath, wantOpenVLM: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			probe := newFakeProbe(tc.strapped...)
			for _, f := range tc.failing {
				probe.fail(f)
			}

			sel, err := device.SelectCM108(tc.descs, probe.read, tc.pin)
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tc.wantPath, sel.Device.SysPath)
			assert.Equal(t, tc.wantOpenVLM, sel.Device.IsOpenVLM)
			assert.Equal(t, tc.wantAmbiguous, sel.Ambiguous)
			assert.Equal(t, tc.wantPinned, sel.PinnedCard)
			assert.Equal(t, len(tc.descs), sel.Candidates)
		})
	}
}

func TestSelectCM108_doesNotModifyInput(t *testing.T) {
	descs := []device.CM108Descriptor{
		{SysPath: "b", HIDPath: hidraw(2), ALSACardIdx: 2},
		{SysPath: "a", HIDPath: hidraw(1), ALSACardIdx: 1},
	}

	_, err := device.SelectCM108(descs, newFakeProbe(hidraw(1), hidraw(2)).read, device.NoCard)
	require.NoError(t, err)
	assert.Equal(t, "b", descs[0].SysPath)
	assert.False(t, descs[0].IsOpenVLM)
}
