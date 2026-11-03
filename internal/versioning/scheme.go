package versioning

import (
	"regexp"
	"slices"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

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
	// "prerelease", "precision", "prefix", "suffix", "major", "not newer" or
	// "policy: <why>". accept may be nil (accept all).
	Best(cur string, cands []string, accept func(v string, kind dep.UpdateType) (bool, string), reject func(v, reason string)) (string, bool)
	// Rank returns every candidate Best would accept, best first, so callers that need to
	// skip candidates (release age) parse and filter the list once. Rejections are reported
	// as in Best.
	Rank(cur string, cands []string, accept func(v string, kind dep.UpdateType) (bool, string), reject func(v, reason string)) []string
	// Compatible reports whether next is a valid upgrade target for cur ignoring
	// policy: compatible under the scheme's rules and newer.
	Compatible(cur, next string) bool
}

// Get returns the scheme registered under name: "tag", "go", "docker" or "semver".
func Get(name string) (Scheme, bool) {
	s, ok := schemes[name]
	return s, ok
}

var schemes = map[string]Scheme{
	"tag":    impl[Tag]{parse: ParseTag, compat: tagCompat, cmp: CompareTag, kind: TagKind, semver: Tag.Semver},
	"go":     impl[string]{parse: parseGo, compat: goCompat, cmp: semver.Compare, kind: GoKind, semver: GoSemver},
	"docker": impl[dockerTag]{parse: parseDocker, compat: dockerCompat, cmp: compareDocker, kind: dockerKind, semver: dockerTag.semver},
	"semver": semverScheme{impl[*msemver.Version]{parse: parseSemver, compat: semverCompat, cmp: compareSemver, kind: semverKind, semver: semverOf}},
}

// impl implements Scheme over a parsed version type T so every candidate is parsed once.
type impl[T any] struct {
	parse  func(string) (T, bool)
	compat func(cur, next T) string // "" = compatible, else the reject reason
	cmp    func(a, b T) int
	kind   func(cur, next T) dep.UpdateType
	semver func(T) *msemver.Version
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

func (s impl[T]) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) []string {
	c, ok := s.parse(cur)
	if !ok {
		return nil
	}
	type cand struct {
		raw string
		v   T
	}
	var kept []cand
	for _, raw := range cands {
		n, ok := s.parse(raw)
		var why string
		switch {
		case !ok:
			why = "invalid"
		case s.compat(c, n) != "":
			why = s.compat(c, n)
		case s.cmp(n, c) <= 0:
			why = "not newer"
		case accept != nil:
			if ok, w := accept(raw, s.kind(c, n)); !ok {
				why = "policy: " + w
			}
		}
		if why != "" {
			if reject != nil {
				reject(raw, why)
			}
			continue
		}
		kept = append(kept, cand{raw, n})
	}
	slices.SortStableFunc(kept, func(a, b cand) int { return s.cmp(b.v, a.v) })
	out := make([]string, len(kept))
	for i, k := range kept {
		out[i] = k.raw
	}
	return out
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

// go

func parseGo(v string) (string, bool) {
	return v, semver.IsValid(v) && !module.IsPseudoVersion(v)
}

func goCompat(cur, next string) string {
	switch {
	case semver.Prerelease(next) != "" && semver.Prerelease(cur) == "":
		return "prerelease"
	case strings.HasSuffix(next, "+incompatible") && !strings.HasSuffix(cur, "+incompatible"):
		return "suffix"
	}
	if m, c := semver.Major(next), semver.Major(cur); m != c && (c != "v0" || m != "v1") {
		return "major"
	}
	return ""
}

// docker

var dockerRe = regexp.MustCompile(`^(v?)(\d+(?:\.\d+)*)(.*)$`)

type dockerTag struct {
	prefix, suffix string
	parts          []int
}

func parseDocker(s string) (dockerTag, bool) {
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

// semver

// semverScheme is Masterminds loose semver. Best rewrites the result to cur's "v" prefix
// style, so `1.2.3` with candidate `v1.3.0` yields `1.3.0`.
type semverScheme struct{ impl[*msemver.Version] }

func (s semverScheme) Rank(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) []string {
	ranked := s.impl.Rank(cur, cands, accept, reject)
	for i, v := range ranked {
		ranked[i] = s.style(cur, v)
	}
	return ranked
}

// style writes v with cur's "v" prefix convention.
func (semverScheme) style(cur, v string) string {
	v = strings.TrimPrefix(v, "v")
	if strings.HasPrefix(cur, "v") {
		return "v" + v
	}
	return v
}

func (s semverScheme) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	best, ok := s.impl.Best(cur, cands, accept, reject)
	if !ok {
		return "", false
	}
	best = strings.TrimPrefix(best, "v")
	if strings.HasPrefix(cur, "v") {
		best = "v" + best
	}
	return best, true
}

func parseSemver(s string) (*msemver.Version, bool) {
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
