// Package bitbucketpipelines extracts container images (`image`, service definitions) and
// pipes (`pipe:`) from Bitbucket Pipelines configuration files.
package bitbucketpipelines

import (
	"errors"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/azurepipelines"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "bitbucket-pipelines"

// Dep types.
const (
	TypeImage   = "image"
	TypeService = "service"
	TypePipe    = "pipe"
)

// pipeHost is where Bitbucket pipes (`<account>/<repo>:<version>`) live.
const pipeHost = "https://bitbucket.org/"

// pipeRe is a Bitbucket pipe reference: a repository slug and an optional version.
var pipeRe = regexp.MustCompile(`^([\w.-]+/[\w.-]+)(?::(.*))?$`)

type bitbucketManager struct{}

// New returns the bitbucket-pipelines manager.
func New() manager.Manager { return bitbucketManager{} }

func (bitbucketManager) Name() string { return Name }

// Match reports whether path is a `*-pipelines.yml` file (Renovate's pattern: also shared
// pipeline configurations), except Azure Pipelines files (`azure-pipelines.yml`), which the
// azure-pipelines manager reads.
func (bitbucketManager) Match(p string) bool {
	return strings.HasSuffix(path.Base(p), "-pipelines.yml") && !azurepipelines.IsPipelineFile(p)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (bitbucketManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yml"}}
}

// Extract returns the images and pipes of src.
func (bitbucketManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := extractor{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	x.walk(doc)
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager, and pipe versions in place.
func (bitbucketManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == "docker" {
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("digest pinning of Bitbucket pipes is not supported")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Rename implements manager.Renamer for image references, like the docker manager; pipes
// cannot be renamed.
func (bitbucketManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.Datasource != "docker" {
		return d, errors.New("only images can be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	docker.Images
}

// definitions extracts the images of `services` (DepType service) and walks the rest
// (anchored `steps`, `caches`).
func (x *extractor) definitions(v *yaml.Node) {
	if v.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(v.Content); i += 2 {
		k, val := v.Content[i].Value, v.Content[i+1]
		if k != "services" || val.Kind != yaml.MappingNode {
			x.walk(val)
			continue
		}
		for j := 1; j < len(val.Content); j += 2 {
			x.image(TypeService, yamlspan.Get(val.Content[j], "image"))
		}
	}
}

// walk extracts every `image` and `pipe` below n: the global image, and the steps nested in
// `pipelines.<kind>` lists, `parallel`, `stage` and anchored `definitions.steps`. Aliases are
// not followed; the anchored node is walked where it is written.
func (x *extractor) walk(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			x.walk(c)
		}
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		switch k, v := n.Content[i].Value, n.Content[i+1]; k {
		case "image":
			x.image(TypeImage, v)
		case "pipe":
			x.pipe(v)
		case "definitions":
			x.definitions(v)
		default:
			x.walk(v)
		}
	}
}

// image handles `image: ref` and `image: {name: ref, username, password, aws}`.
func (x *extractor) image(depType string, v *yaml.Node) {
	if v != nil && v.Kind == yaml.MappingNode {
		v = yamlspan.Get(v, "name")
	}
	x.Add(depType, v)
}

// pipe handles `pipe: <account>/<repo>:<version>` (a Bitbucket repository, versioned by its
// tags) and `pipe: docker://<image>`.
func (x *extractor) pipe(v *yaml.Node) {
	if v == nil || v.Kind != yaml.ScalarNode || v.Value == "" {
		return
	}
	if strings.HasPrefix(v.Value, "docker://") {
		x.AddPrefixed(TypePipe, v, "docker://")
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: TypePipe, File: x.File, Line: v.Line, Name: v.Value}
	m := pipeRe.FindStringSubmatch(v.Value)
	off := yamlspan.Locate(x.Src, x.Lines, v)
	switch {
	case strings.Contains(v.Value, "$"):
		d.Skip = "variable reference"
	case m == nil:
		d.Skip = "invalid pipe"
	case m[2] == "":
		d.Name, d.Skip = m[1], "no version"
	case off < 0:
		d.Name, d.Skip = m[1], "cannot locate value"
	default:
		d.Name, d.Source, d.Version = m[1], pipeHost+m[1], m[2]
		start := off + len(m[1]) + 1
		d.RefSpan = dep.Span{Start: start, End: start + len(m[2])}
	}
	x.Deps = append(x.Deps, d)
}
