// Package clojure updates the dependencies of Clojure projects: Leiningen's project.clj
// (leiningen manager) and the Clojure CLI's deps.edn and Babashka's bb.edn (deps-edn manager).
package clojure

import (
	"bytes"
	"cmp"
	"fmt"
	"strings"

	clojureds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/clojure"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Skip reasons.
const (
	skipNonLiteral = "non-literal version"
	skipLocate     = "cannot locate value"
	skipRange      = "version range"
	skipDynamic    = "dynamic version"
)

// skipUnresolved is the skip reason of a `~name` version no top-level `def` defines.
func skipUnresolved(ref string) string { return "unresolved version ~" + ref }

// mavenDep is a clojure-datasource dep of manager m in file named by the lib symbol lib
// (`group/artifact`, `artifact` for `artifact/artifact`) with the version form v.
func mavenDep(m, depType, file string, src []byte, lib string, v *form, source string) dep.Dep {
	return dep.Dep{
		Manager: m, Datasource: clojureds.Name, Versioning: "maven", DepType: depType,
		File: file, Line: lineOf(src, v.start), Name: coordinates(lib), Source: source,
	}
}

// coordinates maps a lib symbol to `groupId:artifactId`: `group/artifact`, or `artifact` for
// `artifact/artifact`; a tools.deps classifier (`group/artifact$classifier`) is dropped.
func coordinates(lib string) string {
	lib, _, _ = strings.Cut(lib, "$")
	if group, artifact, ok := strings.Cut(lib, "/"); ok {
		return group + ":" + artifact
	}
	return lib + ":" + lib
}

// setVersion sets the version of d from the string form v: a plain version, an exact Maven
// range `[1.0]` like a plain version, other valid ranges as Range (as the maven manager),
// or a skip.
func setVersion(d *dep.Dep, v *form) {
	if !v.plain {
		d.Skip, d.Version = skipLocate, v.text
		return
	}
	span := dep.Span{Start: v.end - 1 - len(v.text), End: v.end - 1}
	switch {
	case v.text == "RELEASE" || v.text == "LATEST":
		d.Skip, d.Version = skipDynamic, v.text
	case versioning.IsMavenRange(v.text):
		r, err := versioning.ParseMavenRange(v.text)
		if err != nil {
			d.Skip, d.Version = skipRange, v.text
			return
		}
		if len(r) == 1 && r[0].Exact() {
			d.Version = r[0].Min
			d.RefSpan = dep.Span{Start: span.Start + r[0].MinSpan.Start, End: span.Start + r[0].MinSpan.End}
			return
		}
		d.Range, d.Version, d.RefSpan = v.text, cmp.Or(r[0].Min, r[0].Max), span
	default:
		d.Version, d.RefSpan = v.text, span
	}
}

// lineOf returns the 1-based line of offset off.
func lineOf(src []byte, off int) int { return bytes.Count(src[:off], []byte{'\n'}) + 1 }

// rangeManager implements the Maven range handling of the maven manager.
type rangeManager struct{}

// InRange implements manager.RangeUpdater: whether d's Maven range admits v.
func (rangeManager) InRange(d dep.Dep, v string) bool {
	r, err := versioning.ParseMavenRange(d.Range)
	return err == nil && r.Admits(v)
}

// NewRange implements manager.RangeUpdater as the maven manager: pin writes v, a range
// admitting v is kept, one v exceeds is extended to admit it.
func (rangeManager) NewRange(d dep.Dep, v, strategy string) (string, bool) {
	if strategy == dep.RangePin {
		return v, v != d.Range
	}
	r, err := versioning.ParseMavenRange(d.Range)
	if err != nil {
		return "", false
	}
	return r.Extend(d.Range, v)
}

// Edits replaces the version (the `def` a `~name` version refers to), rewrites a range, and
// writes a git dependency's new commit at the length it is written with.
func (m rangeManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	switch {
	case d.Range != "":
		out, ok := m.NewRange(d, u.NewVersion, u.RangeStrategy)
		if !ok {
			return nil, fmt.Errorf("range %s cannot be rewritten for %s", d.Range, u.NewVersion)
		}
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Range, New: out})
	case u.NewVersion != d.Version:
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	if u.NewDigest != "" {
		if d.Digest == "" {
			return nil, fmt.Errorf("cannot pin %s: no commit to update", d.Name)
		}
		nd := u.NewDigest
		if len(d.Digest) < len(nd) {
			nd = nd[:len(d.Digest)]
		}
		if !strings.EqualFold(nd, d.Digest) {
			edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: nd})
		}
	}
	return edits, nil
}
