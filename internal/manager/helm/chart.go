package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// File names of a chart directory.
const (
	chartFile = "Chart.yaml"
	lockFile  = "Chart.lock"
)

// chartDeps extracts Chart.yaml `dependencies[]` with RefSpan on `version`; the name and the
// repository of a chart from a repository URL are its NameParts.
func chartDeps(file string, src []byte) ([]dep.Dep, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 {
		return nil, nil
	}
	reqs := yamlspan.Get(doc.Content[0], "dependencies")
	if reqs == nil || reqs.Kind != yaml.SequenceNode {
		return nil, nil
	}
	lines := yamlspan.LineStarts(src)
	deps := make([]dep.Dep, 0, len(reqs.Content))
	for _, r := range reqs.Content {
		name := yamlspan.Str(yamlspan.Get(r, "name"))
		if name == nil || name.Value == "" {
			continue
		}
		d := newDep(file, TypeChart)
		d.Name, d.Line = name.Value, name.Line
		repoNode := yamlspan.Str(yamlspan.Get(r, "repository"))
		if d.Source, d.Skip = chartRepository(yamlspan.Value(repoNode)); d.Skip == "" {
			d.NameParts = ChartParts(src, lines, name, d.Name, false, repoNode)
			setVersion(&d, src, lines, yamlspan.Get(r, "version"))
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// chartRepository classifies a Chart.yaml `repository`: an http(s) or oci:// URL (trailing
// slashes dropped) is the dep's Source; local charts and aliases of the user's repository
// configuration are skipped.
func chartRepository(repo string) (source, skip string) {
	switch {
	case repo == "" || strings.HasPrefix(repo, "file://"):
		return "", "local chart"
	case isAlias(repo):
		return "", "repository alias"
	case strings.HasPrefix(repo, "oci://") || strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://"):
		return strings.TrimRight(repo, "/"), ""
	}
	return "", "unknown chart repository " + repo
}
