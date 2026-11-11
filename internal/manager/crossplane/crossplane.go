// Package crossplane extracts the package images of Crossplane Provider, Configuration and
// Function resources (pkg.crossplane.io `spec.package`).
package crossplane

import (
	"bytes"
	"log/slog"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "crossplane"

// Dep types: the lowercased kind, as Renovate's.
const (
	TypeProvider      = "provider"
	TypeConfiguration = "configuration"
	TypeFunction      = "function"
)

// group is the API group of Crossplane packages; apiVersion is "<group>/<version>".
const group = "pkg.crossplane.io"

// kinds maps the package kinds to their dep types.
var kinds = map[string]string{"Provider": TypeProvider, "Configuration": TypeConfiguration, "Function": TypeFunction}

type crossplaneManager struct{}

// New returns the crossplane manager.
func New() manager.Manager { return crossplaneManager{} }

func (crossplaneManager) Name() string { return Name }

// Match reports whether path is a YAML file: Crossplane files follow no naming convention, so
// every one is scanned (Extract skips files without the package API group).
func (crossplaneManager) Match(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

// Extract returns the spec.package images of the package resources of every document of src.
// Files and documents without "pkg.crossplane.io/" are not parsed; a file that does not parse
// (a template) yields no deps.
func (crossplaneManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	if !mentionsGroup(src) {
		return nil, nil
	}
	x := docker.Images{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	err := env.YAML.Documents(src, mentionsGroup, func(doc *yaml.Node) {
		if len(doc.Content) != 1 {
			return
		}
		n := doc.Content[0]
		g, _, _ := strings.Cut(yamlspan.Value(yamlspan.Get(n, "apiVersion")), "/")
		if depType := kinds[yamlspan.Value(yamlspan.Get(n, "kind"))]; g == group && depType != "" {
			x.Add(depType, yamlspan.Get(yamlspan.Get(n, "spec"), "package"))
		}
	})
	if err != nil {
		slog.Debug("skipping unparsable yaml", "file", file, "err", err)
		return nil, nil
	}
	return x.Deps, nil
}

// mentionsGroup reports whether b contains the package API group followed by "/".
func mentionsGroup(b []byte) bool { return bytes.Contains(b, []byte(group+"/")) }

// Edits rewrites image references like the docker manager.
func (crossplaneManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (crossplaneManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	return docker.Rename(d, name)
}
