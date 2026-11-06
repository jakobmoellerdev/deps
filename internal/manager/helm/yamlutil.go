package helm

import (
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
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
// ("1.2" means "1.2.x").
func ExactVersion(v string) bool { return exactRe.MatchString(v) }

// SkipRange is the skip reason of a chart version range deps cannot rewrite.
const SkipRange = "unsupported range"

// ChartVersion classifies chart version v, a Masterminds constraint for Helm: an exact version
// is version itself; a range (`~1.2.0`, `^1`, `1.2.x`, `1.2`, `>=1.0.0 <2`, `>= 1.2, < 2`,
// `^1 || ^2`) is rng = v with version its anchor (`^1.2` → 1.2.0, npm.MastermindsAnchor),
// `v`-prefixed when the range's first version is (`~v1.14.0` → v1.14.0), so updates keep the
// style of charts versioned `v1.2.3`. skip is "no version" when v is empty, "template value"
// for a template and SkipRange for anything else (`*`, `!=1.2.3`). Managers that pin Helm
// charts (kustomize, terraform) share it.
func ChartVersion(v string) (version, rng, skip string) {
	switch {
	case v == "":
		return "", "", "no version"
	case strings.Contains(v, "{{"):
		return "", "", "template value"
	case ExactVersion(v):
		return v, "", ""
	}
	a, ok := npm.MastermindsAnchor(v)
	if !ok {
		return "", "", SkipRange
	}
	if strings.HasPrefix(strings.TrimLeft(v, "~^<>= \t"), "v") {
		a = "v" + a
	}
	return a, v, ""
}

// NewRange implements manager.RangeRewriter for chart version ranges (dep.Dep.Range) as
// Renovate's helm versioning (its npm versioning) does: npm.NewMastermindsRange. Managers that
// pin Helm charts share it.
func NewRange(d dep.Dep, v, strategy string) (string, bool) {
	return npm.NewMastermindsRange(d.Range, v, strategy)
}

// VersionEdits returns the edit of u's chart version: an exact version is replaced, a range
// (dep.Dep.Range) rewritten by NewRange under u.RangeStrategy; none when it stays.
func VersionEdits(u dep.Update) []patch.Edit {
	d := u.Dep
	if d.Range == "" {
		if u.NewVersion == d.Version {
			return nil
		}
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}
	}
	r, ok := NewRange(d, u.NewVersion, u.RangeStrategy)
	if !ok {
		return nil
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: r}}
}

// setVersion sets d's version (and range, see ChartVersion) from scalar v (nil = absent)
// located in src, or its skip reason.
func setVersion(d *dep.Dep, src []byte, lines []int, v *yaml.Node) {
	if v = yamlspan.Str(v); v == nil {
		d.Skip = "no version"
		return
	}
	d.Line = v.Line
	if d.Version, d.Range, d.Skip = ChartVersion(v.Value); d.Skip != "" {
		d.Version = v.Value
		return
	}
	off := yamlspan.Locate(src, lines, v)
	if off < 0 {
		d.Skip = "cannot locate value"
		return
	}
	d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
}
