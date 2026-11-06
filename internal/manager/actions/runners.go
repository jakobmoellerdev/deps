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
// `runs-on:`, a label or a list of labels, as Renovate does. A label is `<name>-<version>` with
// a runner name and version from the github-runners table (or `latest`, skipped); others
// (`self-hosted`, larger-runner labels, expressions) are not deps.
func (x *extractor) runners(root *yaml.Node) {
	if forgeWorkflow(x.file) || root.Kind != yaml.DocumentNode || len(root.Content) == 0 {
		return
	}
	jobs := yamlspan.Get(root.Content[0], "jobs")
	if jobs == nil || jobs.Kind != yaml.MappingNode {
		return
	}
	for _, job := range yamlspan.Items(jobs) {
		runsOn := yamlspan.Get(job, "runs-on")
		if l := yamlspan.Str(runsOn); l != nil {
			x.runner(l)
			continue
		}
		for _, l := range yamlspan.Seq(runsOn) {
			if l.Kind == yaml.ScalarNode {
				x.runner(l)
			}
		}
	}
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
