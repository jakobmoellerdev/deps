package pep621

import (
	"path"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// pdmSource is one index of `[[tool.pdm.source]]`; url "" is PIP_INDEX_URL or PyPI.
type pdmSource struct {
	url              string
	include, exclude []string // fnmatch patterns of normalized names
}

// pdmSources are the indexes pdm searches, in order: the source named `pypi` (else PyPI)
// first, then the other index sources. `type = "find_links"` sources are not indexes.
type pdmSources []pdmSource

func newPDMSources(doc *tomlspan.Value) pdmSources {
	a := doc.Get("tool", "pdm", "source")
	if a == nil || a.Kind != tomlspan.Array || len(a.Items) == 0 {
		return nil
	}
	s := pdmSources{{}}
	for _, v := range a.Items {
		u := v.Get("url")
		if u == nil || u.Kind != tomlspan.String {
			continue
		}
		if t := v.Get("type"); t != nil && t.Str != "index" {
			continue // find_links
		}
		src := pdmSource{url: u.Str, include: strs(v.Get("include_packages")), exclude: strs(v.Get("exclude_packages"))}
		if strings.Contains(src.url, "${") {
			src.url = "" // environment variables are not expanded: PIP_INDEX_URL or PyPI
		}
		if n := v.Get("name"); n != nil && n.Str == "pypi" {
			s[0] = src
			continue
		}
		if src.url != "" {
			s = append(s, src)
		}
	}
	return s
}

func strs(v *tomlspan.Value) []string {
	items := v.Strings()
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, s.Str)
	}
	return out
}

// match reports whether the normalized name matches one of the patterns.
func match(patterns []string, name string) bool {
	return slices.ContainsFunc(patterns, func(p string) bool {
		ok, _ := path.Match(p, name)
		return ok
	})
}

// apply sets d's Source and ExtraSources like pdm selects indexes: the sources whose
// include_packages match the dep's name, else every source whose exclude_packages do not.
// An index pinned by `[tool.uv.sources]` is kept; uv's extra indexes are replaced.
func (s pdmSources) apply(d *dep.Dep) {
	if len(s) == 0 || d.Skip != "" || d.Source != "" {
		return
	}
	name := pyreq.Normalize(d.Name)
	var urls []string
	for _, src := range s {
		if match(src.include, name) {
			urls = append(urls, src.url)
		}
	}
	if urls == nil {
		for _, src := range s {
			if !match(src.exclude, name) {
				urls = append(urls, src.url)
			}
		}
	}
	if len(urls) == 0 {
		return
	}
	d.Source, d.ExtraSources = urls[0], nil
	for _, u := range urls[1:] {
		if u != d.Source && !slices.Contains(d.ExtraSources, u) {
			d.ExtraSources = append(d.ExtraSources, u)
		}
	}
}
