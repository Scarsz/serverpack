// Package descriptor reads and writes jar descriptors: the per-jar .toml files that live in
// plugins/, mods/ (and the [server] table of server.toml) and declare where a jar comes from.
package descriptor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"github.com/pelletier/go-toml/v2"
)

// Version selector keywords.
const (
	Latest    = "latest"
	LatestAny = "latest-any"
)

// Descriptor is the parsed content of a jar descriptor file.
type Descriptor struct {
	Source    string `toml:"source,omitempty" json:"source,omitempty"`
	Project   string `toml:"project,omitempty" json:"project,omitempty"`
	Version   string `toml:"version,omitempty" json:"version,omitempty"`
	Channel   string `toml:"channel,omitempty" json:"channel,omitempty"`
	Filename  string `toml:"filename,omitempty" json:"filename,omitempty"`
	MCVersion string `toml:"mc_version,omitempty" json:"mc_version,omitempty"`
	Loader    string `toml:"loader,omitempty" json:"loader,omitempty"`
	Disabled  bool   `toml:"disabled,omitempty" json:"disabled,omitempty"`

	// Source-specific fields.
	Build            string `toml:"build,omitempty" json:"build,omitempty"`                         // papermc, purpur
	LoaderVersion    string `toml:"loader_version,omitempty" json:"loader_version,omitempty"`       // fabric
	InstallerVersion string `toml:"installer_version,omitempty" json:"installer_version,omitempty"` // fabric
	Repo             string `toml:"repo,omitempty" json:"repo,omitempty"`                           // maven repository URL
	Asset            string `toml:"asset,omitempty" json:"asset,omitempty"`                         // github/jenkins artifact glob
	Classifier       string `toml:"classifier,omitempty" json:"classifier,omitempty"`               // maven
	URL              string `toml:"url,omitempty" json:"url,omitempty"`                             // url, jenkins job
	Path             string `toml:"path,omitempty" json:"path,omitempty"`                           // local
	SHA256           string `toml:"sha256,omitempty" json:"sha256,omitempty"`                       // url/local verification

	Resolved *Resolved `toml:"resolved,omitempty" json:"-"`
}

// Compat records whether the resolved version declares support for the MC version it was resolved against.
type Compat string

const (
	CompatYes     Compat = "yes"
	CompatNo      Compat = "no"
	CompatUnknown Compat = "unknown"
)

// Resolved is the pin written by `serverpack lock`/`update`.
type Resolved struct {
	Version   string `toml:"version"`
	URL       string `toml:"url,omitempty"`
	SHA256    string `toml:"sha256"`
	Size      int64  `toml:"size"`
	MCVersion string `toml:"mc_version,omitempty"`
	Loader    string `toml:"loader,omitempty"`
	Compat    Compat `toml:"compat,omitempty"`
	SpecHash  string `toml:"spec_hash"`
}

// Load parses a descriptor file, rejecting unknown keys so typos surface early.
func Load(path string) (*Descriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return Parse(data, path)
}

// Parse parses descriptor content; name is used in error messages.
func Parse(data []byte, name string) (*Descriptor, error) {
	var d Descriptor
	dec := toml.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("%s: %w", name, describeTOMLError(err))
	}
	if !d.Disabled && d.Source == "" {
		return nil, fmt.Errorf("%s: missing required field \"source\"", name)
	}
	return &d, nil
}

func describeTOMLError(err error) error {
	if se, ok := err.(*toml.StrictMissingError); ok {
		return fmt.Errorf("%s", strings.TrimSpace(se.String()))
	}
	if de, ok := err.(*toml.DecodeError); ok {
		row, col := de.Position()
		return fmt.Errorf("line %d, column %d: %s", row, col, de.Error())
	}
	return err
}

// SpecHash hashes every field except [resolved], so edits to the spec invalidate the pin.
func (d *Descriptor) SpecHash() string {
	c := *d
	c.Resolved = nil
	data, _ := json.Marshal(c)
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:16]
}

// Stale reports whether the descriptor needs (re-)resolving.
func (d *Descriptor) Stale() bool {
	return d.Resolved == nil || d.Resolved.SpecHash != d.SpecHash()
}

// Selector returns the normalized version selector ("latest" when unset).
func (d *Descriptor) Selector() string {
	if d.Version == "" {
		return Latest
	}
	return d.Version
}

// IsFloating reports whether the selector moves over time (latest / latest-any).
func (d *Descriptor) IsFloating() bool {
	s := d.Selector()
	return s == Latest || s == LatestAny
}

// Channel ranks: a descriptor allows every channel up to and including its own.
const (
	ChannelRelease = iota
	ChannelBeta
	ChannelAlpha
)

// ChannelRank returns the least-stable channel the descriptor allows.
func (d *Descriptor) ChannelRank() (int, error) {
	switch strings.ToLower(d.Channel) {
	case "", "release", "stable":
		return ChannelRelease, nil
	case "beta":
		return ChannelBeta, nil
	case "alpha":
		return ChannelAlpha, nil
	}
	return 0, fmt.Errorf("unknown channel %q (want release, beta or alpha)", d.Channel)
}

// Marshal encodes a descriptor (including [resolved], if set) as TOML.
func Marshal(d *Descriptor) ([]byte, error) {
	var b strings.Builder
	encode(&b, "", reflect.ValueOf(*d))
	return []byte(b.String()), nil
}

// Write creates or overwrites a descriptor file.
func Write(path string, d *Descriptor) error {
	data, err := Marshal(d)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// MarshalTable encodes d as the TOML table [name] (with its pin as [name.resolved]).
func MarshalTable(name string, d *Descriptor) []byte {
	var b strings.Builder
	encode(&b, name, reflect.ValueOf(*d))
	return []byte(b.String())
}

// ResolvedTable is the table holding a plain descriptor file's pin.
const ResolvedTable = "resolved"

// WriteResolved replaces the given resolved table ("resolved" for descriptor files, "server.resolved"
// in server.toml) of an existing file in place, leaving the rest of the file (including comments and
// formatting) untouched.
func WriteResolved(path, table string, r *Resolved) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	out, err := ReplaceResolved(data, table, r)
	if err != nil {
		return err
	}
	return os.WriteFile(path, out, 0o644)
}

// ReplaceResolved returns data with table replaced by r. If the table is absent it is inserted at the
// end of its parent table (e.g. right after [server] for "server.resolved"), or appended to the file.
func ReplaceResolved(data []byte, table string, r *Resolved) ([]byte, error) {
	var tb strings.Builder
	encode(&tb, table, reflect.ValueOf(*r))
	encoded := tb.String()
	nl := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		nl = "\r\n"
		encoded = strings.ReplaceAll(encoded, "\n", "\r\n")
	}
	lines := strings.SplitAfter(string(data), "\n")

	// findTable returns the line range [start, end) of a table: its header through the line before the next header.
	findTable := func(name string) (int, int) {
		start := -1
		for i, line := range lines {
			h := tableHeader(line)
			if start < 0 {
				if h == name {
					start = i
				}
			} else if h != "" {
				return start, i
			}
		}
		return start, len(lines)
	}
	// splice replaces lines[start:end] with the encoded table, keeping one blank line before what follows.
	splice := func(start, end int) []byte {
		head := strings.TrimRight(strings.Join(lines[:start], ""), "\r\n")
		var b strings.Builder
		if head != "" {
			b.WriteString(head + nl + nl)
		}
		b.WriteString(encoded)
		if end < len(lines) {
			b.WriteString(nl + strings.Join(lines[end:], ""))
		}
		return []byte(b.String())
	}

	if start, end := findTable(table); start >= 0 {
		return splice(start, end), nil
	}
	if i := strings.LastIndex(table, "."); i >= 0 {
		if start, end := findTable(table[:i]); start >= 0 {
			return splice(end, end), nil // insert right after the parent table's content
		}
	}
	return splice(len(lines), len(lines)), nil
}

// tableHeader returns the table name if line is a TOML table header ("[name]" / "[[name]]").
func tableHeader(line string) string {
	s := strings.TrimSpace(line)
	if i := strings.Index(s, "#"); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	if !strings.HasPrefix(s, "[") || !strings.HasSuffix(s, "]") {
		return ""
	}
	s = strings.Trim(s, "[]")
	return strings.TrimSpace(s)
}
