// Package azurepipelines extracts container resources, GitHub repository resources pinned to a
// tag, and task versions (`task: <name>@<version>`) from Azure Pipelines YAML files.
package azurepipelines

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
const Name = "azure-pipelines"

// Dep types. Tasks are ignored by default (see config.IgnoredByDefault).
const (
	TypeContainer  = "container"
	TypeRepository = "repository"
	TypeTask       = "task"
)

// TasksDatasource is the azure-pipelines-tasks datasource name.
const TasksDatasource = "azure-pipelines-tasks"

// tagRef prefixes a repository resource ref that names a tag.
const tagRef = "refs/tags/"

// Renovate's patterns: YAML files below `.azuredevops/`, and paths matching
// `azure.*pipelines?.*\.ya?ml$` (`azure-pipelines.yml`, `.azure-pipelines/build.yaml`).
var (
	devopsRe = regexp.MustCompile(`(^|/)\.azuredevops/.+\.ya?ml$`)
	fileRe   = regexp.MustCompile(`azure.*pipelines?.*\.ya?ml$`)
)

type azureManager struct{}

// New returns the azure-pipelines manager.
func New() manager.Manager { return azureManager{} }

func (azureManager) Name() string { return Name }

// Match reports whether path is an Azure Pipelines file (IsPipelineFile).
func (azureManager) Match(path string) bool { return IsPipelineFile(path) }

// IsPipelineFile reports whether the slash path is a YAML file below `.azuredevops/` or one
// whose path matches `azure.*pipelines?.*\.ya?ml$`.
func IsPipelineFile(path string) bool {
	return strings.Contains(path, ".azuredevops/") && devopsRe.MatchString(path) ||
		strings.Contains(path, "azure") && strings.Contains(path, "pipeline") && fileRe.MatchString(path)
}

// Extract returns the container and repository resources of src and every task step,
// wherever it nests (stages, jobs, deployment strategies, templates, `extends` parameters,
// `${{ if }}` blocks).
func (azureManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	doc, err := env.YAML.Document(src)
	if err != nil {
		return nil, err
	}
	x := extractor{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src)}
	if len(doc.Content) != 1 {
		return nil, nil
	}
	root := doc.Content[0]
	resources := yamlspan.Get(root, "resources")
	for _, r := range yamlspan.Seq(yamlspan.Get(resources, "repositories")) {
		x.repository(r)
	}
	for _, c := range yamlspan.Seq(yamlspan.Get(resources, "containers")) {
		x.Add(TypeContainer, yamlspan.Get(c, "image"))
	}
	x.walk(root)
	return x.Deps, nil
}

// Edits rewrites container images like the docker manager, and repository refs and task
// versions in place.
func (azureManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Datasource == "docker" {
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("digest pinning of Azure Pipelines repositories and tasks is not supported")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Rename implements manager.Renamer for container images, like the docker manager;
// repositories and tasks cannot be renamed.
func (azureManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.Datasource != "docker" {
		return d, errors.New("only container images can be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	docker.Images
}

// repository handles `{repository, type, name, ref}`: a GitHub repository whose ref is
// `refs/tags/<tag>` is a git-tags dep; Azure Repos (`git`) and Bitbucket repositories are
// skipped, as in Renovate.
func (x *extractor) repository(r *yaml.Node) {
	name, ref := yamlspan.Str(yamlspan.Get(r, "name")), yamlspan.Str(yamlspan.Get(r, "ref"))
	if name == nil || name.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: TypeRepository, File: x.File, Line: name.Line, Name: name.Value}
	switch typ := yamlspan.Value(yamlspan.Get(r, "type")); {
	case strings.Contains(name.Value, "$") || ref != nil && strings.Contains(ref.Value, "$"):
		d.Skip = "variable reference"
	case typ != "github":
		d.Skip = "unsupported repository type"
	case ref == nil || ref.Value == "":
		d.Skip = "no ref"
	case !strings.HasPrefix(ref.Value, tagRef) || ref.Value == tagRef:
		d.Line, d.Skip = ref.Line, "non-tag ref"
	default:
		d.Line, d.Source, d.Version = ref.Line, "https://github.com/"+name.Value, ref.Value[len(tagRef):]
		off := yamlspan.Locate(x.Src, x.Lines, ref)
		if off < 0 {
			d.Version, d.Skip = "", "cannot locate value"
			break
		}
		start := off + len(tagRef)
		d.RefSpan = dep.Span{Start: start, End: start + len(d.Version)}
	}
	x.Deps = append(x.Deps, d)
}

// walk extracts the `task` of every step (a sequence item) below n. Aliases are not followed.
func (x *extractor) walk(n *yaml.Node) {
	for _, c := range n.Content {
		if n.Kind == yaml.SequenceNode {
			x.task(yamlspan.Str(yamlspan.Get(c, "task")))
		}
		x.walk(c)
	}
}

// task handles `<name>@<version>`: a built-in task (`Bash`), a task of an extension
// (`<publisher>.<extension>.<contribution>.<task>`) or a task id, and a major (`3`) or full
// version.
func (x *extractor) task(v *yaml.Node) {
	if v == nil || v.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: TasksDatasource, Versioning: "tag", DepType: TypeTask, File: x.File, Line: v.Line, Name: v.Value}
	name, ver, ok := strings.Cut(v.Value, "@")
	off := yamlspan.Locate(x.Src, x.Lines, v)
	switch {
	case strings.Contains(v.Value, "$"):
		d.Skip = "variable reference"
	case !ok || ver == "" || name == "":
		d.Name, d.Skip = name, "no version"
	case off < 0:
		d.Name, d.Skip = name, "cannot locate value"
	default:
		d.Name, d.Version = name, ver
		start := off + len(name) + 1
		d.RefSpan = dep.Span{Start: start, End: start + len(ver)}
	}
	x.Deps = append(x.Deps, d)
}
