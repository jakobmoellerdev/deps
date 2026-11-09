package helm

import (
	"bytes"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// skipArchiveURL is the skip reason of a Fleet chart given as the URL of a chart archive.
const skipArchiveURL = "chart archive URL"

// isFleet reports whether the slash path is a Rancher Fleet bundle config (fleet.y(a)ml).
func isFleet(p string) bool {
	b := path.Base(p)
	return b == "fleet.yaml" || b == "fleet.yml"
}

// fleetDeps extracts the charts of a Fleet bundle config (fleet.yaml), as Renovate's fleet
// manager does: `helm.chart` with `helm.repo` (a chart repository URL; registryAliases maps
// it) or an `oci://` chart, at `helm.version`. A targetCustomizations[] entry whose `helm`
// names a chart, repo or version is a dep of its own, the chart and repo defaulting to the
// bundle's; its version is never inherited (a customization without one is "no version").
// Without repo the chart is a path in the bundle ("local chart") or an archive URL.
func fleetDeps(file string, src []byte, aliases map[string]string) ([]dep.Dep, error) {
	x := crdExtractor{file: file, src: src, lines: yamlspan.LineStarts(src), aliases: aliases}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(doc.Content) != 1 {
			continue
		}
		base := yamlspan.Get(doc.Content[0], "helm")
		x.fleetChart(base, nil)
		for _, c := range yamlspan.Seq(yamlspan.Get(doc.Content[0], "targetCustomizations")) {
			if h := yamlspan.Get(c, "helm"); yamlspan.Get(h, "chart") != nil || yamlspan.Get(h, "repo") != nil || yamlspan.Get(h, "version") != nil {
				x.fleetChart(h, base)
			}
		}
	}
	return x.deps, nil
}

// fleetChart adds the chart of Fleet helm options h; chart and repo not set in h come from
// base (nil for the bundle's own options). Only the scalars written in h are NameParts.
func (x *crdExtractor) fleetChart(h, base *yaml.Node) {
	chartNode, ownChart := fleetField(h, base, "chart")
	repoNode, ownRepo := fleetField(h, base, "repo")
	chart, repo := yamlspan.Value(chartNode), yamlspan.Value(repoNode)
	if chart == "" {
		return
	}
	version := yamlspan.Get(h, "version")
	var skip string
	oci := false
	aliased := false
	switch {
	case strings.Contains(chart, "{{") || strings.Contains(repo, "{{"):
		skip = "template value"
	case strings.HasPrefix(chart, "oci://"):
		oci = true
		if repo, chart = splitOCI(chart); chart == "" {
			repo, chart, skip = "", yamlspan.Value(chartNode), "invalid chart reference"
		}
	case repo != "":
		if a, ok := x.aliases[repo]; ok {
			repo, aliased = a, true
		}
		repo, skip = chartRepository(repo)
		if skip == "" && strings.Contains(chart, "/") {
			skip = "invalid chart reference"
		}
	case strings.Contains(chart, "://"):
		skip = skipArchiveURL
	default:
		skip = "local chart"
	}
	i := x.add(TypeFleet, repo, chart, chartNode, version, skip)
	if x.deps[i].Skip != "" || !ownChart {
		return
	}
	if !ownRepo || aliased {
		repoNode = nil
	}
	x.rename(i, chartNode, oci, repoNode)
}

// fleetField returns the scalar key of h, else of base, and whether it is h's.
func fleetField(h, base *yaml.Node, key string) (*yaml.Node, bool) {
	if n := yamlspan.Str(yamlspan.Get(h, key)); n != nil {
		return n, true
	}
	return yamlspan.Str(yamlspan.Get(base, key)), false
}

// fleetGitRepo extracts a Fleet GitRepo (fleet.cattle.io): spec.revision of spec.repo is a
// git-tags dep, as in Renovate's fleet manager; without revision Fleet follows spec.branch.
func (x *crdExtractor) fleetGitRepo(spec *yaml.Node) {
	if d, ok := gitTagDep(x.file, TypeFleet, x.src, x.lines, yamlspan.Str(yamlspan.Get(spec, "repo")), yamlspan.Get(spec, "revision"), ""); ok {
		x.deps = append(x.deps, d)
	}
}

// commitRe is a full git commit hash.
var commitRe = regexp.MustCompile(`^[0-9a-f]{40}$`)

// gitTagDep returns the git-tags dep of the git repository at scalar url checked out at ref
// (nil or empty: the default branch), false without url. A ref is the dep's version when it
// is a `v?N[.N[.N]]` tag after prefix (which stays in the file); a commit is "commit ref",
// anything else (a branch) "non-version ref".
func gitTagDep(file, depType string, src []byte, lines []int, url, ref *yaml.Node, prefix string) (dep.Dep, bool) {
	if url == nil || url.Value == "" {
		return dep.Dep{}, false
	}
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: depType, File: file, Line: url.Line}
	d.Name, d.Source = manager.GitRepo(url.Value)
	ref = yamlspan.Str(ref)
	v := strings.TrimPrefix(yamlspan.Value(ref), prefix)
	switch {
	case d.Source == "":
		d.Name, d.Skip = url.Value, skipGitURL
		return d, true
	case v == "":
		d.Skip = skipNonVersion
		return d, true
	}
	d.Version, d.Line = v, ref.Line
	_, isTag := versioning.ParseTag(v)
	switch {
	case strings.Contains(v, "{{"):
		d.Skip = "template value"
	case commitRe.MatchString(v):
		d.Skip = skipCommitRef
	case !isTag:
		d.Skip = skipNonVersion
	default:
		if off := yamlspan.Locate(src, lines, ref); off < 0 {
			d.Skip = "cannot locate value"
		} else {
			d.RefSpan = dep.Span{Start: off + len(ref.Value) - len(v), End: off + len(ref.Value)}
		}
	}
	return d, true
}
