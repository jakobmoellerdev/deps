package versioning

import (
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	msemver "github.com/Masterminds/semver/v3"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"
)

// BestGo returns the highest acceptable Go module version above cur.
func BestGo(cur string, versions []string, accept func(v string, kind dep.UpdateType) bool) (string, bool) {
	curPre := semver.Prerelease(cur) != ""
	curInc := strings.HasSuffix(cur, "+incompatible")
	curMajor := semver.Major(cur)
	best := ""
	for _, v := range versions {
		if !semver.IsValid(v) || module.IsPseudoVersion(v) {
			continue
		}
		if semver.Prerelease(v) != "" && !curPre {
			continue
		}
		if strings.HasSuffix(v, "+incompatible") && !curInc {
			continue
		}
		if m := semver.Major(v); m != curMajor && !(curMajor == "v0" && m == "v1") {
			continue
		}
		if semver.Compare(v, cur) <= 0 || (best != "" && semver.Compare(v, best) <= 0) {
			continue
		}
		if accept != nil && !accept(v, GoKind(cur, v)) {
			continue
		}
		best = v
	}
	return best, best != ""
}

// GoKind classifies cur → next.
func GoKind(cur, next string) dep.UpdateType {
	if semver.Major(cur) != semver.Major(next) {
		return dep.Major
	}
	if semver.MajorMinor(cur) != semver.MajorMinor(next) {
		return dep.Minor
	}
	return dep.Patch
}

// GoSemver converts a Go version to a Masterminds version, or nil.
func GoSemver(v string) *msemver.Version {
	s, err := msemver.NewVersion(strings.TrimPrefix(v, "v"))
	if err != nil {
		return nil
	}
	return s
}
