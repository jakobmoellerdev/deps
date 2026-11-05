// Package actions extracts GitHub/Gitea/Forgejo Actions `uses:` references and the version
// inputs of well-known setup actions (DepType setup).
package actions

import (
	"bytes"
	"errors"
	"fmt"
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

var workflowRe = regexp.MustCompile(`(^|/)(workflow-templates|\.(github|gitea|forgejo)/(workflows|actions))/.+\.ya?ml$`)

type actionsManager struct{}

// New returns the github-actions manager.
func New() manager.Manager { return actionsManager{} }

func (actionsManager) Name() string { return Name }

// Match reports whether the slash path is a workflow or action file.
func (actionsManager) Match(path string) bool {
	return IsWorkflowFile(path)
}

// IsWorkflowFile reports whether path is a GitHub, Gitea or Forgejo
// workflow, workflow template or composite action file. Only YAML paths with a workflow
// directory's literal part reach the regexp; action files are matched by their name.
func IsWorkflowFile(path string) bool {
	if !strings.HasSuffix(path, ".yml") && !strings.HasSuffix(path, ".yaml") {
		return false
	}
	if base := path[strings.LastIndexByte(path, '/')+1:]; base == "action.yml" || base == "action.yaml" {
		return true
	}
	return (strings.Contains(path, "workflow") || strings.Contains(path, "actions/")) && workflowRe.MatchString(path)
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
// tag → digest with " # <version>" inserted after the value. A per-action tag prefix
// (`@get-vault-secrets/v1.3.1`) is kept; with u.NewName it becomes the new name's prefix
// (see newPrefix).
func (actionsManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	switch {
	case u.NewDigest == "":
		if d.Digest != "" {
			return nil, errors.New("cannot unpin a digest-pinned action")
		}
		sp := d.RefSpan
		if d.VersionSpan != (dep.Span{}) {
			sp = d.VersionSpan
		}
		edits = []patch.Edit{{Start: sp.Start, End: sp.End, Old: d.Version, New: u.NewVersion}}
	case d.Digest != "":
		edits = []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Digest, New: u.NewDigest}}
		if u.NewVersion != d.Version {
			edits = append(edits, patch.Edit{Start: d.VersionSpan.Start, End: d.VersionSpan.End, Old: d.Version, New: u.NewVersion})
		}
	default:
		p := refPrefix(d)
		np := p
		if u.NewName != "" {
			np = newPrefix(p, u.NewName)
		}
		edits = []patch.Edit{
			{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: p + d.Version, New: u.NewDigest},
			{Start: d.InsertAt, End: d.InsertAt, New: " # " + np + u.NewVersion},
		}
	}
	if d.InputSpan.End > d.InputSpan.Start && u.NewVersion != d.Version {
		edits = append(edits, patch.Edit{Start: d.InputSpan.Start, End: d.InputSpan.End, Old: d.Version, New: u.NewVersion})
	}
	if u.NewName != "" {
		nd, prefix := d, -1
		for i, p := range d.NameParts {
			if p.Role == roleTagPrefix {
				nd.NameParts, prefix = d.NameParts[:i:i], i
				break
			}
		}
		ne, err := manager.NameEdits(nd, u.NewName)
		if err != nil {
			return nil, err
		}
		edits = append(edits, ne...)
		// The ref's prefix is gone when the whole ref became a digest.
		if prefix >= 0 && (u.NewDigest == "" || d.Digest != "") {
			p := d.NameParts[prefix]
			if np := newPrefix(p.Text, u.NewName); np != p.Text {
				edits = append(edits, patch.Edit{Start: p.Span.Start, End: p.Span.End, Old: p.Text, New: np})
			}
		}
	}
	return edits, nil
}

// roleTagPrefix is the role of the NamePart over the per-action tag prefix (`get-vault-secrets/`)
// of a ref or version comment. Edits writes it itself; it is never passed to NameEdits.
const roleTagPrefix = "actions-tag-prefix"

// newPrefix returns the per-action tag prefix of name in the form of prefix (without the
// "/"-joined path elements: the last element; with them: the full subpath), plus "/";
// "" for no prefix or a root action name. A single-element prefix of a multi-element new
// path is ambiguous and rejected by Rename.
func newPrefix(prefix, name string) string {
	if prefix == "" {
		return ""
	}
	sub, last := subpath(name)
	if sub == "" {
		return ""
	}
	if strings.Contains(strings.TrimSuffix(prefix, "/"), "/") {
		return sub + "/"
	}
	return last + "/"
}

// Rename implements manager.Renamer: name is owner/repo[/path] on the host of the current
// action. A per-action tag prefix follows the new path (Edits) in its written form; a
// prefix of a one-element path (`x/` of owner/repo/x) cannot tell whether a multi-element
// new path's tags use the full path or its last element, so that is rejected.
func (actionsManager) Rename(d dep.Dep, name string) (dep.Dep, error) {
	owner, rest, _ := strings.Cut(name, "/")
	repo, _, _ := strings.Cut(rest, "/")
	if owner == "" || repo == "" || strings.ContainsAny(name, "@ \t") {
		return d, fmt.Errorf("action name %q is not owner/repo[/path]", name)
	}
	for _, p := range d.NameParts {
		sub, last := subpath(d.Name)
		nsub, nlast := subpath(name)
		if p.Role == roleTagPrefix && sub == last && nsub != nlast {
			return d, fmt.Errorf("ambiguous per-action tag prefix %q for %q: %s/ or %s/", p.Text, name, nsub, nlast)
		}
	}
	host, _, _ := strings.Cut(strings.TrimPrefix(d.Source, "https://"), "/")
	d.Name, d.Source = name, "https://"+host+"/"+owner+"/"+repo
	return d, nil
}

// refPrefix returns the per-action tag prefix of an unpinned ref (`get-vault-secrets/` of
// `@get-vault-secrets/v1.3.1`): its VersionSpan starts after the prefix, which is the
// trailing path of the name plus "/" (see tagPrefixLen). "" without a prefix.
func refPrefix(d dep.Dep) string {
	if d.VersionSpan == (dep.Span{}) {
		return ""
	}
	n := d.VersionSpan.Start - d.RefSpan.Start
	return d.Name[len(d.Name)-n+1:] + "/"
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
	if strings.HasPrefix(v.Value, u.Name+"@") {
		// owner/repo[/sub]@ref written literally: replacements rewrite its name.
		d.NameParts = []dep.NamePart{{Span: dep.Span{Start: start, End: start + len(u.Name)}, Text: u.Name}}
	}
	d.InsertAt = end
	if q := manager.QuoteChar(v.Style); q != 0 && end < le && x.src[end] == q {
		d.InsertAt++
	}

	switch {
	case manager.IsHex(u.Ref, 40, 64):
		d.Digest = u.Ref
		if sp, ver, p, ok := x.commentVersion(u.Name, d.InsertAt, le); ok {
			d.Version, d.VersionSpan = ver, sp
			if s := sp.Start - p; p > 0 && d.NameParts != nil {
				d.NameParts = append(d.NameParts, dep.NamePart{Span: dep.Span{Start: s, End: sp.Start}, Text: string(x.src[s:sp.Start]), Role: roleTagPrefix})
			}
		} else {
			d.Skip = "pinned without version comment"
		}
	default:
		if _, ok := versioning.ParseTag(u.Ref); ok {
			d.Version = u.Ref
		} else if p := tagPrefixLen(u.Name, u.Ref); p > 0 {
			d.Version, d.VersionSpan = u.Ref[p:], dep.Span{Start: d.RefSpan.Start + p, End: d.RefSpan.End}
			if d.NameParts != nil {
				d.NameParts = append(d.NameParts, dep.NamePart{Span: dep.Span{Start: d.RefSpan.Start, End: d.RefSpan.Start + p}, Text: u.Ref[:p], Role: roleTagPrefix})
			}
		} else {
			d.Skip = "non-version ref"
		}
	}
	x.deps = append(x.deps, d)
}

// commentVersion finds the version token in the first comment in src[from:le]; a per-action
// tag prefix of action name (`# get-vault-secrets/v1.3.1`) is not part of it, its length is
// returned as pre.
func (x *extractor) commentVersion(name string, from, le int) (sp dep.Span, ver string, pre int, ok bool) {
	h := bytes.IndexByte(x.src[from:le], '#')
	if h < 0 {
		return dep.Span{}, "", 0, false
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
			return dep.Span{}, "", 0, false
		}
		s += a + 1
		tok = tok[a+1:]
	}
	pre = tagPrefixLen(name, tok)
	s += pre
	tok = tok[pre:]
	if _, ok := versioning.ParseTag(tok); !ok {
		return dep.Span{}, "", 0, false
	}
	return dep.Span{Start: s, End: e}, tok, pre, true
}
