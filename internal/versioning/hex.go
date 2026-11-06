package versioning

import (
	"errors"
	"strings"
)

// hexScheme is the versioning of Hex packages (Renovate's `hex`): versions are SemVer 2.0
// as Elixir's Version module parses them (MAJOR.MINOR.PATCH, an optional prerelease and
// build, no `v` prefix), with Elixir version requirements (MatchRange).
type hexScheme struct{ impl[semverVersion] }

// HexCmp is one comparator of an Elixir version requirement: Op (`==`, `!=`, `>`, `>=`,
// `<`, `<=`, `~>`, "" for a bare version, which means `==`) and Ver as written from Off to
// End; Start is the operator's offset (Off without one). Ver is a full version
// (`1.2.3-rc.0`), or MAJOR.MINOR after `~>` (`~> 2.0`).
type HexCmp struct {
	Op, Ver         string
	Start, Off, End int
}

// HexAlt is one alternative of a requirement (Start, End its bytes): comparators joined by
// `and` that must all hold.
type HexAlt struct {
	Start, End int
	Cmps       []HexCmp
}

// HexRequirement is a parsed Elixir version requirement: alternatives joined by `or`.
type HexRequirement struct{ Alts []HexAlt }

// hexOps are the requirement operators, longest first.
var hexOps = []string{"~>", ">=", "<=", "==", "!=", ">", "<"}

var errHex = errors.New("unsupported Elixir version requirement")

// ParseHexRequirement parses an Elixir version requirement (Version.parse_requirement):
// comparators joined by `and` and `or` (`and` binding tighter), each an optional operator
// (blanks after it are optional) and a version; `~>` also takes MAJOR.MINOR.
func ParseHexRequirement(s string) (HexRequirement, error) {
	var r HexRequirement
	alt := HexAlt{Start: -1}
	i := 0
	for {
		for i < len(s) && isBlank(s[i]) {
			i++
		}
		if i == len(s) {
			return HexRequirement{}, errHex // empty, or a trailing `and`/`or`
		}
		c := HexCmp{Start: i}
		for _, op := range hexOps {
			if strings.HasPrefix(s[i:], op) {
				c.Op = op
				i += len(op)
				break
			}
		}
		for i < len(s) && isBlank(s[i]) {
			i++
		}
		c.Off = i
		for i < len(s) && !isBlank(s[i]) && !strings.ContainsRune("~<>=!", rune(s[i])) {
			i++
		}
		c.End, c.Ver = i, s[c.Off:i]
		if _, ok := hexBoundVersion(c.Ver, c.Op == "~>"); !ok {
			return HexRequirement{}, errHex
		}
		if alt.Start < 0 {
			alt.Start = c.Start
		}
		alt.Cmps = append(alt.Cmps, c)
		alt.End = c.End
		for i < len(s) && isBlank(s[i]) {
			i++
		}
		if i == len(s) {
			r.Alts = append(r.Alts, alt)
			return r, nil
		}
		word := ""
		for _, w := range []string{"and", "or"} {
			if strings.HasPrefix(s[i:], w) && (i+len(w) == len(s) || isBlank(s[i+len(w)]) || strings.ContainsRune("~<>=!", rune(s[i+len(w)]))) {
				word = w
			}
		}
		if word == "" || c.End == i { // a word needs blanks before it: `1.0.0and` is a version
			return HexRequirement{}, errHex
		}
		i += len(word)
		if word == "or" {
			r.Alts = append(r.Alts, alt)
			alt = HexAlt{Start: -1}
		}
	}
}

// hexBoundVersion parses the version of a comparator: a full SemVer version, or with approx
// (`~>`) also MAJOR.MINOR with an optional prerelease, read with patch 0.
func hexBoundVersion(ver string, approx bool) (semverVersion, bool) {
	if v, ok := parseStrictSemver(ver); ok {
		return v, true
	}
	if !approx {
		return semverVersion{}, false
	}
	core, rest := ver, ""
	if i := strings.IndexAny(ver, "-+"); i >= 0 {
		core, rest = ver[:i], ver[i:]
	}
	if strings.Count(core, ".") != 1 {
		return semverVersion{}, false
	}
	return parseStrictSemver(core + ".0" + rest)
}

// Parts is the number of numeric parts c.Ver is written with (`~> 2.0`: 2).
func (c HexCmp) Parts() int {
	core := c.Ver
	if i := strings.IndexAny(core, "-+"); i >= 0 {
		core = core[:i]
	}
	return strings.Count(core, ".") + 1
}

// hexBound is one bound of a comparator: v op ver.
type hexBound struct {
	op string
	v  semverVersion
}

func (b hexBound) admits(v semverVersion) bool {
	c := compareSemver(v, b.v)
	switch b.op {
	case "!=":
		return c != 0
	case ">":
		return c > 0
	case ">=":
		return c >= 0
	case "<":
		return c < 0
	case "<=":
		return c <= 0
	}
	return c == 0
}

// bounds returns the bounds of comparator c: `~> 2.1` is `>= 2.1.0 and < 3.0.0-0`, `~> 2.1.3`
// is `>= 2.1.3 and < 2.2.0-0` (Elixir's upper bounds exclude the next release's prereleases).
func (c HexCmp) bounds() []hexBound {
	v, _ := hexBoundVersion(c.Ver, c.Op == "~>")
	switch c.Op {
	case "":
		return []hexBound{{"==", v}}
	case "~>":
		upper := semverVersion{major: v.major + 1, pre: "0"}
		if c.Parts() > 2 {
			upper = semverVersion{major: v.major, minor: v.minor + 1, pre: "0"}
		}
		return []hexBound{{">=", v}, {"<", upper}}
	}
	return []hexBound{{c.Op, v}}
}

// Matcher returns whether a version satisfies r: any alternative whose comparators all admit
// it. As Hex resolves (Version.match? with allow_pre: false), a prerelease only satisfies an
// alternative of which a comparator names a prerelease (`~> 2.0.0-rc.0`).
func (r HexRequirement) Matcher() func(v string) bool {
	type alt struct {
		bounds []hexBound
		pre    bool
	}
	alts := make([]alt, len(r.Alts))
	for i, a := range r.Alts {
		for _, c := range a.Cmps {
			alts[i].bounds = append(alts[i].bounds, c.bounds()...)
			if v, _ := hexBoundVersion(c.Ver, c.Op == "~>"); v.pre != "" {
				alts[i].pre = true
			}
		}
	}
	return func(s string) bool {
		v, ok := parseStrictSemver(s)
		if !ok {
			return false
		}
	next:
		for _, a := range alts {
			if v.pre != "" && !a.pre {
				continue
			}
			for _, b := range a.bounds {
				if !b.admits(v) {
					continue next
				}
			}
			return true
		}
		return false
	}
}

// MatchRange implements RangeMatcher with Elixir version requirement semantics
// (ParseHexRequirement, HexRequirement.Matcher).
func (hexScheme) MatchRange(rng string) (func(v string) bool, error) {
	r, err := ParseHexRequirement(rng)
	if err != nil {
		return nil, err
	}
	return r.Matcher(), nil
}
