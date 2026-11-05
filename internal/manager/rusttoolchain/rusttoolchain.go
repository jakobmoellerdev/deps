// Package rusttoolchain updates the Rust toolchain rustup selects for a directory: the
// `[toolchain] channel` of rust-toolchain.toml and of the legacy rust-toolchain file, or the
// bare toolchain name of a legacy one-line rust-toolchain
// (https://rust-lang.github.io/rustup/overrides.html#the-toolchain-file).
package rusttoolchain

import (
	"bytes"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the manager name (Renovate's).
const Name = "rust-toolchain"

// DepType is the dep type of the toolchain version (Renovate's).
const DepType = "toolchain"

// DepName is the name of the toolchain dep.
const DepName = "rust"

// Datasource is the datasource of the toolchain dep.
const Datasource = "rust-version"

// Skip reasons.
const (
	skipChannel     = "unpinned channel"
	skipDated       = "dated channel"
	skipPath        = "local path"
	skipUnsupported = "unsupported toolchain"
	skipNoChannel   = "no channel"
	skipLocate      = "cannot locate value"
)

var (
	matchRe = regexp.MustCompile(`(^|/)rust-toolchain(\.toml)?$`)
	// versionRe is a toolchain name pinning a release: `X.Y` or `X.Y.Z` (group 1), optionally
	// followed by a host triple (`1.82.0-x86_64-unknown-linux-gnu`), which stays outside the
	// version.
	versionRe = regexp.MustCompile(`^(\d+\.\d+(?:\.\d+)?)(?:-[0-9a-z_]+(?:-[0-9a-z_]+)+)?$`)
	// channelRe is a release channel, optionally dated (group 2) and with a host triple.
	channelRe = regexp.MustCompile(`^(?:stable|beta|nightly)(-\d{4}-\d{2}-\d{2})?(?:-[0-9a-z_]+(?:-[0-9a-z_]+)+)?$`)
)

type rustToolchain struct{}

// New returns the rust-toolchain manager.
func New() manager.Manager { return rustToolchain{} }

func (rustToolchain) Name() string { return Name }

// Match reports whether the slash path is a rust-toolchain.toml or rust-toolchain file. The
// substring check skips the regexp for the paths that cannot match.
func (rustToolchain) Match(path string) bool {
	return strings.Contains(path, "rust-toolchain") && matchRe.MatchString(path)
}

// Extract returns the toolchain dep of the file, or none when a TOML file has no
// `[toolchain]` table. A rust-toolchain file is the legacy one-line form unless it is TOML, as
// rustup reads it: a single line without `=` or `[`.
func (rustToolchain) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	if !strings.HasSuffix(file, ".toml") {
		if start, end, ok := legacy(src); ok {
			d := newDep(file, 1+bytes.Count(src[:start], []byte{'\n'}))
			classify(&d, string(src[start:end]), start)
			return []dep.Dep{d}, nil
		}
	}
	root, err := tomlspan.Parse(src)
	if err != nil {
		return nil, err
	}
	tc := root.Get("toolchain")
	if tc == nil || tc.Kind != tomlspan.Table {
		return nil, nil
	}
	d := newDep(file, tc.Line)
	ch := tc.Fields["channel"]
	switch {
	case tc.Fields["path"] != nil:
		d.Skip = skipPath
	case ch == nil || ch.Kind != tomlspan.String:
		d.Skip = skipNoChannel
	case !ch.Exact:
		d.Line = ch.Line
		d.Skip = skipLocate
	default:
		d.Line = ch.Line
		classify(&d, ch.Str, ch.Start)
	}
	return []dep.Dep{d}, nil
}

// legacy returns the byte range of the toolchain name of a legacy rust-toolchain file: its
// only non-blank line, when that is no TOML.
func legacy(src []byte) (start, end int, ok bool) {
	trimmed := bytes.TrimSpace(src)
	if len(trimmed) == 0 || bytes.ContainsAny(trimmed, "\n=[") {
		return 0, 0, false
	}
	start = bytes.Index(src, trimmed)
	return start, start + len(trimmed), true
}

func newDep(file string, line int) dep.Dep {
	return dep.Dep{Manager: Name, Datasource: Datasource, DepType: DepType, File: file, Line: line, Name: DepName}
}

// classify sets the version of toolchain name raw, found at byte offset at, or the reason it
// is not updatable. A full X.Y.Z uses semver; X.Y uses tag versioning, which keeps its
// precision against the derived X.Y releases of the datasource.
func classify(d *dep.Dep, raw string, at int) {
	d.Version = raw
	if m := versionRe.FindStringSubmatch(raw); m != nil {
		d.Version, d.Versioning = m[1], "semver"
		if strings.Count(m[1], ".") == 1 {
			d.Versioning = "tag"
		}
		d.RefSpan = dep.Span{Start: at, End: at + len(m[1])}
		return
	}
	if c := channelRe.FindStringSubmatch(raw); c != nil {
		d.Skip = skipChannel
		if c[1] != "" {
			d.Skip = skipDated
		}
		return
	}
	d.Skip = skipUnsupported
}

// Edits replaces the version; a host triple after it is kept.
func (rustToolchain) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
