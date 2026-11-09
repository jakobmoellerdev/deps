package helm

import (
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// vendirFile is the file name of a vendir config (vendir's default, Renovate's pattern).
const vendirFile = "vendir.yml"

// skipNoRepository is the skip reason of a vendir helmChart without repository.url (vendir
// then uses the Helm repositories configured on the machine).
const skipNoRepository = "no repository"

// vendirDeps extracts the contents of a vendir config (apiVersion vendir.k14s.io/*, kind
// Config) as Renovate's vendir manager does: `directories[].contents[]` with a `helmChart`
// (`name`, `version`, `repository.url`), a `git` source (`url` at `ref`, git-tags; an
// `origin/` prefix stays in the file) or a `githubRelease` (`slug` at `tag`, github-releases).
// A file that does not parse or is no vendir config yields nothing.
func vendirDeps(file string, src []byte) []dep.Dep {
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || len(doc.Content) != 1 {
		return nil
	}
	root := doc.Content[0]
	if !strings.HasPrefix(yamlspan.Value(yamlspan.Get(root, "apiVersion")), "vendir.k14s.io/") || yamlspan.Value(yamlspan.Get(root, "kind")) != "Config" {
		return nil
	}
	x := crdExtractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	for _, dir := range yamlspan.Seq(yamlspan.Get(root, "directories")) {
		for _, c := range yamlspan.Seq(yamlspan.Get(dir, "contents")) {
			if h := yamlspan.Get(c, "helmChart"); h != nil {
				x.vendirChart(h)
			}
			if g := yamlspan.Get(c, "git"); g != nil {
				x.vendirGit(g)
			}
			if r := yamlspan.Get(c, "githubRelease"); r != nil {
				x.vendirRelease(r)
			}
		}
	}
	return x.deps
}

// vendirChart adds a helmChart content: name in repository.url (an http(s) or oci://
// repository) at version. name and the URL are the NameParts.
func (x *crdExtractor) vendirChart(h *yaml.Node) {
	nameNode := yamlspan.Str(yamlspan.Get(h, "name"))
	if nameNode == nil || nameNode.Value == "" {
		return
	}
	urlNode := yamlspan.Str(yamlspan.Get(yamlspan.Get(h, "repository"), "url"))
	repo, skip := chartRepository(yamlspan.Value(urlNode))
	switch {
	case urlNode == nil || urlNode.Value == "":
		repo, skip = "", skipNoRepository
	case strings.Contains(nameNode.Value, "/"):
		skip = "invalid chart reference"
	}
	if i := x.add(TypeVendir, repo, nameNode.Value, nameNode, yamlspan.Get(h, "version"), skip); x.deps[i].Skip == "" {
		x.rename(i, nameNode, false, urlNode)
	}
}

// vendirGit adds a git content: url at ref, or SkipRange for a refSelection.
func (x *crdExtractor) vendirGit(g *yaml.Node) {
	url, ref := yamlspan.Str(yamlspan.Get(g, "url")), yamlspan.Get(g, "ref")
	d, ok := gitTagDep(x.file, TypeVendir, x.src, x.lines, url, ref, "origin/")
	if !ok {
		return
	}
	if ref == nil && yamlspan.Get(g, "refSelection") != nil && d.Skip == skipNonVersion {
		d.Skip = SkipRange
	}
	x.deps = append(x.deps, d)
}

// vendirRelease adds a githubRelease content: the release tag of the GitHub repository slug
// (github-releases, `tag` versioning), or SkipRange for a tagSelection.
func (x *crdExtractor) vendirRelease(r *yaml.Node) {
	slug := yamlspan.Str(yamlspan.Get(r, "slug"))
	if slug == nil || slug.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "github-releases", Versioning: "tag", DepType: TypeVendir, File: x.file, Line: slug.Line, Name: slug.Value, Source: "https://github.com/" + slug.Value}
	tag := yamlspan.Str(yamlspan.Get(r, "tag"))
	switch {
	case tag == nil || tag.Value == "":
		d.Skip = "no version"
		if yamlspan.Get(r, "tagSelection") != nil {
			d.Skip = SkipRange
		}
	default:
		d.Version, d.Line = tag.Value, tag.Line
		if _, ok := versioning.ParseTag(tag.Value); !ok {
			d.Skip = skipNonVersion
		} else if off := yamlspan.Locate(x.src, x.lines, tag); off < 0 {
			d.Skip = "cannot locate value"
		} else {
			d.RefSpan = dep.Span{Start: off, End: off + len(tag.Value)}
		}
	}
	x.deps = append(x.deps, d)
}
