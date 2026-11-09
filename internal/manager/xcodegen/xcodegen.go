// Package xcodegen extracts the Swift packages an XcodeGen project spec (`project.yml`)
// declares under `packages:`.
package xcodegen

import (
	"fmt"
	"path"

	"github.com/Masterminds/semver/v3"
	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/swift"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "xcodegen"

// Dep types: the requirement key of a package (Renovate's).
const (
	DepTypeFrom         = "from"
	DepTypeMajorVersion = "majorVersion"
	DepTypeMinorVersion = "minorVersion"
	DepTypeExactVersion = "exactVersion"
	DepTypeVersion      = "version"
)

// Datasource and Versioning of remote packages (the swift manager's).
const (
	Datasource = swift.Datasource
	Versioning = swift.Versioning
)

// Skip reasons.
const (
	skipLocal       = "local package"
	skipURL         = "unrecognized url"
	skipBranch      = "branch reference"
	skipRevision    = "revision pin"
	skipRange       = "version range"
	skipRequirement = "unsupported requirement"
	skipLocate      = "cannot locate value"
)

// requirementKeys are the requirement keys of a remote package in the order XcodeGen reads
// them (SwiftPackage.VersionRequirement): the first one present is the requirement;
// minVersion counts only together with maxVersion.
var requirementKeys = []string{DepTypeExactVersion, DepTypeVersion, "revision", "branch", "minVersion", DepTypeMinorVersion, DepTypeMajorVersion, DepTypeFrom}

type xcodegenManager struct{}

// New returns the xcodegen manager.
func New() manager.Manager { return xcodegenManager{} }

func (xcodegenManager) Name() string { return Name }

// Match reports whether the slash path is a project.yml.
func (xcodegenManager) Match(p string) bool { return path.Base(p) == "project.yml" }

// Extract returns one Dep per entry of the top-level `packages:` mapping whose value is a
// mapping, in source order. `from:`, `majorVersion:` (up to the next major version) and
// `minorVersion:` (up to the next minor version) are ranges whose version is the dep's Range
// and Version; `exactVersion:` and `version:` are plain versions. Local, branch, revision and
// minVersion/maxVersion packages are skipped.
func (xcodegenManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	pkgs := yamlspan.Get(doc.Content[0], "packages")
	if pkgs == nil || pkgs.Kind != yaml.MappingNode {
		return nil, nil
	}
	lines := yamlspan.LineStarts(src)
	var deps []dep.Dep
	for i := 0; i+1 < len(pkgs.Content); i += 2 {
		if v := pkgs.Content[i+1]; v.Kind == yaml.MappingNode {
			deps = append(deps, packageDep(file, src, lines, pkgs.Content[i], v))
		}
	}
	return deps, nil
}

// packageDep returns the dep of the package named key with the spec pkg; lines are src's
// LineStarts.
func packageDep(file string, src []byte, lines []int, key, pkg *yaml.Node) dep.Dep {
	d := dep.Dep{
		Manager: Name, Datasource: Datasource, Versioning: Versioning, ExtractVersion: swift.ExtractVersion,
		File: file, Line: key.Line, Name: key.Value,
	}
	if yamlspan.Get(pkg, "path") != nil {
		d.Skip = skipLocal
		return d
	}
	u := yamlspan.Value(yamlspan.Get(pkg, "url"))
	if gh := yamlspan.Get(pkg, "github"); gh != nil {
		u = "https://github.com/" + yamlspan.Value(gh)
	}
	if name, source := manager.GitRepo(u); source != "" {
		d.Name, d.Source = name, source
	} else {
		d.Skip = skipURL
		if u != "" {
			d.Name = u
		}
		return d
	}
	var req, v *yaml.Node
	for _, k := range requirementKeys {
		if v = yamlspan.Get(pkg, k); v != nil && (k != "minVersion" || yamlspan.Get(pkg, "maxVersion") != nil) {
			req = v
			d.DepType = k
			break
		}
	}
	switch {
	case req == nil:
		d.Skip = skipRequirement
		return d
	case d.DepType == "revision":
		d.DepType, d.Skip = "", skipRevision
		return d
	case d.DepType == "branch":
		d.DepType, d.Skip = "", skipBranch
		return d
	case d.DepType == "minVersion":
		d.DepType, d.Skip = "", skipRange
		return d
	}
	d.Line = req.Line
	lo, err := semver.StrictNewVersion(yamlspan.Value(req))
	if err != nil {
		d.Skip = skipRequirement
		return d
	}
	off := yamlspan.Locate(src, lines, req)
	if off < 0 {
		d.Skip = skipLocate
		return d
	}
	d.Version, d.RefSpan = req.Value, dep.Span{Start: off, End: off + len(req.Value)}
	switch d.DepType {
	case DepTypeFrom, DepTypeMajorVersion:
		d.Range, d.Constraint = req.Value, fmt.Sprintf(">=%s <%d.0.0", req.Value, lo.Major()+1)
	case DepTypeMinorVersion:
		d.Range, d.Constraint = req.Value, fmt.Sprintf(">=%s <%d.%d.0", req.Value, lo.Major(), lo.Minor()+1)
	}
	return d
}

// Edits replaces the version: the lower bound of a range, which keeps its form.
func (xcodegenManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
