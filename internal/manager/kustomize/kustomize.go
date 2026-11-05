// Package kustomize updates Kustomize kustomization files: `images[]` tags, digests and names
// (replacements), `helmCharts[]` versions, and the `?ref=` of remote git `resources`,
// `components` and `bases`.
package kustomize

import (
	"bytes"
	"errors"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/helm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name.
const Name = "kustomize"

// Dep types.
const (
	TypeImage     = "image"
	TypeHelmChart = "helmChart"
	TypeResource  = "resource"
)

var fileRe = regexp.MustCompile(`(^|/)(kustomization\.ya?ml|Kustomization)$`)

type kustomizeManager struct{}

// New returns the kustomize manager.
func New() manager.Manager { return kustomizeManager{} }

func (kustomizeManager) Name() string { return Name }

// Match reports whether the slash path is a kustomization file. The suffix check skips the
// regexp for the paths that cannot match.
func (kustomizeManager) Match(path string) bool {
	if !strings.HasSuffix(path, "kustomization.yaml") && !strings.HasSuffix(path, "kustomization.yml") &&
		!strings.HasSuffix(path, "Kustomization") {
		return false
	}
	return fileRe.MatchString(path)
}

// Extract returns the images, Helm charts and remote git resources of src with byte spans
// into src.
func (kustomizeManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		return nil, err
	}
	if len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, nil
	}
	root := doc.Content[0]
	x := extractor{file: file, src: src, lines: yamlspan.LineStarts(src)}
	for _, m := range yamlspan.Seq(yamlspan.Get(root, "images")) {
		x.image(m)
	}
	for _, m := range yamlspan.Seq(yamlspan.Get(root, "helmCharts")) {
		x.helmChart(m)
	}
	for _, key := range []string{"resources", "components", "bases"} {
		for _, v := range yamlspan.Seq(yamlspan.Get(root, key)) {
			x.resource(v)
		}
	}
	return x.deps, nil
}

// Edits replaces the version (image tag, chart version or git ref) and, for digest-pinned
// images, the digest. Images without a digest cannot be pinned: kustomize has no place to
// insert one without adding a `digest` field. A replacement (u.NewName) rewrites an image's
// newName, or inserts one after its `name` line, and a chart's name and repo (helm.NameEdits).
func (kustomizeManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewName != "" {
		ne, err := nameEdits(d, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = ne
	}
	if u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion})
	}
	switch {
	case u.NewDigest == "" && d.Digest != "":
		return nil, errors.New("cannot unpin a digest-pinned image")
	case u.NewDigest == "":
	case d.Digest == "":
		return nil, errors.New("cannot pin an image without a digest")
	default:
		edits = append(edits, patch.Edit{Start: d.DigestSpan.Start, End: d.DigestSpan.End, Old: d.Digest, New: u.NewDigest})
	}
	return edits, nil
}

// Rename returns d as name names it: an image (docker.Rename) or a chart, possibly with the
// repository it moves to (helm.Rename).
func (kustomizeManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	if d.DepType == TypeHelmChart {
		return helm.Rename(d, name)
	}
	return docker.Rename(d, name)
}

// roleNameLine is the role of the NamePart over the `name` line of an image without newName
// (from the line start to the line end, `  - name: nginx`): Edits inserts a newName key after
// it, indented like the `name` key, instead of rewriting the part. It is never passed to
// manager.NameEdits.
const roleNameLine = "kustomize-name-line"

// nameEdits writes name as d's name: a chart's name and repo, an image's newName, or a newName
// key inserted after an image's `name` line (roleNameLine).
func nameEdits(d dep.Dep, name string) ([]patch.Edit, error) {
	if d.DepType == TypeHelmChart {
		return helm.NameEdits(d, name)
	}
	if len(d.NameParts) != 1 || d.NameParts[0].Role != roleNameLine {
		return manager.NameEdits(d, name)
	}
	p := d.NameParts[0]
	indent := len(p.Text) - len(strings.TrimLeft(p.Text, " -"))
	return []patch.Edit{{Start: p.Span.End, End: p.Span.End, New: "\n" + strings.Repeat(" ", indent) + "newName: " + name}}, nil
}

type extractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
}

// image extracts an `images[]` entry: the image is newName (else name), the version newTag
// (`1.2` or `1.2@sha256:…`), the digest newTag's or the `digest` field.
func (x *extractor) image(m *yaml.Node) {
	nameNode := yamlspan.Str(yamlspan.Get(m, "name"))
	if nameNode == nil || nameNode.Value == "" {
		return
	}
	name := nameNode.Value
	newName := yamlspan.Str(yamlspan.Get(m, "newName"))
	if newName != nil && newName.Value != "" {
		name = newName.Value
	} else {
		newName = nil
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeImage, File: x.file, Line: nameNode.Line, Name: name}
	tag, dg := yamlspan.Str(yamlspan.Get(m, "newTag")), yamlspan.Str(yamlspan.Get(m, "digest"))
	switch {
	case strings.Contains(name, "$") || tag != nil && strings.Contains(tag.Value, "$"):
		d.Skip = "variable reference"
	case (tag == nil || tag.Value == "") && dg != nil && dg.Value != "":
		d.Line, d.Skip = dg.Line, "digest without tag"
	case tag == nil || tag.Value == "":
		d.Skip = "no tag"
	default:
		d.Line = tag.Line
		off := yamlspan.Locate(x.src, x.lines, tag)
		if off < 0 {
			d.Skip = "cannot locate value"
			break
		}
		ref := name + ":" + tag.Value
		docker.ParseRef(&d, off+len(tag.Value)-len(ref), ref)
		if d.Skip != "" || d.Digest != "" || dg == nil || dg.Value == "" {
			break
		}
		o := yamlspan.Locate(x.src, x.lines, dg)
		switch {
		case !strings.HasPrefix(dg.Value, "sha256:"):
			d.Skip = "invalid digest"
		case o < 0:
			d.Skip = "cannot locate value"
		default:
			d.Digest, d.DigestSpan = dg.Value, dep.Span{Start: o, End: o + len(dg.Value)}
		}
	}
	if d.Skip == "" {
		x.imageNameParts(&d, m, nameNode, newName)
	}
	x.deps = append(x.deps, d)
}

// imageNameParts makes the updatable image d renamable: one NameWhole part over its newName
// value, or without newName (nil) a roleNameLine part over the `name` line, as rewriting
// `name`, the key matching the manifests' images, would stop the override from applying. Flow
// mappings, an empty or non-scalar newName, a `name` value off its key's line and values that
// cannot be located leave d not renamable.
func (x *extractor) imageNameParts(d *dep.Dep, m, nameNode, newName *yaml.Node) {
	if newName != nil {
		if off := yamlspan.Locate(x.src, x.lines, newName); off >= 0 {
			d.NameParts = []dep.NamePart{{Span: dep.Span{Start: off, End: off + len(newName.Value)}, Text: newName.Value}}
		}
		return
	}
	key := mappingKey(m, "name")
	off := yamlspan.Locate(x.src, x.lines, nameNode)
	if m.Style&yaml.FlowStyle != 0 || mappingKey(m, "newName") != nil || key.Line != nameNode.Line || off < 0 {
		return
	}
	start := x.lines[key.Line-1]
	if key.Column-1 > off-start || strings.Trim(string(x.src[start:start+key.Column-1]), " -") != "" {
		return
	}
	end := len(x.src)
	if i := bytes.IndexByte(x.src[off:], '\n'); i >= 0 {
		end = off + i
	}
	end = len(bytes.TrimSuffix(x.src[:end], []byte("\r")))
	d.NameParts = []dep.NamePart{{Span: dep.Span{Start: start, End: end}, Text: string(x.src[start:end]), Role: roleNameLine}}
}

// helmChart extracts a `helmCharts[]` entry: chart name from repo at version. The name and
// the repo are its NameParts (helm.ChartParts); the default releaseName does not derive from
// the name, so renaming the chart keeps the rendered resources' names.
func (x *extractor) helmChart(m *yaml.Node) {
	nameNode := yamlspan.Str(yamlspan.Get(m, "name"))
	if nameNode == nil || nameNode.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "helm", Versioning: "semver", DepType: TypeHelmChart, File: x.file, Line: nameNode.Line, Name: nameNode.Value}
	repoNode := yamlspan.Str(yamlspan.Get(m, "repo"))
	repo := strings.TrimRight(yamlspan.Value(repoNode), "/")
	v := yamlspan.Str(yamlspan.Get(m, "version"))
	if v != nil {
		d.Line, d.Version = v.Line, v.Value
	}
	switch {
	case repo == "":
		d.Skip = "no repo"
	case !chartRepository(repo):
		d.Skip = "unknown chart repository " + repo
	case v == nil || v.Value == "":
		d.Skip = "no version"
	case !helm.ExactVersion(v.Value):
		d.Skip = "version range"
	}
	d.Source = repo
	if d.Skip == "" {
		if off := yamlspan.Locate(x.src, x.lines, v); off < 0 {
			d.Skip = "cannot locate value"
		} else {
			d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
			d.NameParts = helm.ChartParts(x.src, x.lines, nameNode, nameNode.Value, false, repoNode)
		}
	}
	x.deps = append(x.deps, d)
}

// chartRepository reports whether repo is an `https://`, `http://` or `oci://` URL with a host.
func chartRepository(repo string) bool {
	for _, scheme := range []string{"https://", "http://", "oci://"} {
		if rest, ok := strings.CutPrefix(repo, scheme); ok {
			return rest != "" && !strings.HasPrefix(rest, "/")
		}
	}
	return false
}

// resource extracts a remote git target with a `ref` (or `version`) query parameter; local
// paths and URLs without a ref are no deps.
func (x *extractor) resource(v *yaml.Node) {
	if v.Kind != yaml.ScalarNode {
		return
	}
	r, ok := parseRemote(v.Value)
	if !ok {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: TypeResource, File: x.file, Line: v.Line, Name: r.name, Source: r.source, Version: r.ref}
	switch {
	case r.source == "":
		d.Name, d.Skip = v.Value, "invalid repository URL"
	case r.ref == "":
		d.Skip = "no ref"
	case manager.IsHex(r.ref, 40, 64):
		d.Version, d.Digest, d.Skip = "", r.ref, "commit ref"
	default:
		if _, ok := versioning.ParseTag(r.ref); !ok {
			d.Skip = "non-version ref"
			break
		}
		off := yamlspan.Locate(x.src, x.lines, v)
		if off < 0 {
			d.Skip = "cannot locate value"
			break
		}
		d.RefSpan = dep.Span{Start: off + r.refOff, End: off + r.refOff + len(r.ref)}
	}
	x.deps = append(x.deps, d)
}

// mappingKey returns the key node of key in mapping m, or nil.
func mappingKey(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i]
		}
	}
	return nil
}
