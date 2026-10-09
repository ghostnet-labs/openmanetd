package control

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"testing/fstest"
	"time"

	"github.com/rs/zerolog"

	"github.com/openmanet/openmanetd/internal/comms/device"
)

// ─── mockHIDDevice ────────────────────────────────────────────────────────────

// mockHIDDevice satisfies HIDDevice. Each call to Read returns the next queued
// report; when the queue is empty it blocks until Close is called.
type mockHIDDevice struct {
	reports    chan []byte
	closed     chan struct{}
	closeErr   error
	closeCalls int
}

func newMockHIDDevice() *mockHIDDevice {
	return &mockHIDDevice{
		reports: make(chan []byte, 16),
		closed:  make(chan struct{}),
	}
}

func (m *mockHIDDevice) queueReport(report []byte) {
	m.reports <- report
}

func (m *mockHIDDevice) Close() error {
	m.closeCalls++

	select {
	case <-m.closed:
	default:
		close(m.closed)
	}

	return m.closeErr
}

func (m *mockHIDDevice) Read(b []byte) (int, error) {
	select {
	case report := <-m.reports:
		n := copy(b, report)

		return n, nil
	case <-m.closed:
		return 0, errors.New("device closed")
	}
}

// ─── errHIDDevice ─────────────────────────────────────────────────────────────

type errHIDDevice struct{}

func (e *errHIDDevice) Read(_ []byte) (int, error) { return 0, errors.New("read error") }
func (e *errHIDDevice) Close() error               { return nil }

// ─── helpers ──────────────────────────────────────────────────────────────────

// makeOpenVLMReport builds a 5-byte OpenVLM report [ReportID, IR0, IR1, IR2, IR3].
// gpio3High sets or clears bit 2 of IR1.
func makeOpenVLMReport(gpio3High bool) []byte {
	return makeOpenVLMReportFull(0x00, gpio3High)
}

// makeOpenVLMReportFull builds a 5-byte OpenVLM report with a caller-supplied
// IR0 byte (volume up = bit 0, volume down = bit 1) and a GPIO3 bit in IR1.
func makeOpenVLMReportFull(ir0 byte, gpio3High bool) []byte {
	ir1 := byte(0x00)
	if gpio3High {
		ir1 |= OpenVLMGPIO3Mask
	}

	return []byte{0x00, ir0, ir1, 0x00, 0x00}
}

func collectAuxEvents(ch <-chan AuxEvent, timeout time.Duration) []AuxEvent {
	var events []AuxEvent

	deadline := time.After(timeout)

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}

			events = append(events, ev)
		case <-deadline:
			return events
		}
	}
}

func collectPTTEvents(ch <-chan PTTEvent, timeout time.Duration) []PTTEvent {
	var events []PTTEvent

	deadline := time.After(timeout)

	for {
		select {
		case ev, ok := <-ch:
			if !ok {
				return events
			}

			events = append(events, ev)
		case <-deadline:
			return events
		}
	}
}

func pathOpenerReturning(dev HIDDevice) HIDPathOpener {
	return func(string) (HIDDevice, error) {
		return dev, nil
	}
}

// newTestOpenVLMSource builds a source over a fake binder bound to a fixed
// hidraw path, with millisecond backoff so loss/rebind tests stay fast.
func newTestOpenVLMSource(opener HIDPathOpener, log zerolog.Logger) EventSource {
	return NewOpenVLMSourceWithOptions(log, OpenVLMOptions{
		Opener:   opener,
		Binder:   newFakeDeviceBinder("/dev/hidraw0", 1),
		RetryMin: time.Millisecond,
		RetryMax: 5 * time.Millisecond,
	})
}

func openerReturning(dev HIDDevice) HIDOpener {
	return func(_, _ uint16, _ string) (HIDDevice, error) {
		return dev, nil
	}
}

func openerFailing(err error) HIDOpener {
	return func(_, _ uint16, _ string) (HIDDevice, error) {
		return nil, err
	}
}

// ─── Tests ────────────────────────────────────────────────────────────────────

func TestOpenVLMSource_OpenerError_RetriesUntilCanceled(t *testing.T) {
	binder := newFakeDeviceBinder("/dev/hidraw3", 2)
	opens := make(chan string, 16)
	opener := func(path string) (HIDDevice, error) {
		select {
		case opens <- path:
		default:
		}

		return nil, errors.New("no device")
	}

	src := NewOpenVLMSourceWithOptions(zerolog.Nop(), OpenVLMOptions{
		Opener: opener, Binder: binder, RetryMin: time.Millisecond, RetryMax: 2 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	ch := src.Events(ctx)

	for range 3 {
		select {
		case <-opens:
		case <-time.After(time.Second):
			t.Fatal("source stopped retrying after an open failure")
		}
	}

	cancel()

	for range ch {
	}

	if binder.releaseCount() != 0 {
		t.Error("an open failure must not release the binding")
	}
}

func TestOpenVLMSource_OpenerCalledWithBoundHIDPath(t *testing.T) {
	resultCh := make(chan string, 1)

	mock := newMockHIDDevice()
	opener := func(path string) (HIDDevice, error) {
		resultCh <- path

		return mock, nil
	}

	src := NewOpenVLMSourceWithOptions(zerolog.Nop(), OpenVLMOptions{
		Opener: opener, Binder: newFakeDeviceBinder("/dev/hidraw7", 3),
	})

	src.Events(t.Context())

	select {
	case got := <-resultCh:
		if got != "/dev/hidraw7" {
			t.Errorf("opened %q, want the bound hidraw /dev/hidraw7", got)
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("opener was not called within timeout")
	}
}

func TestOpenVLMSource_GPIO3_LowReport_NoEvent(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReport(false))

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	ch := src.Events(ctx)
	events := collectPTTEvents(ch, 200*time.Millisecond)

	if len(events) != 0 {
		t.Errorf("expected no events for unchanged LOW state; got %v", events)
	}
}

func TestOpenVLMSource_GPIO3_HighReport_EmitsPTTDown(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReport(true))

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch := src.Events(ctx)

	select {
	case ev := <-ch:
		if ev != PTTDown {
			t.Errorf("expected PTTDown; got %v", ev)
		}
	case <-time.After(400 * time.Millisecond):
		t.Error("timed out waiting for PTTDown event")
	}
}

func TestOpenVLMSource_HighThenLow_EmitsPTTDownThenPTTUp(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReport(true))
	mock.queueReport(makeOpenVLMReport(false))

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch := src.Events(ctx)
	events := collectPTTEvents(ch, 400*time.Millisecond)

	if len(events) < 2 {
		t.Fatalf("expected 2 events; got %d: %v", len(events), events)
	}

	if events[0] != PTTDown {
		t.Errorf("event[0]: got %v, want PTTDown", events[0])
	}

	if events[1] != PTTUp {
		t.Errorf("event[1]: got %v, want PTTUp", events[1])
	}
}

func TestOpenVLMSource_DuplicateState_NoExtraEvent(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReport(true))  // HIGH → PTTDown
	mock.queueReport(makeOpenVLMReport(true))  // HIGH again → no event
	mock.queueReport(makeOpenVLMReport(false)) // LOW → PTTUp

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	ch := src.Events(ctx)
	events := collectPTTEvents(ch, 400*time.Millisecond)

	if len(events) != 2 {
		t.Fatalf("expected 2 events (no duplicate); got %d: %v", len(events), events)
	}
}

func TestOpenVLMSource_ContextCancel_ClosesChannel(t *testing.T) {
	mock := newMockHIDDevice() // empty queue — will block

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())
	ch := src.Events(ctx)

	cancel()

	// Give the goroutine time to observe the cancellation.
	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after context cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("channel not closed after context cancel")
	}
}

func TestOpenVLMSource_ReadError_ReleasesAndRebinds(t *testing.T) {
	binder := newFakeDeviceBinder("/dev/hidraw3", 2)

	src := NewOpenVLMSourceWithOptions(zerolog.Nop(), OpenVLMOptions{
		Opener:   pathOpenerReturning(&errHIDDevice{}),
		Binder:   binder,
		RetryMin: time.Millisecond,
		RetryMax: 2 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	ch := src.Events(ctx)

	deadline := time.After(time.Second)

	for binder.releaseCount() < 2 || binder.bindCount() < 3 {
		select {
		case _, ok := <-ch:
			if !ok {
				t.Fatal("event channel closed on device loss; source must keep rediscovering")
			}
		case <-deadline:
			t.Fatalf("binds=%d releases=%d; want repeated loss/rebind", binder.bindCount(), binder.releaseCount())
		case <-time.After(time.Millisecond):
		}
	}

	cancel()

	for range ch {
	}
}

func auxSource(t *testing.T, src EventSource) AuxEventSource {
	t.Helper()

	aux, ok := src.(AuxEventSource)
	if !ok {
		t.Fatalf("EventSource does not implement AuxEventSource: %T", src)
	}

	return aux
}

func TestOpenVLMSource_VolumeUp_PressRelease_EmitsAuxEvents(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReportFull(0x00, false))             // baseline
	mock.queueReport(makeOpenVLMReportFull(OpenVLMVolUpMask, false)) // press
	mock.queueReport(makeOpenVLMReportFull(0x00, false))             // release

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = src.Events(ctx)

	aux := auxSource(t, src).AuxEvents()
	events := collectAuxEvents(aux, 400*time.Millisecond)

	if len(events) < 2 {
		t.Fatalf("expected 2 aux events; got %d: %v", len(events), events)
	}

	if events[0] != VolumeUpPressed {
		t.Errorf("aux[0]: got %v, want VolumeUpPressed", events[0])
	}

	if events[1] != VolumeUpReleased {
		t.Errorf("aux[1]: got %v, want VolumeUpReleased", events[1])
	}
}

func TestOpenVLMSource_VolumeDown_PressRelease_EmitsAuxEvents(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReportFull(0x00, false))
	mock.queueReport(makeOpenVLMReportFull(OpenVLMVolDnMask, false))
	mock.queueReport(makeOpenVLMReportFull(0x00, false))

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = src.Events(ctx)

	aux := auxSource(t, src).AuxEvents()
	events := collectAuxEvents(aux, 400*time.Millisecond)

	if len(events) < 2 {
		t.Fatalf("expected 2 aux events; got %d: %v", len(events), events)
	}

	if events[0] != VolumeDownPressed {
		t.Errorf("aux[0]: got %v, want VolumeDownPressed", events[0])
	}

	if events[1] != VolumeDownReleased {
		t.Errorf("aux[1]: got %v, want VolumeDownReleased", events[1])
	}
}

func TestOpenVLMSource_VolumeUp_DuplicateLevel_NoExtraEvent(t *testing.T) {
	mock := newMockHIDDevice()
	mock.queueReport(makeOpenVLMReportFull(OpenVLMVolUpMask, false)) // press
	mock.queueReport(makeOpenVLMReportFull(OpenVLMVolUpMask, false)) // still pressed → no event
	mock.queueReport(makeOpenVLMReportFull(0x00, false))             // release

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	_ = src.Events(ctx)

	aux := auxSource(t, src).AuxEvents()
	events := collectAuxEvents(aux, 400*time.Millisecond)

	if len(events) != 2 {
		t.Fatalf("expected exactly 2 aux events (no duplicate); got %d: %v", len(events), events)
	}
}

func TestOpenVLMSource_VolumeAndPTT_EmitOnTheirOwnChannels(t *testing.T) {
	mock := newMockHIDDevice()
	// Press VOL+ while GPIO3 is LOW: only an aux event should fire.
	mock.queueReport(makeOpenVLMReportFull(OpenVLMVolUpMask, false))
	// Release VOL+ and assert PTT (GPIO3 HIGH): aux release + PTTDown.
	mock.queueReport(makeOpenVLMReportFull(0x00, true))

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	pttCh := src.Events(ctx)
	auxCh := auxSource(t, src).AuxEvents()

	pttEvents := collectPTTEvents(pttCh, 400*time.Millisecond)
	auxEvents := collectAuxEvents(auxCh, 50*time.Millisecond) // already drained, just to flush

	if len(pttEvents) < 1 || pttEvents[0] != PTTDown {
		t.Errorf("expected PTTDown; got %v", pttEvents)
	}

	wantAux := []AuxEvent{VolumeUpPressed, VolumeUpReleased}

	if len(auxEvents) != len(wantAux) {
		t.Fatalf("expected aux events %v; got %v", wantAux, auxEvents)
	}

	for i, want := range wantAux {
		if auxEvents[i] != want {
			t.Errorf("aux[%d]: got %v, want %v", i, auxEvents[i], want)
		}
	}
}

func TestOpenVLMSource_AuxChannel_ClosedOnContextCancel(t *testing.T) {
	mock := newMockHIDDevice()

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithCancel(context.Background())

	_ = src.Events(ctx)

	aux := auxSource(t, src).AuxEvents()

	cancel()

	select {
	case _, ok := <-aux:
		if ok {
			t.Error("expected aux channel to be closed after context cancel")
		}
	case <-time.After(500 * time.Millisecond):
		t.Error("aux channel not closed after context cancel")
	}
}

// ─── ALSA card detection tests ───────────────────────────────────────────────

// mkCM108SysFS builds a sysfs tree with one CM108-family device at
// bus/usb/devices/<name>: a hidraw child on interface :1.3 and an ALSA
// card child on interface :1.0, mirroring a real CM108B layout.
func mkCM108SysFS(fsys fstest.MapFS, name, product string, hidraw, card int) fstest.MapFS {
	if fsys == nil {
		fsys = fstest.MapFS{}
	}

	base := "bus/usb/devices/" + name
	fsys[base+"/idVendor"] = &fstest.MapFile{Data: []byte("0d8c\n")}
	fsys[base+"/idProduct"] = &fstest.MapFile{Data: []byte(product + "\n")}
	fsys[base+"/serial"] = &fstest.MapFile{Data: []byte("SN-" + name + "\n")}

	if card >= 0 {
		fsys[base+"/"+name+":1.0/sound/card"+strconv.Itoa(card)+"/id"] = &fstest.MapFile{Data: []byte("Device\n")}
	}

	if hidraw >= 0 {
		fsys[base+"/"+name+":1.3/0003:0D8C:"+product+".0001/hidraw/hidraw"+strconv.Itoa(hidraw)+"/dev"] =
			&fstest.MapFile{Data: []byte("242:0\n")}
	}

	return fsys
}

// strapProbe reports GPIO1 high for the hidraw paths in strapped.
func strapProbe(strapped ...string) device.HIDInputReader {
	return func(path string) ([]byte, error) {
		for _, p := range strapped {
			if p == path {
				return []byte{0, 0, 0x01, 0, 0}, nil
			}
		}

		return []byte{0, 0, 0, 0, 0}, nil
	}
}

func TestDetectAndSetALSACardWith(t *testing.T) {
	tests := []struct {
		name     string
		fsys     fstest.MapFS
		env      []string
		probe    device.HIDInputReader
		wantOK   bool
		wantCard string
	}{
		{
			name:     "single OpenVLM sets its own card",
			fsys:     mkCM108SysFS(nil, "1-1", "0012", 2, 4),
			probe:    strapProbe("/dev/hidraw2"),
			wantOK:   true,
			wantCard: "4",
		},
		{
			name:     "OpenVLM preferred over generic CM108 on a lower path",
			fsys:     mkCM108SysFS(mkCM108SysFS(nil, "1-1", "013c", 0, 1), "1-2", "0012", 5, 3),
			probe:    strapProbe("/dev/hidraw5"),
			wantOK:   true,
			wantCard: "3",
		},
		{
			name:     "operator override is never overwritten",
			fsys:     mkCM108SysFS(nil, "1-1", "0012", 2, 4),
			env:      []string{"ALSA_CARD", "9"},
			probe:    strapProbe("/dev/hidraw2"),
			wantOK:   true,
			wantCard: "9",
		},
		{
			name:  "empty sysfs leaves ALSA_CARD unset",
			fsys:  fstest.MapFS{},
			probe: strapProbe(),
		},
		{
			name:  "device without an ALSA card is not paired",
			fsys:  mkCM108SysFS(nil, "1-1", "0012", 2, -1),
			probe: strapProbe("/dev/hidraw2"),
		},
		{
			name:  "non-CM108 product is skipped",
			fsys:  mkCM108SysFS(nil, "1-1", "0002", 2, 4),
			probe: strapProbe("/dev/hidraw2"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := newFakeEnv(tt.env...)
			b := device.NewBinder(device.BinderConfig{FS: tt.fsys, Probe: tt.probe, Env: env})

			ok := DetectAndSetALSACardWith(b, zerolog.Nop())
			if ok != tt.wantOK {
				t.Fatalf("DetectAndSetALSACardWith = %v, want %v", ok, tt.wantOK)
			}

			if got := env.Getenv("ALSA_CARD"); got != tt.wantCard {
				t.Errorf("ALSA_CARD = %q, want %q", got, tt.wantCard)
			}
		})
	}
}

// ─── OpenVLM short-report test ─────────────────────────────────────────────────

func TestOpenVLMSource_ShortReport_SkippedAndContinues(t *testing.T) {
	// A 1-byte report is below the 2-byte minimum and must be skipped.
	// The source should sleep 50 ms then continue processing the queued
	// valid report and emit PTTDown.
	mock := newMockHIDDevice()
	mock.queueReport([]byte{0x00})            // 1-byte short report — skipped
	mock.queueReport(makeOpenVLMReport(true)) // valid HIGH report → PTTDown

	src := newTestOpenVLMSource(pathOpenerReturning(mock), zerolog.Nop())

	ctx, cancel := context.WithTimeout(context.Background(), 800*time.Millisecond)
	defer cancel()

	ch := src.Events(ctx)

	select {
	case ev := <-ch:
		if ev != PTTDown {
			t.Errorf("expected PTTDown after short report skipped; got %v", ev)
		}
	case <-time.After(700 * time.Millisecond):
		t.Error("timed out — short report may not have been skipped correctly")
	}
}
