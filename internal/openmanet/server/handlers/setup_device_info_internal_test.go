package handlers

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseDeviceProduct(t *testing.T) {
	cases := []struct {
		name string
		info string
		want string
	}{
		{name: "single quotes", info: "DEVICE_MANUFACTURER='Protectli'\nDEVICE_PRODUCT='x86_64'\nDEVICE_REVISION='v0'\n", want: "x86_64"},
		{name: "double quotes", info: "DEVICE_PRODUCT=\"Venice\"\n", want: "Venice"},
		{name: "unquoted", info: "DEVICE_PRODUCT=BCM2712\n", want: "BCM2712"},
		{name: "space in product", info: "DEVICE_PRODUCT='NanoPC-T6 LTS'\n", want: "NanoPC-T6 LTS"},
		{name: "missing", info: "DEVICE_MANUFACTURER='OpenWrt'\n", want: ""},
		{name: "empty file", info: "", want: ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, parseDeviceProduct(tc.info))
		})
	}
}
