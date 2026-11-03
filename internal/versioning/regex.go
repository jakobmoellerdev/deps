package versioning

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Regex scheme groups: the numeric components in comparison order, then the strings.
var (
	regexNumGroups = []string{"major", "minor", "patch", "build", "revision"}
	regexGroups    = append(slices.Clone(regexNumGroups), "prerelease", "compatibility")
)

// regexVersion is a version parsed by a regex scheme. Absent or non-participating numeric
// groups read as 0.
type regexVersion struct {
	nums   [5]uint64 // major, minor, patch, build, revision
	pre    string    // prerelease group; "" = release
	compat string    // compatibility group
}

// newRegexScheme compiles Renovate's regex versioning: expr must match the whole version
// (anchor it) and name its parts with the groups major (required), minor, patch, build,
// revision (numeric), prerelease and compatibility. Go's regexp accepts both (?P<name>...)
// and Renovate's (?<name>...) group syntax, so expressions from renovate.json5 work as they
// are. Other named groups are rejected so a typo does not silently read as 0.
func newRegexScheme(expr string) (Scheme, error) {
	re, err := regexp.Compile(expr)
	if err != nil {
		return nil, fmt.Errorf("regex: invalid RE2 regexp: %w", err)
	}
	var idx [len(regexVersion{}.nums)]int
	for i, g := range regexNumGroups {
		idx[i] = re.SubexpIndex(g)
	}
	if idx[0] < 0 {
		return nil, errors.New("regex: must have a (?<major>...) group")
	}
	for _, n := range re.SubexpNames() {
		if n != "" && !slices.Contains(regexGroups, n) {
			return nil, fmt.Errorf("regex: unknown group %q (allowed: %s)", n, strings.Join(regexGroups, ", "))
		}
	}
	gPre, gCompat := re.SubexpIndex("prerelease"), re.SubexpIndex("compatibility")
	parse := func(s string) (regexVersion, bool) {
		m := re.FindStringSubmatchIndex(s)
		if m == nil {
			return regexVersion{}, false
		}
		var v regexVersion
		for i, g := range idx {
			if g < 0 || m[2*g] < 0 {
				continue
			}
			n, err := strconv.ParseUint(s[m[2*g]:m[2*g+1]], 10, 64)
			if err != nil {
				return regexVersion{}, false // not a number, or overflow
			}
			v.nums[i] = n
		}
		v.pre, v.compat = group(s, m, gPre), group(s, m, gCompat)
		return v, true
	}
	return impl[regexVersion]{parse: parse, compat: regexCompat, cmp: compareRegex, kind: regexKind, semver: regexVersion.semver, major: regexVersion.major}, nil
}

// group returns the text of submatch g, or "" when g is absent or did not participate.
func group(s string, m []int, g int) string {
	if g < 0 || m[2*g] < 0 {
		return ""
	}
	return s[m[2*g]:m[2*g+1]]
}

// regexCompat requires an equal compatibility group and rejects prereleases unless cur is one.
func regexCompat(cur, next regexVersion) string {
	switch {
	case next.compat != cur.compat:
		return "compatibility"
	case next.pre != "" && cur.pre == "":
		return "prerelease"
	}
	return ""
}

// compareRegex orders major, minor, patch, build and revision numerically, then a release
// above its prereleases and prereleases by their identifiers.
func compareRegex(a, b regexVersion) int {
	for i := range a.nums {
		if a.nums[i] != b.nums[i] {
			if a.nums[i] < b.nums[i] {
				return -1
			}
			return 1
		}
	}
	return comparePrerelease(a.pre, b.pre)
}

// regexKind classifies by group: major, minor, else patch (also for build, revision and
// prerelease changes).
func regexKind(cur, next regexVersion) dep.UpdateType {
	switch {
	case cur.nums[0] != next.nums[0]:
		return dep.Major
	case cur.nums[1] != next.nums[1]:
		return dep.Minor
	}
	return dep.Patch
}

// semver builds MAJOR.MINOR.PATCH[-prerelease] from the groups for allow constraints.
func (v regexVersion) semver() *msemver.Version {
	return msemver.New(v.nums[0], v.nums[1], v.nums[2], v.pre, "")
}

func (v regexVersion) major() string { return strconv.FormatUint(v.nums[0], 10) }
