// Package buildkite extracts the plugins (`<plugin>#<version>`) and container images of
// Buildkite pipeline files.
package buildkite

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
const Name = "buildkite"

// Dep types.
const (
	TypePlugin = "plugin"
	TypeImage  = "image"
)

var (
	// fileRe is Renovate's patterns `buildkite\.ya?ml` and `\.buildkite/.+\.ya?ml$`, both
	// anchored at the end.
	fileRe = regexp.MustCompile(`(?:buildkite\.ya?ml|(?:^|/)\.buildkite/.+\.ya?ml)$`)
	// pluginRe is a plugin reference with a version, as Renovate matches it:
	// `<location>#<version>`.
	pluginRe = regexp.MustCompile(`^([^#\s'"]+)#([^:\s'"]+)$`)
	// gitURLRe is a plugin location given as a git URL: the host and the repository path.
	gitURLRe = regexp.MustCompile(`^(?:ssh://git@|https://)([^/]+)/(.+)$`)
)

type buildkiteManager struct{}

// New returns the buildkite manager.
func New() manager.Manager { return buildkiteManager{} }

func (buildkiteManager) Name() string { return Name }

// Match reports whether path ends in `buildkite.y(a)ml` (`pipeline.buildkite.yml`) or is a
// YAML file below a `.buildkite` directory.
func (buildkiteManager) Match(path string) bool {
	return strings.Contains(path, "buildkite") && fileRe.MatchString(path)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (buildkiteManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yml", ".yaml"}}
}

// Extract returns the plugins and images of src.
func (buildkiteManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := extractor{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	x.walk(doc)
	return x.Deps, nil
}

// Edits rewrites image references like the docker manager, and plugin versions in place.
func (buildkiteManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == "docker" {
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("digest pinning of Buildkite plugins is not supported")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Rename implements manager.Renamer for image references, like the docker manager; plugins
// cannot be renamed.
func (buildkiteManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.Datasource != "docker" {
		return d, errors.New("only images can be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	docker.Images
}

// walk extracts every plugin and image below n. A plugin is a mapping key `<plugin>#<version>`
// (a plugin with options, in `plugins` or anchored anywhere, as Buildkite recommends for
// shared plugin configurations) or such a scalar in a `plugins` list. An image is the value of
// an `image` key: of the pipeline or a step, of the docker plugin, of agent-stack-k8s pod specs
// and sidecars. `agents` is not walked: its `image` is a VM image of the agent (Elastic CI
// stack, GCP image families), not a container. Aliases are not followed; the anchored node is
// walked where it is written.
func (x *extractor) walk(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		for _, c := range n.Content {
			x.walk(c)
		}
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		k, v := n.Content[i], n.Content[i+1]
		switch {
		case k.Kind != yaml.ScalarNode:
		case k.Value == "agents":
			continue
		case k.Value == "image":
			x.Add(TypeImage, v)
			continue
		case k.Value == "plugins":
			for _, p := range yamlspan.Seq(v) {
				if p.Kind == yaml.ScalarNode {
					x.plugin(p)
				}
			}
		case strings.Contains(k.Value, "#"):
			x.plugin(k)
		}
		x.walk(v)
	}
}

// plugin handles the reference in scalar v if it is `<location>#<version>`: a git URL
// (`https://<host>/<path>`, `ssh://git@<host>/<path>`) is looked up with git-tags of that
// repository; `<name>` is github.com/buildkite-plugins/<name>-buildkite-plugin and
// `<org>/<name>` github.com/<org>/<name>-buildkite-plugin, as the Buildkite agent resolves them.
func (x *extractor) plugin(v *yaml.Node) {
	m := pluginRe.FindStringSubmatch(v.Value)
	if m == nil {
		return
	}
	loc, version := m[1], m[2]
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: TypePlugin, File: x.File, Line: v.Line, Name: loc}
	off := yamlspan.Locate(x.Src, x.Lines, v)
	if g := gitURLRe.FindStringSubmatch(loc); g != nil {
		d.Name = strings.TrimSuffix(g[2], ".git")
		d.Source = "https://" + g[1] + "/" + d.Name
	} else if segs := strings.Count(loc, "/"); segs <= 1 {
		repo := loc
		if segs == 0 {
			repo = "buildkite-plugins/" + loc
		}
		if !strings.HasSuffix(repo, "-buildkite-plugin") {
			repo += "-buildkite-plugin"
		}
		d.Source = "https://github.com/" + repo
	}
	switch {
	case strings.Contains(v.Value, "$"):
		d.Source, d.Skip = "", "variable reference"
	case d.Source == "":
		d.Skip = "invalid plugin"
	case off < 0:
		d.Source, d.Skip = "", "cannot locate value"
	default:
		d.Version = version
		start := off + len(loc) + 1
		d.RefSpan = dep.Span{Start: start, End: start + len(version)}
	}
	x.Deps = append(x.Deps, d)
}
