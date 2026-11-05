// Package swift extracts the package dependencies of Swift Package Manager manifests
// (`Package.swift`), reads the versions their Package.resolved pins, and moves those pins with
// an update (see Locker).
package swift

import (
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
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
	deps := make([]dep.Dep, 0, len(calls))
	for _, c := range calls {
		deps = append(deps, callDep(file, src, lines, c))
	}
	return deps, nil
}

// callDep makes the Dep of one `.package(…)` call.
func callDep(file string, src []byte, lines []int, c call) dep.Dep {
	d := dep.Dep{
		Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepTypeDependencies,
		ExtractVersion: extractVersion, File: file, Line: lineOf(lines, c.start),
	}
	var u, req *arg
	extra := false
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
		case "name", "traits":
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
	d.Name, d.Source = parseRepo(u.toks[0].text)
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
		d.RefSpan = dep.Span{Start: req.start, End: req.end}
		d.Line = lineOf(lines, req.start+r.lower.span.Start)
	default:
		d.Skip = skipRequirement
	}
	return d
}

// Edits replaces an exact version, or rewrites the version strings of a range requirement
// for the new version under the update's rangeStrategy (requirement.edits); the rest of the
// requirement is kept byte for byte.
func (swiftManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range == "" {
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	}
	r := parseRequirement(d.Range)
	if !r.isRange() {
		return nil, fmt.Errorf("unsupported requirement %q", d.Range)
	}
	if u.RangeStrategy == dep.RangePin {
		return nil, fmt.Errorf("cannot pin the requirement %q", d.Range)
	}
	var edits []patch.Edit
	for _, e := range r.edits(u.NewVersion, u.RangeStrategy) {
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
	out := r.rewrite(d.Range, v, strategy)
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

// parseRepo turns an https/http/ssh/git or scp-style (`git@host:owner/repo`) URL into the
// repository path (`owner/repo`) and the source git-tags reads: the http(s) URL as written
// (host and port kept), https for the others, without `.git`; source is "" when the URL is not
// recognized (a file:// or relative URL).
func parseRepo(s string) (name, source string) {
	scheme := "https"
	var host, p string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", ""
		}
		switch u.Scheme {
		case "https", "http": // as written, with the port
			scheme, host = u.Scheme, u.Host
		case "ssh", "git": // the port is git's, not HTTP's
			host = u.Hostname()
		default:
			return "", ""
		}
		p = u.Path
	default:
		at := strings.IndexByte(s, '@')
		c := strings.IndexByte(s, ':')
		if c < 0 || at > c || at < 0 {
			return "", ""
		}
		host, p = s[at+1:c], s[c+1:]
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || !strings.Contains(p, "/") {
		return "", ""
	}
	return p, scheme + "://" + host + "/" + p
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
