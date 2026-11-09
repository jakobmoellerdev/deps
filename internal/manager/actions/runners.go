package actions

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/githubrunners"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// DepTypeRunner is the DepType of a GitHub-hosted runner label in a job's `runs-on:`.
const DepTypeRunner = "github-runner"

// forgeWorkflow reports whether path is a Gitea or Forgejo workflow or action file: their
// runners advertise labels of their own configuration, which need not follow GitHub's images.
func forgeWorkflow(path string) bool {
	path = "/" + path
	return strings.Contains(path, "/.gitea/") || strings.Contains(path, "/.forgejo/")
}

// runners extracts the GitHub-hosted runner labels (`ubuntu-24.04`, `macos-15`) of every job's
// `runs-on:`: a label, a list of labels, or a `{group, labels}` mapping, whose labels may be
// `${{ matrix.<key> }}` and then stand for the job's matrix values of key (its list and
// `include` entries). A label is `<name>-<version>` with a runner name and version from the
// github-runners table (or `latest`, skipped); others (`self-hosted`, larger-runner labels,
// other expressions) are not deps.
func (x *extractor) runners(root *yaml.Node) {
	if forgeWorkflow(x.file) || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return
	}
	jobs := yamlspan.Get(root.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	// A matrix value referenced by several labels is one dep.
	seen := map[*yaml.Node]bool{}
	for _, job := range yamlspan.Items(jobs) {
		runsOn := yamlspan.Get(job, "runs-on")
		if runsOn != nil && runsOn.Kind == yaml.MappingNode {
			runsOn = yamlspan.Get(runsOn, "labels")
		}
		labels := yamlspan.Seq(runsOn)
		if l := yamlspan.Str(runsOn); l != nil {
			labels = []*yaml.Node{l}
		}
		for _, l := range labels {
			if l.Kind != yaml.ScalarNode {
				continue
			}
			key, ok := matrixKey(l.Value)
			if !ok {
				x.runner(l)
				continue
			}
			for _, v := range matrixValues(job, key) {
				if !seen[v] {
					seen[v] = true
					x.runner(v)
				}
			}
		}
	}
}

// matrixKey returns key when label is exactly the expression `${{ matrix.<key> }}`.
func matrixKey(label string) (string, bool) {
	expr, ok := strings.CutPrefix(label, "${{")
	if !ok {
		return "", false
	}
	if expr, ok = strings.CutSuffix(expr, "}}"); !ok {
		return "", false
	}
	key, ok := strings.CutPrefix(strings.TrimSpace(expr), "matrix.")
	if !ok || key == "" || strings.ContainsFunc(key, func(r rune) bool {
		return r != '_' && r != '-' && (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9')
	}) {
		return "", false
	}
	return key, true
}

// matrixValues returns the scalar values of key in job's `strategy.matrix`: the items of its
// list (or its single value) and the key of every `include` entry.
func matrixValues(job *yaml.Node, key string) []*yaml.Node {
	matrix := yamlspan.Get(yamlspan.Get(job, "strategy"), "matrix")
	var vs []*yaml.Node
	add := func(n *yaml.Node) {
		if n := yamlspan.Str(n); n != nil {
			vs = append(vs, n)
		}
	}
	values := yamlspan.Get(matrix, key)
	add(values)
	for _, v := range yamlspan.Seq(values) {
		add(v)
	}
	for _, inc := range yamlspan.Seq(yamlspan.Get(matrix, "include")) {
		add(yamlspan.Get(inc, key))
	}
	return vs
}

// runner adds runner label v when it names a GitHub-hosted runner image.
func (x *extractor) runner(v *yaml.Node) {
	name, version, ok := strings.Cut(v.Value, "-")
	if !ok || !githubrunners.Known(name, version) {
		return
	}
	d := dep.Dep{
		Manager: Name, Datasource: githubrunners.Name, Versioning: "docker", DepType: DepTypeRunner,
		File: x.file, Line: v.Line, Name: name, Source: githubrunners.SourceURL, Version: version,
	}
	switch off := yamlspan.Locate(x.src, x.lines, v); {
	case version == "latest":
		d.Skip = "non-version value"
	case off < 0:
		d.Skip = "cannot locate value"
	default:
		s := off + len(name) + 1
		d.RefSpan = dep.Span{Start: s, End: s + len(version)}
	}
	x.deps = append(x.deps, d)
}
