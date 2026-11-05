package helm

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// newDep returns a helm-datasource dep of depType in file.
func newDep(file, depType string) dep.Dep {
	return dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: depType, File: file}
}

// exactRe is a complete SemVer 2 version with an optional "v" prefix.
var exactRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// ExactVersion reports whether chart version v is a complete SemVer 2 version with an
// optional "v" prefix: the only chart version form Helm does not treat as a constraint
// ("1.2" means "1.2.x"). Managers that pin Helm charts (kustomize, terraform) share it.
func ExactVersion(v string) bool { return exactRe.MatchString(v) }

// versionSkip returns the skip reason of chart version v: "" for an exact version, "no
// version" when empty, "template value" for a template, "version range" otherwise (`~1.2.0`,
// `^1`, `>=1, <2`, `1.2.x`, `1.2`).
func versionSkip(v string) string {
	switch {
	case v == "":
		return "no version"
	case strings.Contains(v, "{{"):
		return "template value"
	case ExactVersion(v):
		return ""
	}
	return "version range"
}

// setVersion sets d's version from scalar v (nil = absent) located in src, or its skip reason.
func setVersion(d *dep.Dep, src []byte, lines []int, v *yaml.Node) {
	if v = yamlspan.Str(v); v == nil {
		d.Skip = "no version"
		return
	}
	d.Line, d.Version = v.Line, v.Value
	if d.Skip = versionSkip(v.Value); d.Skip != "" {
		return
	}
	off := yamlspan.Locate(src, lines, v)
	if off < 0 {
		d.Skip = "cannot locate value"
		return
	}
	d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
}
