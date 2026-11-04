package helm

import (
	"bytes"
	"regexp"
	"strings"
	"unicode/utf8"

	"go.yaml.in/yaml/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// newDep returns a helm-datasource dep of depType in file.
func newDep(file, depType string) dep.Dep {
	return dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: depType, File: file}
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

// lineOf returns the 1-based line of byte offset off.
func lineOf(lines []int, off int) int {
	lo, hi := 0, len(lines)
	for lo < hi {
		m := int(uint(lo+hi) >> 1)
		if lines[m] <= off {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
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

// exactRe is a complete SemVer 2 version with an optional "v" prefix: the only chart version
// form Helm does not treat as a constraint ("1.2" means "1.2.x").
var exactRe = regexp.MustCompile(`^v?(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(-[0-9A-Za-z.-]+)?(\+[0-9A-Za-z.-]+)?$`)

// versionSkip returns the skip reason of chart version v: "" for an exact version, "no
// version" when empty, "template value" for a template, "version range" otherwise (`~1.2.0`,
// `^1`, `>=1, <2`, `1.2.x`, `1.2`).
func versionSkip(v string) string {
	switch {
	case v == "":
		return "no version"
	case strings.Contains(v, "{{"):
		return "template value"
	case exactRe.MatchString(v):
		return ""
	}
	return "version range"
}

// setVersion sets d's version from scalar v (nil = absent) located in src, or its skip reason.
func setVersion(d *dep.Dep, src []byte, lines []int, v *yaml.Node) {
	if v = str(v); v == nil {
		d.Skip = "no version"
		return
	}
	d.Line, d.Version = v.Line, v.Value
	if d.Skip = versionSkip(v.Value); d.Skip != "" {
		return
	}
	off := locate(src, lines, v)
	if off < 0 {
		d.Skip = "cannot locate value"
		return
	}
	d.RefSpan = dep.Span{Start: off, End: off + len(v.Value)}
}
