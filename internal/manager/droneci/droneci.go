// Package droneci extracts the container images of Drone CI pipelines (`steps` and
// `services` of every document of `.drone.yml`).
package droneci

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

// Name is the manager name (Renovate's).
const Name = "droneci"

// Dep types.
const (
	TypeImage   = "image"
	TypeService = "service"
)

type droneManager struct{}

// New returns the droneci manager.
func New() manager.Manager { return droneManager{} }

func (droneManager) Name() string { return Name }

// Match reports whether path is `.drone.yml`, in any directory.
func (droneManager) Match(path string) bool {
	return path == ".drone.yml" || strings.HasSuffix(path, "/.drone.yml")
}

// Extract returns the step and service images of every document of src (pipelines, secrets
// and signatures are separate documents), from the documents of env.YAML, the file's shared
// parse. Documents without "image" are not parsed.
func (droneManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	x := docker.Images{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	err := env.YAML.Documents(src, func(doc []byte) bool {
		return bytes.Contains(doc, []byte("image"))
	}, func(doc *yaml.Node) {
		if len(doc.Content) != 1 {
			return
		}
		// steps (Drone 1.0+) and pipeline (0.8) hold steps, as a list or a mapping of names.
		for _, key := range []string{"steps", "pipeline", "services"} {
			depType := TypeImage
			if key == "services" {
				depType = TypeService
			}
			for _, step := range yamlspan.Items(yamlspan.Get(doc.Content[0], key)) {
				x.Add(depType, yamlspan.Get(step, "image"))
			}
		}
	})
	if err != nil {
		return nil, err
	}
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager.
func (droneManager) Edits(u dep.Update) ([]patch.Edit, error) { return docker.New().Edits(u) }

// Rename implements manager.Renamer like the docker manager.
func (droneManager) Rename(d dep.Dep, name string) (dep.Dep, error) { return docker.Rename(d, name) }
