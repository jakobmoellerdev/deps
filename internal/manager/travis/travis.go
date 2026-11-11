// Package travis extracts the Node.js versions of Travis CI configurations (`node_js` of
// `.travis.yml` and of its `jobs`/`matrix` `include` entries).
package travis

import (
	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/toolversions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "travis"

// DepTypeNode is the dep type of a Node.js version.
const DepTypeNode = "node"

type travisManager struct{}

// New returns the travis manager.
func New() manager.Manager { return travisManager{} }

func (travisManager) Name() string { return Name }

// Match reports whether path is `.travis.y(a)ml` at the repository root (Renovate's pattern,
// where Travis CI reads it).
func (travisManager) Match(path string) bool { return path == ".travis.yml" || path == ".travis.yaml" }

// MatchHints lets discovery skip this manager for paths it cannot match.
func (travisManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{".travis.yml", ".travis.yaml"}}
}

// Extract returns the Node.js versions of the top-level `node_js` and of the `node_js` of every
// `jobs.include` and `matrix.include` entry, a version or a list of them each.
func (travisManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, nil
	}
	root := doc.Content[0]
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	x.add(yamlspan.Get(root, "node_js"))
	for _, key := range []string{"jobs", "matrix"} {
		for _, job := range yamlspan.Seq(yamlspan.Get(yamlspan.Get(root, key), "include")) {
			x.add(yamlspan.Get(job, "node_js"))
		}
	}
	return x.deps, nil
}

type extractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
}

// add collects the version scalar v or the scalars of the list v. A version whose bytes are
// not found in the file (escapes) is skipped.
func (x *extractor) add(v *yaml.Node) {
	items := []*yaml.Node{v}
	if v != nil && v.Kind == yaml.SequenceNode {
		items = v.Content
	}
	for _, n := range items {
		if n = yamlspan.Str(n); n == nil {
			continue
		}
		off := yamlspan.Locate(x.src, x.lines, n)
		if off < 0 {
			x.deps = append(x.deps, dep.Dep{Manager: Name, Datasource: "node-version", DepType: DepTypeNode, File: x.file, Line: n.Line, Name: "node", Version: n.Value, Skip: "cannot locate value"})
			continue
		}
		x.deps = append(x.deps, toolversions.ToolDep(Name, DepTypeNode, x.file, n.Line, "node", n.Value, off))
	}
}

// Edits replaces the version token; quotes and the precision chosen by the versioning stay.
func (travisManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
