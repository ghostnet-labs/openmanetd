package openmanet

import (
	"bytes"
	"slices"
	"sync"

	"github.com/digineo/go-uci/v2"
	"github.com/openmanet/go-alfred"
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
