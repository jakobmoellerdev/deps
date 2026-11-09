package versioning

import (
	"cmp"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// glasskubeVersion is a Glasskube package version (Renovate's `glasskube` versioning): SemVer
// with an optional `v` prefix, whose first build identifier is the package revision
// (`v1.17.0+2`, the second Glasskube package of cert-manager 1.17.0).
type glasskubeVersion struct {
	semverVersion
	build uint64 // the leading digits of the first build identifier; 0 without
}

// parseGlasskube parses s as Renovate's GlasskubeVersioningApi does: node-semver's strict
// SemVer with an optional `v`, and the leading digits of the first build identifier
// (JavaScript's parseInt) as a fourth release part after the patch.
func parseGlasskube(s string) (glasskubeVersion, bool) {
	v, ok := parseStrictSemver(strings.TrimPrefix(s, "v"))
	if !ok {
		return glasskubeVersion{}, false
	}
	v.orig = s
	g := glasskubeVersion{semverVersion: v}
	if _, meta, ok := strings.Cut(s, "+"); ok {
		id, _, _ := strings.Cut(meta, ".")
		n := 0
		for n < len(id) && '0' <= id[n] && id[n] <= '9' {
			n++
		}
		if n > 0 {
			g.build, _ = strconv.ParseUint(id[:n], 10, 64) // out of range: the largest uint64
		}
	}
	return g, true
}

// compareGlasskube is Renovate's GenericVersioningApi order: the release parts major, minor,
// patch and build (absent = 0, so `v1.2.3` equals `v1.2.3+0`), then a prerelease below none.
// Prereleases compare as SemVer's (Renovate compares them as numeric-aware strings).
func compareGlasskube(a, b glasskubeVersion) int {
	if c := cmp.Compare(a.major, b.major); c != 0 {
		return c
	}
	if c := cmp.Compare(a.minor, b.minor); c != 0 {
		return c
	}
	if c := cmp.Compare(a.patch, b.patch); c != 0 {
		return c
	}
	if c := cmp.Compare(a.build, b.build); c != 0 {
		return c
	}
	switch {
	case a.pre == b.pre:
		return 0
	case a.pre == "":
		return 1
	case b.pre == "":
		return -1
	}
	return compareSemverPre(a.pre, b.pre)
}

// glasskubeCompat proposes prereleases only from a prerelease, as semver.
func glasskubeCompat(cur, next glasskubeVersion) string {
	return semverCompat(cur.semverVersion, next.semverVersion)
}

// glasskubeKind classifies by major and minor; anything else (a patch, a new package
// revision `+2`) is a patch.
func glasskubeKind(cur, next glasskubeVersion) dep.UpdateType {
	return semverKind(cur.semverVersion, next.semverVersion)
}

func glasskubeMajor(v glasskubeVersion) string { return semverMajor(v.semverVersion) }
