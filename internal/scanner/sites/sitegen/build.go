package main

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/openmanet/openmanetd/internal/scanner/sites"
)

// siteID identifies a site across the two CSVs.
// Column names shared by both CSV inputs.
const (
	colState  = "state"
	colSystem = "system"
	colSite   = "site"
)

type siteID struct{ state, system, site string }

// siteFreqs are one site's trunked frequencies split by the use column.
type siteFreqs struct {
	control []string
	voice   []string
}

// fitColumns are the site-tuner-fit.csv columns build reads.
var fitColumns = []string{ //nolint:gochecknoglobals // read-only column list
	colState, colSystem, colSite, "county", "lat", "lon", "n",
	"rtl_sdr_needed", "wide_tuners_needed", "band", "phase", "encryption",
}

// freqColumns are the scanner-frequencies.csv columns build reads.
var freqColumns = []string{colState, "category", colSystem, colSite, "freq_mhz", "use"} //nolint:gochecknoglobals // read-only column list

// build writes the uncompressed table (format sites.Format) for every site
// in fit, taking frequencies from the trunked rows of freq. Rows with use
// "control" become control channels; every other trunked row is a voice
// channel.
func build(fit, freq io.Reader, w io.Writer) error {
	byID, err := readFreqs(freq)
	if err != nil {
		return err
	}

	rows, err := readCSV(fit, fitColumns)
	if err != nil {
		return fmt.Errorf("site-tuner-fit: %w", err)
	}

	var (
		systems = newDict()
		details = newDict()
		lines   = make([]string, 0, len(rows))
	)

	for i, r := range rows {
		line, err := siteLine(r, byID, systems, details)
		if err != nil {
			return fmt.Errorf("site-tuner-fit row %d (%s): %w", i+2, r[colSite], err)
		}

		lines = append(lines, line)
	}

	var b strings.Builder

	b.WriteString(sites.Format + "\n")

	for _, s := range systems.order {
		b.WriteString("Y\t" + s + "\n")
	}

	for _, d := range details.order {
		b.WriteString("P\t" + d + "\n")
	}

	for _, l := range lines {
		b.WriteString(l + "\n")
	}

	if _, err := io.WriteString(w, b.String()); err != nil {
		return fmt.Errorf("write table: %w", err)
	}

	return nil
}

func siteLine(r map[string]string, byID map[siteID]*siteFreqs, systems, details *dict) (string, error) {
	for _, k := range []string{colState, colSystem, colSite, "county", "phase"} {
		if strings.ContainsAny(r[k], "\t\n") {
			return "", fmt.Errorf("%s contains a tab or newline", k)
		}
	}

	f := byID[siteID{r[colState], r[colSystem], r[colSite]}]
	if f == nil || len(f.control) == 0 {
		return "", errors.New("no trunked control-channel rows in scanner-frequencies")
	}

	if n, _ := strconv.Atoi(r["n"]); n != len(f.control)+len(f.voice) {
		return "", fmt.Errorf("n=%s but scanner-frequencies lists %d", r["n"], len(f.control)+len(f.voice))
	}

	for _, k := range []string{"lat", "lon"} {
		if _, err := strconv.ParseFloat(r[k], 64); err != nil {
			return "", fmt.Errorf("bad %s: %w", k, err)
		}
	}

	band, err := parseBand(r["band"])
	if err != nil {
		return "", err
	}

	phase, err := parsePhase(r["phase"])
	if err != nil {
		return "", err
	}

	enc, err := parseEncryption(r["encryption"])
	if err != nil {
		return "", err
	}

	ctrl, err := hzList(f.control)
	if err != nil {
		return "", err
	}

	voice, err := hzList(f.voice)
	if err != nil {
		return "", err
	}

	fields := []string{
		"T", r[colState], strconv.Itoa(systems.index(r[colSystem])), r[colSite], r["county"], r["lat"], r["lon"],
		strconv.Itoa(int(band)), strconv.Itoa(int(phase)), strconv.Itoa(details.index(r["phase"])),
		strconv.Itoa(int(enc)), r["rtl_sdr_needed"], r["wide_tuners_needed"], ctrl, voice,
	}

	return strings.Join(fields, "\t"), nil
}

func readFreqs(r io.Reader) (map[siteID]*siteFreqs, error) {
	rows, err := readCSV(r, freqColumns)
	if err != nil {
		return nil, fmt.Errorf("scanner-frequencies: %w", err)
	}

	out := make(map[siteID]*siteFreqs, 300)

	for _, row := range rows {
		if row["category"] != "trunked" {
			continue
		}

		id := siteID{row[colState], row[colSystem], row[colSite]}

		f := out[id]
		if f == nil {
			f = &siteFreqs{}
			out[id] = f
		}

		if row["use"] == "control" {
			f.control = append(f.control, row["freq_mhz"])
		} else {
			f.voice = append(f.voice, row["freq_mhz"])
		}
	}

	return out, nil
}

func readCSV(r io.Reader, want []string) ([]map[string]string, error) {
	cr := csv.NewReader(r)

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	col := make(map[string]int, len(header))
	for i, h := range header {
		col[h] = i
	}

	for _, w := range want {
		if _, ok := col[w]; !ok {
			return nil, fmt.Errorf("missing column %q", w)
		}
	}

	recs, err := cr.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read rows: %w", err)
	}

	out := make([]map[string]string, 0, len(recs))

	for _, rec := range recs {
		m := make(map[string]string, len(want))
		for _, w := range want {
			m[w] = rec[col[w]]
		}

		out = append(out, m)
	}

	return out, nil
}

// mhzToHz converts a decimal MHz string to integer Hz without float
// rounding ("773.06875" -> 773068750).
func mhzToHz(s string) (uint64, error) {
	whole, frac, _ := strings.Cut(strings.TrimSpace(s), ".")
	if len(frac) > 6 {
		return 0, fmt.Errorf("frequency %q has sub-Hz precision", s)
	}

	frac += strings.Repeat("0", 6-len(frac))

	w, err := strconv.ParseUint(whole, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("bad frequency %q: %w", s, err)
	}

	f, err := strconv.ParseUint(frac, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("bad frequency %q: %w", s, err)
	}

	return w*1_000_000 + f, nil
}

func hzList(mhz []string) (string, error) {
	parts := make([]string, 0, len(mhz))

	for _, m := range mhz {
		hz, err := mhzToHz(m)
		if err != nil {
			return "", err
		}

		parts = append(parts, strconv.FormatUint(hz, 10))
	}

	return strings.Join(parts, ","), nil
}

func parseBand(s string) (sites.Band, error) {
	switch s {
	case "VHF":
		return sites.BandVHF, nil
	case "700":
		return sites.Band700, nil
	case "800":
		return sites.Band800, nil
	case "mixed":
		return sites.BandMixed, nil
	default:
		return sites.BandUnknown, fmt.Errorf("unknown band %q", s)
	}
}

// parsePhase maps the source's phase note: anything starting "P25 Phase II"
// is Phase II (TDMA voice), anything else starting "P25 Phase I" is Phase I.
// The verbatim note is kept as the phase detail.
func parsePhase(s string) (sites.Phase, error) {
	switch {
	case strings.HasPrefix(s, "P25 Phase II"):
		return sites.Phase2, nil
	case strings.HasPrefix(s, "P25 Phase I"):
		return sites.Phase1, nil
	default:
		return sites.PhaseUnknown, fmt.Errorf("unknown phase %q", s)
	}
}

func parseEncryption(s string) (sites.Encryption, error) {
	switch s {
	case "none", "":
		return sites.EncryptionNone, nil
	case "partial":
		return sites.EncryptionPartial, nil
	case "full":
		return sites.EncryptionFull, nil
	default:
		return sites.EncryptionUnknown, fmt.Errorf("unknown encryption %q", s)
	}
}

// dict assigns dense indexes to strings in first-seen order.
type dict struct {
	idx   map[string]int
	order []string
}

func newDict() *dict { return &dict{idx: make(map[string]int, 64)} }

func (d *dict) index(s string) int {
	if i, ok := d.idx[s]; ok {
		return i
	}

	d.idx[s] = len(d.order)
	d.order = append(d.order, s)

	return len(d.order) - 1
}
