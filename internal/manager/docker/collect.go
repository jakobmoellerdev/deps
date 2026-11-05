package docker

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Images collects the image references of YAML scalars for the CI configuration managers
// (bitbucket-pipelines, woodpecker, droneci, circleci, azure-pipelines). The references are
// parsed like the docker manager's inline references, so the docker manager's Edits and Rename
// apply to the deps. An alias resolves to its anchored value, which is collected once, where
// it is written.
type Images struct {
	Manager, File string
	Src           []byte
	Lines         []int // yamlspan.LineStarts(Src)
	Deps          []dep.Dep
	seen          map[*yaml.Node]bool
}

// Add collects the reference in v; nil, non-scalar and empty values are none.
func (c *Images) Add(depType string, v *yaml.Node) { c.AddPrefixed(depType, v, "") }

// AddPrefixed collects the reference in v after prefix (`docker://`); values without it, or
// only it, are none.
func (c *Images) AddPrefixed(depType string, v *yaml.Node, prefix string) {
	if v != nil && v.Kind == yaml.AliasNode {
		v = v.Alias
	}
	if v == nil || v.Kind != yaml.ScalarNode || !strings.HasPrefix(v.Value, prefix) || v.Value == prefix || c.seen[v] {
		return
	}
	if c.seen == nil {
		c.seen = map[*yaml.Node]bool{}
	}
	c.seen[v] = true
	ref := v.Value[len(prefix):]
	d := dep.Dep{Manager: c.Manager, Datasource: "docker", Versioning: "docker", DepType: depType, File: c.File, Line: v.Line, Name: ref}
	switch off := yamlspan.Locate(c.Src, c.Lines, v); {
	case strings.Contains(ref, "$"):
		d.Skip = "variable reference"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		ParseInlineRef(&d, off+len(prefix), ref)
	}
	c.Deps = append(c.Deps, d)
}
