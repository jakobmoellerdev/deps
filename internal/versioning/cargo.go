package versioning

import (
	"slices"
	"strconv"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// cargoVersion is a Cargo requirement version: MAJOR[.MINOR[.PATCH[-pre][+build]]] without
// a "v" prefix. n is the number of parts written; parts pads the missing ones with 0. v is
// the parsed version of one with a prerelease or build suffix and nil for a plain release,
// which parses without allocating.
type cargoVersion struct {
	parts [3]uint64
	n     int
	v     *msemver.Version
}

// parseCargo accepts 1–3 numeric parts; a prerelease or build suffix only after 3 parts.
func parseCargo(s string) (cargoVersion, bool) {
	var parts [3]uint64
	n, i := 0, 0
	for {
		j := i
		for j < len(s) && '0' <= s[j] && s[j] <= '9' {
			j++
		}
		if j == i || n == 3 || j-i > 1 && s[i] == '0' {
			return cargoVersion{}, false // empty, a fourth part, or a leading zero
		}
		p, err := strconv.ParseUint(s[i:j], 10, 64)
		if err != nil {
			return cargoVersion{}, false
		}
		parts[n], n, i = p, n+1, j
		if i == len(s) {
			return cargoVersion{parts: parts, n: n}, true
		}
		if s[i] != '.' {
			break
		}
		i++
	}
	if n != 3 || s[i] != '-' && s[i] != '+' {
		return cargoVersion{}, false
	}
	v, err := msemver.StrictNewVersion(s)
	if err != nil {
		return cargoVersion{}, false
	}
	return cargoVersion{parts: parts, n: 3, v: v}, true
}

// pre is the prerelease without "-"; "" = release.
func (c cargoVersion) pre() string {
	if c.v == nil {
		return ""
	}
	return c.v.Prerelease()
}

// cargoCompat requires the same precision (a partial requirement gets candidates of its own
// precision, `1.2` → `1.4`), and prereleases only from a prerelease.
func cargoCompat(cur, next cargoVersion) string {
	switch {
	case next.n != cur.n:
		return "precision"
	case next.pre() != "" && cur.pre() == "":
		return "prerelease"
	}
	return ""
}

// compareCargo orders like Masterminds' Compare: by the parts, then a release above its
// prereleases, which compare by semver precedence; build metadata is ignored.
func compareCargo(a, b cargoVersion) int {
	if c := slices.Compare(a.parts[:], b.parts[:]); c != 0 {
		return c
	}
	switch ap, bp := a.pre(), b.pre(); {
	case ap == "" && bp == "":
		return 0
	case ap == "":
		return 1
	case bp == "":
		return -1
	}
	return a.v.Compare(b.v)
}

// cargoKind follows Cargo's compatibility rules: leaving the range of the leftmost non-zero
// part (1.x, 0.4.x, 0.0.3) is major, so 0.4 → 0.5 and 0.0.3 → 0.0.4 are breaking. Within it,
// a change of the part after it is minor, anything else patch (0.4.1 → 0.4.2 is a patch).
func cargoKind(cur, next cargoVersion) dep.UpdateType {
	if cargoMajor(cur) != cargoMajor(next) {
		return dep.Major
	}
	if cur.parts[0] > 0 && cur.parts[1] != next.parts[1] {
		return dep.Minor
	}
	return dep.Patch
}

// cargoMajor is the compatibility range: "1" for 1.x, "0.4" for 0.4.x, "0.0.3" for 0.0.3.
// It names the buckets of separate: majors (v1, v0.4).
func cargoMajor(c cargoVersion) string {
	switch {
	case c.parts[0] > 0:
		return strconv.FormatUint(c.parts[0], 10)
	case c.parts[1] > 0:
		return "0." + strconv.FormatUint(c.parts[1], 10)
	}
	return "0.0." + strconv.FormatUint(c.parts[2], 10)
}

func (c cargoVersion) semver() *msemver.Version {
	if c.v != nil {
		return c.v
	}
	return msemver.New(c.parts[0], c.parts[1], c.parts[2], "", "")
}
