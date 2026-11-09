package helm

import (
	"bytes"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// skipVariable is the skip reason of a Helmsman value with an environment variable, which
// Helmsman substitutes when it reads the file.
const skipVariable = "variable reference"

// helmsman extracts the apps of a Helmsman desired state file: a YAML mapping with a
// `helmRepos` mapping, or with an `apps` mapping and `namespaces` (Helmsman files follow no
// naming convention; composed files may hold only repositories or only apps). ok is false for
// any other file. Each app's `chart` is `<repo>/<chart>`, the repo a `helmRepos` name (this
// file's, else, for composed desired state files, the one URL all scanned files give it; see
// Link), or an `oci://` reference, at `version`. Values inherited through merge keys (`<<:
// *template`, Helmsman's appsTemplates) count; a template's version shared by several apps is
// one dep, at the template.
func (m *helmManager) helmsman(file string, src []byte) (deps []dep.Dep, ok bool) {
	if !bytes.Contains(src, []byte("helmRepos:")) && (!bytes.Contains(src, []byte("apps:")) || !bytes.Contains(src, []byte("namespaces:"))) {
		return nil, false
	}
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || len(doc.Content) != 1 {
		return nil, false
	}
	root := doc.Content[0]
	apps, helmRepos := yamlspan.Get(root, "apps"), yamlspan.Get(root, "helmRepos")
	isMap := func(n *yaml.Node) bool { return n != nil && n.Kind == yaml.MappingNode }
	if !isMap(helmRepos) && (!isMap(apps) || yamlspan.Get(root, "namespaces") == nil) {
		return nil, false
	}
	x := crdExtractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	repos := map[string]string{}
	for i := 0; isMap(helmRepos) && i+1 < len(helmRepos.Content); i += 2 {
		if url := strings.TrimRight(yamlspan.Value(yamlspan.Str(helmRepos.Content[i+1])), "/"); url != "" {
			repos[helmRepos.Content[i].Value] = url
		}
	}
	x.helmsmanRepos = repos
	seen := map[*yaml.Node]bool{}
	for i := 1; isMap(apps) && i < len(apps.Content); i += 2 {
		app := apps.Content[i]
		chart, version := mergedField(app, "chart"), mergedField(app, "version")
		if chart == nil || chart.Value == "" {
			continue
		}
		at := version
		if at == nil {
			at = chart
		}
		if seen[at] {
			continue
		}
		renamable := !seen[chart]
		seen[at], seen[chart] = true, true
		x.helmsmanApp(chart, version, repos, renamable)
	}
	m.links.add(file, &x.fileLinks)
	return x.deps, true
}

// helmsmanApp adds the dep of an app's chart scalar at version (nil: none); repos are the
// file's helmRepos. A `<repo>/` the file does not declare is resolved by Link.
func (x *crdExtractor) helmsmanApp(chart, version *yaml.Node, repos map[string]string, renamable bool) {
	c := chart.Value
	switch {
	case strings.Contains(c, "$") || strings.Contains(yamlspan.Value(yamlspan.Str(version)), "$"):
		x.add(TypeHelmsman, "", c, chart, version, skipVariable)
		return
	case strings.HasPrefix(c, "oci://"):
		repo, name := splitOCI(c)
		if name == "" {
			x.add(TypeHelmsman, "", c, chart, version, "invalid chart reference")
			return
		}
		if i := x.add(TypeHelmsman, repo, name, chart, version, ""); renamable && x.deps[i].Skip == "" {
			x.rename(i, chart, true, nil)
		}
		return
	case strings.HasPrefix(c, ".") || strings.HasPrefix(c, "/"):
		x.add(TypeHelmsman, "", c, chart, version, "local chart")
		return
	}
	alias, name, ok := strings.Cut(c, "/")
	if !ok || name == "" || strings.Contains(name, "/") {
		x.add(TypeHelmsman, "", c, chart, version, "invalid chart reference")
		return
	}
	url, declared := repos[alias]
	repo, skip := helmsmanRepository(alias, url)
	if !declared {
		repo, skip = "", ""
	}
	i := x.add(TypeHelmsman, repo, name, chart, version, skip)
	if x.deps[i].Skip != "" {
		return
	}
	if renamable {
		x.rename(i, chart, false, nil)
	}
	if !declared {
		x.refs = append(x.refs, ref{i: i, kind: refHelmsman, key: key{name: alias}})
	}
}

// helmsmanRepository classifies the helmRepos URL of alias: an http(s) or oci:// URL without
// environment variables is the dep's Source (Helmsman also reads s3:// and gs:// repositories,
// which the helm datasource does not).
func helmsmanRepository(alias, url string) (source, skip string) {
	if strings.Contains(url, "$") {
		return "", skipVariable
	}
	if source, skip = chartRepository(url); skip != "" {
		skip = "unknown chart repository " + alias
	}
	return source, skip
}

// mergedField returns the scalar key of mapping m, else of the mappings merged into it (`<<:
// *a`, `<<: [*a, *b]`, several `<<` keys), the first found in order.
func mergedField(m *yaml.Node, key string) *yaml.Node {
	if n := yamlspan.Str(yamlspan.Get(m, key)); n != nil || m == nil || m.Kind != yaml.MappingNode {
		return n
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value != "<<" {
			continue
		}
		v := m.Content[i+1]
		srcs := []*yaml.Node{v}
		if v.Kind == yaml.SequenceNode {
			srcs = v.Content
		}
		for _, s := range srcs {
			if s.Kind == yaml.AliasNode {
				s = s.Alias
			}
			if n := mergedField(s, key); n != nil {
				return n
			}
		}
	}
	return nil
}
