// Package actions extracts GitHub/Gitea/Forgejo Actions `uses:` references and the version
// inputs of well-known setup actions (DepType setup).
package actions

import (
	"bytes"
	"errors"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name.
const Name = "github-actions"

var (
	workflowRe = regexp.MustCompile(`(^|/)(workflow-templates|\.(github|gitea|forgejo)/(workflows|actions))/.+\.ya?ml$`)
	actionRe   = regexp.MustCompile(`(^|/)action\.ya?ml$`)
)

type actionsManager struct{}

// New returns the github-actions manager.
func New() manager.Manager { return actionsManager{} }

func (actionsManager) Name() string { return Name }

// Match reports whether the slash path is a workflow or action file.
func (actionsManager) Match(path string) bool {
	return IsWorkflowFile(path)
}

// IsWorkflowFile reports whether path is a GitHub, Gitea or Forgejo
// workflow, workflow template or composite action file.
func IsWorkflowFile(path string) bool {
	return workflowRe.MatchString(path) || actionRe.MatchString(path)
}

// Extract returns every `uses:` reference in src with byte spans into src;
// owner/repo refs resolve against env.DefaultHost.
func (actionsManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	x := extractor{file: file, src: src, host: env.DefaultHost, lines: yamlspan.LineStarts(src)}
	x.walk(&root)
	return x.deps, nil
}

// Edits rewrites the ref: tag → tag, pinned → new digest plus comment version, or
// tag → digest with " # <version>" inserted after the value.
func (actionsManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	switch {
	case u.NewDigest == "":
		if d.Digest != "" {
			return nil, errors.New("cannot unpin a digest-pinned action")
		}
		edits = []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}
	case d.Digest != "":
		edits = []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Digest, New: u.NewDigest}}
		if u.NewVersion != d.Version {
			edits = append(edits, patch.Edit{Start: d.VersionSpan.Start, End: d.VersionSpan.End, Old: d.Version, New: u.NewVersion})
		}
	default:
		edits = []patch.Edit{
			{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewDigest},
			{Start: d.InsertAt, End: d.InsertAt, New: " # " + u.NewVersion},
		}
	}
	if d.InputSpan.End > d.InputSpan.Start && u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.InputSpan.Start, End: d.InputSpan.End, Old: d.Version, New: u.NewVersion})
	}
	return edits, nil
}

type extractor struct {
	file  string
	src   []byte
	host  string
	lines []int
	deps  []dep.Dep
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
		var uses, with *yaml.Node
		idx := -1
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, v := n.Content[i], n.Content[i+1]
			switch k.Value {
			case "with":
				with = v
				continue
			case "env":
				continue
			case "uses":
				if v.Kind == yaml.ScalarNode && v.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 {
					uses = v
					x.add(v)
					idx = len(x.deps) - 1
					continue
				}
			}
			x.walk(v)
		}
		if uses != nil && with != nil {
			x.setup(uses.Value, with)
			x.versionInput(idx, with)
		}
	}
}

func (x *extractor) add(v *yaml.Node) {
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: "action", File: x.file, Line: v.Line}
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
	d.InsertAt = end
	if q := manager.QuoteChar(v.Style); q != 0 && end < le && x.src[end] == q {
		d.InsertAt++
	}

	switch {
	case manager.IsHex(u.Ref, 40, 64):
		d.Digest = u.Ref
		if sp, ver, ok := x.commentVersion(d.InsertAt, le); ok {
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
