// Package gradlewrapper updates the Gradle version in gradle/wrapper/gradle-wrapper.properties.
package gradlewrapper

import (
	"errors"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool/properties"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "gradle-wrapper"

// DepTypeWrapper is the dep type of the distributionUrl version.
const DepTypeWrapper = "wrapper"

var (
	matchRe   = regexp.MustCompile(`(^|/)gradle/wrapper/gradle-wrapper\.properties$`)
	segmentRe = regexp.MustCompile(`^gradle-(\d[0-9A-Za-z.+-]*)-(?:bin|all)\.zip$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

type gradleWrapper struct{}

// New returns the gradle-wrapper manager.
func New() manager.Manager { return gradleWrapper{} }

func (gradleWrapper) Name() string { return Name }

func (gradleWrapper) Match(path string) bool { return matchRe.MatchString(path) }

// Extract returns the Gradle version of `distributionUrl`, with `distributionSha256Sum` as its
// checksum when present.
func (gradleWrapper) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	props := properties.Parse(src)
	u, ok := props["distributionUrl"]
	if !ok {
		return nil, nil
	}
	d := dep.Dep{Manager: Name, Datasource: "gradle-version", Versioning: "maven", DepType: DepTypeWrapper,
		File: file, Line: u.Line, Name: "gradle"}
	slash := strings.LastIndexByte(u.Value, '/')
	m := segmentRe.FindStringSubmatchIndex(u.Value[slash+1:])
	if slash < 0 || m == nil {
		d.Skip = "unsupported distributionUrl"
		return []dep.Dep{d}, nil
	}
	vs, ve := slash+1+m[2], slash+1+m[3]
	d.Version = u.Value[vs:ve]
	d.RefSpan = dep.Span{Start: u.Start + vs, End: u.Start + ve}
	if sum, ok := props["distributionSha256Sum"]; ok {
		if !sha256Re.MatchString(sum.Value) {
			d.Skip = "invalid distributionSha256Sum"
			return []dep.Dep{d}, nil
		}
		d.Checksum = sum.Value
		d.ChecksumSpan = dep.Span{Start: sum.Start, End: sum.End}
		d.ChecksumSource = &dep.ChecksumSource{URL: properties.Unescape(u.Value[:vs]) + "{{version}}" + properties.Unescape(u.Value[ve:]) + ".sha256"}
	}
	return []dep.Dep{d}, nil
}

// Edits replaces the version and, when the dep pins one, the checksum.
func (gradleWrapper) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	hasChecksum := d.ChecksumSpan.End > d.ChecksumSpan.Start
	switch {
	case u.NewChecksum != "" && !hasChecksum:
		return nil, errors.New("dependency has no checksum to update")
	case u.NewChecksum == "" && hasChecksum && u.NewVersion != d.Version:
		return nil, errors.New("checksum must be updated together with the version, but no checksum was resolved")
	}
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	if u.NewChecksum != "" && u.NewChecksum != d.Checksum {
		edits = append(edits, patch.Edit{Start: d.ChecksumSpan.Start, End: d.ChecksumSpan.End, Old: d.Checksum, New: u.NewChecksum})
	}
	return edits, nil
}
