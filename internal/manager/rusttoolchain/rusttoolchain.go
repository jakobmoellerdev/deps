// Package rusttoolchain updates the Rust toolchain rustup selects for a directory: the
// `[toolchain] channel` of rust-toolchain.toml and of the legacy rust-toolchain file, or the
// bare toolchain name of a legacy one-line rust-toolchain
// (https://rust-lang.github.io/rustup/overrides.html#the-toolchain-file).
package rusttoolchain

import (
	"bytes"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/rustversion"
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
	// channelRe is a release channel (group 1), optionally dated (group 2) and with a host
	// triple.
	channelRe = regexp.MustCompile(`^(stable|beta|nightly)(-\d{4}-\d{2}-\d{2})?(?:-[0-9a-z_]+(?:-[0-9a-z_]+)+)?$`)
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
// precision against the derived X.Y releases of the datasource. A dated nightly moves to the
// newest nightly (rustversion.NightlyVersioning). The `stable` and `nightly` channels are
// ranges without a version (Channel), which only rangeStrategy pin writes a release for.
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
	c := channelRe.FindStringSubmatch(raw)
	switch {
	case c == nil:
		d.Skip = skipUnsupported
	case c[1] == "nightly" && c[2] != "":
		d.Version, d.Versioning = c[1]+c[2], rustversion.NightlyVersioning
		d.RefSpan = dep.Span{Start: at, End: at + len(d.Version)}
	case c[2] != "":
		d.Skip = skipDated
	case c[1] == "beta":
		d.Skip = skipChannel
	default:
		// Every stable release (`*` admits no prerelease), or every dated nightly.
		d.Version, d.Range, d.Constraint, d.Versioning = "", c[1], "*", "semver"
		if c[1] == "nightly" {
			d.Versioning = rustversion.NightlyVersioning
		}
		d.RefSpan = dep.Span{Start: at, End: at + len(c[1])}
	}
}

// Channel reports whether d is a toolchain channel, `stable` or `nightly`: a range without a
// version that admits every stable release or every dated nightly. Only rangeStrategy pin
// updates it, to the newest release it admits (Renovate's rust-release-channel pin: `stable`
// → `1.91.1`); under any other strategy it is up to date.
func Channel(d dep.Dep) bool { return d.Manager == Name && d.Range != "" }

// Edits replaces the version, or the channel name of a pinned channel; a host triple after it
// is kept.
func (rustToolchain) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	old := d.Version
	if d.Range != "" {
		old = d.Range
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: old, New: u.NewVersion}}, nil
}

// NewRange implements manager.RangeRewriter for a channel (Channel): rangeStrategy pin writes
// release v in its place; no other strategy rewrites it.
func (rustToolchain) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	if d.Range == "" || v == "" || strategy != dep.RangePin {
		return "", false
	}
	return v, true
}

// Toolchain returns the toolchain the rust-toolchain file src (named file) pins: its version
// (`1.82.0`, `1.82`, `nightly-2025-10-10`; a host triple is dropped, as the host's resolves
// the same Cargo.lock). ok is false when it pins none: a channel (`stable`), a path, a custom
// toolchain, no `[toolchain]` table or no valid file.
func Toolchain(file string, src []byte) (version string, ok bool) {
	deps, err := rustToolchain{}.Extract(file, src, manager.Env{})
	if err != nil || len(deps) == 0 || deps[0].Skip != "" || deps[0].Range != "" {
		return "", false
	}
	return deps[0].Version, true
}
