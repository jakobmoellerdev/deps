// Package kustomize updates Kustomize kustomization files: `images[]` tags and digests,
// `helmCharts[]` versions, and the `?ref=` of remote git `resources`, `components` and `bases`.
package kustomize

import (
	"bytes"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "kustomize"

// Dep types.
const (
	TypeImage     = "image"
	TypeHelmChart = "helmChart"
	TypeResource  = "resource"
)

var (
	fileRe = regexp.MustCompile(`(^|/)(kustomization\.ya?ml|Kustomization)$`)
	// exactRe is a complete SemVer 2 version with an optional "v" prefix: the only chart
	// version form Helm does not treat as a constraint.
	exactRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)
)

type kustomizeManager struct{}

// New returns the kustomize manager.
func New() manager.Manager { return kustomizeManager{} }

func (kustomizeManager) Name() string { return Name }

// Match reports whether the slash path is a kustomization file.
func (kustomizeManager) Match(path string) bool { return fileRe.MatchString(path) }

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
	x := extractor{file: file, src: src, lines: lineStarts(src)}
	for _, m := range items(get(root, "images")) {
		x.image(m)
	}
	for _, m := range items(get(root, "helmCharts")) {
		x.helmChart(m)
	}
	for _, key := range []string{"resources", "components", "bases"} {
		for _, v := range items(get(root, key)) {
			x.resource(v)
		}
	}
	return x.deps, nil
}

// Edits replaces the version (image tag, chart version or git ref) and, for digest-pinned
// images, the digest. Images without a digest cannot be pinned: kustomize has no place to
// insert one without adding a `digest` field.
func (kustomizeManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
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

type extractor struct {
	file  string
	src   []byte
	lines []int
	deps  []dep.Dep
}

// image extracts an `images[]` entry: the image is newName (else name), the version newTag
// (`1.2` or `1.2@sha256:…`), the digest newTag's or the `digest` field.
func (x *extractor) image(m *yaml.Node) {
	nameNode := str(get(m, "name"))
	if nameNode == nil || nameNode.Value == "" {
		return
	}
	name := nameNode.Value
	if n := str(get(m, "newName")); n != nil && n.Value != "" {
		name = n.Value
	}
	d := dep.Dep{Manager: Name, Datasource: "docker", Versioning: "docker", DepType: TypeImage, File: x.file, Line: nameNode.Line, Name: name}
	tag, dg := str(get(m, "newTag")), str(get(m, "digest"))
	switch {
	case strings.Contains(name, "$") || tag != nil && strings.Contains(tag.Value, "$"):
		d.Skip = "variable reference"
	case (tag == nil || tag.Value == "") && dg != nil && dg.Value != "":
		d.Line, d.Skip = dg.Line, "digest without tag"
	case tag == nil || tag.Value == "":
		d.Skip = "no tag"
	default:
		d.Line = tag.Line
		off := locate(x.src, x.lines, tag)
		if off < 0 {
			d.Skip = "cannot locate value"
			break
		}
		ref := name + ":" + tag.Value
		docker.ParseRef(&d, off+len(tag.Value)-len(ref), ref)
		if d.Skip != "" || d.Digest != "" || dg == nil || dg.Value == "" {
			break
		}
		o := locate(x.src, x.lines, dg)
		switch {
		case !strings.HasPrefix(dg.Value, "sha256:"):
			d.Skip = "invalid digest"
		case o < 0:
			d.Skip = "cannot locate value"
		default:
			d.Digest, d.DigestSpan = dg.Value, dep.Span{Start: o, End: o + len(dg.Value)}
		}
	}
	x.deps = append(x.deps, d)
}

// helmChart extracts a `helmCharts[]` entry: chart name from repo at version.
func (x *extractor) helmChart(m *yaml.Node) {
	nameNode := str(get(m, "name"))
	if nameNode == nil || nameNode.Value == "" {
		return
	}
	d := dep.Dep{Manager: Name, Datasource: "helm", Versioning: "semver", DepType: TypeHelmChart, File: x.file, Line: nameNode.Line, Name: nameNode.Value}
	repo := strings.TrimRight(value(get(m, "repo")), "/")
	v := str(get(m, "version"))
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
	case !exactRe.MatchString(v.Value):
		d.Skip = "version range"
	}
	d.Source = repo
	if d.Skip == "" {
		if off := locate(x.src, x.lines, v); off < 0 {
			d.Skip = "cannot locate value"
		} else {
			d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
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
	case isCommit(r.ref):
		d.Version, d.Digest, d.Skip = "", r.ref, "commit ref"
	default:
		if _, ok := versioning.ParseTag(r.ref); !ok {
			d.Skip = "non-version ref"
			break
		}
		off := locate(x.src, x.lines, v)
		if off < 0 {
			d.Skip = "cannot locate value"
			break
		}
		d.RefSpan = dep.Span{Start: off + r.refOff, End: off + r.refOff + len(r.ref)}
	}
	x.deps = append(x.deps, d)
}

// isCommit reports whether ref is a full SHA-1 or SHA-256 commit hash.
func isCommit(ref string) bool {
	if len(ref) != 40 && len(ref) != 64 {
		return false
	}
	for i := range len(ref) {
		if c := ref[i]; !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}

// items returns the elements of sequence n (nil if n is not a sequence).
func items(n *yaml.Node) []*yaml.Node {
	if n == nil || n.Kind != yaml.SequenceNode {
		return nil
	}
	return n.Content
}

// get returns the value of key in mapping m, or nil.
func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// str returns n if it is a scalar, else nil.
func str(n *yaml.Node) *yaml.Node {
	if n == nil || n.Kind != yaml.ScalarNode {
		return nil
	}
	return n
}

// value returns the value of scalar n, or "" when n is not a scalar.
func value(n *yaml.Node) string {
	if n = str(n); n == nil {
		return ""
	}
	return n.Value
}

// lineStarts returns the byte offset of every line start of src.
func lineStarts(src []byte) []int {
	ls := make([]int, 1, bytes.Count(src, []byte{'\n'})+1)
	for i, b := range src {
		if b == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
}

// locate returns the byte offset of scalar v's value in src, or -1 when the source bytes differ
// from the value (escapes, multi-line or block scalars). yaml columns count runes, not bytes,
// and point at the anchor of an anchored value.
func locate(src []byte, lines []int, v *yaml.Node) int {
	if v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 || v.Line < 1 || v.Line > len(lines) {
		return -1
	}
	off := lines[v.Line-1]
	for range v.Column - 1 {
		if off >= len(src) || src[off] == '\n' {
			return -1
		}
		_, n := utf8.DecodeRune(src[off:])
		off += n
	}
	if a := "&" + v.Anchor; v.Anchor != "" && bytes.HasPrefix(src[off:], []byte(a)) {
		off += len(a)
		for off < len(src) && (src[off] == ' ' || src[off] == '\t') {
			off++
		}
	}
	if v.Style&(yaml.SingleQuotedStyle|yaml.DoubleQuotedStyle) != 0 {
		off++
	}
	if off > len(src) || !bytes.HasPrefix(src[off:], []byte(v.Value)) {
		return -1
	}
	return off
}
