//go:build !(mips || mipsle || mips64 || mips64le)

package hardware

// usbdevfsReset is USBDEVFS_RESET, _IO('U', 20).
const usbdevfsReset = 0x5514
