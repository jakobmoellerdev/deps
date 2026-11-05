// Package precommit extracts the hook repositories of pre-commit configurations
// (`.pre-commit-config.yaml`): each `repos[]` entry with a `repo` URL and a `rev`.
package precommit

import (
	"bytes"
	"errors"
	"net/url"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "pre-commit"

var fileRe = regexp.MustCompile(`(^|/)\.pre-commit-config\.ya?ml$`)

type precommitManager struct{}

// New returns the pre-commit manager.
func New() manager.Manager { return precommitManager{} }

func (precommitManager) Name() string { return Name }

// Match reports whether the slash path is a pre-commit configuration.
func (precommitManager) Match(path string) bool { return fileRe.MatchString(path) }

// Extract returns one dep per `repos[]` entry with byte spans into src.
func (precommitManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	top := root.Content[0]
	if top.Kind != yaml.MappingNode {
		return nil, nil
	}
	var deps []dep.Dep
	for i := 0; i+1 < len(top.Content); i += 2 {
		if top.Content[i].Value != "repos" || top.Content[i+1].Kind != yaml.SequenceNode {
			continue
		}
		for _, e := range top.Content[i+1].Content {
			if d, ok := extract(file, src, e); ok {
				deps = append(deps, d)
			}
		}
	}
	return deps, nil
}

// Edits rewrites the rev: tag → tag, frozen SHA → new SHA plus comment version, or
// tag → SHA with " # frozen: <version>" inserted after the value.
func (precommitManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	switch {
	case u.NewDigest == "":
		if d.Digest != "" {
			return nil, errors.New("cannot unpin a frozen pre-commit rev")
		}
		return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
	case d.Digest != "":
		edits := []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Digest, New: u.NewDigest}}
		if u.NewVersion != d.Version {
			edits = append(edits, patch.Edit{Start: d.VersionSpan.Start, End: d.VersionSpan.End, Old: d.Version, New: u.NewVersion})
		}
		return edits, nil
	}
	return []patch.Edit{
		{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewDigest},
		{Start: d.InsertAt, End: d.InsertAt, New: " # frozen: " + u.NewVersion},
	}, nil
}

func extract(file string, src []byte, e *yaml.Node) (dep.Dep, bool) {
	if e.Kind != yaml.MappingNode {
		return dep.Dep{}, false
	}
	var repo, rev *yaml.Node
	for i := 0; i+1 < len(e.Content); i += 2 {
		switch v := e.Content[i+1]; e.Content[i].Value {
		case "repo":
			repo = v
		case "rev":
			rev = v
		}
	}
	if repo == nil || repo.Kind != yaml.ScalarNode {
		return dep.Dep{}, false
	}
	d := dep.Dep{Manager: Name, Datasource: "git-tags", Versioning: "tag", DepType: "repository", File: file, Line: repo.Line}
	switch repo.Value {
	case "local":
		d.Name, d.Skip = repo.Value, "local repository"
		return d, true
	case "meta":
		d.Name, d.Skip = repo.Value, "meta repository"
		return d, true
	}
	d.Name, d.Source = parseRepo(repo.Value)
	if d.Source == "" {
		d.Name, d.Skip = repo.Value, "unrecognized repo"
		return d, true
	}
	if rev == nil || rev.Kind != yaml.ScalarNode || rev.Value == "" {
		d.Skip = "no rev"
		return d, true
	}
	d.Line = rev.Line
	if rev.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		d.Skip = "cannot locate value"
		return d, true
	}
	ls, le := line(src, rev.Line)
	start := -1
	if ls >= 0 {
		l := src[ls:le]
		if k := bytes.Index(l, []byte("rev")); k >= 0 {
			if i := bytes.Index(l[k+3:], []byte(rev.Value)); i >= 0 {
				start = ls + k + 3 + i
			}
		}
	}
	if start < 0 {
		d.Skip = "cannot locate value"
		return d, true
	}
	end := start + len(rev.Value)
	d.RefSpan = dep.Span{Start: start, End: end}
	d.InsertAt = end
	if q := quoteChar(rev.Style); q != 0 && end < le && src[end] == q {
		d.InsertAt++
	}
	switch {
	case isSHA(rev.Value):
		d.Digest = rev.Value
		if sp, ver, ok := commentVersion(src, d.InsertAt, le); ok {
			d.Version, d.VersionSpan = ver, sp
		} else {
			d.Skip = "pinned without version comment"
		}
	default:
		if _, ok := versioning.ParseTag(rev.Value); ok {
			d.Version = rev.Value
		} else {
			d.Skip = "non-version ref"
		}
	}
	return d, true
}

// parseRepo turns an https/http/ssh/scp-style git URL into the owner/repo path and the
// https source git-tags expects; source is "" when the URL is not recognized.
func parseRepo(s string) (name, source string) {
	var host, p string
	switch {
	case strings.Contains(s, "://"):
		u, err := url.Parse(s)
		if err != nil || u.Host == "" {
			return "", ""
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
		default:
			return "", ""
		}
		host, p = u.Hostname(), u.Path
	default: // git@host:owner/repo
		at := strings.IndexByte(s, '@')
		c := strings.IndexByte(s, ':')
		if c < 0 || at > c {
			return "", ""
		}
		host, p = s[at+1:c], s[c+1:]
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if host == "" || !strings.Contains(p, "/") {
		return "", ""
	}
	return p, "https://" + host + "/" + p
}

// line returns [start,end) of 1-based line n, excluding "\r\n".
func line(src []byte, n int) (int, int) {
	s := 0
	for range n - 1 {
		i := bytes.IndexByte(src[s:], '\n')
		if i < 0 {
			return -1, -1
		}
		s += i + 1
	}
	e := len(src)
	if i := bytes.IndexByte(src[s:], '\n'); i >= 0 {
		e = s + i
	}
	if e > s && src[e-1] == '\r' {
		e--
	}
	return s, e
}

// commentVersion finds the version in the first comment in src[from:le]:
// `# frozen: v1.2.3` (pre-commit autoupdate --freeze) or `# v1.2.3`.
func commentVersion(src []byte, from, le int) (dep.Span, string, bool) {
	h := bytes.IndexByte(src[from:le], '#')
	if h < 0 {
		return dep.Span{}, "", false
	}
	s := skipBlank(src, from+h+1, le)
	if bytes.HasPrefix(src[s:le], []byte("frozen:")) {
		s = skipBlank(src, s+len("frozen:"), le)
	}
	e := s
	for e < le && src[e] != ' ' && src[e] != '\t' {
		e++
	}
	tok := string(src[s:e])
	if _, ok := versioning.ParseTag(tok); !ok {
		return dep.Span{}, "", false
	}
	return dep.Span{Start: s, End: e}, tok, true
}

func skipBlank(src []byte, i, le int) int {
	for i < le && (src[i] == ' ' || src[i] == '\t') {
		i++
	}
	return i
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

func isSHA(s string) bool {
	if len(s) != 40 {
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
