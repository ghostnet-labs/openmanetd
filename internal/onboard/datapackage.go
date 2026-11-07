package onboard

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/openmanet/openmanetd/internal/config"
)

// PackageUID identifies the settings package to ATAK. A fixed UID makes a
// re-import replace the earlier package instead of stacking copies.
const PackageUID = "openmanet-onboard-settings"

// PackageName is the name ATAK shows in its data package list.
const PackageName = "OpenMANET setup"

const (
	maxCallsignLen = 40
	prefEntry      = "prefs/openmanet.pref"
)

// ErrBadProfile means a callsign, team or role is not acceptable to ATAK.
var ErrBadProfile = errors.New("invalid profile")

// Teams are the team colors ATAK offers.
func Teams() []string {
	return []string{
		"White", "Yellow", "Orange", "Magenta", "Red", "Maroon", "Purple",
		"Dark Blue", "Blue", "Cyan", "Teal", "Green", "Dark Green", "Brown",
	}
}

// Roles are the team roles ATAK offers.
func Roles() []string {
	return []string{
		"Team Member", "Team Lead", "HQ", "Sniper", "Medic",
		"Forward Observer", "RTO", "K9",
	}
}

// Profile is the per-phone part of the settings package. Empty fields
// are left out so ATAK keeps its own value.
type Profile struct {
	Callsign string
	Team     string
	Role     string
}

// Validate checks the profile against what ATAK accepts.
func (p Profile) Validate() error {
	if len(p.Callsign) > maxCallsignLen {
		return fmt.Errorf("%w: callsign longer than %d characters", ErrBadProfile, maxCallsignLen)
	}

	for _, r := range p.Callsign {
		if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !strings.ContainsRune(" -_.", r) {
			return fmt.Errorf("%w: callsign may use letters, digits, space, '-', '_' and '.'", ErrBadProfile)
		}
	}

	if p.Team != "" && !slices.Contains(Teams(), p.Team) {
		return fmt.Errorf("%w: unknown team %q", ErrBadProfile, p.Team)
	}

	if p.Role != "" && !slices.Contains(Roles(), p.Role) {
		return fmt.Errorf("%w: unknown role %q", ErrBadProfile, p.Role)
	}

	return nil
}

// BuildDataPackage writes an ATAK data package (a zip with
// MANIFEST/manifest.xml) to w. It holds a preference file with the
// profile and one MOBAC map source file per layer, each pointing at
// tileBase + "/<layer>/{$z}/{$x}/{$y}". tileBase is the node URL the
// phone can reach, e.g. "http://10.41.3.7:8080/tiles".
func BuildDataPackage(w io.Writer, tileBase string, p Profile, sources []config.OnboardMapSource) error {
	if err := p.Validate(); err != nil {
		return err
	}

	tileBase = strings.TrimRight(tileBase, "/")

	entries := make([]string, 0, 1+len(sources))
	files := make(map[string][]byte, 1+len(sources))

	if pref := buildPrefs(p); pref != nil {
		entries = append(entries, prefEntry)
		files[prefEntry] = pref
	}

	for _, s := range sources {
		name := "mapsources/" + s.Layer + ".xml"
		entries = append(entries, name)
		files[name] = buildMapSource(tileBase, s)
	}

	zw := zip.NewWriter(w)

	if err := writeZipEntry(zw, "MANIFEST/manifest.xml", buildManifest(entries)); err != nil {
		return err
	}

	for _, name := range entries {
		if err := writeZipEntry(zw, name, files[name]); err != nil {
			return err
		}
	}

	if err := zw.Close(); err != nil {
		return fmt.Errorf("close data package: %w", err)
	}

	return nil
}

func writeZipEntry(zw *zip.Writer, name string, data []byte) error {
	fw, err := zw.Create(name)
	if err != nil {
		return fmt.Errorf("add %s: %w", name, err)
	}

	if _, err := fw.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}

	return nil
}

func buildManifest(entries []string) []byte {
	var b bytes.Buffer

	b.WriteString(`<MissionPackageManifest version="2">` + "\n")
	b.WriteString("  <Configuration>\n")
	b.WriteString(`    <Parameter name="uid" value="` + PackageUID + `"/>` + "\n")
	b.WriteString(`    <Parameter name="name" value="` + PackageName + `"/>` + "\n")
	b.WriteString("  </Configuration>\n")
	b.WriteString("  <Contents>\n")

	for _, e := range entries {
		b.WriteString(`    <Content ignore="false" zipEntry="`)
		escape(&b, e)
		b.WriteString(`"/>` + "\n")
	}

	b.WriteString("  </Contents>\n")
	b.WriteString("</MissionPackageManifest>\n")

	return b.Bytes()
}

// buildPrefs returns ATAK's preference file format, or nil when the
// profile sets nothing.
func buildPrefs(p Profile) []byte {
	type entry struct{ key, val string }

	set := make([]entry, 0, 3)
	if p.Callsign != "" {
		set = append(set, entry{"locationCallsign", p.Callsign})
	}

	if p.Team != "" {
		set = append(set, entry{"locationTeam", p.Team})
	}

	if p.Role != "" {
		set = append(set, entry{"atakRoleType", p.Role})
	}

	if len(set) == 0 {
		return nil
	}

	var b bytes.Buffer

	b.WriteString("<?xml version='1.0' standalone='yes'?>\n<preferences>\n")
	b.WriteString(`  <preference version="1" name="com.atakmap.app_preferences">` + "\n")

	for _, e := range set {
		b.WriteString(`    <entry key="` + e.key + `" class="class java.lang.String">`)
		escape(&b, e.val)
		b.WriteString("</entry>\n")
	}

	b.WriteString("  </preference>\n</preferences>\n")

	return b.Bytes()
}

// buildMapSource returns a MOBAC customMapSource file, the format ATAK
// reads for online tile layers.
func buildMapSource(tileBase string, s config.OnboardMapSource) []byte {
	var b bytes.Buffer

	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n<customMapSource>\n  <name>")
	escape(&b, s.Name)
	b.WriteString("</name>\n  <minZoom>" + strconv.Itoa(s.MinZoom) + "</minZoom>\n")
	b.WriteString("  <maxZoom>" + strconv.Itoa(s.MaxZoom) + "</maxZoom>\n")
	b.WriteString("  <tileType>" + s.TileType + "</tileType>\n")
	b.WriteString("  <tileUpdate>None</tileUpdate>\n  <url>")
	escape(&b, tileBase+"/"+s.Layer+"/{$z}/{$x}/{$y}")
	b.WriteString("</url>\n  <backgroundColor>#000000</backgroundColor>\n</customMapSource>\n")

	return b.Bytes()
}

func escape(b *bytes.Buffer, s string) {
	// xml.EscapeText only fails when the writer fails; bytes.Buffer
	// never does.
	_ = xml.EscapeText(b, []byte(s))
}
