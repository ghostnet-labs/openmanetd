package openmanet

import (
	"bytes"
	"errors"
	"slices"
	"sync"
	"time"

	"github.com/digineo/go-uci/v2"
	"github.com/mdlayher/wifi"
	"github.com/openmanet/go-alfred"
	"tailscale.com/ipn/ipnstate"

	jamv1 "github.com/openmanet/openmanetd/internal/api/openmanet/jamdetect/v1"
	"github.com/openmanet/openmanetd/internal/chanmig"
	"github.com/openmanet/openmanetd/internal/gpsd"
	"github.com/openmanet/openmanetd/internal/hardware"
	"github.com/openmanet/openmanetd/internal/scanner"
	"github.com/openmanet/openmanetd/internal/system"
)

// fakeAlfredClient implements alfred.ReadWriteClient in memory.
type fakeAlfredClient struct {
	requestErr error
	setErr     error
	records    []alfred.Record
	sets       []fakeAlfredSet
	requests   []uint8
	mu         sync.Mutex
}

type fakeAlfredSet struct {
	payload  []byte
	dataType uint8
	version  uint8
}

func (f *fakeAlfredClient) Request(dataType uint8) ([]alfred.Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.requests = append(f.requests, dataType)

	if f.requestErr != nil {
		return nil, f.requestErr
	}

	return slices.Clone(f.records), nil
}

func (f *fakeAlfredClient) Set(dataType, version uint8, payload []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sets = append(f.sets, fakeAlfredSet{dataType: dataType, version: version, payload: bytes.Clone(payload)})

	return f.setErr
}

func (f *fakeAlfredClient) setCalls() []fakeAlfredSet {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.sets)
}

func (f *fakeAlfredClient) requestCalls() []uint8 {
	f.mu.Lock()
	defer f.mu.Unlock()

	return slices.Clone(f.requests)
}

// fakeWirelessUCI implements network.ConfigReader for the wireless config.
type fakeWirelessUCI struct {
	options  map[string]map[string][]string
	types    map[string]string
	sections []string
	mu       sync.Mutex
}

func newFakeWirelessUCI() *fakeWirelessUCI {
	return &fakeWirelessUCI{options: map[string]map[string][]string{}, types: map[string]string{}}
}

func (f *fakeWirelessUCI) addDevice(name, band, channel, htmode string) {
	f.mu.Lock()
	defer f.mu.Unlock()

	f.sections = append(f.sections, name)
	f.types[name] = "wifi-device"
	f.options[name] = map[string][]string{"band": {band}, "channel": {channel}, "htmode": {htmode}}
}

func (f *fakeWirelessUCI) Get(_, section, option string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()

	v, ok := f.options[section][option]

	return slices.Clone(v), ok
}

func (f *fakeWirelessUCI) GetSections(_, secType string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	out := make([]string, 0, len(f.sections))

	for _, s := range f.sections {
		if f.types[s] == secType {
			out = append(out, s)
		}
	}

	return out, nil
}

func (f *fakeWirelessUCI) SetType(_, section, option string, _ uci.OptionType, values ...string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.options[section] == nil {
		f.options[section] = map[string][]string{}
	}

	f.options[section][option] = slices.Clone(values)

	return nil
}

func (f *fakeWirelessUCI) Del(_, section, option string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.options[section], option)

	return nil
}

func (f *fakeWirelessUCI) AddSection(_, section, typ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if _, ok := f.types[section]; !ok {
		f.sections = append(f.sections, section)
		f.types[section] = typ
	}

	return nil
}

func (f *fakeWirelessUCI) DelSection(_, section string) error {
	f.mu.Lock()
	defer f.mu.Unlock()

	delete(f.types, section)
	delete(f.options, section)

	return nil
}

func (f *fakeWirelessUCI) Commit() error { return nil }

func (f *fakeWirelessUCI) ReloadConfig() error { return nil }

// Fakes for the ATAK marker status adapter (takstatus.go).

type fakeSysReader struct {
	mu      sync.Mutex
	uptime  time.Duration
	mem     *system.MemoryInfo
	load    float32
	temp    float32
	loadErr error
	tempErr error
}

func (f *fakeSysReader) GetUptime() (time.Duration, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.uptime, nil
}

func (f *fakeSysReader) GetMemoryInfo() (*system.MemoryInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.mem, nil
}

func (f *fakeSysReader) GetCPULoadPercent() (float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.load, f.loadErr
}

func (f *fakeSysReader) GetCPUTempCelsius() (float32, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.temp, f.tempErr
}

type fakeHardwareReader struct {
	mu sync.Mutex
	st hardware.Status
}

func (f *fakeHardwareReader) Status() hardware.Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.st
}

type fakeJamReporter struct {
	mu     sync.Mutex
	report *jamv1.JamReport
}

func (f *fakeJamReporter) Report() *jamv1.JamReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.report
}

type fakeGPSReader struct {
	mu  sync.Mutex
	pos gpsd.PositionReport
	sky gpsd.SatelliteReport
}

func (f *fakeGPSReader) GetPosition() gpsd.PositionReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.pos
}

func (f *fakeGPSReader) GetSatelliteReport() gpsd.SatelliteReport {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.sky
}

type fakeCommsReader struct {
	mu           sync.Mutex
	talkGroup    int
	transmitting bool
}

func (f *fakeCommsReader) ActiveTalkGroup() int {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.talkGroup
}

func (f *fakeCommsReader) Transmitting() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.transmitting
}

type fakeScannerReader struct {
	mu     sync.Mutex
	st     scanner.Status
	grants []scanner.Grant
	conv   scanner.ConventionalStatus
}

func (f *fakeScannerReader) Status() scanner.Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.st
}

func (f *fakeScannerReader) Grants(limit int) ([]scanner.Grant, int) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.grants[:min(limit, len(f.grants))], len(f.grants)
}

func (f *fakeScannerReader) Conventional() scanner.ConventionalStatus {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.conv
}

type fakeBLOSReader struct {
	mu      sync.Mutex
	running bool
	st      *ipnstate.Status
}

func (f *fakeBLOSReader) IsRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.running
}

func (f *fakeBLOSReader) Status() *ipnstate.Status {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.st
}

type fakeChanmigReader struct {
	mu   sync.Mutex
	snap chanmig.Snapshot
}

func (f *fakeChanmigReader) Snapshot() chanmig.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.snap
}

type fakeStationLister struct {
	mu        sync.Mutex
	ifaces    []*wifi.Interface
	stations  map[string][]*wifi.StationInfo
	ifacesErr error
}

func (f *fakeStationLister) GetMeshInterfaces() ([]*wifi.Interface, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	return f.ifaces, f.ifacesErr
}

func (f *fakeStationLister) StationInfo(iface *wifi.Interface) ([]*wifi.StationInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	st, ok := f.stations[iface.Name]
	if !ok {
		return nil, errors.New("no such interface")
	}

	return st, nil
}
