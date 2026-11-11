// Package cloudbuild extracts the builder images of Google Cloud Build configuration files
// (the `name` of every step of `cloudbuild.y(a)ml`).
package cloudbuild

import (
	"path"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "cloudbuild"

// TypeImage is the dep type of a step's builder image.
const TypeImage = "image"

type cloudbuildManager struct{}

// New returns the cloudbuild manager.
func New() manager.Manager { return cloudbuildManager{} }

func (cloudbuildManager) Name() string { return Name }

// Match reports whether path is `cloudbuild.y(a)ml`, in any directory: Renovate's
// `(^|/)cloudbuild\.ya?ml` anchored at the end, so templates such as `cloudbuild.yaml.hbs`
// are not read.
func (cloudbuildManager) Match(p string) bool {
	b := path.Base(p)
	return b == "cloudbuild.yaml" || b == "cloudbuild.yml"
}

// Extract returns the builder image (`name`) of every step of src.
func (cloudbuildManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	if len(doc.Content) != 1 {
		return nil, nil
	}
	x := docker.Images{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	for _, step := range yamlspan.Seq(yamlspan.Get(doc.Content[0], "steps")) {
		x.Add(TypeImage, yamlspan.Get(step, "name"))
	}
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager.
func (cloudbuildManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (cloudbuildManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	return docker.Rename(d, name)
}
