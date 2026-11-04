package versioning

import (
	"strconv"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// cargoVersion is a Cargo requirement version: MAJOR[.MINOR[.PATCH[-pre][+build]]] without
// a "v" prefix. n is the number of parts written; v pads the missing ones with 0.
type cargoVersion struct {
	v *msemver.Version
	n int
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
			return cargoVersion{v: msemver.New(parts[0], parts[1], parts[2], "", ""), n: n}, true
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
	return cargoVersion{v: v, n: 3}, true
}

// cargoCompat requires the same precision (a partial requirement gets candidates of its own
// precision, `1.2` → `1.4`), and prereleases only from a prerelease.
func cargoCompat(cur, next cargoVersion) string {
	switch {
	case next.n != cur.n:
		return "precision"
	case next.v.Prerelease() != "" && cur.v.Prerelease() == "":
		return "prerelease"
	}
	return ""
}

func compareCargo(a, b cargoVersion) int { return a.v.Compare(b.v) }

// cargoKind follows Cargo's compatibility rules: leaving the range of the leftmost non-zero
// part (1.x, 0.4.x, 0.0.3) is major, so 0.4 → 0.5 and 0.0.3 → 0.0.4 are breaking. Within it,
// a change of the part after it is minor, anything else patch (0.4.1 → 0.4.2 is a patch).
func cargoKind(cur, next cargoVersion) dep.UpdateType {
	if cargoMajor(cur) != cargoMajor(next) {
		return dep.Major
	}
	if cur.v.Major() > 0 && cur.v.Minor() != next.v.Minor() {
		return dep.Minor
	}
	return dep.Patch
}

// cargoMajor is the compatibility range: "1" for 1.x, "0.4" for 0.4.x, "0.0.3" for 0.0.3.
// It names separateMultipleMajor buckets (v1, v0.4).
func cargoMajor(c cargoVersion) string {
	v := c.v
	switch {
	case v.Major() > 0:
		return strconv.FormatUint(v.Major(), 10)
	case v.Minor() > 0:
		return "0." + strconv.FormatUint(v.Minor(), 10)
	}
	return "0.0." + strconv.FormatUint(v.Patch(), 10)
}

func (c cargoVersion) semver() *msemver.Version { return c.v }
