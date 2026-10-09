//go:build mips || mipsle || mips64 || mips64le

package hardware

// usbdevfsReset is USBDEVFS_RESET, _IO('U', 20). MIPS encodes _IOC_NONE as
// 1 at bit 29, so the request number differs from other architectures.
const usbdevfsReset = 0x20005514
