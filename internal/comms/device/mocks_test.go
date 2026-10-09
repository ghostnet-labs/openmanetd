package device_test

import (
	"errors"
	"strconv"
	"sync"
	"testing/fstest"
)

// fakeEnv is an in-memory device.Environment.
type fakeEnv struct {
	mu   sync.Mutex
	vars map[string]string
}

func newFakeEnv(kv ...string) *fakeEnv {
	e := &fakeEnv{vars: make(map[string]string, len(kv)/2)}
	for i := 0; i+1 < len(kv); i += 2 {
		e.vars[kv[i]] = kv[i+1]
	}

	return e
}

func (e *fakeEnv) Getenv(k string) string {
	e.mu.Lock()
	defer e.mu.Unlock()

	return e.vars[k]
}

func (e *fakeEnv) Setenv(k, v string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.vars[k] = v

	return nil
}

func (e *fakeEnv) Unsetenv(k string) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	delete(e.vars, k)

	return nil
}

// fakeProbe answers GPIO1 identity probes per hidraw path: strapped paths
// read high, failing paths return an error, everything else reads low.
type fakeProbe struct {
	mu       sync.Mutex
	strapped map[string]bool
	failing  map[string]bool
	calls    int
}

func newFakeProbe(strapped ...string) *fakeProbe {
	p := &fakeProbe{strapped: make(map[string]bool, len(strapped)), failing: make(map[string]bool)}
	for _, s := range strapped {
		p.strapped[s] = true
	}

	return p
}

func (p *fakeProbe) setStrapped(paths ...string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.strapped = make(map[string]bool, len(paths))
	for _, s := range paths {
		p.strapped[s] = true
	}
}

func (p *fakeProbe) fail(path string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.failing[path] = true
}

func (p *fakeProbe) read(path string) ([]byte, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls++

	if p.failing[path] {
		return nil, errors.New("EPIPE")
	}

	if p.strapped[path] {
		return []byte{0, 0, 0x01, 0, 0}, nil
	}

	return []byte{0, 0, 0, 0, 0}, nil
}

func (p *fakeProbe) callCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.calls
}

// sysDevice describes one CM108-family USB device in a fake sysfs.
type sysDevice struct {
	name    string // USB port path, e.g. "1-1.3"
	product string // idProduct hex
	serial  string
	hidraw  int // -1: no hidraw child
	card    int // -1: no ALSA card child
}

// fakeSysFS builds a sysfs tree rooted at /sys with the given devices.
func fakeSysFS(devs ...sysDevice) fstest.MapFS {
	fsys := fstest.MapFS{}

	for _, d := range devs {
		base := "bus/usb/devices/" + d.name
		fsys[base+"/idVendor"] = &fstest.MapFile{Data: []byte("0d8c\n")}
		fsys[base+"/idProduct"] = &fstest.MapFile{Data: []byte(d.product + "\n")}

		if d.serial != "" {
			fsys[base+"/serial"] = &fstest.MapFile{Data: []byte(d.serial + "\n")}
		}

		if d.card >= 0 {
			fsys[base+"/"+d.name+":1.0/sound/card"+strconv.Itoa(d.card)+"/id"] = &fstest.MapFile{Data: []byte("Device\n")}
		}

		if d.hidraw >= 0 {
			fsys[base+"/"+d.name+":1.3/0003:0D8C:"+d.product+".0001/hidraw/hidraw"+strconv.Itoa(d.hidraw)+"/dev"] =
				&fstest.MapFile{Data: []byte("242:0\n")}
		}
	}

	return fsys
}

func hidraw(n int) string { return "/dev/hidraw" + strconv.Itoa(n) }
