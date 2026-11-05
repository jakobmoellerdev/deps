package helm

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// chartNameRe is a chart name: Helm allows lowercase letters, digits and dashes; existing
// charts also use capitals, dots and underscores.
var chartNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// Rename returns d as the chart name names it (manager.Renamer, see the package func Rename).
func (*helmManager) Rename(d dep.Dep, name string) (dep.Dep, error) { return Rename(d, name) }

// Rename returns chart dep d as the chart name names it (Name, Source), for the managers
// writing chart references like the helm manager (NameParts from ChartParts, edits from
// NameEdits). A name without '/' is a chart of d's repository; `<repository URL>/<chart>`
// (http(s):// or oci://) and `host/path/<chart>` (an OCI registry) also move it to that
// repository, which needs the repository written literally with the chart (a NameRepository
// part).
func Rename(d dep.Dep, name string) (dep.Dep, error) {
	chart, repo, err := parseName(d, name)
	if err != nil {
		return d, err
	}
	d.Name = chart
	if repo != "" {
		d.Source = repo
	}
	return d, nil
}

// parseName splits the new chart name of d into the chart and the repository URL ("" = d's
// repository), validated against where d's name is written.
func parseName(d dep.Dep, name string) (chart, repo string, err error) {
	i := strings.LastIndexByte(name, '/')
	chart = name[i+1:]
	if !chartNameRe.MatchString(chart) {
		return "", "", fmt.Errorf("invalid chart name %q", name)
	}
	if i < 0 {
		return chart, "", nil
	}
	repo = name[:i]
	if !strings.Contains(repo, "://") {
		repo = "oci://" + repo
	}
	scheme, rest, _ := strings.Cut(repo, "://")
	if scheme != "oci" && scheme != "https" && scheme != "http" || rest == "" || strings.HasPrefix(rest, "/") || strings.ContainsAny(rest, " \t\n\"'{}") {
		return "", "", fmt.Errorf("invalid chart repository in %q", name)
	}
	p, ok := repoPart(d)
	switch {
	case !ok:
		return "", "", fmt.Errorf("the repository of chart %s is not written with it, only its name can change", d.Name)
	case scheme != "oci" && ociRef(d, p):
		return "", "", fmt.Errorf("%s is no OCI repository, which the reference %s needs", repo, p.Text)
	}
	return chart, repo, nil
}

// repoPart returns the NameRepository part of d.
func repoPart(d dep.Dep) (dep.NamePart, bool) {
	for _, p := range d.NameParts {
		if p.Role == dep.NameRepository {
			return p, true
		}
	}
	return dep.NamePart{}, false
}

// ociRef reports whether d's repository part p is the prefix of an oci://host/path/chart
// reference (the chart part follows it after the '/'), which holds OCI repositories only.
func ociRef(d dep.Dep, p dep.NamePart) bool {
	for _, c := range d.NameParts {
		if c.Role == dep.NameWhole && c.Span.Start == p.Span.End+1 {
			return true
		}
	}
	return false
}

// NameEdits returns the edits renaming d to name: the chart part gets the chart, the
// repository part the new repository when name has one. An OCI repository replacing one
// written without scheme (an Argo CD repoURL) is written without it as well.
func NameEdits(d dep.Dep, name string) ([]patch.Edit, error) {
	if !d.Renamable() {
		return nil, fmt.Errorf("cannot locate the name of chart %s", d.Name)
	}
	chart, repo, err := parseName(d, name)
	if err != nil {
		return nil, err
	}
	var edits []patch.Edit
	for _, p := range d.NameParts {
		v := chart
		if p.Role == dep.NameRepository {
			if repo == "" || repo == d.Source {
				continue
			}
			if v = repo; !strings.Contains(p.Text, "://") {
				v = strings.TrimPrefix(repo, "oci://")
			}
		}
		if v != p.Text {
			edits = append(edits, patch.Edit{Start: p.Span.Start, End: p.Span.End, Old: p.Text, New: v})
		}
	}
	return edits, nil
}

// ChartParts returns the NameParts of the dep of chart named by scalar n: n's value is the
// chart, possibly after an `<alias>/` or `oci://host/path/` prefix. With ociPrefix that prefix
// (without the '/') is the repository (a NameRepository part), else repo, when not nil, is the
// scalar holding the repository URL. Nil when n cannot be located or does not end in chart,
// or chart is no chart name; a repo that cannot be located only leaves the repository out.
func ChartParts(src []byte, lines []int, n *yaml.Node, chart string, ociPrefix bool, repo *yaml.Node) []dep.NamePart {
	i := len(n.Value) - len(chart) - 1
	if !chartNameRe.MatchString(chart) || !strings.HasSuffix(n.Value, chart) || i >= 0 && n.Value[i] != '/' || ociPrefix && i < 0 {
		return nil
	}
	off := yamlspan.Locate(src, lines, n)
	if off < 0 {
		return nil
	}
	parts := []dep.NamePart{{Span: dep.Span{Start: off + i + 1, End: off + len(n.Value)}, Text: chart}}
	switch {
	case ociPrefix:
		parts = append(parts, dep.NamePart{Span: dep.Span{Start: off, End: off + i}, Text: n.Value[:i], Role: dep.NameRepository})
	case repo != nil:
		if r := yamlspan.Locate(src, lines, repo); r >= 0 {
			parts = append(parts, dep.NamePart{Span: dep.Span{Start: r, End: r + len(repo.Value)}, Text: repo.Value, Role: dep.NameRepository})
		}
	}
	return parts
}
