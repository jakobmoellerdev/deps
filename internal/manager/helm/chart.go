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
	// Helm v2 (apiVersion v1) charts declare their dependencies in requirements.yaml, locked
	// by requirements.lock.
	requirementsFile = "requirements.yaml"
	requirementsLock = "requirements.lock"
)

// chartDeps extracts Chart.yaml `dependencies[]` with RefSpan on `version`; the name and the
// repository of a chart from a repository URL are its NameParts. A repository alias is
// resolved through aliases (config registryAliases), else left to Link and ReadLocks.
func chartDeps(file, depType string, src []byte, aliases map[string]string) ([]dep.Dep, error) {
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
		d := newDep(file, depType)
		d.Name, d.Line = name.Value, name.Line
		repoNode := yamlspan.Str(yamlspan.Get(r, "repository"))
		repo := yamlspan.Value(repoNode)
		if alias, ok := aliasName(repo); ok {
			// An alias is a name only, so a replacement can only rename the chart.
			d.NameParts = ChartParts(src, lines, name, d.Name, false, nil)
			setVersion(&d, src, lines, yamlspan.Get(r, "version"))
			if d.Skip == "" {
				d.Source, d.Skip = repo, skipAlias
				resolveAlias(&d, aliases[alias])
			}
		} else if d.Source, d.Skip = chartRepository(repo); d.Skip == "" {
			d.NameParts = ChartParts(src, lines, name, d.Name, false, repoNode)
			setVersion(&d, src, lines, yamlspan.Get(r, "version"))
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// skipAlias is the skip reason of a Chart.yaml dependency whose `@name` or `alias:name`
// repository nothing resolves (see resolveAlias); its Source is the repository as written.
const skipAlias = "repository alias"

// defaultAliases are the repository aliases resolved when no other source names them:
// Renovate's helm-v3 default registryAliases.
var defaultAliases = map[string]string{"stable": "https://charts.helm.sh/stable"}

// aliasName returns the repository name of a Chart.yaml repository `@name` or `alias:name`.
func aliasName(repo string) (string, bool) {
	if name, ok := strings.CutPrefix(repo, "@"); ok {
		return name, name != ""
	}
	name, ok := strings.CutPrefix(repo, "alias:")
	return name, ok && name != ""
}

// resolveAlias resolves the pending alias dep d (Skip skipAlias) to repository url, an
// http(s) or oci:// URL; any other url ("" = not found) leaves d pending.
func resolveAlias(d *dep.Dep, url string) {
	if d.Skip != skipAlias {
		return
	}
	if source, skip := chartRepository(url); skip == "" {
		d.Source, d.Skip = source, ""
	}
}

// chartRepository classifies a Chart.yaml `repository`: an http(s) or oci:// URL (trailing
// slashes dropped) is the dep's Source; local charts and other repositories are skipped.
func chartRepository(repo string) (source, skip string) {
	switch {
	case repo == "" || strings.HasPrefix(repo, "file://"):
		return "", "local chart"
	case strings.HasPrefix(repo, "oci://") || strings.HasPrefix(repo, "https://") || strings.HasPrefix(repo, "http://"):
		return strings.TrimRight(repo, "/"), ""
	}
	return "", "unknown chart repository " + repo
}
