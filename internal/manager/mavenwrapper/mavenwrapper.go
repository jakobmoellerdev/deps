// Package mavenwrapper updates the Maven and Maven Wrapper versions in
// .mvn/wrapper/maven-wrapper.properties.
package mavenwrapper

import (
	"errors"
	"regexp"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool/properties"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "maven-wrapper"

// Dep types: the Maven distribution (distributionUrl) and the wrapper itself (wrapperVersion,
// wrapperUrl).
const (
	DepTypeMaven   = "maven"
	DepTypeWrapper = "wrapper"
)

// Dep names (maven datasource coordinates).
const (
	MavenName   = "org.apache.maven:apache-maven"
	WrapperName = "org.apache.maven.wrapper:maven-wrapper"
)

// central is Maven Central as the maven datasource writes it; deps there have Source "".
const central = "https://repo.maven.apache.org/maven2/"

var (
	matchRe   = regexp.MustCompile(`(^|/)\.mvn/wrapper/maven-wrapper\.properties$`)
	versionRe = regexp.MustCompile(`^\d[0-9A-Za-z.+-]*$`)
	sha256Re  = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)
)

// artifact is one URL key of the properties file and the checksum key next to it.
type artifact struct {
	// re matches the URL suffix after the repository base; group 1 is the directory
	// version, group 2 the file name version.
	re                      *regexp.Regexp
	name, depType, sumKey   string
	unsupported, invalidSum string
}

var (
	distribution = artifact{
		re:   regexp.MustCompile(`org/apache/maven/apache-maven/([^/]+)/apache-maven-([^/]+)-bin\.zip$`),
		name: MavenName, depType: DepTypeMaven, sumKey: "distributionSha256Sum",
		unsupported: "unsupported distributionUrl", invalidSum: "invalid distributionSha256Sum",
	}
	wrapperJar = artifact{
		re:   regexp.MustCompile(`org/apache/maven/wrapper/maven-wrapper/([^/]+)/maven-wrapper-([^/]+)\.jar$`),
		name: WrapperName, depType: DepTypeWrapper, sumKey: "wrapperSha256Sum",
		unsupported: "unsupported wrapperUrl", invalidSum: "invalid wrapperSha256Sum",
	}
)

type mavenWrapper struct{}

// New returns the maven-wrapper manager.
func New() manager.Manager { return mavenWrapper{} }

func (mavenWrapper) Name() string { return Name }

func (mavenWrapper) Match(path string) bool { return matchRe.MatchString(path) }

// Extract returns the Maven version of distributionUrl and the wrapper version of
// wrapperVersion and wrapperUrl (one dep when both agree).
func (mavenWrapper) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	props := properties.Parse(src)
	var deps []dep.Dep
	if u, ok := props["distributionUrl"]; ok {
		deps = append(deps, distribution.dep(file, u, props))
	}
	wu, hasURL := props["wrapperUrl"]
	var wd dep.Dep
	if hasURL {
		wd = wrapperJar.dep(file, wu, props)
	}
	if v, ok := props["wrapperVersion"]; ok {
		if hasURL && wd.Skip == "" && v.Value == wd.Version {
			wd.InputSpan = dep.Span{Start: v.Start, End: v.End}
		} else {
			d := newDep(file, v.Line, WrapperName, DepTypeWrapper)
			d.Version, d.RefSpan = v.Value, dep.Span{Start: v.Start, End: v.End}
			if !versionRe.MatchString(v.Value) {
				d.Skip = "unsupported wrapperVersion"
			}
			deps = append(deps, d)
		}
	}
	if hasURL {
		deps = append(deps, wd)
	}
	return deps, nil
}

func newDep(file string, line int, name, depType string) dep.Dep {
	return dep.Dep{Manager: Name, Datasource: "maven", Versioning: "maven", DepType: depType, File: file, Line: line, Name: name}
}

// dep is the dep of the artifact URL u: both versions in the URL must agree; Source is the
// repository base and the checksum is the one pinned under a.sumKey, if any. Maven Central
// publishes no .sha256 files (only .sha1, and .sha512 for the distribution), so the new
// checksum is the digest of the new artifact itself.
func (a artifact) dep(file string, u properties.Property, props map[string]properties.Property) dep.Dep {
	d := newDep(file, u.Line, a.name, a.depType)
	m := a.re.FindStringSubmatchIndex(u.Value)
	if len(m) < 6 || u.Value[m[2]:m[3]] != u.Value[m[4]:m[5]] || !versionRe.MatchString(u.Value[m[2]:m[3]]) {
		d.Skip = a.unsupported
		return d
	}
	d.Version = u.Value[m[2]:m[3]]
	d.RefSpan = dep.Span{Start: u.Start + m[2], End: u.Start + m[3]}
	d.VersionSpan = dep.Span{Start: u.Start + m[4], End: u.Start + m[5]}
	base := properties.Unescape(u.Value[:m[0]])
	if base != central {
		d.Source = base
	}
	sum, ok := props[a.sumKey]
	if !ok {
		return d
	}
	if !sha256Re.MatchString(sum.Value) {
		d.Skip = a.invalidSum
		return d
	}
	d.Checksum = sum.Value
	d.ChecksumSpan = dep.Span{Start: sum.Start, End: sum.End}
	suffix := u.Value[m[0]:]
	d.ChecksumSource = &dep.ChecksumSource{Artifact: base + suffix[:m[2]-m[0]] + "{{version}}" + suffix[m[3]-m[0]:m[4]-m[0]] + "{{version}}" + suffix[m[5]-m[0]:]}
	return d
}

// Edits replaces every occurrence of the version (URL directory and file name,
// wrapperVersion) and, when the dep pins one, the checksum.
func (mavenWrapper) Edits(u dep.Update) ([]patch.Edit, error) {
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
		for _, s := range [...]dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
			if s.End > s.Start {
				edits = append(edits, patch.Edit{Start: s.Start, End: s.End, Old: d.Version, New: u.NewVersion})
			}
		}
	}
	if u.NewChecksum != "" && u.NewChecksum != d.Checksum {
		edits = append(edits, patch.Edit{Start: d.ChecksumSpan.Start, End: d.ChecksumSpan.End, Old: d.Checksum, New: u.NewChecksum})
	}
	return edits, nil
}
