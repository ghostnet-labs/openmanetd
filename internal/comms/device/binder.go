package device

import (
	"fmt"
	"io/fs"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
)

// ALSACardEnv is the environment variable alsa-lib's default PCM / CTL
// configuration reads to pick a card. The Binder is the only writer of it
// in this process; it never overwrites a value the operator set before the
// Binder was created.
const ALSACardEnv = "ALSA_CARD"

// Environment is the process-environment seam used by Binder. OSEnvironment
// is the production implementation; tests supply an in-memory fake so they
// never mutate the real process environment.
type Environment interface {
	Getenv(key string) string
	Setenv(key, value string) error
	Unsetenv(key string) error
}

// OSEnvironment implements Environment over package os.
type OSEnvironment struct{}

// Getenv implements Environment.
func (OSEnvironment) Getenv(key string) string { return os.Getenv(key) }

// Setenv implements Environment.
func (OSEnvironment) Setenv(key, value string) error {
	if err := os.Setenv(key, value); err != nil {
		return fmt.Errorf("setenv %s: %w", key, err)
	}

	return nil
}

// Unsetenv implements Environment.
func (OSEnvironment) Unsetenv(key string) error {
	if err := os.Unsetenv(key); err != nil {
		return fmt.Errorf("unsetenv %s: %w", key, err)
	}

	return nil
}

// BinderConfig configures a Binder. Zero values select production defaults.
type BinderConfig struct {
	// FS is a filesystem rooted at /sys. Nil means os.DirFS("/sys").
	FS fs.FS
	// Probe reads a HID input report for the GPIO1 identity check. Nil
	// means the real HIDIOCGINPUT reader.
	Probe HIDInputReader
	// Env is the process environment. Nil means OSEnvironment.
	Env Environment
}

// Binder owns the single process-wide choice of which CM108-family USB
// device the comms subsystem uses. The hidraw node (PTT/volume buttons) and
// the ALSA card (capture, playback and mixer) always come from the same
// Selection, i.e. from one USB parent directory, so they cannot drift onto
// two different devices.
//
// Bind is sticky: once a device is bound, later Bind calls keep it for as
// long as the same USB parent (same sys path, VID/PID and serial) is still
// present with both children, refreshing hidraw / card numbers that the
// kernel renumbered. Release drops the binding after the HID reader has
// lost the device, so the next Bind rescans and re-probes from scratch.
//
// The ALSA card number is published to ALSA_CARD for alsa-lib (malgo's
// default device and the mixer read it). When ALSA_CARD was already set
// when the Binder was created, it is treated as an operator override: the
// Binder never writes or clears it and prefers the CM108 that owns that
// card. Otherwise the Binder sets it on bind and clears it on loss so a
// renumbered card can never leave a stale, possibly foreign, card number
// behind.
//
// Binder is safe for concurrent use.
type Binder struct { //nolint:govet // fieldalignment: mu must sit directly above the fields it guards (.claude/rules/concurrency.md); the pointer-scan-optimal layout would separate them.
	fsys  fs.FS
	probe HIDInputReader
	env   Environment

	// overrideRaw is the operator's ALSA_CARD at construction ("" = none);
	// overrideCard is its parsed value, or NoCard if absent or invalid.
	overrideRaw  string
	overrideCard int

	// snap is the latest published Selection for lock-free snapshot reads;
	// nil while unbound.
	snap atomic.Pointer[Selection]

	binds        atomic.Int64
	losses       atomic.Int64
	bindFailures atomic.Int64

	// opMu serializes whole Bind / Release operations (scan, probe, commit,
	// env update) so concurrent callers cannot interleave a selection with
	// a release. It is held across sysfs reads and the HID identity
	// ioctl by design; it is never taken on an audio or packet hot path.
	opMu sync.Mutex

	mu       sync.Mutex // protects the fields below
	cur      Selection
	bound    bool
	autoCard string
	changed  chan struct{}
}

// NewBinder returns a Binder. The current ALSA_CARD value, if any, is
// captured as the operator override.
func NewBinder(cfg BinderConfig) *Binder {
	if cfg.FS == nil {
		cfg.FS = os.DirFS("/sys")
	}

	if cfg.Env == nil {
		cfg.Env = OSEnvironment{}
	}

	b := &Binder{
		fsys:         cfg.FS,
		probe:        cfg.Probe,
		env:          cfg.Env,
		overrideCard: NoCard,
		changed:      make(chan struct{}),
	}

	if raw := cfg.Env.Getenv(ALSACardEnv); raw != "" {
		b.overrideRaw = raw

		if n, err := strconv.Atoi(raw); err == nil && n >= 0 {
			b.overrideCard = n
		}
	}

	return b
}

var defaultBinder = sync.OnceValue(func() *Binder { return NewBinder(BinderConfig{}) }) //nolint:gochecknoglobals // process-wide device ownership is the point

// DefaultBinder returns the process-wide Binder over the real /sys and
// environment. The HID control source, ALSA card detection and the mixer
// all share it so they agree on one device.
func DefaultBinder() *Binder { return defaultBinder() }

// CardOverride reports whether the operator pinned ALSA_CARD before the
// Binder was created, and its raw value.
func (b *Binder) CardOverride() (string, bool) {
	return b.overrideRaw, b.overrideRaw != ""
}

// Current returns the bound Selection, if any.
func (b *Binder) Current() (Selection, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.cur, b.bound
}

// Changed returns a channel that is closed at the next binding change
// (bind to a different device or different hidraw/card numbers, or loss).
// Callers re-arm by calling Changed again after it fires.
func (b *Binder) Changed() <-chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.changed
}

// Bind returns the device the comms subsystem should use, keeping the
// current binding when its USB parent is still present. Errors wrap
// ErrNoCM108, ErrCM108Incomplete, ErrIdentityUnknown or a sysfs read error;
// all are transient and callers retry with backoff. A failed Bind while a
// device was bound counts as a loss.
func (b *Binder) Bind() (Selection, error) {
	b.opMu.Lock()
	defer b.opMu.Unlock()

	descs, err := DiscoverCM108(b.fsys)
	if err != nil {
		b.bindFailures.Add(1)

		return Selection{}, err
	}

	if cur, ok := b.Current(); ok {
		if d, found := findSameDevice(descs, &cur.Device); found {
			cur.Device.HIDPath = d.HIDPath
			cur.Device.ALSACardIdx = d.ALSACardIdx
			cur.Candidates = len(descs)
			b.commit(cur)

			return cur, nil
		}
	}

	sel, err := SelectCM108(descs, b.probe, b.overrideCard)
	if err != nil {
		b.bindFailures.Add(1)
		b.unbind()

		return sel, err
	}

	b.commit(sel)

	return sel, nil
}

// Release drops the binding if sel is still the bound device. The HID
// reader calls it after a read failure (unplug, USB reset) so the next Bind
// rescans instead of trusting a sticky path whose children may now belong
// to a different device.
func (b *Binder) Release(sel Selection) {
	b.opMu.Lock()
	defer b.opMu.Unlock()

	if cur, ok := b.Current(); !ok || cur.Device.SysPath != sel.Device.SysPath {
		return
	}

	b.unbind()
}

// findSameDevice returns the descriptor in descs that is the same physical
// device as want (same USB parent path, VID/PID and serial) and still has
// both children.
func findSameDevice(descs []CM108Descriptor, want *CM108Descriptor) (CM108Descriptor, bool) {
	for i := range descs {
		d := &descs[i]
		if d.SysPath == want.SysPath && d.VID == want.VID && d.PID == want.PID &&
			d.Serial == want.Serial && d.complete() {
			return *d, true
		}
	}

	return CM108Descriptor{}, false
}

// commit records sel as bound, publishes ALSA_CARD and signals a change if
// anything observable differs. Caller holds opMu.
func (b *Binder) commit(sel Selection) {
	b.mu.Lock()
	changed := !b.bound || b.cur.Device.SysPath != sel.Device.SysPath ||
		b.cur.Device.HIDPath != sel.Device.HIDPath ||
		b.cur.Device.ALSACardIdx != sel.Device.ALSACardIdx
	b.cur = sel
	b.bound = true

	if changed {
		b.signalLocked()
	}
	b.mu.Unlock()

	if changed {
		b.binds.Add(1)

		snap := sel
		b.snap.Store(&snap)
	}

	if b.overrideRaw != "" {
		return
	}

	card := strconv.Itoa(sel.Device.ALSACardIdx)
	if b.env.Getenv(ALSACardEnv) == card {
		b.setAutoCard(card)

		return
	}

	if err := b.env.Setenv(ALSACardEnv, card); err == nil {
		b.setAutoCard(card)
	}
}

// unbind clears the binding (if any), counts a loss, signals the change and
// clears the auto-set ALSA_CARD. Caller holds opMu.
func (b *Binder) unbind() {
	b.mu.Lock()
	wasBound := b.bound
	auto := b.autoCard
	b.bound = false
	b.cur = Selection{}
	b.autoCard = ""

	if wasBound {
		b.signalLocked()
	}
	b.mu.Unlock()

	if !wasBound {
		return
	}

	b.losses.Add(1)
	b.snap.Store(nil)

	// Only clear a value this Binder wrote; never the operator's override.
	if auto != "" && b.env.Getenv(ALSACardEnv) == auto {
		_ = b.env.Unsetenv(ALSACardEnv) //nolint:errcheck // best-effort; os.Unsetenv only fails on invalid keys
	}
}

func (b *Binder) setAutoCard(card string) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.autoCard = card
}

// signalLocked wakes every Changed waiter. Caller holds mu.
func (b *Binder) signalLocked() {
	close(b.changed)
	b.changed = make(chan struct{})
}

// BindingSnapshot is the instrumentation view of the Binder, published as
// comms.device_binding. Field semantics are documented in
// docs/instrumentation-snapshot.md.
type BindingSnapshot struct {
	// SysPath is the bound device's USB parent path under /sys.
	SysPath string `json:"sys_path"`
	// HIDPath is the bound hidraw node.
	HIDPath string `json:"hid_path"`
	// Serial is the bound device's USB serial (may be empty).
	Serial string `json:"serial"`
	// ALSACard is the bound ALSA card index, -1 when unbound.
	ALSACard int `json:"alsa_card"`
	// Candidates is the CM108-family device count seen at the last bind.
	Candidates int `json:"candidates"`
	// Binds counts binding changes (first bind, rebind, renumber).
	Binds int64 `json:"binds"`
	// Losses counts bound devices that disappeared or were released.
	Losses int64 `json:"losses"`
	// BindFailures counts Bind calls that found no usable device.
	BindFailures int64 `json:"bind_failures"`
	// Bound is true while a device is bound.
	Bound bool `json:"bound"`
	// OpenVLM is true when the bound device's GPIO1 strap read high.
	OpenVLM bool `json:"openvlm"`
	// Ambiguous is true when the bound device tied with another candidate.
	Ambiguous bool `json:"ambiguous"`
	// CardOverride is true when ALSA_CARD was set by the operator.
	CardOverride bool `json:"card_override"`
}

// Snapshot fills dst with atomic loads only. Nil-receiver safe, zero-alloc.
func (b *Binder) Snapshot(dst *BindingSnapshot) {
	if dst == nil {
		return
	}

	if b == nil {
		*dst = BindingSnapshot{ALSACard: NoCard}

		return
	}

	dst.Binds = b.binds.Load()
	dst.Losses = b.losses.Load()
	dst.BindFailures = b.bindFailures.Load()
	dst.CardOverride = b.overrideRaw != ""

	sel := b.snap.Load()
	if sel == nil {
		dst.Bound = false
		dst.SysPath, dst.HIDPath, dst.Serial = "", "", ""
		dst.ALSACard = NoCard
		dst.Candidates = 0
		dst.OpenVLM = false
		dst.Ambiguous = false

		return
	}

	dst.Bound = true
	dst.SysPath = sel.Device.SysPath
	dst.HIDPath = sel.Device.HIDPath
	dst.Serial = sel.Device.Serial
	dst.ALSACard = sel.Device.ALSACardIdx
	dst.Candidates = sel.Candidates
	dst.OpenVLM = sel.Device.IsOpenVLM
	dst.Ambiguous = sel.Ambiguous
}
