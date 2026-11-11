// Package tekton extracts the references of Tekton resources: bundles of task and pipeline
// references (the bundles resolver or the legacy `bundle` field), the images of steps,
// sidecars, step templates and StepActions, and the remote tasks and pipelines of
// Pipelines-as-Code annotations.
package tekton

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "tekton"

// Dep types (Renovate's).
const (
	TypeBundle     = "tekton-bundle"
	TypeStepImage  = "tekton-step-image"
	TypeAnnotation = "tekton-annotation"
)

const (
	skipVariable  = "variable reference"
	skipCommit    = "commit ref"
	skipNonVer    = "non-version ref"
	skipNotLocate = "cannot locate value"
)

// group is the API group suffix of every Tekton resource (tekton.dev, triggers.tekton.dev),
// which the Pipelines-as-Code annotations share: a file without it has nothing to extract.
var group = []byte("tekton.dev")

type tektonManager struct{}

// New returns the tekton manager.
func New() manager.Manager { return tektonManager{} }

func (tektonManager) Name() string { return Name }

// Match reports whether path is a YAML file: Tekton resources have no conventional file
// names, so Extract prefilters on the API group.
func (tektonManager) Match(path string) bool {
	return strings.HasSuffix(path, ".yaml") || strings.HasSuffix(path, ".yml")
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (tektonManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{".yaml", ".yml"}, Exts: []string{".yaml", ".yml"}}
}

// Extract returns the bundles, step images and annotation references of the Tekton resources
// in every document of src. Documents without "tekton.dev" are not parsed; a file that does
// not parse (a template) yields no deps.
func (tektonManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	if !bytes.Contains(src, group) {
		return nil, nil
	}
	x := extractor{Manager: Name, File: file, Src: src, Lines: yamlspan.LineStarts(src), Aliases: env.RegistryAliases}
	dec := yaml.NewDecoder(bytes.NewReader(yamlspan.KeepDocuments(src, func(doc []byte) bool { return bytes.Contains(doc, group) })))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			return x.Deps, nil
		}
		if err != nil {
			slog.Debug("skipping unparsable yaml", "file", file, "err", err)
			return nil, nil
		}
		if len(doc.Content) == 1 {
			x.resource(doc.Content[0])
		}
	}
}

// Edits rewrites image references like the docker manager, and the ref of an annotation URL
// in place.
func (tektonManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.DepType != TypeAnnotation {
		return docker.New().Edits(u)
	}
	if u.NewDigest != "" {
		return nil, errors.New("a Pipelines-as-Code annotation ref cannot be pinned to a digest")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Rename implements manager.Renamer for bundles and images, like the docker manager;
// annotation URLs cannot be renamed.
func (tektonManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.DepType == TypeAnnotation {
		return d, errors.New("only bundles and images can be replaced")
	}
	return docker.Rename(d, name)
}

type extractor struct {
	docker.Images
}

// resource extracts a Tekton resource (API group tekton.dev or a subgroup such as
// triggers.tekton.dev) and the items of a list. Like Renovate, the spec is read for every
// kind's shape at once: StepAction, Task, TaskRun, Pipeline, PipelineRun, TriggerTemplate.
func (x *extractor) resource(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		return
	}
	for _, item := range yamlspan.Seq(yamlspan.Get(n, "items")) {
		x.resource(item)
	}
	g, _, _ := strings.Cut(yamlspan.Value(yamlspan.Get(n, "apiVersion")), "/")
	if g != string(group) && !strings.HasSuffix(g, "."+string(group)) {
		return
	}
	x.annotations(yamlspan.Get(yamlspan.Get(n, "metadata"), "annotations"))
	x.spec(yamlspan.Get(n, "spec"))
}

// spec extracts the references of a resource's spec, or of a PipelineRun's inline pipelineSpec.
func (x *extractor) spec(s *yaml.Node) {
	if s == nil || s.Kind != yaml.MappingNode {
		return
	}
	// StepAction
	x.Add(TypeStepImage, yamlspan.Get(s, "image"))
	// TaskRun
	x.ref(yamlspan.Get(s, "taskRef"))
	x.steps(yamlspan.Get(s, "taskSpec"))
	// Task
	x.steps(s)
	// PipelineRun, with an inline Pipeline
	x.ref(yamlspan.Get(s, "pipelineRef"))
	x.spec(yamlspan.Get(s, "pipelineSpec"))
	// Pipeline
	for _, key := range []string{"tasks", "finally"} {
		for _, t := range yamlspan.Seq(yamlspan.Get(s, key)) {
			x.ref(yamlspan.Get(t, "taskRef"))
			x.steps(yamlspan.Get(t, "taskSpec"))
		}
	}
	// TriggerTemplate
	for _, r := range yamlspan.Seq(yamlspan.Get(s, "resourcetemplates")) {
		x.resource(r)
	}
}

// steps extracts the images of a task spec's steps, sidecars and stepTemplate.
func (x *extractor) steps(s *yaml.Node) {
	for _, key := range []string{"steps", "sidecars"} {
		for _, step := range yamlspan.Seq(yamlspan.Get(s, key)) {
			x.Add(TypeStepImage, yamlspan.Get(step, "image"))
		}
	}
	x.Add(TypeStepImage, yamlspan.Get(yamlspan.Get(s, "stepTemplate"), "image"))
}

// ref extracts the bundle of a taskRef or pipelineRef: the `bundle` param of the bundles
// resolver (else the deprecated `resource` list), else the legacy `bundle` field. References
// by name or through other resolvers are none.
func (x *extractor) ref(r *yaml.Node) {
	if r == nil || r.Kind != yaml.MappingNode {
		return
	}
	var v *yaml.Node
	if yamlspan.Value(yamlspan.Get(r, "resolver")) == "bundles" {
		if v = bundleParam(yamlspan.Get(r, "params")); v == nil {
			v = bundleParam(yamlspan.Get(r, "resource"))
		}
	}
	if v == nil {
		v = yamlspan.Get(r, "bundle")
	}
	x.Add(TypeBundle, v)
}

// bundleParam returns the value of the first `name: bundle` item of params.
func bundleParam(params *yaml.Node) *yaml.Node {
	for _, p := range yamlspan.Seq(params) {
		if yamlspan.Value(yamlspan.Get(p, "name")) == "bundle" {
			return yamlspan.Get(p, "value")
		}
	}
	return nil
}

// annotations extracts the remote URLs of the Pipelines-as-Code annotations
// `pipelinesascode.tekton.dev/task`, `task-<N>` and `pipeline`.
func (x *extractor) annotations(m *yaml.Node) {
	if m == nil || m.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if v := yamlspan.Str(m.Content[i+1]); v != nil && pacKey(m.Content[i].Value) {
			x.annotation(v)
		}
	}
}

// pacKey reports whether k is a Pipelines-as-Code remote task or pipeline annotation.
func pacKey(k string) bool {
	k, ok := strings.CutPrefix(k, "pipelinesascode.tekton.dev/")
	if !ok {
		return false
	}
	if k == "task" || k == "pipeline" {
		return true
	}
	n, ok := strings.CutPrefix(k, "task-")
	return ok && n != "" && strings.Trim(n, "0123456789") == ""
}

// annotation extracts the URLs of an annotation value: one entry, or a `[a, b]` list.
func (x *extractor) annotation(v *yaml.Node) {
	s := v.Value
	start, end := 0, len(s)
	if strings.HasSuffix(s, "]") {
		end--
	}
	if strings.HasPrefix(s, "[") && start < end {
		start++
	}
	off := -2 // not located yet
	for start <= end {
		item := s[start:end]
		if i := strings.IndexByte(item, ','); i >= 0 {
			item = item[:i]
		}
		next := start + len(item) + 1
		at := start + len(item) - len(strings.TrimLeft(item, " \t"))
		if r, ok := parseURL(strings.TrimSpace(item)); ok {
			if off == -2 {
				off = yamlspan.Locate(x.Src, x.Lines, v)
			}
			x.Deps = append(x.Deps, x.urlDep(v, r, off, at))
		}
		start = next
	}
}

// remote is a URL of a GitHub repository ref: ref at byte refAt of the URL.
type remote struct {
	datasource, name, source string
	ref                      string
	refAt                    int
}

// parseURL recognizes Renovate's annotation URLs on github.com: release assets
// `https://github.com/<owner>/<repo>/releases/download/<tag>/<asset>` (github-releases) and
// raw files `https://github.com/<owner>/<repo>/raw/<ref>/<path>` and
// `https://raw.githubusercontent.com/<owner>/<repo>/<ref>/<path>` (git-tags). The scheme is
// optional, as in Renovate; `refs/heads/<ref>` and `refs/tags/<ref>` are read as `<ref>`.
// Other values (Artifact Hub names, repository paths, other hosts) are none.
func parseURL(u string) (remote, bool) {
	rest := u
	for _, scheme := range []string{"https://", "http://"} {
		rest = strings.TrimPrefix(rest, scheme)
	}
	at := len(u) - len(rest)
	host, path, ok := strings.Cut(rest, "/")
	if !ok || strings.ContainsAny(path, "?# \t") {
		return remote{}, false
	}
	at += len(host) + 1
	parts := strings.SplitN(path, "/", 3)
	if len(parts) < 3 || parts[0] == "" || parts[1] == "" {
		return remote{}, false
	}
	repo := parts[0] + "/" + parts[1]
	at += len(repo) + 1
	tail := parts[2]
	r := remote{datasource: "git-tags", name: "github.com/" + repo, source: "https://github.com/" + repo}
	switch {
	case host == "raw.githubusercontent.com":
	case host != "github.com":
		return remote{}, false
	case strings.HasPrefix(tail, "raw/"):
		tail, at = tail[len("raw/"):], at+len("raw/")
	case strings.HasPrefix(tail, "releases/download/"):
		tail, at = tail[len("releases/download/"):], at+len("releases/download/")
		i := strings.LastIndexByte(tail, '/')
		if i <= 0 || i == len(tail)-1 {
			return remote{}, false
		}
		r.datasource, r.name, r.ref, r.refAt = "github-releases", repo, tail[:i], at
		return r, true
	default:
		return remote{}, false
	}
	if t, ok := strings.CutPrefix(tail, "refs/"); ok {
		kind, t, _ := strings.Cut(t, "/")
		if kind != "heads" && kind != "tags" {
			return remote{}, false
		}
		tail, at = t, at+len("refs/")+len(kind)+1
	}
	ref, file, ok := strings.Cut(tail, "/")
	if !ok || ref == "" || file == "" {
		return remote{}, false
	}
	r.ref, r.refAt = ref, at
	return r, true
}

// urlDep returns the dep of URL r, which starts at byte at of scalar v's value; off is the
// value's offset in the source (-1 = not located).
func (x *extractor) urlDep(v *yaml.Node, r remote, off, at int) dep.Dep {
	d := dep.Dep{Manager: Name, Datasource: r.datasource, Versioning: "tag", DepType: TypeAnnotation, File: x.File, Line: v.Line, Name: r.name, Source: r.source}
	if _, ok := versioning.ParseTag(r.ref); ok {
		d.Version = r.ref
	}
	switch {
	case strings.Contains(r.ref, "$") || strings.Contains(r.ref, "{{"):
		d.Skip = skipVariable
	case manager.IsHex(r.ref, 40):
		d.Skip = skipCommit
	case d.Version == "":
		d.Skip = skipNonVer
	case off < 0:
		d.Skip = skipNotLocate
	default:
		start := off + at + r.refAt
		d.RefSpan = dep.Span{Start: start, End: start + len(r.ref)}
	}
	return d
}
