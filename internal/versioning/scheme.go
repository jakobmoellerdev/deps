package versioning

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Scheme parses, compares and selects versions of one versioning style.
type Scheme interface {
	Valid(v string) bool
	// Compare orders two valid versions.
	Compare(a, b string) int
	Kind(cur, next string) dep.UpdateType
	// Semver returns v as a Masterminds version, or nil if it is not representable.
	Semver(v string) *msemver.Version
	// Best returns the highest candidate newer than cur that is compatible (same
	// precision/prefix/suffix/major rules of the scheme) and accepted. reject (may be
	// nil) receives every dropped candidate with a short reason: "invalid",
	// "prerelease", "precision", "prefix", "suffix", "major", "compatibility",
	// "not newer" or "policy: <why>". accept may be nil (accept all).
	Best(cur string, cands []string, accept func(v string, kind dep.UpdateType) (bool, string), reject func(v, reason string)) (string, bool)
	// Rank returns every candidate Best would accept, best first, so callers that bucket
	// candidates or skip some (release age) parse and filter the list once. Rejections are
	// reported as in Best.
	Rank(cur string, cands []string, accept func(v string, kind dep.UpdateType) (bool, string), reject func(v, reason string)) Ranking
	// Compatible reports whether next is a valid upgrade target for cur ignoring
	// policy: compatible under the scheme's rules and newer.
	Compatible(cur, next string) bool
}

// Ranking is the result of Scheme.Rank. Candidates are rendered on access: callers usually
// look at the first few of hundreds.
type Ranking interface {
	Len() int
	At(i int) Candidate // i-th best, 0 <= i < Len()
}

// Candidate is a ranked upgrade candidate.
type Candidate struct {
	Version string         // as written into the file
	Kind    dep.UpdateType // major, minor or patch relative to the current version
	Major   string         // decimal major component of a major candidate, for per-major PR buckets
}

// Names are the versioning scheme names; "regex:<RE2>" schemes come on top (RegexPrefix).
var Names = []string{"tag", "go", "docker", "semver", "loose"}

// RegexPrefix starts a regex scheme name: "regex:" followed by an RE2 regexp with named groups.
const RegexPrefix = "regex:"

// Get returns the scheme registered under name ("tag", "go", "docker", "semver", "loose"), or
// the regex scheme of a valid "regex:<RE2>" name.
func Get(name string) (Scheme, bool) {
	s, err := Parse(name)
	return s, err == nil
}

// Parse is Get with the reason for an unknown name or an invalid regex scheme. Regex schemes
// are compiled once per name and cached for the process.
func Parse(name string) (Scheme, error) {
	if s, ok := schemes[name]; ok {
		return s, nil
	}
	expr, ok := strings.CutPrefix(name, RegexPrefix)
	if !ok {
		return nil, &UnknownError{Name: name}
	}
	if s, ok := regexSchemes.Load(name); ok {
		return s.(Scheme), nil
	}
	s, err := newRegexScheme(expr)
	if err != nil {
		return nil, err
	}
	actual, _ := regexSchemes.LoadOrStore(name, s)
	return actual.(Scheme), nil
}

// UnknownError is returned by Parse for a name that is neither a scheme nor a regex scheme.
type UnknownError struct{ Name string }

func (e *UnknownError) Error() string {
	return "must be one of " + strings.Join(Names, ", ") + " or regex:<RE2>, got " + strconv.Quote(e.Name)
}

// regexSchemes caches compiled regex schemes by their full name.
var regexSchemes sync.Map

var schemes = map[string]Scheme{
	"tag":    impl[Tag]{parse: ParseTag, compat: tagCompat, cmp: CompareTag, kind: TagKind, semver: Tag.Semver, major: Tag.major},
	"go":     impl[goVersion]{parse: parseGo, compat: goCompat, cmp: compareGo, kind: goKind, semver: goVersion.semver, major: goVersion.majorNum},
	"docker": impl[dockerTag]{parse: parseDocker, compat: dockerCompat, cmp: compareDocker, kind: dockerKind, semver: dockerTag.semver, major: dockerTag.major},
	"semver": semverScheme{impl[*msemver.Version]{parse: parseSemver, compat: semverCompat, cmp: compareSemver, kind: semverKind, semver: semverOf, major: semverMajor}},
	"loose":  impl[looseVersion]{parse: parseLoose, compat: looseCompat, cmp: compareLoose, kind: looseKind, semver: looseVersion.semver, major: looseVersion.major},
}

// impl implements Scheme over a parsed version type T so every candidate is parsed once.
type impl[T any] struct {
	parse  func(string) (T, bool)
	compat func(cur, next T) string // "" = compatible, else the reject reason
	cmp    func(a, b T) int
	kind   func(cur, next T) dep.UpdateType
	semver func(T) *msemver.Version
	major  func(T) string
}

func (s impl[T]) Valid(v string) bool {
	_, ok := s.parse(v)
	return ok
}

func (s impl[T]) Compare(a, b string) int {
	x, okA := s.parse(a)
	y, okB := s.parse(b)
	if !okA || !okB {
		return strings.Compare(a, b)
	}
	return s.cmp(x, y)
}

// Kind classifies cur → next; invalid input counts as major.
func (s impl[T]) Kind(cur, next string) dep.UpdateType {
	c, okC := s.parse(cur)
	n, okN := s.parse(next)
	if !okC || !okN {
		return dep.Major
	}
	return s.kind(c, n)
}

func (s impl[T]) Semver(v string) *msemver.Version {
	p, ok := s.parse(v)
	if !ok {
		return nil
	}
	return s.semver(p)
}

func (s impl[T]) Compatible(cur, next string) bool {
	c, okC := s.parse(cur)
	n, okN := s.parse(next)
	return okC && okN && s.compat(c, n) == "" && s.cmp(n, c) > 0
}

func (s impl[T]) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	c, ok := s.parse(cur)
	if !ok {
		return "", false
	}
	drop := func(v, reason string) {
		if reject != nil {
			reject(v, reason)
		}
	}
	var best string
	var bestV T
	found := false
	for _, raw := range cands {
		n, ok := s.parse(raw)
		if !ok {
			drop(raw, "invalid")
			continue
		}
		if r := s.compat(c, n); r != "" {
			drop(raw, r)
			continue
		}
		if s.cmp(n, c) <= 0 {
			drop(raw, "not newer")
			continue
		}
		if accept != nil {
			if ok, why := accept(raw, s.kind(c, n)); !ok {
				drop(raw, "policy: "+why)
				continue
			}
		}
		if found && s.cmp(n, bestV) <= 0 {
			continue
		}
		best, bestV, found = raw, n, true
	}
	return best, found
}

func (s impl[T]) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) Ranking {
	return s.rank(cur, cands, accept, reject)
}

// rank parses and filters cands once and sorts the kept ones by version, best first. Only
// the parsed version and the candidate's index are kept; Candidates are built by At.
func (s impl[T]) rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) *ranking[T] {
	r := &ranking[T]{s: s, cands: cands}
	c, ok := s.parse(cur)
	if !ok {
		return r
	}
	r.cur = c
	for i, raw := range cands {
		n, ok := s.parse(raw)
		why := "invalid"
		if ok {
			if why = s.compat(c, n); why == "" {
				if s.cmp(n, c) <= 0 {
					why = "not newer"
				} else if accept != nil {
					if ok, w := accept(raw, s.kind(c, n)); !ok {
						why = "policy: " + w
					}
				}
			}
		}
		if why != "" {
			if reject != nil {
				reject(raw, why)
			}
			continue
		}
		r.kept = append(r.kept, rankedVersion[T]{v: n, i: i})
	}
	slices.SortStableFunc(r.kept, func(a, b rankedVersion[T]) int { return s.cmp(b.v, a.v) })
	return r
}

// ranking implements Ranking over the parsed kept candidates.
type ranking[T any] struct {
	s     impl[T]
	cur   T
	cands []string
	kept  []rankedVersion[T]
	style func(string) string // rewrites Candidate.Version; nil = as is
}

type rankedVersion[T any] struct {
	v T
	i int // index into cands
}

func (r *ranking[T]) Len() int { return len(r.kept) }

func (r *ranking[T]) At(i int) Candidate {
	k := r.kept[i]
	c := Candidate{Version: r.cands[k.i], Kind: r.s.kind(r.cur, k.v)}
	if c.Kind == dep.Major {
		c.Major = r.s.major(k.v)
	}
	if r.style != nil {
		c.Version = r.style(c.Version)
	}
	return c
}

// tag

func tagCompat(cur, next Tag) string {
	switch {
	case next.Prefix != cur.Prefix:
		return "prefix"
	case len(next.Parts) != len(cur.Parts):
		return "precision"
	}
	return ""
}

// docker

var dockerRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

// commitHash reports whether s looks like a git commit hash (`3a22eba`, a full 40-character
// SHA-1): 7 to 40 lowercase hex digits, not all decimal. As in Renovate's docker versioning,
// such a tag is not a version; it would otherwise parse as its leading digits with the rest of
// the hash as suffix, which no other tag shares.
func commitHash(s string) bool {
	if len(s) < 7 || len(s) > 40 {
		return false
	}
	letter := false
	for i := range len(s) {
		switch c := s[i]; {
		case c >= 'a' && c <= 'f':
			letter = true
		case c < '0' || c > '9':
			return false
		}
	}
	return letter
}

type dockerTag struct {
	prefix, suffix string
	parts          []int
}

func parseDocker(s string) (dockerTag, bool) {
	if commitHash(s) {
		return dockerTag{}, false
	}
	m := dockerRe.FindStringSubmatch(s)
	if m == nil {
		return dockerTag{}, false
	}
	t := dockerTag{prefix: m[1], suffix: m[3], parts: make([]int, 0, strings.Count(m[2], ".")+1)}
	for p := range strings.SplitSeq(m[2], ".") {
		n, err := strconv.Atoi(p)
		if err != nil {
			return dockerTag{}, false
		}
		t.parts = append(t.parts, n)
	}
	return t, true
}

func dockerCompat(cur, next dockerTag) string {
	switch {
	case next.prefix != cur.prefix:
		return "prefix"
	case len(next.parts) != len(cur.parts):
		return "precision"
	case next.suffix != cur.suffix:
		return "suffix"
	}
	return ""
}

func compareDocker(a, b dockerTag) int {
	if c := CompareTag(Tag{Parts: a.parts}, Tag{Parts: b.parts}); c != 0 {
		return c
	}
	return strings.Compare(a.suffix, b.suffix)
}

func dockerKind(cur, next dockerTag) dep.UpdateType {
	return TagKind(Tag{Parts: cur.parts}, Tag{Parts: next.parts})
}

func (t dockerTag) semver() *msemver.Version { return Tag{Parts: t.parts}.Semver() }

func (t dockerTag) major() string { return strconv.Itoa(t.parts[0]) }

// semver

// semverScheme is Masterminds loose semver. Best and Rank rewrite candidates to cur's "v"
// prefix style, so `1.2.3` with candidate `v1.3.0` yields `1.3.0`.
type semverScheme struct{ impl[*msemver.Version] }

func (s semverScheme) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) Ranking {
	r := s.rank(cur, cands, accept, reject)
	r.style = func(v string) string { return s.style(cur, v) }
	return r
}

// style writes v with cur's "v" prefix convention.
func (semverScheme) style(cur, v string) string {
	curV, vV := strings.HasPrefix(cur, "v"), strings.HasPrefix(v, "v")
	switch {
	case curV == vV:
		return v
	case curV:
		return "v" + v
	}
	return v[1:]
}

func (s semverScheme) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	best, ok := s.impl.Best(cur, cands, accept, reject)
	if !ok {
		return "", false
	}
	return s.style(cur, best), true
}

func semverMajor(v *msemver.Version) string { return strconv.FormatUint(v.Major(), 10) }

// parseSemver parses s leniently. It tries StrictNewVersion first because
// NewVersion runs a coercion regex that dominates CPU and allocations when
// a package has thousands of releases. Whenever strict parsing succeeds the
// result is identical to NewVersion's (same Original, Prerelease, Metadata
// and ordering); anything strict rejects, such as a leading "v" or a
// partial version like "1.2", falls back to NewVersion.
func parseSemver(s string) (*msemver.Version, bool) {
	if v, err := msemver.StrictNewVersion(s); err == nil {
		return v, true
	}
	v, err := msemver.NewVersion(s)
	return v, err == nil
}

func semverCompat(cur, next *msemver.Version) string {
	if next.Prerelease() != "" && cur.Prerelease() == "" {
		return "prerelease"
	}
	return ""
}

func compareSemver(a, b *msemver.Version) int { return a.Compare(b) }

func semverKind(cur, next *msemver.Version) dep.UpdateType {
	switch {
	case cur.Major() != next.Major():
		return dep.Major
	case cur.Minor() != next.Minor():
		return dep.Minor
	}
	return dep.Patch
}

func semverOf(v *msemver.Version) *msemver.Version { return v }
