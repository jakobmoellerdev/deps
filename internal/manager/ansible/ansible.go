// Package ansible extracts the dependencies of Ansible content: the container images of task
// files (manager ansible) and the roles and collections of requirements files and collection
// metadata (manager ansible-galaxy).
package ansible

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the ansible manager name (Renovate's).
const Name = "ansible"

// DepTypeImage is the dep type of task images.
const DepTypeImage = "image"

// skipTemplate is the skip reason of a Jinja2-templated image.
const skipTemplate = "template"

type tasksManager struct{}

// New returns the ansible manager.
func New() manager.Manager { return tasksManager{} }

func (tasksManager) Name() string { return Name }

// Match reports whether path is a YAML file directly in a `tasks` directory (a role's
// `tasks/main.yml`, a playbook's `tasks/*.yaml`).
func (tasksManager) Match(path string) bool {
	dir, file, ok := strings.CutLast(path, "/")
	if !ok || !(dir == "tasks" || strings.HasSuffix(dir, "/tasks")) {
		return false
	}
	return strings.HasSuffix(file, ".yml") || strings.HasSuffix(file, ".yaml")
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (tasksManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yml", ".yaml"}}
}

// Extract returns the scalar `image` values of every mapping in the task file (the `image`
// option of docker_container, podman_container, kubernetes and other modules, at any depth).
// Files without "image:" are not parsed.
func (tasksManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	if !bytes.Contains(src, []byte("image")) {
		return nil, nil
	}
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := docker.Images{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	walk(doc, func(v *yaml.Node) {
		if strings.Contains(v.Value, "{{") {
			x.Deps = append(x.Deps, dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: DepTypeImage, File: file, Line: v.Line, Name: v.Value, Skip: skipTemplate})
			return
		}
		x.Add(DepTypeImage, v)
	})
	return x.Deps, nil
}

// walk calls image with the scalar value of every `image` key under n.
func walk(n *yaml.Node, image func(*yaml.Node)) {
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 {
			if v := n.Content[i+1]; n.Content[i].Value == "image" && v.Kind == yaml.ScalarNode && v.Value != "" {
				image(v)
				continue
			}
			walk(n.Content[i+1], image)
		}
		return
	}
	for _, c := range n.Content {
		walk(c, image)
	}
}

// Edits rewrites image references like the docker manager.
func (tasksManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (tasksManager) Rename(d dep.Dep, name string) (dep.Dep, error) { return docker.Rename(d, name) }
