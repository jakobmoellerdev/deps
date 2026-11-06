// Package homebrew updates the stable `url` and `sha256` of Homebrew formulae
// (Formula/*.rb): GitHub tag archives, GitHub release assets and npm registry tarballs.
package homebrew

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "homebrew"

// DepType is the dep type of the formula's stable download.
const DepType = "formula"

// Skip reasons.
const (
	SkipUnsupported = "unsupported url"
	SkipNoSHA256    = "invalid sha256"
)

var (
	// matchRe is Renovate's managerFilePatterns: Formula/<name>.rb, also sharded
	// (Formula/<letter>/<name>.rb as in homebrew-core).
	matchRe = regexp.MustCompile(`^Formula/\w*/?[^/]+\.rb$`)
	// urlRe is a `url "…"` statement; group 1 the URL. sha256Re is a `sha256 "…"` statement
	// (not the `sha256 cellar: …, arm64_…: "…"` lines of a bottle block); group 1 the hex.
	urlRe    = regexp.MustCompile(`(?m)^[ \t]*url[ \t]+"([^"]*)"`)
	sha256Re = regexp.MustCompile(`(?m)^[ \t]*sha256[ \t]+"([^"]*)"`)
	// archiveRe is a GitHub tag archive (group 1 owner/repo, 2 the tag), releaseRe a GitHub
	// release asset (group 1 owner/repo, 2 the tag, 3 the asset), npmRe an npm registry
	// tarball (group 1 the package, 2 its unscoped name, 3 the file name without `.tgz`).
	archiveRe = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/archive/(?:refs/tags/)?(.+?)\.(?:tar\.gz|tgz|zip)$`)
	releaseRe = regexp.MustCompile(`^https://github\.com/([^/]+/[^/]+)/releases/download/([^/]+)/([^/]+)$`)
	npmRe     = regexp.MustCompile(`^https://registry\.npmjs\.org/((?:@[^/]+/)?([^/@]+))/-/([^/]+)\.tgz$`)
)

type homebrew struct{}

// New returns the homebrew manager.
func New() manager.Manager { return homebrew{} }

func (homebrew) Name() string { return Name }

// Match reports whether the slash path is a formula: Formula/<name>.rb at the repository root,
// optionally one directory deeper. The prefix check skips the regexp for most paths.
func (homebrew) Match(path string) bool {
	return strings.HasPrefix(path, "Formula/") && strings.HasSuffix(path, ".rb") && matchRe.MatchString(path)
}

// Extract returns the dep of the formula's first `url` (the stable download) with the first
// `sha256` after it.
func (homebrew) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	m := urlRe.FindSubmatchIndex(src)
	if m == nil {
		return nil, nil
	}
	u := string(src[m[2]:m[3]])
	d := dep.Dep{Manager: Name, DepType: DepType, File: file, Line: 1 + bytes.Count(src[:m[2]], []byte("\n"))}
	if !locate(&d, u, m[2]) {
		return []dep.Dep{{Manager: Name, DepType: DepType, File: file, Line: d.Line, Name: u, Skip: SkipUnsupported}}, nil
	}
	rest := src[m[1]:]
	if n := urlRe.FindIndex(rest); n != nil {
		rest = rest[:n[0]]
	}
	s := sha256Re.FindSubmatchIndex(rest)
	if len(s) < 4 || !manager.IsSHA256(string(rest[s[2]:s[3]])) {
		d.Skip = SkipNoSHA256
		return []dep.Dep{d}, nil
	}
	d.Checksum = string(rest[s[2]:s[3]])
	d.ChecksumSpan = dep.Span{Start: m[1] + s[2], End: m[1] + s[3]}
	return []dep.Dep{d}, nil
}

// locate sets the datasource, name, source, version and version spans of d from the URL u
// at offset off, and the artifact template of its checksum; false = not a supported URL.
// The version is the tag after its prefix (`v1.2.3`, `jq-1.8.2`, `libpng16-1.6.43`), which
// the dep's extractVersion maps back; every occurrence of it in the tag and asset is replaced.
// An npm tarball is named `<unscoped name>-<version>.tgz`.
func locate(d *dep.Dep, u string, off int) bool {
	var tagAt int // offset in u of the version search
	if m := npmRe.FindStringSubmatchIndex(u); m != nil {
		file, base := u[m[6]:m[7]], u[m[4]:m[5]]
		if len(file) <= len(base)+1 || !strings.HasPrefix(file, base) || file[len(base)] != '-' || !isDigit(file[len(base)+1]) {
			return false
		}
		d.Datasource, d.Versioning, d.Name = "npm", "semver", u[m[2]:m[3]]
		d.Version, tagAt = file[len(base)+1:], m[6]+len(base)+1
	} else {
		m := archiveRe.FindStringSubmatchIndex(u)
		d.Datasource = "git-tags"
		if m == nil {
			if m = releaseRe.FindStringSubmatchIndex(u); m == nil {
				return false
			}
			d.Datasource = "github-releases"
		}
		d.Versioning, d.Name, d.Source = "loose", u[m[2]:m[3]], "https://github.com/"+u[m[2]:m[3]]
		tag := u[m[4]:m[5]]
		i := versionStart(tag)
		if i < 0 {
			return false
		}
		if i > 0 {
			d.ExtractVersion = "^" + regexp.QuoteMeta(tag[:i]) + `(?<version>\d.*)$`
		}
		d.Version, tagAt = tag[i:], m[4]+i
	}
	spans := occurrences(u, d.Version, tagAt)
	if len(spans) == 0 || len(spans) > 3 {
		return false
	}
	tmpl := u
	for _, at := range slices.Backward(spans) {
		tmpl = tmpl[:at] + "{{version}}" + tmpl[at+len(d.Version):]
	}
	d.ChecksumSource = &dep.ChecksumSource{Artifact: tmpl}
	targets := [...]*dep.Span{&d.RefSpan, &d.VersionSpan, &d.InputSpan}
	for i, at := range spans {
		*targets[i] = dep.Span{Start: off + at, End: off + at + len(d.Version)}
	}
	return true
}

// versionStart is the offset of the version in tag, -1 for none or a commit hash: the first
// digit at a boundary (the tag start, after `-` or `_`, or after a `v` at the tag start or
// after `-` or `_`), so digits in a name prefix (`wget2-`, `k3s-v`) are skipped; without
// one, the first digit (`r1234`).
func versionStart(tag string) int {
	if manager.IsHex(tag, 40, 64) {
		return -1
	}
	for i := range len(tag) {
		if !isDigit(tag[i]) {
			continue
		}
		if i == 0 || isSep(tag[i-1]) || (tag[i-1] == 'v' || tag[i-1] == 'V') && (i == 1 || isSep(tag[i-2])) {
			return i
		}
	}
	return strings.IndexAny(tag, "0123456789")
}

func isSep(c byte) bool { return c == '-' || c == '_' }

// occurrences are the offsets in u, from the first at from, of v not followed by more
// version (a digit, or a dot and a digit).
func occurrences(u, v string, from int) []int {
	var out []int
	for i := from; ; {
		j := strings.Index(u[i:], v)
		if j < 0 {
			return out
		}
		i += j
		end := i + len(v)
		if !(end < len(u) && isDigit(u[end])) && !(end+1 < len(u) && u[end] == '.' && isDigit(u[end+1])) {
			out = append(out, i)
		}
		i = end
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// Edits replaces every occurrence of the version in the URL and the sha256.
func (homebrew) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		for _, s := range [...]dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
			if s.End > s.Start {
				edits = append(edits, patch.Edit{Start: s.Start, End: s.End, Old: d.Version, New: u.NewVersion})
			}
		}
	}
	return manager.ChecksumEdits(edits, u)
}
