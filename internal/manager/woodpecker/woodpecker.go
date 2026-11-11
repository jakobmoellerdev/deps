// Package woodpecker extracts the container images of Woodpecker CI workflow files (`steps`,
// `services`, `clone` and the legacy `pipeline`), and of Crow CI's, a Woodpecker fork with the
// same workflow format (the crow manager).
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

// Manager names (Renovate's).
const (
	Name     = "woodpecker"
	CrowName = "crow"
)

// Dep types.
const (
	TypeImage   = "image"
	TypeService = "service"
)

// The file patterns are Renovate's: `.<ci>.y(a)ml` or a file directly in `.<ci>/`, at the
// repository root (where Woodpecker and Crow read them).
var (
	fileRe     = regexp.MustCompile(`^\.woodpecker(?:/[^/]+)?\.ya?ml$`)
	crowFileRe = regexp.MustCompile(`^\.crow(?:/[^/]+)?\.ya?ml$`)
)

type woodpeckerManager struct {
	name, prefix string // prefix: the literal start of fileRe's matches
	fileRe       *regexp.Regexp
}

// New returns the woodpecker manager.
func New() manager.Manager { return woodpeckerManager{Name, ".woodpecker", fileRe} }

// NewCrow returns the crow manager.
func NewCrow() manager.Manager { return woodpeckerManager{CrowName, ".crow", crowFileRe} }

func (m woodpeckerManager) Name() string { return m.name }

// Match reports whether path is `.<ci>.y(a)ml` or `.<ci>/<name>.y(a)ml`.
func (m woodpeckerManager) Match(path string) bool {
	return strings.HasPrefix(path, m.prefix) && m.fileRe.MatchString(path)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (m woodpeckerManager) MatchHints() manager.Hints {
	return manager.Hints{
		Names: []string{m.prefix + ".yaml", m.prefix + ".yml", ".yaml", ".yml"},
		Exts:  []string{".yaml", ".yml"},
	}
}

// Extract returns the images of the steps (a list, or a mapping of names), services and
// clone steps of src.
func (m woodpeckerManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := docker.Images{Manager: m.name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
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
