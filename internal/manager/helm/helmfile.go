package helm

import (
	"bytes"
	"errors"
	"io"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// helmfileDeps extracts `releases[]` of every document of a helmfile, resolving `chart:
// <repo>/<name>` through the `repositories[]` of all documents. In `.gotmpl` files every line
// containing `{{` is blanked out before parsing (offsets stay valid), so templated values
// are not deps. The chart name of `chart` is a NamePart; the repository is one only in an
// `oci://` reference (a `<repo>/` alias may serve other releases).
func helmfileDeps(file string, src []byte) ([]dep.Dep, error) {
	parse := src
	if strings.HasSuffix(file, ".gotmpl") {
		parse = blankTemplates(src)
	}
	var docs []*yaml.Node
	dec := yaml.NewDecoder(bytes.NewReader(parse))
	for {
		var doc yaml.Node
		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, err
		}
		if len(doc.Content) == 1 && doc.Content[0].Kind == yaml.MappingNode {
			docs = append(docs, doc.Content[0])
		}
	}
	repos := map[string]string{}
	for _, doc := range docs {
		if rs := yamlspan.Get(doc, "repositories"); rs != nil && rs.Kind == yaml.SequenceNode {
			for _, r := range rs.Content {
				name, url := yamlspan.Value(yamlspan.Get(r, "name")), strings.TrimRight(yamlspan.Value(yamlspan.Get(r, "url")), "/")
				if name == "" || url == "" {
					continue
				}
				if yamlspan.Value(yamlspan.Get(r, "oci")) == "true" && !strings.HasPrefix(url, "oci://") {
					url = "oci://" + url
				}
				repos[name] = url
			}
		}
	}
	lines := yamlspan.LineStarts(src)
	var deps []dep.Dep
	for _, doc := range docs {
		rels := yamlspan.Get(doc, "releases")
		if rels == nil || rels.Kind != yaml.SequenceNode {
			continue
		}
		for _, r := range rels.Content {
			chart := yamlspan.Str(yamlspan.Get(r, "chart"))
			if chart == nil || chart.Value == "" {
				continue
			}
			d := newDep(file, TypeHelmfile)
			d.Line = chart.Line
			d.Source, d.Name, d.Skip = helmfileChart(chart.Value, repos)
			if d.Skip == "" {
				d.NameParts = ChartParts(src, lines, chart, d.Name, strings.HasPrefix(chart.Value, "oci://"), nil)
				setVersion(&d, src, lines, yamlspan.Get(r, "version"))
			}
			deps = append(deps, d)
		}
	}
	return deps, nil
}

// helmfileChart splits a release's chart into repository URL and chart name: `oci://host/path/
// name` directly, `<repo>/<name>` through repos. Local paths are skipped.
func helmfileChart(chart string, repos map[string]string) (source, name, skip string) {
	if rest, ok := strings.CutPrefix(chart, "oci://"); ok {
		repo, name, ok := strings.CutLast(rest, "/")
		if !ok {
			return "", chart, "invalid chart reference"
		}
		return "oci://" + repo, name, ""
	}
	alias, name, ok := strings.Cut(chart, "/")
	switch {
	case !ok || strings.HasPrefix(chart, ".") || strings.HasPrefix(chart, "/") || strings.Contains(chart, "://"):
		return "", chart, "local chart"
	case strings.Contains(name, "/"):
		return "", chart, "invalid chart reference"
	}
	if url, ok := repos[alias]; ok {
		return url, name, ""
	}
	return "", name, "unknown chart repository " + alias
}

// blankTemplates returns a copy of src with every byte of a line containing `{{` replaced by a
// space, keeping line breaks and byte offsets.
func blankTemplates(src []byte) []byte {
	out := bytes.Clone(src)
	for pos := 0; pos < len(out); {
		end := bytes.IndexByte(out[pos:], '\n')
		if end < 0 {
			end = len(out)
		} else {
			end += pos
		}
		if bytes.Contains(out[pos:end], []byte("{{")) {
			for i := pos; i < end; i++ {
				if out[i] != '\r' {
					out[i] = ' '
				}
			}
		}
		pos = end + 1
	}
	return out
}
