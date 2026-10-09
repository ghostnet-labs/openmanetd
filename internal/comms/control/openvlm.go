package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/rs/zerolog"
	hid "github.com/sstallion/go-hid"

	"github.com/openmanet/openmanetd/internal/comms/device"
)

// ─── OpenVLM constants ────────────────────────────────────────────────────────

const (
	// OpenVLMVendorID is the C-Media Electronics USB vendor identifier.
	OpenVLMVendorID uint16 = 0x0D8C
	// OpenVLMProductID identifies the OpenVLM (Open Voice Link Module) USB audio dongle.
	OpenVLMProductID uint16 = 0x0012

	// OpenVLMReportSize is the total HID input report buffer size.
	// The OS prepends a Report ID byte followed by the 4 OpenVLM data bytes
	// (IR0..IR3), giving 5 bytes total.
	OpenVLMReportSize = 5

	// OpenVLMPayloadOffset is the byte index at which the OpenVLM data payload
	// begins when the OS has prepended the one-byte Report ID.
	OpenVLMPayloadOffset = 1

	// OpenVLMGPIO3Mask selects GPIO3 within IR1.
	OpenVLMGPIO3Mask byte = 0x04

	// OpenVLMVolUpMask selects the CM108B volume-up button bit in IR0.
	// When set, the volume-up button is pressed; when clear, released.
	OpenVLMVolUpMask byte = 0x01

	// OpenVLMVolDnMask selects the CM108B volume-down button bit in IR0.
	OpenVLMVolDnMask byte = 0x02
)

// ─── HIDDevice / HIDOpener abstractions ──────────────────────────────────────

// HIDDevice is a minimal interface over a USB HID device.
type HIDDevice interface {
	Read(b []byte) (int, error)
	Close() error
}

// HIDOpener opens a HID device identified by its Vendor/Product ID pair.
// When serial is non-empty, the opener selects only the device whose USB
// serial number matches; an empty serial means "any matching device". The
// ROIP COS reader uses it; the OpenVLM source opens by hidraw path instead
// (HIDPathOpener) so it reads the exact device whose ALSA card is in use.
type HIDOpener func(vendorID, productID uint16, serial string) (HIDDevice, error)

// HIDPathOpener opens the HID device at a platform path (on Linux the
// /dev/hidrawN node DiscoverCM108 resolved from the USB parent).
type HIDPathOpener func(path string) (HIDDevice, error)

// hidDeviceWrapper decorates a *hid.Device so that Close also calls hid.Exit,
// keeping HIDAPI initialisation and teardown balanced.
type hidDeviceWrapper struct {
	inner *hid.Device
}

func (w *hidDeviceWrapper) Read(b []byte) (int, error) {
	n, err := w.inner.Read(b)
	if err != nil {
		return 0, fmt.Errorf("hid read: %w", err)
	}

	return n, nil
}

func (w *hidDeviceWrapper) Close() error {
	err := w.inner.Close()
	_ = hid.Exit()

	return err
}

// DefaultHIDOpener is the production HIDOpener. It initializes HIDAPI,
// opens the device (optionally filtering by USB serial number), and wraps
// it so that Close() performs cleanup. Pass serial=="" for "any matching
// device"; pass a non-empty serial to pin the open to a specific unit.
func DefaultHIDOpener(vendorID, productID uint16, serial string) (HIDDevice, error) {
	if err := hid.Init(); err != nil {
		return nil, fmt.Errorf("hid.Init: %w", err)
	}

	dev, err := hid.Open(vendorID, productID, serial)
	if err != nil {
		_ = hid.Exit()

		return nil, fmt.Errorf("hid.Open VID=0x%04X PID=0x%04X serial=%q: %w",
			vendorID, productID, serial, err)
	}

	return &hidDeviceWrapper{inner: dev}, nil
}

// DefaultHIDPathOpener is the production HIDPathOpener. On Linux hidapi's
// hidraw backend accepts the /dev/hidrawN path directly.
func DefaultHIDPathOpener(path string) (HIDDevice, error) {
	if err := hid.Init(); err != nil {
		return nil, fmt.Errorf("hid.Init: %w", err)
	}

	dev, err := hid.OpenPath(path)
	if err != nil {
		_ = hid.Exit()

		return nil, fmt.Errorf("hid.OpenPath %s: %w", path, err)
	}

	return &hidDeviceWrapper{inner: dev}, nil
}

// DeviceBinder is the slice of *device.Binder the OpenVLM source needs:
// pick (or keep) the paired CM108 device, and drop it after HID loss.
type DeviceBinder interface {
	Bind() (device.Selection, error)
	Release(sel device.Selection)
}

// ─── OpenVLMSource ────────────────────────────────────────────────────────────

const (
	openvlmAuxBufSize = 8

	// defaultOpenVLMRetryMin is the first wait after a failed bind/open and
	// the settle wait after a HID loss, giving udev time to remove the old
	// nodes before the rescan.
	defaultOpenVLMRetryMin = 500 * time.Millisecond
	// defaultOpenVLMRetryMax caps the exponential backoff. A device plugged
	// in after boot is picked up within this long; an absent device costs
	// one small sysfs scan per interval.
	defaultOpenVLMRetryMax = 10 * time.Second
)

// OpenVLMSource reads GPIO3 and the volume-button bits of HID_IR0 from an
// OpenVLM (Open Voice Link Module) USB HID audio device. It emits PTTDown
// when GPIO3 transitions HIGH and PTTUp when it transitions LOW; it also
// emits AuxEvent values on its AuxEvents() channel for volume up/down
// button transitions.
//
// The device is chosen through a DeviceBinder, so the hidraw node it reads
// belongs to the same USB device as the ALSA card used for audio. The
// source survives the device being absent at start, unplugged, or
// re-enumerated: it retries with capped exponential backoff until ctx is
// canceled, releases the binding on every HID loss, and balances a held
// PTT (and held volume buttons) with release events before reconnecting.
// The event channels close only when ctx ends.
//
// The aux channel buffer is small (8) and sends are non-blocking — a
// missing or slow consumer cannot stall the HID read loop.
type OpenVLMSource struct {
	log      zerolog.Logger
	opener   HIDPathOpener
	binder   DeviceBinder
	auxCh    chan AuxEvent
	retryMin time.Duration
	retryMax time.Duration
}

// OpenVLMOptions overrides OpenVLMSource dependencies. Zero values select
// the production defaults.
type OpenVLMOptions struct {
	Opener   HIDPathOpener
	Binder   DeviceBinder
	RetryMin time.Duration
	RetryMax time.Duration
}

// NewOpenVLMSource constructs an OpenVLMSource backed by the real HIDAPI
// library and the process-wide device.DefaultBinder.
func NewOpenVLMSource(log zerolog.Logger) EventSource {
	log.Info().Msgf("comms: PTT on OpenVLM HID device (VID=0x%04X PID=0x%04X preferred)",
		OpenVLMVendorID, OpenVLMProductID)

	return NewOpenVLMSourceWithOptions(log, OpenVLMOptions{})
}

// NewOpenVLMSourceWithOptions constructs an OpenVLMSource with injectable
// dependencies (tests use fakes for the opener and binder).
func NewOpenVLMSourceWithOptions(log zerolog.Logger, opts OpenVLMOptions) EventSource {
	s := &OpenVLMSource{
		log:      log,
		opener:   opts.Opener,
		binder:   opts.Binder,
		auxCh:    make(chan AuxEvent, openvlmAuxBufSize),
		retryMin: opts.RetryMin,
		retryMax: opts.RetryMax,
	}

	if s.opener == nil {
		s.opener = DefaultHIDPathOpener
	}

	if s.binder == nil {
		s.binder = device.DefaultBinder()
	}

	if s.retryMin <= 0 {
		s.retryMin = defaultOpenVLMRetryMin
	}

	if s.retryMax < s.retryMin {
		s.retryMax = max(defaultOpenVLMRetryMax, s.retryMin)
	}

	return s
}

// AuxEvents implements AuxEventSource. The returned channel is closed when
// the goroutine started by Events exits (context cancel).
func (s *OpenVLMSource) AuxEvents() <-chan AuxEvent { return s.auxCh }

// emitAux performs a non-blocking send to the aux channel. If no consumer
// is attached or the buffer is full, the event is dropped with a debug log;
// the HID read loop must never block on aux delivery.
func (s *OpenVLMSource) emitAux(ev AuxEvent) {
	select {
	case s.auxCh <- ev:
	default:
		s.log.Debug().Msg("OpenVLM: aux event dropped (no consumer / buffer full)")
	}
}

// Events implements EventSource. A single goroutine owns the whole
// bind → open → read → loss → rebind cycle and exits when ctx ends.
func (s *OpenVLMSource) Events(ctx context.Context) <-chan PTTEvent {
	ch := make(chan PTTEvent, 4)

	go func() {
		defer close(ch)
		defer close(s.auxCh)

		backoff := s.retryMin
		failures := 0

		for ctx.Err() == nil {
			sel, dev, err := s.connect()
			if err != nil {
				failures++
				s.logConnectFailure(err, failures, backoff)

				if !waitCtx(ctx, backoff) {
					return
				}

				backoff = min(backoff*2, s.retryMax)

				continue
			}

			if failures > 0 {
				s.log.Info().Int("failed_attempts", failures).Msg("OpenVLM: device attached")
			}

			failures = 0
			openedAt := time.Now()

			s.log.Info().
				Str("sys_path", sel.Device.SysPath).
				Str("hid_path", sel.Device.HIDPath).
				Int("alsa_card", sel.Device.ALSACardIdx).
				Str("serial", sel.Device.Serial).
				Str("vid_pid", fmt.Sprintf("%04x:%04x", sel.Device.VID, sel.Device.PID)).
				Bool("openvlm_confirmed", sel.Device.IsOpenVLM).
				Bool("ambiguous", sel.Ambiguous).
				Int("candidates", sel.Candidates).
				Msg("OpenVLM: opened HID device paired with ALSA card")

			if sel.Ambiguous {
				s.log.Warn().Str("sys_path", sel.Device.SysPath).
					Msg("OpenVLM: several equally ranked CM108 devices; chose lowest USB path")
			}

			if !sel.Device.IsOpenVLM {
				s.log.Warn().Str("sys_path", sel.Device.SysPath).
					Msg("OpenVLM: no GPIO1-strapped OpenVLM found; using generic CM108 device")
			}

			if !s.readUntilLoss(ctx, dev, ch) {
				return
			}

			s.binder.Release(sel)

			// A connection that held for a while resets the backoff; one
			// that dropped right after opening (stale node, flapping port)
			// keeps growing it so a bad device cannot spin the loop.
			if time.Since(openedAt) >= s.retryMax {
				backoff = s.retryMin
			}

			if !waitCtx(ctx, backoff) {
				return
			}

			backoff = min(backoff*2, s.retryMax)
		}
	}()

	return ch
}

// connect binds a device and opens its hidraw node.
func (s *OpenVLMSource) connect() (device.Selection, HIDDevice, error) {
	sel, err := s.binder.Bind()
	if err != nil {
		return sel, nil, fmt.Errorf("bind CM108 device: %w", err)
	}

	dev, err := s.opener(sel.Device.HIDPath)
	if err != nil {
		return sel, nil, fmt.Errorf("open %s: %w", sel.Device.HIDPath, err)
	}

	return sel, dev, nil
}

// logConnectFailure logs the first failure of a streak at Warn and the rest
// at Debug, so a node without an OpenVLM does not flood the log.
func (s *OpenVLMSource) logConnectFailure(err error, failures int, retryIn time.Duration) {
	ev := s.log.Debug()
	if failures == 1 {
		ev = s.log.Warn()
	}

	if errors.Is(err, device.ErrNoCM108) {
		ev = s.log.Debug()
		if failures == 1 {
			ev = s.log.Info()
		}
	}

	ev.Err(err).Int("attempt", failures).Dur("retry_in", retryIn).
		Msg("OpenVLM: device not available; waiting")
}

// waitCtx waits for d or ctx. Returns false when ctx ended the wait.
func waitCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// openvlmButtons is the last reported button state of one connection.
type openvlmButtons struct {
	ptt, volUp, volDn bool
}

// readUntilLoss reads reports from dev until the device fails or ctx ends.
// It returns false when ctx ended (caller must exit) and true after a
// device loss, by which time any held PTT / volume button has been
// balanced with a release event.
func (s *OpenVLMSource) readUntilLoss(ctx context.Context, dev HIDDevice, ch chan<- PTTEvent) bool {
	var closeOnce sync.Once

	closeDevice := func() {
		closeOnce.Do(func() {
			if cerr := dev.Close(); cerr != nil {
				s.log.Warn().Err(cerr).Msg("OpenVLM: error closing HID device")
			}
		})
	}

	stop := context.AfterFunc(ctx, closeDevice)

	defer func() {
		stop()
		closeDevice()
	}()

	buf := make([]byte, OpenVLMReportSize)

	var st openvlmButtons

	for {
		n, readErr := dev.Read(buf)
		if readErr != nil {
			if ctx.Err() != nil {
				return false
			}

			s.log.Warn().Err(readErr).Bool("ptt_held", st.ptt).
				Msg("OpenVLM: HID read failed; device lost, will rediscover")

			return s.releaseHeld(ctx, ch, st)
		}

		payloadStart := 0
		if n >= OpenVLMReportSize {
			payloadStart = OpenVLMPayloadOffset
		}

		if n < payloadStart+2 {
			s.log.Debug().Msgf("OpenVLM: short report (%d bytes), skipping", n)

			if !waitCtx(ctx, 50*time.Millisecond) {
				return false
			}

			continue
		}

		if !s.handleReport(ctx, ch, &st, buf[payloadStart], buf[payloadStart+1]) {
			return false
		}
	}
}

// releaseHeld balances held buttons after a device loss: a disconnected
// device cannot report the release edge, so without this the consumer
// would keep transmitting. Returns false when ctx ended first.
func (s *OpenVLMSource) releaseHeld(ctx context.Context, ch chan<- PTTEvent, st openvlmButtons) bool {
	if st.volUp {
		s.emitAux(VolumeUpReleased)
	}

	if st.volDn {
		s.emitAux(VolumeDownReleased)
	}

	if !st.ptt {
		return true
	}

	select {
	case ch <- PTTUp:
		return true
	case <-ctx.Done():
		return false
	}
}

// handleReport decodes one report and emits edge events. Returns false
// when ctx ended while delivering a PTT event.
func (s *OpenVLMSource) handleReport(ctx context.Context, ch chan<- PTTEvent, st *openvlmButtons, ir0, ir1 byte) bool {
	gpio3 := (ir1 & OpenVLMGPIO3Mask) != 0
	volUp := (ir0 & OpenVLMVolUpMask) != 0
	volDn := (ir0 & OpenVLMVolDnMask) != 0

	s.log.Trace().Msgf("OpenVLM: IR0=0x%02X IR1=0x%02X GPIO3=%v VOL+=%v VOL-=%v",
		ir0, ir1, gpio3, volUp, volDn)

	if volUp != st.volUp {
		st.volUp = volUp

		if volUp {
			s.log.Debug().Msg("OpenVLM: VOL+ pressed")
			s.emitAux(VolumeUpPressed)
		} else {
			s.log.Debug().Msg("OpenVLM: VOL+ released")
			s.emitAux(VolumeUpReleased)
		}
	}

	if volDn != st.volDn {
		st.volDn = volDn

		if volDn {
			s.log.Debug().Msg("OpenVLM: VOL- pressed")
			s.emitAux(VolumeDownPressed)
		} else {
			s.log.Debug().Msg("OpenVLM: VOL- released")
			s.emitAux(VolumeDownReleased)
		}
	}

	if gpio3 == st.ptt {
		return true
	}

	st.ptt = gpio3

	ev := PTTUp
	if gpio3 {
		ev = PTTDown

		s.log.Debug().Msg("OpenVLM: GPIO3 HIGH → PTTDown")
	} else {
		s.log.Debug().Msg("OpenVLM: GPIO3 LOW → PTTUp")
	}

	select {
	case ch <- ev:
		return true
	case <-ctx.Done():
		return false
	}
}

// ─── OpenVLM ALSA card detection ──────────────────────────────────────────────

// DetectAndSetALSACard binds the process-wide CM108 device (see
// device.Binder) and publishes its ALSA card number to ALSA_CARD. The card
// always belongs to the same USB device as the hidraw node the OpenVLM
// source reads. An operator-set ALSA_CARD is left unchanged.
func DetectAndSetALSACard(log zerolog.Logger) {
	DetectAndSetALSACardWith(device.DefaultBinder(), log)
}

// DetectAndSetALSACardWith is the testable core of DetectAndSetALSACard.
// It reports whether a device is bound afterwards.
func DetectAndSetALSACardWith(b *device.Binder, log zerolog.Logger) bool {
	if raw, ok := b.CardOverride(); ok {
		log.Debug().Str("ALSA_CARD", raw).Msg("OpenVLM: ALSA_CARD set by operator; not overriding it")
	}

	sel, err := b.Bind()
	if err != nil {
		log.Warn().Err(err).
			Int("cm108_count", sel.Candidates).
			Int("complete", sel.Complete).
			Int("probe_errors", sel.ProbeErrors).
			Msg("OpenVLM: no CM108 device bound; ALSA_CARD not set")

		return false
	}

	log.Info().
		Str("ALSA_CARD", strconv.Itoa(sel.Device.ALSACardIdx)).
		Str("hid_path", sel.Device.HIDPath).
		Str("sys_path", sel.Device.SysPath).
		Bool("openvlm_confirmed", sel.Device.IsOpenVLM).
		Bool("pinned_card", sel.PinnedCard).
		Msgf("OpenVLM: bound card %d via sysfs (VID=0x%04X PID=0x%04X)",
			sel.Device.ALSACardIdx, sel.Device.VID, sel.Device.PID)

	return true
}
