package versioning

import (
	"strings"

	msemver "github.com/Masterminds/semver/v3"
	"golang.org/x/mod/semver"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

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
