// Package velaci extracts the container images of Vela pipelines (`.vela.y(a)ml`): steps,
// services, the steps of stages and the images of secret origins.
package velaci

import (
	"path"
	"regexp"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "velaci"

// Dep types.
const (
	TypeImage   = "image"
	TypeService = "service"
	TypeSecret  = "secret"
)

// fileRe is Renovate's pattern `(^|/)\.vela\.ya?ml$` for the base name.
var fileRe = regexp.MustCompile(`^\.vela\.ya?ml$`)

type velaManager struct{}

// New returns the velaci manager.
func New() manager.Manager { return velaManager{} }

func (velaManager) Name() string { return Name }

// Match reports whether path is `.vela.y(a)ml`, in any directory.
func (velaManager) Match(p string) bool { return fileRe.MatchString(path.Base(p)) }

// Extract returns the images of the steps, services, stage steps and secret origins of src.
func (velaManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, nil
	}
	root := doc.Content[0]
	x := docker.Images{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	for _, step := range yamlspan.Seq(yamlspan.Get(root, "steps")) {
		x.Add(TypeImage, yamlspan.Get(step, "image"))
	}
	for _, svc := range yamlspan.Seq(yamlspan.Get(root, "services")) {
		x.Add(TypeService, yamlspan.Get(svc, "image"))
	}
	for _, stage := range yamlspan.Items(yamlspan.Get(root, "stages")) {
		for _, step := range yamlspan.Seq(yamlspan.Get(stage, "steps")) {
			x.Add(TypeImage, yamlspan.Get(step, "image"))
		}
	}
	for _, secret := range yamlspan.Items(yamlspan.Get(root, "secrets")) {
		x.Add(TypeSecret, yamlspan.Get(yamlspan.Get(secret, "origin"), "image"))
	}
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager.
func (velaManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (velaManager) Rename(d dep.Dep, name string) (dep.Dep, error) { return docker.Rename(d, name) }
