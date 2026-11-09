// Package gitsubmodules extracts the submodules of `.gitmodules` files: each `[submodule]` with
// a `url`, its `path` and optional `branch`, whose commit is the submodule's gitlink in the git
// index.
package gitsubmodules

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Name is the manager name.
const Name = "git-submodules"

// DepTypeSubmodule is the dep type of every submodule.
const DepTypeSubmodule = "submodule"

// Datasource names: git-tags for a `branch` that is a version tag, git-refs for a branch.
const (
	dsTags = "git-tags"
	dsRefs = "git-refs"
)

// defaultBranch is the version of a submodule without `branch`: the remote's HEAD.
const defaultBranch = "HEAD"

// submodule is one `[submodule "<name>"]` section.
type submodule struct {
	name              string
	line              int // of the section header
	path, url, branch value
	gitlink           string // path joined to the file's directory; "" when invalid
}

// value is a variable of a section; the last assignment wins, as for `git config --get`.
type value struct {
	s    string
	set  bool
	line int
	span dep.Span // bytes of s in the file; zero when s was escaped or continued
}

type submodulesManager struct{}

// New returns the git-submodules manager.
func New() manager.Manager { return submodulesManager{} }

func (submodulesManager) Name() string { return Name }

// Match reports whether the slash path is a `.gitmodules` file. A suffix check, not a regexp:
// discovery calls Match on every path of the repository.
func (submodulesManager) Match(p string) bool {
	return p == ".gitmodules" || strings.HasSuffix(p, "/.gitmodules")
}

// Extract returns one dep per `[submodule]` section, its digest the gitlink commit at the
// submodule's path (relative to the file's directory) in the git index.
func (submodulesManager) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	subs := parse(src)
	if len(subs) == 0 {
		return nil, nil
	}
	dir := path.Dir(file)
	paths := make([]string, 0, len(subs))
	for i := range subs {
		s := &subs[i]
		if p := s.path.s; p != "" && !path.IsAbs(p) {
			if s.gitlink = path.Join(dir, p); s.gitlink == ".." || strings.HasPrefix(s.gitlink, "../") {
				s.gitlink = ""
			} else {
				paths = append(paths, s.gitlink)
			}
		}
	}
	links := map[string]string{}
	if len(paths) > 0 && env.Gitlinks != nil {
		var err error
		if links, err = env.Gitlinks(paths); err != nil {
			return nil, err
		}
	}
	deps := make([]dep.Dep, 0, len(subs))
	for _, s := range subs {
		d, err := s.dep(file, links, env.Superproject)
		if err != nil {
			return nil, err
		}
		deps = append(deps, d)
	}
	return deps, nil
}

// dep turns s into a dep; links are the gitlink commits by path, and superproject reports
// the origin and branch relative urls and `branch = .` resolve against (nil = unknown).
func (s submodule) dep(file string, links map[string]string, superproject func() (string, string, error)) (dep.Dep, error) {
	d := dep.Dep{Manager: Name, Datasource: dsRefs, Versioning: "tag", DepType: DepTypeSubmodule, File: file, Line: s.line, Name: s.name, Gitlink: s.gitlink}
	repoURL := s.url.s
	relative := strings.HasPrefix(repoURL, "./") || strings.HasPrefix(repoURL, "../")
	needsSuper := relative || s.branch.set && s.branch.s == "."
	var origin, branch string
	if needsSuper && superproject != nil {
		var err error
		if origin, branch, err = superproject(); err != nil {
			return d, err
		}
	}
	switch {
	case repoURL == "":
		d.Skip = "no url"
		return d, nil
	case relative:
		abs, ok := resolveURL(origin, repoURL)
		if !ok {
			d.Name, d.Skip = repoURL, "relative url without a URL origin remote"
			return d, nil
		}
		repoURL = abs
	}
	if d.Name, d.Source = manager.GitRepo(repoURL); d.Source == "" {
		d.Name, d.Skip = s.url.s, "unrecognized url"
		return d, nil
	}
	switch {
	case s.path.s == "":
		d.Skip = "no path"
		return d, nil
	case s.gitlink == "":
		d.Skip = "invalid path"
		return d, nil
	}
	commit, ok := links[s.gitlink]
	if !ok {
		d.Skip = "not a gitlink in the git index"
		return d, nil
	}
	d.Digest, d.Version = commit, defaultBranch
	switch b := s.branch; {
	case !b.set:
	case b.s == ".":
		// Renovate: the superproject's current branch.
		d.Line = b.line
		if d.Version = branch; branch == "" {
			d.Version, d.Skip = b.s, "branch = . with a detached HEAD"
		}
	case validTag(b.s):
		d.Datasource, d.Version, d.Line = dsTags, b.s, b.line
		if b.span == (dep.Span{}) {
			d.Skip = "cannot locate branch value"
		}
		d.RefSpan = b.span
	default:
		d.Version, d.Line = b.s, b.line
	}
	return d, nil
}

// resolveURL resolves a submodule url relative to the superproject's origin as Renovate does
// (WHATWG `new URL(rel, origin + "/")`): only an origin with a scheme and host qualifies, so
// scp-style (`git@host:path`) and local origins do not.
func resolveURL(origin, rel string) (string, bool) {
	base, err := url.Parse(origin + "/")
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", false
	}
	ref, err := url.Parse(rel)
	if err != nil {
		return "", false
	}
	return base.ResolveReference(ref).String(), true
}

// GitSuperproject returns an Env.Superproject reading, once, `remote.origin.url` and the
// checked-out branch of the work tree containing root; either is "" when unset or detached.
func GitSuperproject(root string) func() (origin, branch string, err error) {
	type result struct {
		origin, branch string
		err            error
	}
	get := sync.OnceValue(func() result {
		origin, err := gitOutput(root, "config", "--get", "remote.origin.url")
		if err != nil {
			return result{err: err}
		}
		branch, err := gitOutput(root, "symbolic-ref", "-q", "--short", "HEAD")
		return result{origin, branch, err}
	})
	return func() (string, string, error) {
		r := get()
		return r.origin, r.branch, r.err
	}
}

// gitOutput runs git in dir and returns its trimmed output; exit status 1 (unset, detached) is
// "" without an error.
func gitOutput(dir string, args ...string) (string, error) {
	// Extract has no context; the commands read local state only.
	cmd := exec.CommandContext(context.Background(), "git", args...)
	cmd.Dir = dir
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok && ee.ExitCode() == 1 {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// Edits rewrites the `branch` tag of a submodule tracking version tags; the gitlink moves to
// u.NewDigest with every update (engine.Build, dep.Dep.Gitlink).
func (submodulesManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if u.NewVersion == d.Version {
		return nil, nil
	}
	if d.Datasource != dsTags || d.RefSpan == (dep.Span{}) {
		return nil, errors.New("cannot change the branch of a submodule tracking a branch")
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// IndexGitlinks returns an Env.Gitlinks reading the git index of the work tree containing root
// (`git ls-files --stage`); paths are relative to root.
func IndexGitlinks(root string) func(paths []string) (map[string]string, error) {
	return func(paths []string) (map[string]string, error) {
		args := make([]string, 0, 4+len(paths))
		args = append(args, "ls-files", "--stage", "-z", "--")
		for _, p := range paths {
			args = append(args, ":(literal)"+p)
		}
		// Extract has no context; the command reads the local index only.
		cmd := exec.CommandContext(context.Background(), "git", args...)
		cmd.Dir = root
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("git ls-files: %w: %s", err, strings.TrimSpace(stderr.String()))
		}
		return parseStage(out), nil
	}
}

// parseStage returns the gitlinks (mode 160000) of `git ls-files --stage -z` output by path:
// records "<mode> <object> <stage>\t<path>\x00".
func parseStage(out []byte) map[string]string {
	links := map[string]string{}
	for rec := range bytes.SplitSeq(out, []byte{0}) {
		meta, p, ok := bytes.Cut(rec, []byte{'\t'})
		mode, rest, _ := bytes.Cut(meta, []byte{' '})
		if !ok || string(mode) != "160000" {
			continue
		}
		oid, _, _ := bytes.Cut(rest, []byte{' '})
		links[string(p)] = string(oid)
	}
	return links
}

// parse reads the submodule sections of a git config file. Other sections, malformed lines and
// variables other than path, url and branch are ignored.
func parse(src []byte) []submodule {
	var subs []submodule
	var cur *submodule
	for n, start := 1, 0; start < len(src); n++ {
		end := len(src)
		if i := bytes.IndexByte(src[start:], '\n'); i >= 0 {
			end = start + i
		}
		l := src[start:end]
		l = bytes.TrimSuffix(l, []byte{'\r'})
		i := skipBlank(l, 0)
		switch {
		case i == len(l) || l[i] == '#' || l[i] == ';':
		case l[i] == '[':
			cur = nil
			if name, ok := header(l[i+1:]); ok {
				subs = append(subs, submodule{name: name, line: n})
				cur = &subs[len(subs)-1]
			}
		case cur != nil:
			cur.variable(l, i, start, n)
		}
		start = end + 1
	}
	return subs
}

// header returns the subsection of a `submodule "<name>"]` header (after `[`); the deprecated
// `submodule.<name>]` form is accepted too.
func header(h []byte) (string, bool) {
	i := skipBlank(h, 0)
	j := i
	for j < len(h) && (isKeyChar(h[j]) || h[j] == '.') {
		j++
	}
	sec := string(h[i:j])
	if old, ok := strings.CutPrefix(strings.ToLower(sec), "submodule."); ok {
		return old, old != "" && j < len(h) && h[j] == ']'
	}
	if !strings.EqualFold(sec, "submodule") {
		return "", false
	}
	j = skipBlank(h, j)
	if j == len(h) || h[j] != '"' {
		return "", false
	}
	var name strings.Builder
	for j++; j < len(h) && h[j] != '"'; j++ {
		if h[j] == '\\' && j+1 < len(h) {
			j++
		}
		name.WriteByte(h[j])
	}
	if j == len(h) {
		return "", false
	}
	j = skipBlank(h, j+1)
	return name.String(), j < len(h) && h[j] == ']'
}

// variable records a `key = value` line of the section; l starts at byte offset off of the file,
// its key at i.
func (s *submodule) variable(l []byte, i, off, line int) {
	k := i
	for k < len(l) && isKeyChar(l[k]) {
		k++
	}
	var v *value
	switch strings.ToLower(string(l[i:k])) {
	case "path":
		v = &s.path
	case "url":
		v = &s.url
	case "branch":
		v = &s.branch
	default:
		return
	}
	k = skipBlank(l, k)
	if k == len(l) || l[k] != '=' {
		return
	}
	*v = parseValue(l, skipBlank(l, k+1), off, line)
}

// parseValue reads a git config value from l[i:] (i past the blanks after `=`): double quotes
// group, `\` escapes, `#` and `;` outside quotes start a comment, and trailing blanks outside
// quotes are dropped. The span is set when the value is written plainly: unquoted or wholly
// quoted, without escapes.
func parseValue(l []byte, i, off, line int) value {
	var b []byte
	keep := 0          // len(b) without trailing unquoted blanks
	first, end := i, i // the value's bytes as written, quotes included, up to the last kept byte
	quoted, escaped := false, false
	quotes := 0
loop:
	for ; i < len(l); i++ {
		switch c := l[i]; {
		case !quoted && (c == '#' || c == ';'):
			break loop
		case !quoted && (c == ' ' || c == '\t'):
			b = append(b, c)
			continue
		case c == '"':
			quoted = !quoted
			quotes++
		case c == '\\':
			escaped = true
			if i+1 == len(l) { // continuation lines are not supported
				break loop
			}
			i++
			switch c = l[i]; c {
			case 'n':
				c = '\n'
			case 't':
				c = '\t'
			}
			b = append(b, c)
		default:
			b = append(b, c)
		}
		keep, end = len(b), i+1
	}
	v := value{s: string(b[:keep]), set: true, line: line}
	switch {
	case escaped || v.s == "":
	case quotes == 0:
		v.span = dep.Span{Start: off + first, End: off + end}
	case quotes == 2 && l[first] == '"' && l[end-1] == '"' && end-first-2 == len(v.s):
		v.span = dep.Span{Start: off + first + 1, End: off + end - 1}
	}
	return v
}

func isKeyChar(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-'
}

func validTag(s string) bool {
	_, ok := versioning.ParseTag(s)
	return ok
}

func skipBlank(l []byte, i int) int {
	for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	return i
}
