package versioning

import (
	"cmp"
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
var Names = []string{"tag", "go", "docker", "semver", "loose", "cargo", "pep440", "hashicorp", "maven", "ruby", "conda", "composer", "julia", "bazel-module", "nuget"}

// RegexPrefix starts a regex scheme name: "regex:" followed by an RE2 regexp with named groups.
const RegexPrefix = "regex:"

// Get returns the scheme registered under name ("tag", "go", "docker", "semver", "loose", "cargo",
// "pep440", "hashicorp", "maven", "ruby", "conda", "composer", "julia", "bazel-module", "nuget"), or the
// regex scheme of a valid "regex:<RE2>" name.
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
	"tag":          impl[Tag]{parse: ParseTag, compat: tagCompat, cmp: CompareTag, kind: TagKind, semver: Tag.Semver, major: Tag.major},
	"go":           impl[goVersion]{parse: parseGo, compat: goCompat, cmp: compareGo, kind: goKind, semver: goVersion.semver, major: goVersion.majorNum},
	"docker":       impl[dockerTag]{parse: parseDocker, compat: dockerCompat, cmp: compareDocker, kind: dockerKind, semver: dockerTag.semver, major: dockerTag.major},
	"semver":       semverScheme{impl[semverVersion]{parse: parseSemver, compat: semverCompat, cmp: compareSemver, kind: semverKind, semver: semverVersion.semver, major: semverMajor}},
	"loose":        impl[looseVersion]{parse: parseLoose, compat: looseCompat, cmp: compareLoose, kind: looseKind, semver: looseVersion.semver, major: looseVersion.major},
	"cargo":        impl[cargoVersion]{parse: parseCargo, compat: cargoCompat, cmp: compareCargo, kind: cargoKind, semver: cargoVersion.semver, major: cargoMajor},
	"pep440":       pep440Scheme{impl[pep440Version]{parse: parsePEP440, compat: pep440Compat, cmp: comparePEP440, kind: pep440Kind, semver: pep440Version.semver, major: pep440Version.major}},
	"hashicorp":    hashicorpScheme{impl[hcVersion]{parse: parseHashicorp, compat: hashicorpCompat, cmp: compareHashicorp, kind: hashicorpKind, semver: hcVersion.semver, major: hcVersion.major}},
	"maven":        impl[mavenVersion]{parse: parseMaven, compat: mavenCompat, cmp: compareMaven, kind: mavenKindOf, semver: mavenVersion.semver, major: mavenVersion.major},
	"ruby":         rubyScheme{impl[rubyVersion]{parse: parseRuby, compat: rubyCompat, cmp: compareRuby, kind: rubyKind, semver: rubyVersion.semver, major: rubyVersion.major}},
	"conda":        condaScheme{impl[condaVersion]{parse: parseConda, compat: condaCompat, cmp: compareConda, kind: condaKind, semver: condaVersion.semver, major: condaVersion.major}},
	"composer":     composerScheme{impl[composerVersion]{parse: parseComposer, compat: composerCompat, cmp: compareComposer, kind: composerKind, semver: composerVersion.semver, major: composerVersion.major}},
	"julia":        juliaScheme{impl[cargoVersion]{parse: parseCargo, compat: juliaCompat, cmp: compareJulia, kind: juliaKind, semver: cargoVersion.semver, major: cargoMajor}},
	"bazel-module": impl[bazelVersion]{parse: parseBazel, compat: bazelCompat, cmp: compareBazel, kind: bazelKind, semver: bazelVersion.semver, major: bazelVersion.major},
	"nuget":        nugetScheme{impl[nugetVersion]{parse: parseNuGet, compat: nugetCompat, cmp: compareNuGet, kind: nugetKind, semver: nugetVersion.semver, major: nugetVersion.major}},
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
	c, ok := s.parse(cur)
	if !ok {
		return false
	}
	_, why := s.consider(c, next, nil)
	return why == ""
}

// consider parses raw and runs it through the filters Best and rank share, in order:
// "invalid", the scheme's compat reason, "not newer", then "policy: " + accept's reason.
// It returns the parsed version and "" when raw is kept, else the reject reason.
func (s impl[T]) consider(c T, raw string, accept func(string, dep.UpdateType) (bool, string)) (T, string) {
	n, ok := s.parse(raw)
	if !ok {
		return n, "invalid"
	}
	if why := s.compat(c, n); why != "" {
		return n, why
	}
	if s.cmp(n, c) <= 0 {
		return n, "not newer"
	}
	if accept != nil {
		if ok, why := accept(raw, s.kind(c, n)); !ok {
			return n, "policy: " + why
		}
	}
	return n, ""
}

func (s impl[T]) Best(cur string, cands []string, accept func(string, dep.UpdateType) (bool, string), reject func(string, string)) (string, bool) {
	c, ok := s.parse(cur)
	if !ok {
		return "", false
	}
	var best string
	var bestV T
	found := false
	for _, raw := range cands {
		n, why := s.consider(c, raw, accept)
		if why != "" {
			if reject != nil {
				reject(raw, why)
			}
			continue
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
		n, why := s.consider(c, raw, accept)
		if why != "" {
			if reject != nil {
				reject(raw, why)
			}
			continue
		}
		r.kept = append(r.kept, rankedVersion[T]{v: n, i: i})
	}
	// Ties keep cands order, as a stable sort would; pdqsort moves far fewer of the
	// (often large) parsed versions than SortStableFunc's insertion sort and merges.
	slices.SortFunc(r.kept, func(a, b rankedVersion[T]) int {
		if c := s.cmp(b.v, a.v); c != 0 {
			return c
		}
		return a.i - b.i // indices are small and distinct
	})
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

// parseDocker splits s into an optional "v" prefix, dot-separated decimal parts and the
// remaining suffix (`v1.2.3-alpine` → "v", [1 2 3], "-alpine"). The release part is the longest
// run of digit groups joined by single dots, so `1.2.` keeps the trailing dot as suffix. A tag
// containing a newline, a commit hash, or a part overflowing int is not a version.
func parseDocker(s string) (dockerTag, bool) {
	if commitHash(s) || strings.IndexByte(s, '\n') >= 0 {
		return dockerTag{}, false
	}
	var t dockerTag
	i := 0
	if i < len(s) && s[i] == 'v' {
		t.prefix, i = s[:1], 1
	}
	// Count the parts first so parts is allocated once at its final size.
	n, end := 0, i
	for {
		j := end
		if n > 0 {
			if j >= len(s) || s[j] != '.' {
				break
			}
			j++
		}
		k := j
		for k < len(s) && s[k] >= '0' && s[k] <= '9' {
			k++
		}
		if k == j {
			break
		}
		n, end = n+1, k
	}
	if n == 0 {
		return dockerTag{}, false
	}
	t.parts = make([]int, 0, n)
	for p := range strings.SplitSeq(s[i:end], ".") {
		v, err := strconv.Atoi(p)
		if err != nil {
			return dockerTag{}, false
		}
		t.parts = append(t.parts, v)
	}
	t.suffix = s[end:]
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
type semverScheme struct{ impl[semverVersion] }

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

func semverMajor(v semverVersion) string { return strconv.FormatUint(v.major, 10) }

// semverVersion is a version of the semver scheme: the fields Masterminds' Version.Compare,
// Major, Minor and Prerelease read, with pre slicing orig. Ranking parses every candidate of
// every dependency, hundreds per npm package, so a version is a value, not a Masterminds
// allocation; the Masterminds version is only built for Semver (allow constraints).
type semverVersion struct {
	major, minor, patch uint64
	pre, orig           string
}

func (v semverVersion) semver() *msemver.Version {
	sv, _ := parseMasterminds(v.orig)
	return sv
}

// parseSemver parses s as Masterminds' NewVersion would. A strict semver version, the bulk
// of any registry, is parsed without allocating (parseStrictSemver); anything else, such
// as a leading "v" or a partial version like "1.2", goes through parseMasterminds.
func parseSemver(s string) (semverVersion, bool) {
	if v, ok := parseStrictSemver(s); ok {
		return v, true
	}
	sv, ok := parseMasterminds(s)
	if !ok {
		return semverVersion{}, false
	}
	return semverVersion{major: sv.Major(), minor: sv.Minor(), patch: sv.Patch(), pre: sv.Prerelease(), orig: s}, true
}

// parseMasterminds parses s leniently. It tries StrictNewVersion first because
// NewVersion runs a coercion regex that dominates CPU and allocations when
// a package has thousands of releases. Whenever strict parsing succeeds the
// result is identical to NewVersion's (same Original, Prerelease, Metadata
// and ordering); anything strict rejects falls back to NewVersion.
func parseMasterminds(s string) (*msemver.Version, bool) {
	if v, err := msemver.StrictNewVersion(s); err == nil {
		return v, true
	}
	v, err := msemver.NewVersion(s)
	return v, err == nil
}

// parseStrictSemver is Masterminds' StrictNewVersion without allocations: it accepts
// exactly what StrictNewVersion accepts, with the same major, minor, patch and prerelease.
// MAJOR.MINOR.PATCH are decimal without leading zeros; the patch is followed by an optional
// "-" prerelease and "+" metadata, checked as validatePrerelease and validateMetadata do.
func parseStrictSemver(s string) (semverVersion, bool) {
	if s == "" || len(s) > msemver.MaxVersionLen {
		return semverVersion{}, false
	}
	v := semverVersion{orig: s}
	rest := s
	var ok bool
	if v.major, rest, ok = semverNumber(rest); !ok || rest == "" || rest[0] != '.' {
		return semverVersion{}, false
	}
	if v.minor, rest, ok = semverNumber(rest[1:]); !ok || rest == "" || rest[0] != '.' {
		return semverVersion{}, false
	}
	// StrictNewVersion cuts the metadata at the first "+" before the prerelease at the
	// first "-": "1.2.3+a-b" is metadata "a-b".
	rest = rest[1:]
	if i := strings.IndexByte(rest, '+'); i >= 0 {
		if !semverIdents(rest[i+1:], false) {
			return semverVersion{}, false
		}
		rest = rest[:i]
	}
	if i := strings.IndexByte(rest, '-'); i >= 0 {
		if !semverIdents(rest[i+1:], true) {
			return semverVersion{}, false
		}
		v.pre = rest[i+1:]
		rest = rest[:i]
	}
	if v.patch, rest, ok = semverNumber(rest); !ok || rest != "" {
		return semverVersion{}, false
	}
	return v, true
}

// semverNumber parses the leading decimal digits of s: at least one, no leading zero and
// within uint64, as StrictNewVersion's containsOnly, leading "0" and ParseUint checks.
func semverNumber(s string) (n uint64, rest string, ok bool) {
	i := 0
	for i < len(s) && '0' <= s[i] && s[i] <= '9' {
		i++
	}
	if i == 0 || i > 1 && s[0] == '0' {
		return 0, s, false
	}
	n, err := strconv.ParseUint(s[:i], 10, 64)
	return n, s[i:], err == nil
}

// semverIdents reports whether s is a dot-separated list of non-empty [0-9A-Za-z-]
// identifiers; for a prerelease (pre), numeric ones must not have a leading zero.
func semverIdents(s string, pre bool) bool {
	for {
		id, rest, more := strings.Cut(s, ".")
		if id == "" {
			return false
		}
		numeric := true
		for i := range len(id) {
			switch c := id[i]; {
			case '0' <= c && c <= '9':
			case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', c == '-':
				numeric = false
			default:
				return false
			}
		}
		if pre && numeric && len(id) > 1 && id[0] == '0' {
			return false
		}
		if !more {
			return true
		}
		s = rest
	}
}

func semverCompat(cur, next semverVersion) string {
	if next.pre != "" && cur.pre == "" {
		return "prerelease"
	}
	return ""
}

// compareSemver is Masterminds' Version.Compare: metadata is ignored and a prerelease
// sorts before its release.
func compareSemver(a, b semverVersion) int {
	if c := cmp.Compare(a.major, b.major); c != 0 {
		return c
	}
	if c := cmp.Compare(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmp.Compare(a.patch, b.patch); c != 0 {
		return c
	}
	switch {
	case a.pre == "" && b.pre == "":
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return compareSemverPre(a.pre, b.pre)
}

// compareSemverPre is Masterminds' comparePrerelease without splitting: dot-separated
// identifiers compare pairwise, a missing one counting as "".
func compareSemverPre(a, b string) int {
	for a != "" || b != "" {
		x, restA, _ := strings.Cut(a, ".")
		y, restB, _ := strings.Cut(b, ".")
		if c := compareSemverPrePart(x, y); c != 0 {
			return c
		}
		a, b = restA, restB
	}
	return 0
}

// compareSemverPrePart is Masterminds' comparePrePart: an empty identifier sorts first,
// numbers (as ParseUint reads them) compare numerically and before strings, strings bytewise.
func compareSemverPrePart(s, o string) int {
	switch {
	case s == o:
		return 0
	case s == "":
		return -1
	case o == "":
		return 1
	}
	si, errS := strconv.ParseUint(s, 10, 64)
	oi, errO := strconv.ParseUint(o, 10, 64)
	switch {
	case errS != nil && errO != nil:
		if s > o {
			return 1
		}
		return -1
	case errO != nil:
		return -1
	case errS != nil:
		return 1
	case si > oi:
		return 1
	}
	return -1
}

func semverKind(cur, next semverVersion) dep.UpdateType {
	switch {
	case cur.major != next.major:
		return dep.Major
	case cur.minor != next.minor:
		return dep.Minor
	}
	return dep.Patch
}
