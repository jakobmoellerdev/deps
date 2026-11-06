package versioning

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// pubScheme is the versioning of Dart packages (pub_semver): strict semantic versions
// (`1.2.3`, `1.2.3-dev.1`, `1.2.3+1`) with build metadata ordered after its release
// (`1.2.3+1` > `1.2.3`, pub's hotfix builds, compared as compareJulia does) and pub's version
// constraints (MatchRange, ParsePubConstraint).
type pubScheme struct{ impl[cargoVersion] }

// parsePub accepts a strict semver version: exactly three numeric parts, an optional
// prerelease and build.
func parsePub(s string) (cargoVersion, bool) {
	v, ok := parseCargo(s)
	if !ok || v.n != 3 {
		return cargoVersion{}, false
	}
	return v, true
}

// pubCompat accepts prereleases only from a prerelease.
func pubCompat(cur, next cargoVersion) string {
	if next.pre() != "" && cur.pre() == "" {
		return "prerelease"
	}
	return ""
}

// pubKind follows pub's notion of a breaking change (the caret range `^v`): a new major, or a
// new minor of a 0.x version, is major; any other change of the minor is minor, the rest
// (patch, prerelease, build) patch.
func pubKind(cur, next cargoVersion) dep.UpdateType {
	switch {
	case pubMajor(cur) != pubMajor(next):
		return dep.Major
	case cur.parts[1] != next.parts[1]:
		return dep.Minor
	}
	return dep.Patch
}

// pubMajor is the compatibility range of a caret constraint: "1" for 1.x, "0.4" for 0.4.x and
// 0.0.x alike ("0.0": `^0.0.3` is `<0.1.0` in pub). It names separateMultipleMajor buckets.
func pubMajor(c cargoVersion) string {
	if c.parts[0] > 0 {
		return strconv.FormatUint(c.parts[0], 10)
	}
	return "0." + strconv.FormatUint(c.parts[1], 10)
}

// PubNextBreaking returns the first version a caret constraint `^v` excludes: the next major,
// or the next minor for a 0.x version (pub_semver's nextBreaking); false for an invalid v.
func PubNextBreaking(v string) (string, bool) {
	c, ok := parsePub(v)
	if !ok {
		return "", false
	}
	if c.parts[0] > 0 {
		return strconv.FormatUint(c.parts[0]+1, 10) + ".0.0", true
	}
	return "0." + strconv.FormatUint(c.parts[1]+1, 10) + ".0", true
}

// ComparePub orders two pub versions (invalid ones bytewise).
func ComparePub(a, b string) int { return schemes["pub"].Compare(a, b) }

// PubTerm is one comparison of a pub version constraint. Op is "^", ">=", ">", "<=", "<" or ""
// (exactly V). Start and End delimit V in the constraint.
type PubTerm struct {
	Op         string
	V          string
	Start, End int
}

// PubConstraint is a pub version constraint: `any`, or terms that must all hold
// (`^1.2.3`, `>=1.2.3 <2.0.0`, `1.2.3`).
type PubConstraint struct {
	Any   bool
	Terms []PubTerm
}

// ParsePubConstraint parses s as pub_semver's VersionConstraint.parse: `any`, or one or more
// terms, each an optional operator (`^`, `>=`, `>`, `<=`, `<`; blanks may follow it) and a
// full version, separated by blanks or written back to back (`>=1.0.0<2.0.0`). Only `^`
// and comparisons are combined with other terms; `^` and exact versions stand alone in practice
// but are accepted anywhere.
func ParsePubConstraint(s string) (PubConstraint, error) {
	if strings.TrimSpace(s) == "any" {
		return PubConstraint{Any: true}, nil
	}
	var c PubConstraint
	i := 0
	for {
		for i < len(s) && pubBlank(s[i]) {
			i++
		}
		if i == len(s) {
			break
		}
		op := ""
		for _, o := range [...]string{">=", "<=", ">", "<", "^"} {
			if strings.HasPrefix(s[i:], o) {
				op = o
				break
			}
		}
		i += len(op)
		for op != "" && i < len(s) && pubBlank(s[i]) {
			i++
		}
		start := i
		for i < len(s) && pubVersionByte(s[i]) {
			i++
		}
		v := s[start:i]
		if _, ok := parsePub(v); !ok {
			return PubConstraint{}, fmt.Errorf("invalid version %q in constraint %q", v, s)
		}
		c.Terms = append(c.Terms, PubTerm{Op: op, V: v, Start: start, End: i})
	}
	if len(c.Terms) == 0 {
		return PubConstraint{}, errors.New("empty constraint")
	}
	return c, nil
}

func pubBlank(b byte) bool { return b == ' ' || b == '\t' }

func pubVersionByte(b byte) bool {
	return '0' <= b && b <= '9' || 'a' <= b && b <= 'z' || 'A' <= b && b <= 'Z' || b == '.' || b == '-' || b == '+'
}

// Floor returns the term giving the constraint's lower bound (`^`, `>=`, `>` or an exact
// version), or false when it has none (`<2.0.0`, `any`).
func (c PubConstraint) Floor() (PubTerm, bool) {
	for _, t := range c.Terms {
		if t.Op == "^" || t.Op == ">=" || t.Op == ">" || t.Op == "" {
			return t, true
		}
	}
	return PubTerm{}, false
}

// Admits reports whether the valid pub version v satisfies every term. As in pub_semver, an
// exclusive upper bound that is a release excludes that release's prereleases (`<2.0.0` and
// `^1.2.3` exclude 2.0.0-dev.1), unless the lower bound is a prerelease of it.
func (c PubConstraint) Admits(v string) bool {
	x, ok := parsePub(v)
	if !ok {
		return false
	}
	if c.Any {
		return true
	}
	floor, _ := c.Floor()
	for _, t := range c.Terms {
		y, _ := parsePub(t.V)
		cmp := compareJulia(x, y)
		switch t.Op {
		case "":
			if cmp != 0 {
				return false
			}
		case ">=":
			if cmp < 0 {
				return false
			}
		case ">":
			if cmp <= 0 {
				return false
			}
		case "<=":
			if cmp > 0 {
				return false
			}
		case "<":
			if cmp >= 0 || excludedPre(x, y, floor) {
				return false
			}
		case "^":
			hi, _ := PubNextBreaking(t.V)
			h, _ := parsePub(hi)
			if cmp < 0 || compareJulia(x, h) >= 0 || excludedPre(x, h, t) {
				return false
			}
		}
	}
	return true
}

// excludedPre reports whether x is a prerelease of the release hi that an exclusive upper bound
// `<hi` leaves out: hi is a release and the floor is not a prerelease of hi's parts.
func excludedPre(x, hi cargoVersion, floor PubTerm) bool {
	if x.pre() == "" || hi.pre() != "" || x.parts != hi.parts {
		return false
	}
	f, ok := parsePub(floor.V)
	return !ok || floor.Op == "^" || f.pre() == "" || f.parts != hi.parts
}

// MatchRange implements RangeMatcher with pub's constraint semantics (ParsePubConstraint,
// PubConstraint.Admits).
func (pubScheme) MatchRange(rng string) (func(v string) bool, error) {
	c, err := ParsePubConstraint(rng)
	if err != nil {
		return nil, err
	}
	return c.Admits, nil
}
