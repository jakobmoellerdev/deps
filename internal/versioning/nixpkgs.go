package versioning

import (
	"regexp"
	"strconv"

	msemver "github.com/Masterminds/semver/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Nixpkgs is the name of the nixpkgs branch versioning (Renovate's `nixpkgs`).
const Nixpkgs = "nixpkgs"

// nixpkgsRe matches a branch of NixOS/nixpkgs as Renovate's nixpkgs versioning does: the
// prefix, the release (YY.MM) or unstable, and an optional suffix.
var nixpkgsRe = regexp.MustCompile(`^(nixos|nixpkgs|release)-(?:(\d{2})\.(\d{2})|unstable)(?:-(small|aarch64|darwin))?$`)

// nixpkgsVersion is a nixpkgs branch: release YY.MM (major, minor), or unstable (no
// release), and its compatibility, the prefix with the suffix (`nixos-small`).
type nixpkgsVersion struct {
	major, minor uint64
	unstable     bool
	compat       string
}

func parseNixpkgs(s string) (nixpkgsVersion, bool) {
	m := nixpkgsRe.FindStringSubmatch(s)
	if m == nil {
		return nixpkgsVersion{}, false
	}
	v := nixpkgsVersion{compat: m[1], unstable: m[2] == ""}
	if m[4] != "" {
		v.compat += "-" + m[4]
	}
	if !v.unstable {
		v.major, _ = strconv.ParseUint(m[2], 10, 64) // two digits
		v.minor, _ = strconv.ParseUint(m[3], 10, 64)
	}
	return v, true
}

// NixpkgsRelease reports whether v is a nixpkgs release branch (`nixos-24.05`,
// `nixpkgs-24.05-darwin`), not unstable.
func NixpkgsRelease(v string) bool {
	p, ok := parseNixpkgs(v)
	return ok && !p.unstable
}

// nixpkgsCompat requires the same prefix and suffix; unstable is a prerelease: a release
// branch never moves to it.
func nixpkgsCompat(cur, next nixpkgsVersion) string {
	switch {
	case next.compat != cur.compat:
		return "compatibility"
	case next.unstable && !cur.unstable:
		return "prerelease"
	}
	return ""
}

// compareNixpkgs orders releases by year and month, unstable above every release.
func compareNixpkgs(a, b nixpkgsVersion) int {
	switch {
	case a.unstable || b.unstable:
		return compareBool(a.unstable, b.unstable)
	case a.major != b.major:
		return cmpUint(a.major, b.major)
	}
	return cmpUint(a.minor, b.minor)
}

// nixpkgsKind: a new year is major (`nixos-24.11` → `nixos-25.05`), a new month minor.
func nixpkgsKind(cur, next nixpkgsVersion) dep.UpdateType {
	switch {
	case cur.major != next.major || cur.unstable != next.unstable:
		return dep.Major
	case cur.minor != next.minor:
		return dep.Minor
	}
	return dep.Patch
}

// semver is YY.MM.0 of a release; unstable has none.
func (v nixpkgsVersion) semver() *msemver.Version {
	if v.unstable {
		return nil
	}
	return msemver.New(v.major, v.minor, 0, "", "")
}

func (v nixpkgsVersion) majorNum() string { return strconv.FormatUint(v.major, 10) }
