// Package receivers finds the node's software-defined receivers by USB
// serial and works out which service owns each one. Receivers are only
// ever claimed by serial, never by bus index:
//
//   - 1090 MHz ADS-B: dump1090, UCI dump1090.main.device_index (a serial)
//   - 978 MHz UAT: dump978-fa, UCI dump978-fa.main.serial
//   - wide tuner (Airspy R2 class): openmanetd, config scanner.receivers.wideSerial
//   - scan tuner (RTL-SDR): openmanetd, config scanner.receivers.scanSerial
package receivers

import (
	"cmp"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"time"
)

// DefaultSysfsRoot is where USB devices appear in sysfs.
const DefaultSysfsRoot = "/sys/bus/usb/devices"

// Hardware is a supported USB receiver family.
type Hardware uint8

// Hardware values.
const (
	HardwareUnknown Hardware = iota
	HardwareRTLSDR
	HardwareAirspy
)

// usbID is a vendor:product pair as sysfs prints it (lowercase hex).
type usbID struct{ vendor, product string }

// knownIDs maps supported USB IDs to their family. RTL2832U boards ship as
// 0bda:2838 (most dongle-style receivers) or 0bda:2832.
var knownIDs = map[usbID]Hardware{ //nolint:gochecknoglobals // read-only lookup table
	{"0bda", "2838"}: HardwareRTLSDR,
	{"0bda", "2832"}: HardwareRTLSDR,
	{"1d50", "60a1"}: HardwareAirspy,
}

// Device is a supported receiver found in sysfs.
type Device struct {
	// Serial is the USB iSerial string as sysfs reports it.
	Serial string
	// Dir is the sysfs device directory name (e.g. "1-1.2").
	Dir      string
	Hardware Hardware
}

// Scan lists supported receivers under fsys, which is rooted at the sysfs
// USB devices directory (DefaultSysfsRoot). Entries without idVendor (USB
// interfaces, hubs' ports) or with an unsupported ID are skipped. Devices
// are sorted by serial then directory.
func Scan(fsys fs.FS) ([]Device, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("list usb devices: %w", err)
	}

	out := make([]Device, 0, 4)

	for _, e := range entries {
		dir := e.Name()
		if strings.Contains(dir, ":") { // interface node, not a device
			continue
		}

		vendor, err := readAttr(fsys, dir, "idVendor")
		if err != nil {
			continue
		}

		product, err := readAttr(fsys, dir, "idProduct")
		if err != nil {
			continue
		}

		hw, ok := knownIDs[usbID{strings.ToLower(vendor), strings.ToLower(product)}]
		if !ok {
			continue
		}

		serial, _ := readAttr(fsys, dir, "serial") // a missing serial stays empty

		out = append(out, Device{Serial: serial, Dir: dir, Hardware: hw})
	}

	slices.SortFunc(out, func(a, b Device) int {
		return cmp.Or(cmp.Compare(a.Serial, b.Serial), cmp.Compare(a.Dir, b.Dir))
	})

	return out, nil
}

func readAttr(fsys fs.FS, dir, name string) (string, error) {
	b, err := fs.ReadFile(fsys, dir+"/"+name)
	if err != nil {
		return "", fmt.Errorf("read %s/%s: %w", dir, name, err)
	}

	return strings.TrimSpace(string(b)), nil
}

// SerialMatches reports whether a claimed serial names a device serial.
// RTL-SDR serials compare exactly. Airspy serials are a 64-bit hex number
// that tools print with or without an "AIRSPY SN:" or "0x" prefix and
// leading zeros, so those are normalised away before comparing.
func SerialMatches(claimed string, d Device) bool {
	if claimed == "" {
		return false
	}

	if claimed == d.Serial {
		return true
	}

	if d.Hardware != HardwareAirspy {
		return false
	}

	return normalizeAirspy(claimed) == normalizeAirspy(d.Serial)
}

func normalizeAirspy(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.TrimPrefix(s, "airspy sn:")
	s = strings.TrimPrefix(strings.TrimSpace(s), "0x")

	return strings.TrimLeft(s, "0")
}

// UCIGetter is the read side of a UCI tree.
type UCIGetter interface {
	Get(config, section, option string) ([]string, bool)
}

// Claims are the serials claimed by each role.
type Claims struct {
	ADSB1090 string
	UAT978   string
	Wide     string
	Scan     string
	// ADSBByIndex is true when dump1090.main.device_index holds a small
	// number dump1090 would read as a bus index rather than a serial.
	ADSBByIndex bool
	// ADSBEnabled and UATEnabled reflect the services' UCI enable flags.
	ADSBEnabled bool
	UATEnabled  bool
}

// maxIndexLike is the largest device_index dump1090 could treat as a bus
// index on a node (it reads a number as an index when that many receivers
// are attached; 1090 never is).
const maxIndexLike = 99

// ReadClaims reads dump1090 and dump978-fa ownership from UCI and takes the
// wide and scan serials from openmanetd config. A nil reader yields only
// the config serials.
func ReadClaims(u UCIGetter, wideSerial, scanSerial string) Claims {
	c := Claims{Wide: wideSerial, Scan: scanSerial}
	if u == nil {
		return c
	}

	get := func(config, section, option string) string {
		v, ok := u.Get(config, section, option)
		if !ok || len(v) == 0 {
			return ""
		}

		return strings.TrimSpace(v[0])
	}

	c.ADSB1090 = get("dump1090", "main", "device_index")
	if n, err := strconv.Atoi(c.ADSB1090); err == nil && n <= maxIndexLike {
		c.ADSBByIndex = true
	}

	c.ADSBEnabled = get("dump1090", "main", "enabled") != "0" && get("dump1090", "main", "disabled") != "1"
	c.UAT978 = get("dump978-fa", "main", "serial")
	c.UATEnabled = get("dump978-fa", "main", "enabled") == "1"

	return c
}

// Role is the job a receiver is claimed for.
type Role uint8

// Role values, in report order.
const (
	RoleADSB1090 Role = iota + 1
	RoleUAT978
	RoleWide
	RoleScan
	RoleUnassigned
)

// State is a receiver's run state.
type State uint8

// State values.
const (
	StateUnknown State = iota
	StateRunning
	StateStopped
	StateMissing
	StateError
)

// Activity is what the owning service of a role has produced.
type Activity struct {
	// LastSample is when the service last produced data (zero: never).
	LastSample time.Time
	// Err is a fault in the service's output (unreadable file etc.).
	Err error
	// Idle explains why a present receiver is not producing data; it is
	// used as the detail of a stopped receiver.
	Idle string
	// Fresh is true when LastSample is within the role's stale timeout.
	Fresh bool
}

// Receiver is the assessed state of one role or unassigned device.
type Receiver struct {
	LastSample time.Time
	Serial     string
	Owner      string
	Detail     string
	Role       Role
	Hardware   Hardware
	State      State
	Present    bool
}

// Owners per role.
const (
	OwnerDump1090   = "dump1090"
	OwnerDump978    = "dump978-fa"
	OwnerOpenMANETd = "openmanetd"
)

// errReserved is returned when a scanner role claims an aircraft
// receiver's serial (or one dump1090 would fall back to).
var errReserved = errors.New("serial is reserved for an aircraft receiver")

// Assess combines claims, the devices present and each role's activity
// into one Receiver per role (1090, 978, wide, scan) followed by present
// devices no role claims.
func Assess(c Claims, devices []Device, act map[Role]Activity) []Receiver {
	type roleSpec struct {
		serial string
		owner  string
		role   Role
		want   Hardware
	}

	specs := [...]roleSpec{
		{role: RoleADSB1090, serial: c.ADSB1090, owner: OwnerDump1090, want: HardwareRTLSDR},
		{role: RoleUAT978, serial: c.UAT978, owner: OwnerDump978, want: HardwareRTLSDR},
		{role: RoleWide, serial: c.Wide, owner: OwnerOpenMANETd},
		{role: RoleScan, serial: c.Scan, owner: OwnerOpenMANETd, want: HardwareRTLSDR},
	}

	out := make([]Receiver, 0, len(specs)+len(devices))
	claimed := make([]bool, len(devices))

	for _, sp := range specs {
		r := Receiver{Role: sp.role, Serial: sp.serial, Owner: sp.owner}

		for i, d := range devices {
			if SerialMatches(sp.serial, d) {
				r.Present, r.Hardware = true, d.Hardware
				claimed[i] = true

				break
			}
		}

		a := act[sp.role]
		r.LastSample = a.LastSample

		if err := conflict(sp.role, c); err != nil {
			r.State, r.Detail = StateError, err.Error()
			out = append(out, r)

			continue
		}

		switch {
		case sp.serial == "":
			r.State, r.Detail = StateMissing, "no serial configured"
		case !r.Present:
			r.State, r.Detail = StateMissing, "no receiver with serial "+strconv.Quote(sp.serial)+" plugged in"
		case sp.want != HardwareUnknown && r.Hardware != sp.want:
			r.State, r.Detail = StateError, "serial matches an unsupported receiver type for this role"
		case a.Err != nil:
			r.State, r.Detail = StateError, a.Err.Error()
		case a.Fresh:
			r.State = StateRunning
		default:
			r.State, r.Detail = StateStopped, cmp.Or(a.Idle, "no recent data")
		}

		out = append(out, r)
	}

	for i, d := range devices {
		if !claimed[i] {
			out = append(out, Receiver{
				Role: RoleUnassigned, Serial: d.Serial, Hardware: d.Hardware, Present: true,
				State: StateStopped, Detail: "not claimed by any role",
			})
		}
	}

	return out
}

// conflict reports a misconfiguration of role: dump1090 bound by index, a
// scanner role on an aircraft serial, or two roles on one serial.
func conflict(role Role, c Claims) error {
	serials := [...]string{
		RoleADSB1090: c.ADSB1090, RoleUAT978: c.UAT978, RoleWide: c.Wide, RoleScan: c.Scan,
	}
	mine := serials[role]

	if role == RoleADSB1090 && c.ADSBByIndex {
		return fmt.Errorf("dump1090 device_index %q is a bus index, not a serial", c.ADSB1090)
	}

	if mine == "" {
		return nil
	}

	if role == RoleWide || role == RoleScan {
		if mine == "1090" || mine == "978" || strings.HasPrefix(mine, "1090") || strings.HasSuffix(mine, "1090") {
			return fmt.Errorf("%q: %w", mine, errReserved)
		}
	}

	for other, s := range serials {
		if Role(other) != role && s == mine {
			return fmt.Errorf("serial %q is also claimed by another role", mine)
		}
	}

	return nil
}
