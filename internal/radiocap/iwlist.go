package radiocap

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// maxIWLineBytes bounds a single `iw list` line. Real lines are well under
// 1 KiB; the largest are the "radar detect widths" combination continuations.
const maxIWLineBytes = 16 * 1024

// ErrNoPHYs is returned when the input contains no "Wiphy" block.
var ErrNoPHYs = errors.New("radiocap: no Wiphy blocks in iw output")

var (
	// "* 2412 MHz [1] (20.0 dBm)" or "* 2412.0 MHz [1] (disabled)".
	freqRe = regexp.MustCompile(`^\*\s+(\d+(?:\.\d+)?)\s+MHz\s+\[\d+\](.*)$`)
	// "freq range: 2400.0 MHz - 2500.0 MHz" (OpenWrt wiphy radios patch).
	freqRangeRe = regexp.MustCompile(`^freq range:\s*(\d+(?:\.\d+)?)\s*MHz\s*-\s*(\d+(?:\.\d+)?)\s*MHz$`)
	// "#{ AP, mesh point } <= 16".
	limitRe = regexp.MustCompile(`#\{\s*([^}]*?)\s*\}\s*<=\s*(\d+)`)
	// "total <= 19".
	totalRe = regexp.MustCompile(`total\s*<=\s*(\d+)`)
	// "#channels <= 1".
	channelsRe = regexp.MustCompile(`#channels\s*<=\s*(\d+)`)
)

// section is the top-level (one-tab) block of a Wiphy the parser is in.
type section int

const (
	secNone section = iota
	secModes
	secSoftwareModes
	secBand
	secCombinations
	secRadio
)

// ParseIWList parses the text printed by `iw list` or `iw phy` (one or more
// "Wiphy phyN" blocks) into PHY capability records.
//
// The parser reads supported interface modes, the bands implied by enabled
// channel frequencies, and the "valid interface combinations" table. Every
// other line is ignored. Returned PHYs have Qualified and Available set to
// false: enumeration is not qualification, and the caller must set both from
// runtime evidence before admission.
//
// Both the stock format and the format printed by OpenWrt 24.10's patched iw
// (package/network/utils/iw/patches/300-wiphy_radios.patch) are understood.
// When a wiphy lists "wiphy radio N:" blocks, each radio is an independently
// tunable radio and becomes its own PHY named "<wiphy>/radio<N>", with the
// bands of the wiphy's enabled channels inside its frequency ranges and the
// radio's own interface combinations. The wiphy-wide combination is then not
// used for admission; per-radio limits are the binding ones.
func ParseIWList(r io.Reader) ([]PHY, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), maxIWLineBytes)

	p := iwParser{}
	for sc.Scan() {
		if err := p.line(sc.Text()); err != nil {
			return nil, err
		}
	}

	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("radiocap: read iw output: %w", err)
	}

	if err := p.flush(); err != nil {
		return nil, err
	}

	if len(p.phys) == 0 {
		return nil, ErrNoPHYs
	}

	return p.phys, nil
}

// radioBlock is one "wiphy radio N:" block of the patched iw output.
type radioBlock struct {
	index  string
	ranges [][2]float64
	combos []Combination
}

type iwParser struct {
	cur      *PHY
	bandSet  map[Band]struct{}
	combo    strings.Builder // pending combination text (may span lines)
	phys     []PHY
	freqs    []float64    // enabled channel frequencies of cur, in MHz
	radios   []radioBlock // radio blocks of cur
	sec      section
	inRadioC bool // inside a radio's "valid interface combinations:"
}

func (p *iwParser) line(raw string) error {
	if name, ok := strings.CutPrefix(raw, "Wiphy "); ok {
		if err := p.flush(); err != nil {
			return err
		}

		name = strings.TrimSpace(name)
		p.phys = append(p.phys, PHY{Name: name, Wiphy: name})
		p.cur = &p.phys[len(p.phys)-1]
		p.sec = secNone
		p.bandSet = make(map[Band]struct{}, 2)
		p.freqs = p.freqs[:0]
		p.radios = p.radios[:0]

		return nil
	}

	if p.cur == nil {
		return nil // preamble before the first Wiphy (none expected)
	}

	tabs := leadingTabs(raw)
	text := strings.TrimSpace(raw)

	if tabs == 1 {
		if err := p.flushCombo(); err != nil {
			return err
		}

		p.sec = topSection(text)
		p.inRadioC = false

		if p.sec == secRadio {
			idx := strings.TrimSuffix(strings.TrimPrefix(text, "wiphy radio "), ":")
			p.radios = append(p.radios, radioBlock{index: strings.TrimSpace(idx)})
		}

		return nil
	}

	if text == "" || tabs == 0 {
		return p.flushCombo()
	}

	switch p.sec {
	case secModes, secSoftwareModes:
		if mode, ok := strings.CutPrefix(text, "* "); ok && tabs == 2 {
			p.addMode(IfType(strings.TrimSpace(mode)))
		}
	case secBand:
		p.addFrequency(text)
	case secCombinations:
		return p.addComboText(text)
	case secRadio:
		return p.radioLine(tabs, text)
	case secNone:
	}

	return nil
}

func (p *iwParser) radioLine(tabs int, text string) error {
	if tabs == 2 {
		if err := p.flushCombo(); err != nil {
			return err
		}

		p.inRadioC = text == "valid interface combinations:"

		if m := freqRangeRe.FindStringSubmatch(text); m != nil {
			lo, errLo := strconv.ParseFloat(m[1], 64)
			hi, errHi := strconv.ParseFloat(m[2], 64)

			if errLo != nil || errHi != nil || lo >= hi {
				return fmt.Errorf("radiocap: %s: bad radio frequency range %q", p.cur.Name, text)
			}

			r := &p.radios[len(p.radios)-1]
			r.ranges = append(r.ranges, [2]float64{lo, hi})
		}

		return nil
	}

	if p.inRadioC {
		return p.addComboText(text)
	}

	return nil
}

func topSection(text string) section {
	switch {
	case text == "Supported interface modes:":
		return secModes
	case strings.HasPrefix(text, "software interface modes"):
		return secSoftwareModes
	case strings.HasPrefix(text, "Band ") && strings.HasSuffix(text, ":"):
		return secBand
	case text == "valid interface combinations:":
		return secCombinations
	case strings.HasPrefix(text, "wiphy radio ") && strings.HasSuffix(text, ":"):
		return secRadio
	default:
		return secNone
	}
}

func (p *iwParser) addMode(t IfType) {
	if p.sec == secSoftwareModes {
		p.cur.SoftwareTypes = append(p.cur.SoftwareTypes, t)

		return
	}

	p.cur.SupportedTypes = append(p.cur.SupportedTypes, t)
}

func (p *iwParser) addFrequency(text string) {
	m := freqRe.FindStringSubmatch(text)
	if m == nil || strings.Contains(m[2], "(disabled)") {
		return
	}

	mhz, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return
	}

	b := BandForMHz(mhz)
	if b == "" {
		return
	}

	p.freqs = append(p.freqs, mhz)

	if _, seen := p.bandSet[b]; !seen {
		p.bandSet[b] = struct{}{}
		p.cur.Bands = append(p.cur.Bands, b)
	}
}

// addComboText accumulates a combination entry. A line starting with "*"
// begins a new entry; indented continuation lines are appended.
func (p *iwParser) addComboText(text string) error {
	if rest, ok := strings.CutPrefix(text, "* "); ok {
		if err := p.flushCombo(); err != nil {
			return err
		}

		p.combo.WriteString(rest)

		return nil
	}

	if p.combo.Len() > 0 {
		p.combo.WriteByte(' ')
		p.combo.WriteString(text)
	}

	return nil
}

func (p *iwParser) flushCombo() error {
	if p.combo.Len() == 0 {
		return nil
	}

	text := p.combo.String()
	p.combo.Reset()

	c, err := parseCombination(text)
	if err != nil {
		return fmt.Errorf("radiocap: %s: %w", p.cur.Name, err)
	}

	if p.sec == secRadio {
		r := &p.radios[len(p.radios)-1]
		r.combos = append(r.combos, c)

		return nil
	}

	p.cur.Combinations = append(p.cur.Combinations, c)

	return nil
}

func (p *iwParser) flush() error {
	if p.cur == nil {
		return nil
	}

	if err := p.flushCombo(); err != nil {
		return err
	}

	if len(p.radios) > 0 {
		p.expandRadios()
	}

	return nil
}

// expandRadios replaces the current wiphy with one PHY per radio block.
func (p *iwParser) expandRadios() {
	wiphy := p.phys[len(p.phys)-1]
	p.phys = p.phys[:len(p.phys)-1]
	p.cur = nil

	for _, r := range p.radios {
		phy := PHY{
			Name:           wiphy.Name + "/radio" + r.index,
			Wiphy:          wiphy.Name,
			Radio:          r.index,
			SupportedTypes: slices.Clone(wiphy.SupportedTypes),
			SoftwareTypes:  slices.Clone(wiphy.SoftwareTypes),
			Combinations:   r.combos,
		}

		for _, f := range p.freqs {
			b := BandForMHz(f)
			if inRanges(f, r.ranges) && !slices.Contains(phy.Bands, b) {
				phy.Bands = append(phy.Bands, b)
			}
		}

		p.phys = append(p.phys, phy)
	}
}

func inRanges(mhz float64, ranges [][2]float64) bool {
	for _, r := range ranges {
		if mhz >= r[0] && mhz <= r[1] {
			return true
		}
	}

	return false
}

// parseCombination parses one combination entry, e.g.
// "#{ managed } <= 1, #{ AP, mesh point } <= 16, total <= 17, #channels <= 1, ...".
func parseCombination(text string) (Combination, error) {
	matches := limitRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return Combination{}, fmt.Errorf("combination without interface limits: %q", text)
	}

	c := Combination{Limits: make([]Limit, 0, len(matches))}
	for _, m := range matches {
		n, err := strconv.Atoi(m[2])
		if err != nil {
			return Combination{}, fmt.Errorf("limit %q: %w", m[0], err)
		}

		parts := strings.Split(m[1], ",")

		types := make([]IfType, 0, len(parts))
		for _, part := range parts {
			if t := strings.TrimSpace(part); t != "" {
				types = append(types, IfType(t))
			}
		}

		c.Limits = append(c.Limits, Limit{Types: types, Max: n})
	}

	total := totalRe.FindStringSubmatch(text)

	chans := channelsRe.FindStringSubmatch(text)
	if total == nil || chans == nil {
		return Combination{}, fmt.Errorf("combination missing total or #channels: %q", text)
	}

	var err error
	if c.MaxInterfaces, err = strconv.Atoi(total[1]); err != nil {
		return Combination{}, fmt.Errorf("total: %w", err)
	}

	if c.MaxChannels, err = strconv.Atoi(chans[1]); err != nil {
		return Combination{}, fmt.Errorf("#channels: %w", err)
	}

	return c, nil
}

func leadingTabs(s string) int {
	n := 0
	for n < len(s) && s[n] == '\t' {
		n++
	}

	return n
}
