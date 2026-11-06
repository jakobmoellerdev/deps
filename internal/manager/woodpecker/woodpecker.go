// Package woodpecker extracts the container images of Woodpecker CI workflow files (`steps`,
// `services`, `clone` and the legacy `pipeline`).
package woodpecker

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "woodpecker"

// Dep types.
const (
	TypeImage   = "image"
	TypeService = "service"
)

// fileRe is Renovate's pattern: `.woodpecker.y(a)ml` or a file directly in `.woodpecker/`,
// at the repository root (where Woodpecker reads them).
var fileRe = regexp.MustCompile(`^\.woodpecker(?:/[^/]+)?\.ya?ml$`)

type woodpeckerManager struct{}

// New returns the woodpecker manager.
func New() manager.Manager { return woodpeckerManager{} }

func (woodpeckerManager) Name() string { return Name }

// Match reports whether path is `.woodpecker.y(a)ml` or `.woodpecker/<name>.y(a)ml`.
func (woodpeckerManager) Match(path string) bool {
	return strings.HasPrefix(path, ".woodpecker") && fileRe.MatchString(path)
}

// Extract returns the images of the steps (a list, or a mapping of names), services and
// clone steps of src.
func (woodpeckerManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := docker.Images{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	if len(doc.Content) != 1 {
		return nil, nil
	}
	root := doc.Content[0]
	for _, key := range []string{"clone", "steps", "pipeline", "services"} {
		depType := TypeImage
		if key == "services" {
			depType = TypeService
		}
		for _, step := range yamlspan.Items(yamlspan.Get(root, key)) {
			x.Add(depType, yamlspan.Get(step, "image"))
		}
	}
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager.
func (woodpeckerManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (woodpeckerManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	return docker.Rename(d, name)
}
