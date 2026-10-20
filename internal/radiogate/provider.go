// Package radiogate wires the radio capacity model (internal/radiocap) into
// the running daemon (GHO-59). It builds a radiocap.Model from `iw list`
// plus operator-supplied qualification evidence, checks radio
// configuration changes against it in off, advisory or enforce mode, keeps
// a bounded history of recent admission decisions, and adapts the model to
// the path selector's pathsel.BandAdmitter capacity gate (GHO-51).
//
// Enumeration is never qualification: every PHY `iw list` reports starts
// unqualified, and only Settings.QualifiedPHYs makes one qualified. A
// probe failure (iw missing, unparsable output, ubus down) never blocks a
// change; it is logged and recorded instead.
package radiogate

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"sync"
	"time"

	"github.com/openmanet/openmanetd/internal/radiocap"
)

// Probe tuning.
const (
	// DefaultTTL is how long a successful probe is reused. The PHY set
	// only changes when hardware is added, removed or fails, so a minute
	// keeps `iw list` forks rare while still noticing a lost radio.
	DefaultTTL = time.Minute
	// DefaultFailureBackoff is how long a failed probe is remembered
	// before `iw list` is run again, so a missing iw binary is not
	// re-forked on every radio change.
	DefaultFailureBackoff = 10 * time.Second
	// iwListTimeout bounds one `iw list` run.
	iwListTimeout = 5 * time.Second
	// maxIWListBytes bounds the captured `iw list` output. A tri-radio
	// wiphy prints well under 64 KiB.
	maxIWListBytes = 1 << 20
)

// ErrOutputTooLarge is returned when `iw list` prints more than
// maxIWListBytes.
var ErrOutputTooLarge = errors.New("radiogate: iw list output exceeds limit")

// Settings is the operator-supplied evidence applied to enumerated PHYs.
// Names match a PHY's Name ("phy0", "phy0/radio1") or its Wiphy ("phy0"
// matches every radio of a multi-radio wiphy).
type Settings struct {
	QualifiedPHYs        []string
	SharingQualifiedPHYs []string
	UnavailablePHYs      []string
	AllowUnqualified     bool
}

func (s *Settings) equal(o *Settings) bool {
	return s.AllowUnqualified == o.AllowUnqualified &&
		slices.Equal(s.QualifiedPHYs, o.QualifiedPHYs) &&
		slices.Equal(s.SharingQualifiedPHYs, o.SharingQualifiedPHYs) &&
		slices.Equal(s.UnavailablePHYs, o.UnavailablePHYs)
}

func (s *Settings) clone() Settings {
	return Settings{
		QualifiedPHYs:        slices.Clone(s.QualifiedPHYs),
		SharingQualifiedPHYs: slices.Clone(s.SharingQualifiedPHYs),
		UnavailablePHYs:      slices.Clone(s.UnavailablePHYs),
		AllowUnqualified:     s.AllowUnqualified,
	}
}

// Provider builds the node's radiocap.Model at runtime. The zero value
// probes the real system with no qualification evidence. All fields must
// be set before first use; methods are safe for concurrent use.
type Provider struct { //nolint:govet // fieldalignment: probeMu and mu must sit directly above the state they guard (.claude/rules/concurrency.md); the pointer-scan-optimal layout would separate them.
	// Exec returns `iw list` output. nil runs the iw binary.
	Exec func(ctx context.Context) ([]byte, error)
	// HaLowPHYs returns the wiphy names driven by the Morse Micro HaLow
	// driver. Morse maps S1G onto 5 GHz channel numbers, so `iw` alone
	// reports those PHYs as 5 GHz. nil means no PHY is HaLow.
	HaLowPHYs func(ctx context.Context) ([]string, error)
	// Settings returns the current qualification evidence. nil means
	// none: every PHY is unqualified.
	Settings func() Settings
	// Now is the clock. nil uses time.Now.
	Now func() time.Time
	// TTL bounds probe reuse. 0 uses DefaultTTL.
	TTL time.Duration
	// FailureBackoff bounds how long a probe error is reused. 0 uses
	// DefaultFailureBackoff.
	FailureBackoff time.Duration

	// probeMu makes probes single-flight: concurrent callers that find
	// the cache expired wait for one `iw list` run instead of forking
	// several. It is held across the probe by design and never while
	// holding mu.
	probeMu sync.Mutex

	mu       sync.Mutex // protects the fields below
	probedAt time.Time
	physAt   time.Time // when phys was last probed successfully
	probeErr error
	model    *radiocap.Model
	phys     []radiocap.PHY // parsed, HaLow-marked, no evidence applied
	settings Settings       // evidence the cached model was built with
	gen      uint64         // bumped on every successful probe
	modelGen uint64         // gen the cached model was built from
	stale    bool           // Invalidate was called
}

// Model returns the current capacity model, probing `iw list` when the
// cached probe is older than TTL or was invalidated. The returned pointer
// is unchanged until the probe or the settings change, so callers may key
// caches on it.
func (p *Provider) Model(ctx context.Context) (*radiocap.Model, error) {
	if err := p.ensureProbe(ctx, false); err != nil {
		return nil, err
	}

	return p.buildModel(p.currentSettings())
}

// Refresh forces a new probe now and returns the resulting model.
func (p *Provider) Refresh(ctx context.Context) (*radiocap.Model, error) {
	if err := p.ensureProbe(ctx, true); err != nil {
		return nil, err
	}

	return p.buildModel(p.currentSettings())
}

// Invalidate makes the next Model call probe again.
func (p *Provider) Invalidate() {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.stale = true
}

// PHYs returns a copy of the last successfully probed PHYs (HaLow-marked,
// without qualification evidence), or nil before the first probe.
func (p *Provider) PHYs() []radiocap.PHY {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.phys == nil {
		return nil
	}

	out := make([]radiocap.PHY, len(p.phys))
	for i := range p.phys {
		out[i] = clonePHY(&p.phys[i])
	}

	return out
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}

	return time.Now()
}

func (p *Provider) ttl() time.Duration {
	if p.TTL > 0 {
		return p.TTL
	}

	return DefaultTTL
}

func (p *Provider) failureBackoff() time.Duration {
	if p.FailureBackoff > 0 {
		return p.FailureBackoff
	}

	return DefaultFailureBackoff
}

func (p *Provider) currentSettings() Settings {
	if p.Settings == nil {
		return Settings{}
	}

	return p.Settings()
}

// fresh reports whether the cached probe result (success or failure) may
// be reused, and that result's error. The caller holds p.mu.
func (p *Provider) fresh() (bool, error) {
	if p.stale || p.probedAt.IsZero() {
		return false, nil
	}

	age := p.now().Sub(p.probedAt)
	if p.probeErr != nil {
		return age < p.failureBackoff(), p.probeErr
	}

	return age < p.ttl(), nil
}

func (p *Provider) ensureProbe(ctx context.Context, force bool) error {
	if !force {
		p.mu.Lock()
		ok, err := p.fresh()
		p.mu.Unlock()

		if ok {
			return err
		}
	}

	p.probeMu.Lock()
	defer p.probeMu.Unlock()

	// Another caller may have probed while this one waited.
	if !force {
		p.mu.Lock()
		ok, err := p.fresh()
		p.mu.Unlock()

		if ok {
			return err
		}
	}

	phys, err := p.probe(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.probedAt = p.now()
	p.stale = false
	p.probeErr = err

	if err != nil {
		return err
	}

	p.phys = phys
	p.physAt = p.probedAt
	p.gen++

	return nil
}

// probe runs `iw list`, parses it and marks HaLow PHYs. It holds no lock.
func (p *Provider) probe(ctx context.Context) ([]radiocap.PHY, error) {
	run := p.Exec
	if run == nil {
		run = RunIWList
	}

	out, err := run(ctx)
	if err != nil {
		return nil, fmt.Errorf("radiogate: iw list: %w", err)
	}

	phys, err := radiocap.ParseIWList(bytes.NewReader(out))
	if err != nil {
		return nil, fmt.Errorf("radiogate: parse iw list: %w", err)
	}

	if p.HaLowPHYs == nil {
		return phys, nil
	}

	halow, err := p.HaLowPHYs(ctx)
	if err != nil {
		return nil, fmt.Errorf("radiogate: identify HaLow PHYs: %w", err)
	}

	for i := range phys {
		if slices.Contains(halow, phys[i].Wiphy) || slices.Contains(halow, phys[i].Name) {
			phys[i].SetHaLow()
		}
	}

	return phys, nil
}

// buildModel applies s to the cached PHYs, reusing the cached model when
// neither the probe nor the settings changed.
func (p *Provider) buildModel(s Settings) (*radiocap.Model, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if p.model != nil && p.modelGen == p.gen && p.settings.equal(&s) {
		return p.model, nil
	}

	phys := make([]radiocap.PHY, len(p.phys))
	for i := range p.phys {
		phys[i] = p.phys[i]
		s.apply(&phys[i])
	}

	// NewModel deep-copies, so the shallow copies above never alias the
	// model's internals.
	m, err := radiocap.NewModel(phys)
	if err != nil {
		return nil, fmt.Errorf("radiogate: build model: %w", err)
	}

	p.model = m
	p.modelGen = p.gen
	p.settings = s.clone()

	return m, nil
}

// Inventory is the probed PHY list with qualification evidence applied.
type Inventory struct {
	// UpdatedAt is when the PHYs were last probed successfully.
	UpdatedAt time.Time
	// PHYs are deep copies the caller may modify.
	PHYs []radiocap.PHY
}

// Inventory returns the current PHYs with the current settings applied,
// probing `iw list` under the same TTL, backoff and single-flight rules as
// Model.
func (p *Provider) Inventory(ctx context.Context) (Inventory, error) {
	if err := p.ensureProbe(ctx, false); err != nil {
		return Inventory{}, err
	}

	s := p.currentSettings()

	p.mu.Lock()
	defer p.mu.Unlock()

	inv := Inventory{UpdatedAt: p.physAt, PHYs: make([]radiocap.PHY, len(p.phys))}
	for i := range p.phys {
		inv.PHYs[i] = clonePHY(&p.phys[i])
		s.apply(&inv.PHYs[i])
	}

	return inv, nil
}

// apply sets the evidence-derived flags of phy.
func (s *Settings) apply(phy *radiocap.PHY) {
	phy.Available = !matches(s.UnavailablePHYs, phy)
	phy.Qualified = matches(s.QualifiedPHYs, phy)
	phy.SharingQualified = matches(s.SharingQualifiedPHYs, phy)
}

func matches(names []string, phy *radiocap.PHY) bool {
	return slices.Contains(names, phy.Name) || (phy.Wiphy != "" && slices.Contains(names, phy.Wiphy))
}

func clonePHY(p *radiocap.PHY) radiocap.PHY {
	c := *p
	c.Bands = slices.Clone(p.Bands)
	c.SupportedTypes = slices.Clone(p.SupportedTypes)
	c.SoftwareTypes = slices.Clone(p.SoftwareTypes)
	c.Combinations = make([]radiocap.Combination, len(p.Combinations))

	for i, combo := range p.Combinations {
		c.Combinations[i] = radiocap.Combination{
			Limits:        make([]radiocap.Limit, len(combo.Limits)),
			MaxInterfaces: combo.MaxInterfaces,
			MaxChannels:   combo.MaxChannels,
		}
		for j, l := range combo.Limits {
			c.Combinations[i].Limits[j] = radiocap.Limit{Types: slices.Clone(l.Types), Max: l.Max}
		}
	}

	return c
}

// limitedBuffer is an io.Writer that keeps at most max bytes and records
// whether anything was dropped.
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)

	room := b.max - b.buf.Len()
	if n > room {
		b.truncated = true
		p = p[:max(room, 0)]
	}

	b.buf.Write(p)

	// Report the full length so exec keeps draining the pipe instead of
	// failing with a short write; RunIWList surfaces the truncation.
	return n, nil
}

// RunIWList runs `iw list` with a timeout and a bounded output buffer.
func RunIWList(ctx context.Context) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, iwListTimeout)
	defer cancel()

	out := &limitedBuffer{max: maxIWListBytes}

	cmd := exec.CommandContext(ctx, "iw", "list")
	cmd.Stdout = out

	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("run iw list: %w", err)
	}

	if out.truncated {
		return nil, ErrOutputTooLarge
	}

	return out.buf.Bytes(), nil
}
