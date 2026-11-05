package versioning

import (
	"cmp"
	"errors"
	"fmt"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Composer stabilities (composer/semver VersionParser), lowest first: a release is stable, a
// `-p1`/`-patch1`/`-pl1` version is a stable patch release above it.
const (
	composerDev = iota
	composerAlpha
	composerBeta
	composerRC
	composerStable
	composerPatch
)

// composerStabilities maps the stability names VersionParser accepts (lower-cased) to their
// rank, longest first so `beta` is not read as `b`.
var composerStabilities = []struct {
	name string
	rank int8
}{
	{"stable", composerStable}, {"alpha", composerAlpha}, {"patch", composerPatch}, {"beta", composerBeta},
	{"rc", composerRC}, {"pl", composerPatch}, {"a", composerAlpha}, {"b", composerBeta}, {"p", composerPatch},
}

// composerVersion is a Composer package version (composer/semver VersionParser::normalize):
// an optional `v`, 1 to 4 numeric parts, then an optional stability with numbers
// (`-beta1`, `-RC.2`, `.alpha`, `-p1`) and an optional `-dev`. Build metadata (`+…`) is
// ignored. n is the number of parts written; missing ones count as 0.
type composerVersion struct {
	parts [4]int
	n     int
	stab  int8
	snum  [2]int // the stability's numbers (`beta1.2`); missing ones count as 0
	dev   bool   // a `-dev` suffix after the stability (`1.0.0-beta1-dev`)
}

// parseComposer parses s as VersionParser::normalize does for a tagged version; branches
// (`dev-main`, `1.x-dev`) and date versions are not versions here.
func parseComposer(s string) (composerVersion, bool) {
	var v composerVersion
	v.stab = composerStable
	s, _, _ = strings.Cut(s, "+")
	if s != "" && (s[0] == 'v' || s[0] == 'V') {
		s = s[1:]
	}
	i := 0
	for v.n < len(v.parts) {
		j := i
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if i == j || i-j > 9 {
			return composerVersion{}, false
		}
		v.parts[v.n], _ = strconv.Atoi(s[j:i])
		v.n++
		if i+1 >= len(s) || s[i] != '.' || !isDigit(s[i+1]) {
			break
		}
		i++
	}
	rest := s[i:]
	if rest == "" {
		return v, true
	}
	if r, ok := strings.CutSuffix(strings.ToLower(rest), "dev"); ok && (r == "-" || r == ".") {
		v.stab = composerDev // `1.0.0-dev`
		return v, true
	}
	if rest[0] == '.' || rest[0] == '-' || rest[0] == '_' {
		rest = rest[1:]
	}
	lower := strings.ToLower(rest)
	found := false
	for _, st := range composerStabilities {
		if strings.HasPrefix(lower, st.name) {
			v.stab, rest, found = st.rank, rest[len(st.name):], true
			break
		}
	}
	if !found {
		return composerVersion{}, false
	}
	for k := 0; rest != "" && k < len(v.snum); k++ {
		start := 0
		if rest[0] == '.' || rest[0] == '-' {
			start = 1
		}
		j := start
		for j < len(rest) && isDigit(rest[j]) {
			j++
		}
		if j == start || j-start > 9 {
			break
		}
		v.snum[k], _ = strconv.Atoi(rest[start:j])
		rest = rest[j:]
	}
	switch strings.ToLower(rest) {
	case "":
	case "-dev", ".dev", "dev":
		v.dev = true
	default:
		return composerVersion{}, false
	}
	return v, true
}

// pre reports whether v is below stable (dev, alpha, beta, RC).
func (v composerVersion) pre() bool { return v.stab < composerStable || v.dev }

// compareComposer orders as Composer does (version_compare of normalized versions): the
// numeric parts, then the stability (dev < alpha < beta < RC < stable < patch) and its
// numbers; a `-dev` suffix sorts below the version without it.
func compareComposer(a, b composerVersion) int {
	for i := range a.parts {
		if c := cmp.Compare(a.parts[i], b.parts[i]); c != 0 {
			return c
		}
	}
	if c := cmp.Compare(a.stab, b.stab); c != 0 {
		return c
	}
	for i := range a.snum {
		if c := cmp.Compare(a.snum[i], b.snum[i]); c != 0 {
			return c
		}
	}
	switch {
	case a.dev == b.dev:
		return 0
	case a.dev:
		return -1
	}
	return 1
}

// composerCompat rejects prereleases unless cur is one.
func composerCompat(cur, next composerVersion) string {
	if next.pre() && !cur.pre() {
		return "prerelease"
	}
	return ""
}

func composerKind(cur, next composerVersion) dep.UpdateType {
	switch {
	case cur.parts[0] != next.parts[0]:
		return dep.Major
	case cur.parts[1] != next.parts[1]:
		return dep.Minor
	}
	return dep.Patch
}

// semver maps the first three parts and the stability below stable to a Masterminds version
// (`2.0.0-beta1` → 2.0.0-beta.1, `1.0.0-dev` → 1.0.0-dev.0); a patch release is its release.
func (v composerVersion) semver() *msemver.Version {
	pre := ""
	switch {
	case v.stab < composerStable:
		pre = [...]string{"dev", "alpha", "beta", "rc"}[v.stab] + "." + strconv.Itoa(v.snum[0])
	case v.dev:
		pre = "dev.0"
	}
	return msemver.New(uint64(v.parts[0]), uint64(v.parts[1]), uint64(v.parts[2]), pre, "")
}

func (v composerVersion) major() string { return strconv.Itoa(v.parts[0]) }

// lowest is the lowest version of v's release, its dev stability (Composer's `-dev` bounds:
// `>= 1.2` is `>= 1.2.0.0-dev`).
func (v composerVersion) lowest() composerVersion {
	return composerVersion{parts: v.parts, n: v.n, stab: composerDev}
}

// next is the dev stability of the release after v's part at index i (`1.2.3`, 1 → 1.3-dev).
func (v composerVersion) next(i int) composerVersion {
	out := composerVersion{n: i + 1, stab: composerDev}
	copy(out.parts[:i], v.parts[:i])
	out.parts[i] = v.parts[i] + 1
	return out
}

// composerScheme is the versioning of Composer packages (Renovate's `composer`), with
// Composer constraints (MatchRange).
type composerScheme struct{ impl[composerVersion] }

// ComposerCmp is one comparator of a Composer constraint: Op (`^`, `~`, `>=`, `>`, `<=`, `<`,
// `=`, `==`, `!=`, `<>`, "" for a bare version) and Ver as written at Off, with an optional
// stability flag (`@dev`, Flag without the `@`) up to End. Ver is a version (`1.2`,
// `v1.2.3`, `2.0-beta1`) or a wildcard (`1.2.*`, `1.x`, `*`).
type ComposerCmp struct {
	Op         string
	Start, Off int
	Ver, Flag  string
	End        int
}

// ComposerAlt is one alternative of a Composer constraint (Start, End its bytes): comparators
// that must all hold, or a hyphen range (Hyphen; Cmps[0] its low, Cmps[1] its high end).
type ComposerAlt struct {
	Start, End int
	Hyphen     bool
	Cmps       []ComposerCmp
}

// ComposerConstraint is a parsed Composer version constraint: alternatives separated by `||`
// or `|`. Sep is the first separator as written (with its blanks), "" for one alternative.
type ComposerConstraint struct {
	Alts []ComposerAlt
	Sep  string
}

// composerOps are the comparator operators, longest first.
var composerOps = []string{">=", "<=", "<>", "!=", "==", "^", "~", ">", "<", "="}

// errComposer is returned for constraints outside the supported grammar.
var errComposer = errors.New("unsupported composer constraint")

// ParseComposerConstraint parses a Composer constraint (composer/semver
// VersionParser::parseConstraints): `||`/`|` alternatives of comparators separated by `,`
// or blanks (blanks after an operator belong to it: `>= 1.2`), or hyphen ranges (`1.0 - 2.0`).
// Branch constraints (`dev-main`), aliases (`1.0 as 2.0`) and bare stability flags (`@dev`)
// are errors.
func ParseComposerConstraint(s string) (ComposerConstraint, error) {
	var c ComposerConstraint
	start := 0
	for i := 0; i <= len(s); i++ {
		if i < len(s) && s[i] != '|' {
			continue
		}
		end := i
		if a, ok := parseComposerAlt(s, start, end); ok {
			c.Alts = append(c.Alts, a)
		} else {
			return ComposerConstraint{}, fmt.Errorf("%w %q", errComposer, s)
		}
		if i == len(s) {
			break
		}
		j := i + 1
		if j < len(s) && s[j] == '|' {
			j++
		}
		if c.Sep == "" {
			ls, rs := i, j
			for ls > start && isBlank(s[ls-1]) {
				ls--
			}
			for rs < len(s) && isBlank(s[rs]) {
				rs++
			}
			c.Sep = s[ls:rs]
		}
		start, i = j, j-1
	}
	return c, nil
}

func isBlank(c byte) bool { return c == ' ' || c == '\t' }

// parseComposerAlt parses the alternative s[start:end].
func parseComposerAlt(s string, start, end int) (ComposerAlt, bool) {
	for start < end && isBlank(s[start]) {
		start++
	}
	for end > start && isBlank(s[end-1]) {
		end--
	}
	a := ComposerAlt{Start: start, End: end}
	i := start
	for i < end {
		cm, next, ok := parseComposerCmp(s, i, end)
		if !ok {
			return ComposerAlt{}, false
		}
		a.Cmps = append(a.Cmps, cm)
		i = next
		sep, commas := false, 0
		for i < end && (isBlank(s[i]) || s[i] == ',') {
			if s[i] == ',' {
				commas++
			}
			sep, i = true, i+1
		}
		if i < end && !sep || commas > 1 || commas == 1 && i == end {
			return ComposerAlt{}, false
		}
		if i+1 < end && s[i] == '-' && isBlank(s[i+1]) && len(a.Cmps) == 1 && cm.Op == "" && !a.Hyphen {
			a.Hyphen = true // `<low> - <high>`
			for i++; i < end && isBlank(s[i]); i++ {
			}
		}
	}
	if len(a.Cmps) == 0 || a.Hyphen && (len(a.Cmps) != 2 || a.Cmps[1].Op != "" || wildcard(a.Cmps[0].Ver) || wildcard(a.Cmps[1].Ver)) {
		return ComposerAlt{}, false
	}
	return a, true
}

// parseComposerCmp reads one comparator at i.
func parseComposerCmp(s string, i, end int) (ComposerCmp, int, bool) {
	c := ComposerCmp{Start: i}
	for _, op := range composerOps {
		if strings.HasPrefix(s[i:end], op) {
			c.Op = op
			i += len(op)
			break
		}
	}
	for c.Op != "" && i < end && isBlank(s[i]) {
		i++
	}
	c.Off = i
	for i < end && !isBlank(s[i]) && s[i] != ',' && s[i] != '@' {
		i++
	}
	c.Ver = s[c.Off:i]
	if i < end && s[i] == '@' {
		j := i + 1
		for j < end && !isBlank(s[j]) && s[j] != ',' {
			j++
		}
		c.Flag = s[i+1 : j]
		if _, ok := composerFlags[strings.ToLower(c.Flag)]; !ok {
			return ComposerCmp{}, 0, false
		}
		i = j
	}
	c.End = i
	if !validComposerVer(c.Ver) || wildcard(c.Ver) && c.Op != "" && c.Op != "=" && c.Op != "==" {
		return ComposerCmp{}, 0, false
	}
	return c, i, true
}

// wildcard reports whether ver ends in a `*`, `x` or `X` part (`1.2.*`, `*`).
func wildcard(ver string) bool {
	if ver == "" {
		return false
	}
	switch ver[len(ver)-1] {
	case '*', 'x', 'X':
		return len(ver) == 1 || ver[len(ver)-2] == '.'
	}
	return false
}

// wildcardParts returns the numeric parts before a wildcard (`1.2.*` → [1 2]).
func wildcardParts(ver string) ([]int, bool) {
	if ver != "" && (ver[0] == 'v' || ver[0] == 'V') {
		ver = ver[1:]
	}
	var out []int
	for p := range strings.SplitSeq(ver, ".") {
		if p == "*" || p == "x" || p == "X" {
			return out, len(out) < 4
		}
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 || len(p) > 9 {
			return nil, false
		}
		out = append(out, n)
	}
	return nil, false
}

func validComposerVer(ver string) bool {
	if wildcard(ver) {
		_, ok := wildcardParts(ver)
		return ok
	}
	_, ok := parseComposer(ver)
	return ok
}

// ComposerVersionParts returns the numeric parts of the Composer version v as written and
// its stability suffix (from the first character after the parts, `-beta1`); ok is false for
// an invalid version.
func ComposerVersionParts(v string) (parts []int, suffix string, ok bool) {
	p, ok := parseComposer(v)
	if !ok {
		return nil, "", false
	}
	s := v
	if s[0] == 'v' || s[0] == 'V' {
		s = s[1:]
	}
	i := 0
	for k := range p.n {
		for i < len(s) && isDigit(s[i]) {
			i++
		}
		if k < p.n-1 {
			i++
		}
	}
	return append([]int(nil), p.parts[:p.n]...), s[i:], true
}

// IsComposerPrerelease reports whether v is a valid Composer version below stable.
func IsComposerPrerelease(v string) bool {
	p, ok := parseComposer(v)
	return ok && p.pre()
}

// composerBound is one bound of a comparator: v op ver.
type composerBound struct {
	op  string // ">=", ">", "<=", "<", "==", "!="
	ver composerVersion
}

func (b composerBound) admits(v composerVersion) bool {
	c := compareComposer(v, b.ver)
	switch b.op {
	case ">=":
		return c >= 0
	case ">":
		return c > 0
	case "<=":
		return c <= 0
	case "<":
		return c < 0
	case "!=":
		return c != 0
	}
	return c == 0
}

// bounds returns the bounds of comparator c (composer/semver VersionParser::parseConstraint);
// none means any version.
func (c ComposerCmp) bounds() []composerBound {
	if wildcard(c.Ver) {
		ps, _ := wildcardParts(c.Ver)
		if len(ps) == 0 {
			return nil
		}
		var v composerVersion
		v.n = copy(v.parts[:], ps)
		return []composerBound{{">=", v.lowest()}, {"<", v.next(v.n - 1)}}
	}
	v, _ := parseComposer(c.Ver)
	low := v
	if !v.pre() && v.stab != composerPatch {
		low = v.lowest()
	}
	switch c.Op {
	case "^":
		i := 0
		switch {
		case v.parts[0] != 0 || v.n == 1:
		case v.parts[1] != 0 || v.n == 2:
			i = 1
		default:
			i = 2
		}
		return []composerBound{{">=", low}, {"<", v.next(i)}}
	case "~":
		return []composerBound{{">=", low}, {"<", v.next(max(0, v.n-2))}}
	case ">=":
		return []composerBound{{">=", low}}
	case "<":
		return []composerBound{{"<", low}}
	case ">", "<=":
		return []composerBound{{c.Op, v}}
	case "!=", "<>":
		return []composerBound{{"!=", v}}
	}
	return []composerBound{{"==", v}}
}

// hyphenBounds returns the bounds of hyphen range a: `>= low`, and `<= high` for a high end
// with three or more parts or a stability, else `<` the release after its last part
// (`1.0 - 2.1` admits 2.1.9).
func hyphenBounds(a ComposerAlt) []composerBound {
	low, _ := parseComposer(a.Cmps[0].Ver)
	high, _ := parseComposer(a.Cmps[1].Ver)
	if !low.pre() && low.stab != composerPatch {
		low = low.lowest()
	}
	upper := composerBound{"<=", high}
	if high.n < 3 && high.stab == composerStable {
		upper = composerBound{"<", high.next(high.n - 1)}
	}
	return []composerBound{{">=", low}, upper}
}

// composerFlags are the stability flags of a constraint (`@beta`) by lower-cased name.
var composerFlags = map[string]int8{"dev": composerDev, "alpha": composerAlpha, "beta": composerBeta, "rc": composerRC, "stable": composerStable}

// stability is v's stability; a `-dev` suffix makes it dev.
func (v composerVersion) stability() int8 {
	if v.dev {
		return composerDev
	}
	return v.stab
}

// Matcher returns whether a version satisfies c. As Composer's default minimum-stability
// (stable) has it, a version below stable only satisfies a constraint that names a version of
// its stability or a lower one (`^2.0-beta1` admits betas and RCs) or carries such a stability
// flag (`^2.0@beta`, `@dev`).
func (c ComposerConstraint) Matcher() func(v string) bool {
	alts := make([][]composerBound, len(c.Alts))
	least := int8(composerStable) // the lowest stability admitted
	for i, a := range c.Alts {
		if a.Hyphen {
			alts[i] = hyphenBounds(a)
		}
		for _, cm := range a.Cmps {
			if !a.Hyphen {
				alts[i] = append(alts[i], cm.bounds()...)
			}
			if v, ok := parseComposer(cm.Ver); ok && v.pre() {
				least = min(least, v.stability())
			}
			if s, ok := composerFlags[strings.ToLower(cm.Flag)]; ok {
				least = min(least, s)
			}
		}
	}
	return func(s string) bool {
		v, ok := parseComposer(s)
		if !ok || v.pre() && v.stability() < least {
			return false
		}
		for _, bs := range alts {
			all := true
			for _, b := range bs {
				if !b.admits(v) {
					all = false
					break
				}
			}
			if all {
				return true
			}
		}
		return false
	}
}

// MatchRange implements RangeMatcher with Composer constraint semantics
// (ParseComposerConstraint, ComposerConstraint.Matcher).
func (composerScheme) MatchRange(rng string) (func(v string) bool, error) {
	c, err := ParseComposerConstraint(rng)
	if err != nil {
		return nil, err
	}
	return c.Matcher(), nil
}
