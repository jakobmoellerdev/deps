// Package npm extracts dependencies, the packageManager field and the engines/volta tool
// versions from package.json files, reads the versions package-lock.json locks them to, and
// lists the lock roots lock file maintenance refreshes.
package npm

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name.
const Name = "npm"

// DepTypes of the packageManager field and of the engines and volta tool versions.
const (
	DepTypePackageManager = "packageManager"
	DepTypeEngines        = "engines"
	DepTypeVolta          = "volta"
)

// skipRange is the skip reason of a value outside npm's range grammar (and of the tool and
// packageManager versions, which are not ranges).
const skipRange = "unsupported range"

var (
	matchRe = regexp.MustCompile(`(^|/)package\.json$`)
	// rangeRe is the supported range grammar: an optional ^, ~, >= or = operator and a full
	// MAJOR.MINOR.PATCH[-prerelease] version (group 2).
	rangeRe = regexp.MustCompile(`^(\^|~|>=|=)?(\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?)$`)
	// partialRe is an optional operator and a MAJOR[.MINOR] version (group 2), matched with
	// the precision-keeping tag versioning (^26 -> ^27).
	partialRe = regexp.MustCompile(`^(\^|~|>=|=)?(\d+(?:\.\d+)?)$`)
	versionRe = regexp.MustCompile(`^\d+\.\d+\.\d+(?:-[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)
	tagRe     = regexp.MustCompile(`^[A-Za-z][0-9A-Za-z._-]*$`)
	// shorthandRe is a GitHub `owner/repo[#ref]` shorthand.
	shorthandRe = regexp.MustCompile(`^[^@\s/:][^\s/:]*/[^\s/:]+$`)
)

// sections maps the package.json dependency objects to their DepType.
var sections = map[string]bool{
	"dependencies":         true,
	"devDependencies":      true,
	"optionalDependencies": true,
	"peerDependencies":     true,
}

// tools maps the engines/volta tool keys to their datasource.
var tools = map[string]string{"node": "node-version", "npm": "npm", "pnpm": "npm", "yarn": "npm"}

// packageManagers are the tools a packageManager field may name.
var packageManagers = map[string]bool{"npm": true, "pnpm": true, "yarn": true}

type npmManager struct{}

// New returns the npm manager.
func New() manager.Manager { return npmManager{} }

func (npmManager) Name() string { return Name }

// Match reports whether the slash path is a package.json file. The suffix check skips the
// regexp for the paths that cannot match.
func (npmManager) Match(p string) bool {
	return strings.HasSuffix(p, "package.json") && matchRe.MatchString(p)
}

// Extract streams src with encoding/json/jsontext and returns one Dep per entry of the
// dependency sections plus the packageManager field. A dependency-section range is the dep's
// Range and RefSpan covers all of it; Version is its bare version (`^1.2.3` → 1.2.3) or, for
// the rest of npm's range grammar (`||`, x-ranges, partial versions, `<`, hyphen ranges), its
// anchor (npmRange.anchor). For the packageManager field and the engines/volta tools RefSpan
// covers only the version, so an operator (^1.2.3) is preserved. Values the grammar does not
// cover are skipped with a reason. Duplicate names are accepted, as npm's JSON.parse does.
func (npmManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(src), jsontext.AllowDuplicateNames(true))
	x := extractor{file: file, src: src, dec: dec, line: 1}
	if err := x.run(); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF // a truncated document, not a clean end
		}
		return nil, err
	}
	return x.deps, nil
}

// Edits rewrites a dependency-section range for the new version under the update's
// rangeStrategy (npmRange.newRange), and replaces the version token of the other deps.
func (npmManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	r, ok := parseRange(d.Range)
	if !ok {
		return nil, fmt.Errorf("unsupported range %q", d.Range)
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: r.newRange(u.NewVersion, u.RangeStrategy)}}, nil
}

// NewRange implements manager.RangeRewriter (npmRange.newRange).
func (npmManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	r, ok := parseRange(d.Range)
	if !ok {
		return "", false
	}
	out := r.newRange(v, strategy)
	if out == d.Range || !admits(out, v) {
		return "", false
	}
	return out, true
}

// ReadLocks records the version package-lock.json locks each dependency-section dep to
// (nodetool.ReadLocked), or LockUnread when its lock root uses another lockfile.
func (npmManager) ReadLocks(root string, files [][]dep.Dep) []error {
	var wants []nodetool.Want
	var refs []*dep.Dep
	for _, f := range files {
		for j := range f {
			if d := &f[j]; d.Range != "" && d.Skip == "" {
				wants = append(wants, nodetool.Want{Manifest: d.File, Name: d.Name})
				refs = append(refs, d)
			}
		}
	}
	if len(wants) == 0 {
		return nil
	}
	states, errs := nodetool.ReadLocked(root, wants)
	for i, s := range states {
		refs[i].Locked, refs[i].LockUnread = s.Version, s.Unread
	}
	return errs
}

// LockRoots implements manager.LockMaintainer: the lockfile the post-processor updates
// (nodetool.Lockfile) in each file's lock root (nodetool.Locker.Scope), with the package.json
// next to it; workspace members share their root's. A root without package.json (only
// pnpm-workspace.yaml) is left out: the package managers need it.
func (npmManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	seen := map[string]bool{}
	for _, f := range files {
		dir, ok := nodetool.Locker{}.Scope(context.Background(), root, f)
		if !ok || seen[dir] {
			continue
		}
		seen[dir] = true
		abs := filepath.Join(root, filepath.FromSlash(dir))
		if st, err := os.Stat(filepath.Join(abs, "package.json")); err != nil || !st.Mode().IsRegular() {
			continue
		}
		name, err := nodetool.Lockfile(abs)
		if err != nil || name == "" {
			slog.Debug("lock root unreadable", "dir", dir, "err", err)
			continue
		}
		out = append(out, manager.LockRoot{Manifest: path.Join(dir, "package.json"), Lockfile: path.Join(dir, name)})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return out
}

type extractor struct {
	file string
	src  []byte
	dec  *jsontext.Decoder
	deps []dep.Dep

	// line is the 1-based line of offset pos; offsets only grow, so lines are counted
	// incrementally.
	pos, line int
}

func (x *extractor) run() error {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return errors.New("top level is not an object")
	}
	for x.dec.PeekKind() != '}' {
		key, err := x.key()
		if err != nil {
			return err
		}
		switch {
		case sections[key]:
			err = x.section(key)
		case key == "packageManager":
			err = x.packageManager()
		case key == DepTypeEngines || key == DepTypeVolta:
			err = x.tools(key)
		default:
			err = x.dec.SkipValue()
		}
		if err != nil {
			return err
		}
	}
	_, err = x.dec.ReadToken()
	return err
}

// key reads an object member name.
func (x *extractor) key() (string, error) {
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", err
	}
	return tok.String(), nil
}

// value reads the next value; ok is false (and the value consumed) when it is not a string.
// span is the content of the quoted string, or the zero span when the raw bytes differ from
// the decoded string (escapes).
func (x *extractor) value() (s string, span dep.Span, ok bool, err error) {
	if x.dec.PeekKind() != '"' {
		return "", dep.Span{}, false, x.dec.SkipValue()
	}
	tok, err := x.dec.ReadToken()
	if err != nil {
		return "", dep.Span{}, false, err
	}
	s = tok.String()
	end := int(x.dec.InputOffset())
	start := end - len(s) - 2
	if start >= 0 && x.src[start] == '"' && x.src[end-1] == '"' && string(x.src[start+1:end-1]) == s {
		span = dep.Span{Start: start + 1, End: end - 1}
	}
	return s, span, true, nil
}

// lineAt returns the 1-based line of offset off (non-decreasing across calls).
func (x *extractor) lineAt(off int) int {
	x.line += bytes.Count(x.src[x.pos:off], []byte{'\n'})
	x.pos = off
	return x.line
}

func (x *extractor) newDep(depType, name, raw string, span dep.Span) dep.Dep {
	off := span.Start
	if span == (dep.Span{}) {
		off = int(x.dec.InputOffset())
	}
	return dep.Dep{
		Manager: Name, Datasource: "npm", Versioning: "semver", DepType: depType,
		File: x.file, Line: x.lineAt(off), Name: name, Version: raw,
	}
}

func (x *extractor) section(depType string) error {
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		name, err := x.key()
		if err != nil {
			return err
		}
		raw, span, ok, err := x.value()
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d := x.newDep(depType, name, raw, span)
		ver, off, skip := classify(raw)
		if r, ok := parseRange(raw); ok && skip == skipRange {
			if a := r.anchor(); a != "" {
				ver, off, skip = a, 0, ""
			}
		}
		x.finish(&d, span, ver, off, skip)
		if d.Skip == "" {
			d.Version, d.Range, d.RefSpan = ver, raw, span
		}
		x.deps = append(x.deps, d)
	}
	_, err := x.dec.ReadToken()
	return err
}

// tools extracts the node/npm/pnpm/yarn entries of the engines or volta object; other keys
// are ignored.
func (x *extractor) tools(depType string) error {
	if ok, err := x.object(); !ok || err != nil {
		return err
	}
	for x.dec.PeekKind() != '}' {
		name, err := x.key()
		if err != nil {
			return err
		}
		ds, known := tools[name]
		if !known {
			if err := x.dec.SkipValue(); err != nil {
				return err
			}
			continue
		}
		raw, span, ok, err := x.value()
		if err != nil {
			return err
		}
		if !ok {
			continue
		}
		d := x.newDep(depType, name, raw, span)
		d.Datasource = ds
		ver, off, skip := classify(raw)
		if m := partialRe.FindStringSubmatchIndex(raw); m != nil {
			ver, off, skip = raw[m[4]:m[5]], m[4], ""
			d.Versioning = "tag"
		}
		x.finish(&d, span, ver, off, skip)
		x.deps = append(x.deps, d)
	}
	_, err := x.dec.ReadToken()
	return err
}

// packageManager extracts `<tool>@<version>`; Name is the tool.
func (x *extractor) packageManager() error {
	raw, span, ok, err := x.value()
	if err != nil || !ok {
		return err
	}
	tool, ver, found := strings.Cut(raw, "@")
	name := tool
	if !found || !packageManagers[tool] {
		name = raw
	}
	d := x.newDep(DepTypePackageManager, name, raw, span)
	var skip string
	switch {
	case !found || !packageManagers[tool]:
		skip = "unsupported packageManager"
	case strings.Contains(ver, "+"):
		skip = "packageManager with hash"
	case !versionRe.MatchString(ver):
		skip = skipRange
	}
	x.finish(&d, span, ver, len(tool)+1, skip)
	x.deps = append(x.deps, d)
	return nil
}

// finish sets the version and its span (at off within the value) or the skip reason.
func (*extractor) finish(d *dep.Dep, span dep.Span, ver string, off int, skip string) {
	switch {
	case skip != "":
		d.Skip = skip
	case span == (dep.Span{}):
		d.Skip = "cannot locate value"
	default:
		d.Version = ver
		d.RefSpan = dep.Span{Start: span.Start + off, End: span.Start + off + len(ver)}
	}
}

// classify returns the bare version of a supported range and its offset in v, or the skip
// reason.
func classify(v string) (ver string, off int, skip string) {
	if m := rangeRe.FindStringSubmatchIndex(v); m != nil {
		return v[m[4]:m[5]], m[4], ""
	}
	switch {
	case strings.HasPrefix(v, "npm:"):
		return "", 0, "npm alias"
	case hasAnyPrefix(v, "file:", "link:", "workspace:", "./", "../", "/", "~/"):
		return "", 0, "local dependency"
	case hasAnyPrefix(v, "github:", "gitlab:", "bitbucket:", "gist:", "git+", "git://", "git@"), shorthandRe.MatchString(v):
		return "", 0, "git dependency"
	case hasAnyPrefix(v, "http://", "https://"):
		return "", 0, "url dependency"
	case tagRe.MatchString(v) && !isRangeLike(v):
		return "", 0, "tag"
	}
	return "", 0, skipRange
}

// isRangeLike reports whether a letter-led value is a range rather than a dist-tag: x/X
// wildcards (x, x.1) and v-prefixed versions (v1.2.3).
func isRangeLike(v string) bool {
	switch {
	case v == "x" || v == "X" || strings.HasPrefix(v, "x.") || strings.HasPrefix(v, "X."):
		return true
	case len(v) > 1 && (v[0] == 'v' || v[0] == 'V') && v[1] >= '0' && v[1] <= '9':
		return true
	}
	return false
}

func hasAnyPrefix(s string, prefixes ...string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// object reads the '{' of the next value and reports true; any other value is skipped.
func (x *extractor) object() (bool, error) {
	if x.dec.PeekKind() != '{' {
		return false, x.dec.SkipValue()
	}
	_, err := x.dec.ReadToken()
	return err == nil, err
}
