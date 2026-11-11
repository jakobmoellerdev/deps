// Package circleci extracts orbs (`orbs:`) and the container images of executors, jobs and
// aliases (`docker: [{image}]`) from CircleCI configuration files.
package circleci

import (
	"errors"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "circleci"

// Dep types.
const (
	TypeOrb   = "orb"
	TypeImage = "image"
)

// Datasource is the orb datasource name.
const Datasource = "orb"

// fileRe is Renovate's pattern: any YAML file below a `.circleci` directory.
var fileRe = regexp.MustCompile(`(^|/)\.circleci/.+\.ya?ml$`)

type circleManager struct{}

// New returns the circleci manager.
func New() manager.Manager { return circleManager{} }

func (circleManager) Name() string { return Name }

// Match reports whether path is a YAML file below `.circleci/`.
func (circleManager) Match(path string) bool {
	return strings.Contains(path, ".circleci/") && fileRe.MatchString(path)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (circleManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yml", ".yaml"}}
}

// Extract returns the orbs and images of src; inline orb definitions (`orbs: {name: {…}}`)
// are scanned like the file.
func (circleManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := extractor{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	if len(doc.Content) == 1 {
		root := doc.Content[0]
		x.definition(root)
		for _, a := range yamlspan.Seq(yamlspan.Get(root, "aliases")) {
			x.image(yamlspan.Get(a, "image"))
		}
		// Other top-level keys (`defaults: &defaults`, `x-*`) hold anchored blocks merged
		// into jobs with `<<`; their `docker` images are extracted where they are written.
		for i := 0; root.Kind == yaml.MappingNode && i+1 < len(root.Content); i += 2 {
			switch root.Content[i].Value {
			case "orbs", "executors", "jobs", "aliases", "parameters":
			default:
				x.walk(root.Content[i+1])
			}
		}
	}
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager, and orb versions in place.
func (circleManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == "docker" {
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("digest pinning of CircleCI orbs is not supported")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Rename implements manager.Renamer for image references, like the docker manager; orbs
// cannot be renamed.
func (circleManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.Datasource != "docker" {
		return d, errors.New("only images can be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	docker.Images
}

// definition extracts the orbs of m and the images of its executors and jobs.
func (x *extractor) definition(m *yaml.Node) {
	orbs := yamlspan.Get(m, "orbs")
	if orbs != nil && orbs.Kind == yaml.MappingNode {
		for i := 1; i < len(orbs.Content); i += 2 {
			if v := orbs.Content[i]; v.Kind == yaml.MappingNode {
				x.definition(v)
			} else {
				x.orb(v)
			}
		}
	}
	for _, key := range []string{"executors", "jobs"} {
		for _, env := range yamlspan.Items(yamlspan.Get(m, key)) {
			for _, c := range yamlspan.Seq(yamlspan.Get(env, "docker")) {
				x.image(yamlspan.Get(c, "image"))
			}
		}
	}
}

// walk extracts the images of every `docker: [{image}]` list below n; `parameters` are not
// entered and aliases are not followed.
func (x *extractor) walk(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			x.walk(c)
		}
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch k, v := n.Content[i].Value, n.Content[i+1]; k {
		case "docker":
			for _, c := range yamlspan.Seq(v) {
				x.image(yamlspan.Get(c, "image"))
			}
		case "parameters":
		default:
			x.walk(v)
		}
	}
}

// image adds the image reference in v; one with a `<< parameters.x >>` reference is skipped.
func (x *extractor) image(v *yaml.Node) {
	if s := yamlspan.Str(v); s != nil && strings.Contains(s.Value, "<<") {
		x.Deps = append(x.Deps, dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeImage,
			File: x.File, Line: s.Line, Name: s.Value, Skip: "parameter reference"})
		return
	}
	x.Add(TypeImage, v)
}

// orb handles `<namespace>/<name>@<version>`; the version is a full or partial (`5`, `5.1`)
// version, `volatile` (the latest, not updated) or a `dev:` label.
func (x *extractor) orb(v *yaml.Node) {
	if v.Kind != yaml.ScalarNode || v.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: "tag", DepType: TypeOrb, File: x.File, Line: v.Line, Name: v.Value}
	name, ver, ok := strings.Cut(v.Value, "@")
	off := yamlspan.Locate(x.Src, x.Lines, v)
	switch {
	case strings.Contains(v.Value, "<<") || strings.Contains(v.Value, "$"):
		d.Skip = "parameter reference"
	case !ok || ver == "":
		d.Name, d.Skip = name, "no version"
	case !strings.Contains(name, "/"):
		d.Name, d.Skip = name, "invalid orb"
	case off < 0:
		d.Name, d.Skip = name, "cannot locate value"
	default:
		d.Name, d.Version = name, ver
		start := off + len(name) + 1
		d.RefSpan = dep.Span{Start: start, End: start + len(ver)}
	}
	x.Deps = append(x.Deps, d)
}
