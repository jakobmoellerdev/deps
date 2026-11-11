// Package gradlewrapper updates the Gradle version in gradle/wrapper/gradle-wrapper.properties.
package gradlewrapper

import (
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
)

type gradleWrapper struct{}

// New returns the gradle-wrapper manager.
func New() manager.Manager { return gradleWrapper{} }

func (gradleWrapper) Name() string { return Name }

// Match reports whether the slash path is a gradle/wrapper/gradle-wrapper.properties. The
// suffix check skips the regexp for the paths that cannot match.
func (gradleWrapper) Match(path string) bool {
	return strings.HasSuffix(path, "gradle/wrapper/gradle-wrapper.properties") && matchRe.MatchString(path)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (gradleWrapper) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{"gradle-wrapper.properties"}}
}

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
		if !manager.IsSHA256(sum.Value) {
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
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	return manager.ChecksumEdits(edits, u)
}
