// Package swift extracts the package dependencies of Swift Package Manager manifests
// (`Package.swift`), reads the versions their Package.resolved pins, and moves those pins with
// an update (see Locker).
package swift

import (
	"bytes"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name (Renovate's).
const Name = "swift"

// DepTypeDependencies is the dep type of every `.package(…)` entry.
const DepTypeDependencies = "dependencies"

// Datasource and Versioning of remote packages.
const (
	Datasource = "git-tags"
	Versioning = "semver"
)

// extractVersion maps the repository's tags to the versions SwiftPM reads from them: full
// semantic versions, with a leading "v" dropped (SwiftPM accepts `v1.2.3` tags as 1.2.3).
const extractVersion = `^v?(?<version>\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?)$`

// Skip reasons.
const (
	skipLocal       = "local package"
	skipRegistry    = "registry package"
	skipURL         = "unrecognized url"
	skipBranch      = "branch reference"
	skipRevision    = "revision pin"
	skipRequirement = "unsupported requirement"
)

var matchRe = regexp.MustCompile(`(^|/)Package\.swift$`)

// toolsVersionRe matches the `// swift-tools-version:X.Y` comment that opens a manifest.
var toolsVersionRe = regexp.MustCompile(`(?i)^//\s*swift-tools-version\s*:\s*(\d+)\.(\d+)`)

// exactLabelTools reports whether the manifest src declares swift-tools-version 5.6 or later,
// which introduced the `exact:` requirement (`package(url:exact:)`).
func exactLabelTools(src []byte) bool {
	m := toolsVersionRe.FindSubmatch(bytes.TrimLeft(src, "\ufeff \t\r\n"))
	if m == nil {
		return false
	}
	major, _ := strconv.Atoi(string(m[1]))
	minor, _ := strconv.Atoi(string(m[2]))
	return major > 5 || major == 5 && minor >= 6
}

type swiftManager struct{}

// New returns the swift manager.
func New() manager.Manager { return swiftManager{} }

func (swiftManager) Name() string { return Name }

// Match reports whether the slash path is a Package.swift file. The suffix check skips the
// regexp for the paths that cannot match.
func (swiftManager) Match(p string) bool {
	return strings.HasSuffix(p, manifestFile) && matchRe.MatchString(p)
}

// Extract returns one Dep per `.package(…)` call of src, in source order. A range requirement
// (`from:`, `.upToNextMajor`, `.upToNextMinor`, `"a"..<"b"`, `"a"..."b"`) is the dep's Range,
// RefSpan covering all of it and Version its lower bound; an exact version is the dep's
// Version with RefSpan on its string. Local, registry, branch and revision packages are
// skipped.
func (swiftManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	lines := lineStarts(src)
	calls := packageCalls(tokenize(src))
	exactLabel := exactLabelTools(src)
	deps := make([]dep.Dep, 0, len(calls))
	for _, c := range calls {
		deps = append(deps, callDep(file, src, lines, c, exactLabel))
	}
	return deps, nil
}

// callDep makes the Dep of one `.package(…)` call; exactLabel is exactLabelTools of the
// manifest.
func callDep(file string, src []byte, lines []int, c call, exactLabel bool) dep.Dep {
	d := dep.Dep{
		Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepTypeDependencies,
		ExtractVersion: extractVersion, File: file, Line: lineOf(lines, c.start),
	}
	var u, req *arg
	extra, named := false, false
	for i := range c.args {
		a := &c.args[i]
		switch a.label {
		case "url":
			u = a
		case "path", "id":
			d.Name, d.Skip = string(src[a.start:a.end]), skipLocal
			if a.label == "id" {
				d.Skip = skipRegistry
			}
			if len(a.toks) == 1 && a.toks[0].plain {
				d.Name = a.toks[0].text
			}
			return d
		case "name":
			named = true
		case "traits":
		case "", "from", "exact", "branch", "revision":
			extra = extra || req != nil
			req = a
		default:
			extra = true
		}
	}
	if u == nil || len(u.toks) != 1 || !u.toks[0].plain {
		d.Skip = skipURL
		if u != nil {
			d.Name = string(src[u.start:u.end])
		}
		return d
	}
	// An scp-style URL counts only with its `user@` (`git@host:owner/repo`).
	if text := u.toks[0].text; strings.Contains(text, "://") || strings.Contains(text, "@") {
		d.Name, d.Source = manager.GitRepo(text)
	}
	if d.Source == "" {
		d.Name, d.Skip = u.toks[0].text, skipURL
		return d
	}
	if req == nil || extra {
		d.Skip = skipRequirement
		return d
	}
	text := string(src[req.start:req.end])
	r := parseRequirement(text)
	switch {
	case r.kind == reqBranch:
		d.Skip = skipBranch
	case r.kind == reqRevision:
		d.Skip = skipRevision
	case r.kind == reqExact:
		d.Version = r.lower.v
		d.RefSpan = dep.Span{Start: req.start + r.lower.span.Start, End: req.start + r.lower.span.End}
		d.Line = lineOf(lines, d.RefSpan.Start)
	case r.isRange():
		d.Range, d.Version, d.Constraint = text, r.lower.v, r.constraint()
		d.ExactLabel = exactLabel && !named
		d.RefSpan = dep.Span{Start: req.start, End: req.end}
		d.Line = lineOf(lines, req.start+r.lower.span.Start)
	default:
		d.Skip = skipRequirement
	}
	return d
}

// Edits replaces an exact version, or rewrites a range requirement for the new version under
// the update's rangeStrategy (requirement.edits): its version strings, under pin its label or
// all of its tokens; the rest of the requirement is kept byte for byte.
func (swiftManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	r := parseRequirement(d.Range)
	if !r.isRange() {
		return nil, fmt.Errorf("unsupported requirement %q", d.Range)
	}
	var edits []patch.Edit
	for _, e := range r.edits(d.Range, u.NewVersion, u.RangeStrategy, d.ExactLabel) {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start + e.span.Start, End: d.RefSpan.Start + e.span.End, Old: e.old, New: e.to})
	}
	return edits, nil
}

// NewRange implements manager.RangeRewriter (requirement.rewrite).
func (swiftManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	r := parseRequirement(d.Range)
	if !r.isRange() {
		return "", false
	}
	out := r.rewrite(d.Range, v, strategy, d.ExactLabel)
	if out == d.Range || !parseRequirement(out).admits(strings.TrimPrefix(v, "v")) {
		return "", false
	}
	return out, true
}

// ReadLocks records the version each range requirement is pinned to: the lowest pin it admits
// in the Package.resolved of the root packages whose local graph (root.members) holds its
// manifest's directory, which are the scanned Package.swift files with a Package.resolved
// next to them. An unreadable Package.resolved is reported once and the requirements it pins
// are bumped (LockUnread).
func (swiftManager) ReadLocks(top string, files [][]dep.Dep) []error {
	var roots []root
	for _, f := range files {
		if len(f) == 0 {
			continue
		}
		if dir := path.Dir(f[0].File); hasResolved(top, dir) {
			roots = append(roots, newRoot(top, dir))
		}
	}
	if len(roots) == 0 {
		return nil
	}
	type lock struct {
		res resolved
		err error
	}
	locks := make([]*lock, len(roots))
	var errs []error
	read := func(i int) *lock {
		if locks[i] == nil {
			l := &lock{}
			file := roots[i].resolvedPath()
			b, err := os.ReadFile(filepath.Join(top, filepath.FromSlash(file)))
			if err == nil {
				l.res, err = parseResolved(b)
			}
			if err != nil {
				l.err = fmt.Errorf("%s: %w", file, err)
				errs = append(errs, l.err)
			}
			locks[i] = l
		}
		return locks[i]
	}
	for _, f := range files {
		for j := range f {
			d := &f[j]
			if d.Range == "" || d.Skip != "" {
				continue
			}
			r := parseRequirement(d.Range)
			key := repoKey(d.Source)
			dir := path.Dir(d.File)
			for i, rt := range roots {
				if !rt.members[dir] {
					continue
				}
				l := read(i)
				if l.err != nil {
					d.LockUnread = true
					continue
				}
				k := l.res.find(key)
				if k < 0 {
					continue
				}
				v := l.res.pins[k].version
				if v == "" || !r.admits(v) {
					continue
				}
				if d.Locked == "" || compare(v, d.Locked) < 0 {
					d.Locked = v
				}
			}
		}
	}
	return errs
}

// compare orders two semver versions.
func compare(a, b string) int {
	s, _ := versioning.Get(Versioning)
	return s.Compare(a, b)
}

// lineStarts returns the offsets at which the lines of src start.
func lineStarts(src []byte) []int {
	starts := []int{0}
	for i, c := range src {
		if c == '\n' {
			starts = append(starts, i+1)
		}
	}
	return starts
}

// lineOf returns the 1-based line of offset off.
func lineOf(starts []int, off int) int {
	i, found := slices.BinarySearch(starts, off)
	if found {
		return i + 1
	}
	return i
}
