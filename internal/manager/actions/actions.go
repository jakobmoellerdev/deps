// Package actions extracts GitHub/Gitea/Forgejo Actions `uses:` references.
package actions

import (
	"bytes"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "github-actions"

var (
	workflowRe = regexp.MustCompile(`(^|/)(workflow-templates|\.(github|gitea|forgejo)/(workflows|actions))/.+\.ya?ml$`)
	actionRe   = regexp.MustCompile(`(^|/)action\.ya?ml$`)
)

// Match reports whether the slash path is a workflow or action file.
func Match(path string) bool { return workflowRe.MatchString(path) || actionRe.MatchString(path) }

// Extract returns every `uses:` reference in src with byte spans into src.
func Extract(file string, src []byte, defaultHost string) ([]dep.Dep, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	x := extractor{file: file, src: src, host: defaultHost, lines: lineStarts(src)}
	x.walk(&root)
	return x.deps, nil
}

type extractor struct {
	file  string
	src   []byte
	host  string
	lines []int
	deps  []dep.Dep
}

func lineStarts(src []byte) []int {
	ls := make([]int, 1, bytes.Count(src, []byte{'\n'})+1)
	for i, b := range src {
		if b == '\n' {
			ls = append(ls, i+1)
		}
	}
	return ls
}

// line returns [start,end) of 1-based line n, excluding "\r\n".
func (x *extractor) line(n int) (int, int) {
	if n < 1 || n > len(x.lines) {
		return -1, -1
	}
	s := x.lines[n-1]
	e := len(x.src)
	if n < len(x.lines) {
		e = x.lines[n] - 1
	}
	if e > s && x.src[e-1] == '\r' {
		e--
	}
	return s, e
}

func (x *extractor) walk(n *yaml.Node) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			x.walk(c)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			switch k.Value {
			case "with", "env":
				continue
			case "uses":
				if v.Kind == yaml.ScalarNode && v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
					x.add(v)
					continue
				}
			}
			x.walk(v)
		}
	}
}

func (x *extractor) add(v *yaml.Node) {
	d := dep.Dep{Manager: Name, File: x.file, Line: v.Line}
	u := ParseUses(v.Value, x.host)
	d.Name, d.Source, d.Skip = u.Name, u.Source, u.Skip
	if d.Skip != "" {
		d.Name = v.Value // report what was skipped
		x.deps = append(x.deps, d)
		return
	}
	ls, le := x.line(v.Line)
	start := -1
	if ls >= 0 {
		line := x.src[ls:le]
		if k := bytes.Index(line, []byte("uses")); k >= 0 {
			if i := bytes.Index(line[k+4:], []byte(v.Value)); i >= 0 {
				start = ls + k + 4 + i
			}
		}
	}
	if start < 0 {
		d.Skip = "cannot locate value"
		x.deps = append(x.deps, d)
		return
	}
	end := start + len(v.Value)
	d.RefSpan = dep.Span{Start: end - len(u.Ref), End: end}
	d.CommentInsert = end
	if q := quoteChar(v.Style); q != 0 && end < le && x.src[end] == q {
		d.CommentInsert++
	}

	switch {
	case isDigest(u.Ref):
		d.Digest = u.Ref
		if sp, ver, ok := x.commentVersion(d.CommentInsert, le); ok {
			d.Version, d.VersionSpan = ver, sp
		} else {
			d.Skip = "pinned without version comment"
		}
	default:
		if _, ok := versioning.ParseTag(u.Ref); ok {
			d.Version = u.Ref
		} else {
			d.Skip = "non-version ref"
		}
	}
	x.deps = append(x.deps, d)
}

// commentVersion finds the version token in the first comment in src[from:le].
func (x *extractor) commentVersion(from, le int) (dep.Span, string, bool) {
	h := bytes.IndexByte(x.src[from:le], '#')
	if h < 0 {
		return dep.Span{}, "", false
	}
	s := from + h + 1
	for s < le && (x.src[s] == ' ' || x.src[s] == '\t') {
		s++
	}
	e := s
	for e < le && x.src[e] != ' ' && x.src[e] != '\t' {
		e++
	}
	tok := string(x.src[s:e])
	if strings.HasPrefix(tok, "ratchet:") {
		a := strings.LastIndexByte(tok, '@')
		if a < 0 {
			return dep.Span{}, "", false
		}
		s += a + 1
		tok = tok[a+1:]
	}
	if _, ok := versioning.ParseTag(tok); !ok {
		return dep.Span{}, "", false
	}
	return dep.Span{Start: s, End: e}, tok, true
}

func quoteChar(s yaml.Style) byte {
	switch {
	case s&yaml.SingleQuotedStyle != 0:
		return '\''
	case s&yaml.DoubleQuotedStyle != 0:
		return '"'
	}
	return 0
}

func isDigest(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f' || 'A' <= c && c <= 'F') {
			return false
		}
	}
	return true
}
