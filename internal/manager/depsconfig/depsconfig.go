// Package depsconfig is the deps-config manager: the refs of the `extends` entries of the
// repository's deps configuration (`.deps.yaml`), Renovate's renovate-config manager for
// deps' own config files.
package depsconfig

import (
	"fmt"
	"strings"
	"unicode"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config/extendref"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name; DepTypeExtends the DepType of an extends entry.
const (
	Name           = "deps-config"
	DepTypeExtends = "extends"
)

const (
	dsTags = "git-tags"
	dsRefs = "git-refs"
	// headRef is the version of a commit-pinned entry: git-refs' default branch head.
	headRef = "HEAD"
)

// Skip reasons.
const (
	skipInvalid    = "invalid extends entry"
	skipNoRef      = "no ref"
	skipNonVersion = "non-version ref"
	skipURL        = "unsupported repository URL"
	skipForge      = "unknown forge"
	skipLocate     = "cannot locate value"
)

type depsConfigManager struct{}

// New returns the deps-config manager.
func New() manager.Manager { return depsConfigManager{} }

func (depsConfigManager) Name() string { return Name }

// Match reports whether the slash path is a deps configuration file at the root, as deps
// reads it: `.deps.yaml`, else `.deps.yml`.
func (depsConfigManager) Match(path string) bool {
	return path == ".deps.yaml" || path == ".deps.yml"
}

// Extract returns one dep per string entry of the top-level `extends` list.
func (depsConfigManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		doc = doc.Content[0]
	}
	entries := yamlspan.Seq(yamlspan.Get(doc, "extends"))
	if len(entries) == 0 {
		return nil, nil
	}
	lines := yamlspan.LineStarts(src)
	deps := make([]dep.Dep, 0, len(entries))
	for _, n := range entries {
		if n.Kind != yaml.ScalarNode {
			continue
		}
		deps = append(deps, entry(file, src, lines, n, env.ForgeURL))
	}
	return deps, nil
}

// entry returns the dep of extends entry n: a version-tag ref moves to newer tags (git-tags),
// a commit ref to the newest commit of the default branch (git-refs).
func entry(file string, src []byte, lines []int, n *yaml.Node, forgeURL string) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: dsTags, Versioning: "tag", DepType: DepTypeExtends, File: file, Line: n.Line, Name: n.Value}
	e, err := extendref.Parse(n.Value)
	if err != nil {
		d.Skip = skipInvalid
		return d
	}
	d.Name = e.Repo
	switch {
	case e.IsURL():
		if d.Name, d.Source = manager.GitRepo(e.Repo); d.Source == "" {
			d.Name, d.Skip = e.Repo, skipURL
			return d
		}
	case forgeURL == "":
		d.Skip = skipForge
		return d
	default:
		d.Source = strings.TrimSuffix(forgeURL, "/") + "/" + e.Repo
	}
	if e.Ref == "" {
		d.Skip = skipNoRef
		return d
	}
	d.Version = e.Ref
	start := yamlspan.Locate(src, lines, n)
	if start < 0 {
		d.Skip = skipLocate
		return d
	}
	// The ref is the tail of the value after its last '#' (extendref.Parse), which trims
	// surrounding blanks: a quoted value may keep them in the file.
	refEnd := start + len(strings.TrimRightFunc(n.Value, unicode.IsSpace))
	span := dep.Span{Start: refEnd - len(e.Ref), End: refEnd}
	switch {
	case manager.IsHex(e.Ref, 40):
		d.Datasource, d.Version = dsRefs, headRef
		d.Digest, d.DigestSpan = e.Ref, span
	case isVersion(e.Ref):
		d.RefSpan = span
	default:
		d.Skip = skipNonVersion
	}
	return d
}

// isVersion reports whether ref is a version tag (`v1`, `1.2`, `v1.2.3`).
func isVersion(ref string) bool {
	_, ok := versioning.ParseTag(ref)
	return ok
}

// Edits rewrites the ref: the commit of a commit-pinned entry, else the tag.
func (depsConfigManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == dsRefs {
		if u.NewDigest == "" || d.DigestSpan == (dep.Span{}) {
			return nil, fmt.Errorf("%s: no new commit", d.Name)
		}
		return []patch.Edit{{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest}}, nil
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
