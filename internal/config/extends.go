package config

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// DefaultExtendsPath is the file an extends entry without //<path> reads.
const DefaultExtendsPath = ".deps.yaml"

// Extend is one extends entry, written <repo>[//<path>][#<ref>].
type Extend struct {
	// Repo is owner/name of a repository on the forge of the checkout, or a git URL
	// (https://, http://, ssh://, git://, file:// or user@host:path).
	Repo string
	Path string // slash path of the file in the repository; DefaultExtendsPath when not written
	Ref  string // branch, tag or commit; "" = the repository's default branch
}

var (
	// extendRepoRe is owner/name, with GitLab-style subgroups (group/sub/name).
	extendRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*(/[A-Za-z0-9_.][A-Za-z0-9_.-]*)+$`)
	// scpRe is git's scp-like syntax user@host:path.
	scpRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]`)
	// extendRefRe is a git ref name or commit; a leading '-' would read as an option.
	extendRefRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/+-]*$`)
)

// ParseExtend parses an extends entry.
func ParseExtend(s string) (Extend, error) {
	rest := strings.TrimSpace(s)
	var e Extend
	if i := strings.LastIndexByte(rest, '#'); i >= 0 {
		rest, e.Ref = rest[:i], rest[i+1:]
		if !extendRefRe.MatchString(e.Ref) || strings.Contains(e.Ref, "..") {
			return Extend{}, fmt.Errorf("invalid ref %q after '#'", e.Ref)
		}
	}
	start := 0
	if i := strings.Index(rest, "://"); i >= 0 {
		start = i + len("://")
	}
	e.Path = DefaultExtendsPath
	if i := strings.Index(rest[start:], "//"); i >= 0 {
		rest, e.Path = rest[:start+i], rest[start+i+2:]
		if e.Path == "" || path.IsAbs(e.Path) || path.Clean(e.Path) != e.Path || e.Path == ".." || strings.HasPrefix(e.Path, "../") {
			return Extend{}, fmt.Errorf("invalid path %q after '//': must be a clean relative slash path", e.Path)
		}
	}
	e.Repo = rest
	switch {
	case start > 0:
		u, err := url.Parse(rest)
		if err != nil {
			return Extend{}, fmt.Errorf("invalid repository URL: %w", err)
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
			if u.Host == "" {
				return Extend{}, fmt.Errorf("repository URL %q has no host", rest)
			}
		case "file":
		default:
			return Extend{}, fmt.Errorf("repository URL %q: scheme must be https, http, ssh, git or file", rest)
		}
		if strings.Trim(u.Path, "/") == "" {
			return Extend{}, fmt.Errorf("repository URL %q has no path", rest)
		}
		if u.User != nil {
			if _, hasPass := u.User.Password(); hasPass {
				return Extend{}, fmt.Errorf("repository URL %q must not contain a password; the platform token or git credentials authenticate", u.Redacted())
			}
		}
	case scpRe.MatchString(rest):
	case extendRepoRe.MatchString(rest):
	default:
		return Extend{}, fmt.Errorf("repository %q must be owner/name or a git URL", rest)
	}
	return e, nil
}

// String renders e as <repo>//<path>[#<ref>], the name of the file in messages and in
// deps explain.
func (e Extend) String() string {
	s := e.Repo + "//" + e.Path
	if e.Ref != "" {
		s += "#" + e.Ref
	}
	return s
}

// isURL reports whether e.Repo is a git URL rather than owner/name.
func (e Extend) isURL() bool { return strings.Contains(e.Repo, "://") || scpRe.MatchString(e.Repo) }

// Fetcher reads the files extends entries name.
type Fetcher interface {
	// Fetch returns the content of e.Path in repository e.Repo at e.Ref.
	Fetch(ctx context.Context, e Extend) ([]byte, error)
}

// GitFetcher reads extended files with git: a shallow fetch of the ref into a temporary bare
// repository, then the file's blob. Results are cached, so a file several configs extend (a
// multi-repository run) is fetched once.
type GitFetcher struct {
	// Server returns the base URL of the forge owner/name entries are on, e.g.
	// https://github.com; it is called at most once, when such an entry is first fetched.
	Server func(ctx context.Context) (string, error)
	// Auth supplies the Basic credentials (the platform token, DEPS_GITHUB_COM_TOKEN) for the
	// host of an https repository, through a credential helper scoped to that host; git's own
	// credential helpers apply to other hosts and when Auth has none. nil = git's only.
	Auth *httpx.Auth

	mu        sync.Mutex
	server    string
	serverErr error
	serverSet bool
	cache     map[Extend]fetched
}

type fetched struct {
	data []byte
	err  error
}

// Fetch implements Fetcher.
func (g *GitFetcher) Fetch(ctx context.Context, e Extend) ([]byte, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if r, ok := g.cache[e]; ok {
		return r.data, r.err
	}
	data, err := g.fetch(ctx, e)
	if ctx.Err() == nil { // a canceled fetch is not the file's state
		if g.cache == nil {
			g.cache = map[Extend]fetched{}
		}
		g.cache[e] = fetched{data, err}
	}
	return data, err
}

// repoURL returns the git URL of e.Repo: the URL itself, or owner/name on the forge.
func (g *GitFetcher) repoURL(ctx context.Context, e Extend) (string, error) {
	if e.isURL() {
		return e.Repo, nil
	}
	if !g.serverSet {
		g.serverSet = true
		if g.Server == nil {
			g.serverErr = errors.New("no forge to resolve owner/name against")
		} else {
			g.server, g.serverErr = g.Server(ctx)
		}
	}
	if g.serverErr != nil {
		return "", fmt.Errorf("%w; write the repository as a git URL", g.serverErr)
	}
	return strings.TrimSuffix(g.server, "/") + "/" + e.Repo + ".git", nil
}

func (g *GitFetcher) fetch(ctx context.Context, e Extend) ([]byte, error) {
	u, err := g.repoURL(ctx, e)
	if err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp("", "deps-extends-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	start := time.Now()
	if _, err := gitCmd(ctx, dir, nil, "init", "--quiet", "--bare"); err != nil {
		return nil, err
	}
	ref := cmp.Or(e.Ref, "HEAD")
	if _, err := gitCmd(ctx, dir, g.credentialEnv(u), "fetch", "--quiet", "--depth=1", "--no-tags", "--", u, ref); err != nil {
		return nil, fmt.Errorf("fetch %s %s: %w", redact(u), ref, err)
	}
	data, err := gitCmd(ctx, dir, nil, "cat-file", "blob", "FETCH_HEAD:"+e.Path)
	slog.DebugContext(ctx, "extends fetched", "repo", redact(u), "ref", ref, "path", e.Path, "duration", time.Since(start), "err", err)
	if err != nil {
		return nil, fmt.Errorf("no file %s at %s of %s", e.Path, ref, redact(u))
	}
	return data, nil
}

// credentialEnv returns the environment that makes git answer the credential request for
// the host of the https URL u with Auth's static credentials: a credential helper scoped to
// scheme://host that prints them from the environment, so the token is in neither the
// command line nor a file. nil when Auth has none for the host.
func (g *GitFetcher) credentialEnv(u string) []string {
	p, err := url.Parse(u)
	if err != nil || (p.Scheme != "https" && p.Scheme != "http") {
		return nil
	}
	user, pass, ok := g.Auth.Static(p.Hostname())
	if !ok {
		return nil
	}
	helper := `!f() { test "$1" = get || return 0; echo "username=$DEPS_EXTENDS_USER"; echo "password=$DEPS_EXTENDS_TOKEN"; }; f`
	return append(gitConfigEnv(os.Getenv, "credential."+p.Scheme+"://"+p.Host+".helper", helper),
		"DEPS_EXTENDS_USER="+user, "DEPS_EXTENDS_TOKEN="+pass)
}

// gitConfigEnv appends the config key=value to those given through GIT_CONFIG_COUNT in the
// environment.
func gitConfigEnv(getenv func(string) string, key, value string) []string {
	n, _ := strconv.Atoi(getenv("GIT_CONFIG_COUNT"))
	n = max(n, 0)
	i := strconv.Itoa(n)
	return []string{"GIT_CONFIG_COUNT=" + strconv.Itoa(n+1), "GIT_CONFIG_KEY_" + i + "=" + key, "GIT_CONFIG_VALUE_" + i + "=" + value}
}

// redact drops the userinfo of a URL for messages.
func redact(u string) string {
	if p, err := url.Parse(u); err == nil && p.User != nil {
		p.User = nil
		return p.String()
	}
	return u
}

// gitCmd runs git in dir without prompts and returns stdout; a failure carries its stderr.
func gitCmd(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS="), env...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, errors.New(msg)
		}
		return nil, err
	}
	return out, nil
}

// layer is one file of the config: the local file or a file of its extends chain.
type layer struct {
	name    string // error messages: the local path, or Extend.String()
	display string // deps explain: the local path relative to --dir, or Extend.String()
	// root is the document's top-level mapping without the keys and values the decoder
	// rejected; nil for an empty document.
	root *yaml.Node
	pos  map[string]position // position of each field path in the file
	bad  []string            // fields with a malformed value; validation problems at or below them are dropped
	// via is the index of the local file's extends entry the layer was reached through;
	// -1 for the local file.
	via int
}

// position returns the 1-based line and column of field in the layer's file, or of its
// nearest ancestor present in the file; 0, 0 without one.
func (ly *layer) position(field string) (line, col int) {
	return lookupPosition(ly.pos, field)
}

// loader resolves the extends chain of a local config file.
type loader struct {
	ctx    context.Context
	fetch  Fetcher
	path   string                         // the local file
	fields map[reflect.Type][]structField // the decoders' struct keys
	// localPos are the positions of the local file, once decoded.
	localPos map[string]position
	// extends is the local file's extends list.
	extends []string
	layers  []layer // extended files first (depth first, in list order), the local file last
	// problems are the problems of the files' content; those of an extended file are located
	// at the local file's extends entry it came through (located).
	problems []*Problem
	// incomplete is set when a file of the chain could not be loaded: its configuration is
	// not validated.
	incomplete bool
	seen       map[string]bool
	stack      []string // the chain being loaded, for cycle errors
}

// add decodes src, loads the files it extends, then appends it as a layer. Problems of the
// files' content are collected; a file that cannot be read or parsed, or an extends entry
// that cannot be loaded, leaves the chain incomplete. Only a syntax error of the local file
// ends loading (*Error).
func (l *loader) add(src []byte, name, display string, via int) error {
	var doc yaml.Node
	if err := yaml.Unmarshal(src, &doc); err != nil {
		if via < 0 {
			return &Error{syntaxProblem(name, err)}
		}
		l.report(via, syntaxProblem(name, err))
		l.incomplete = true
		return nil
	}
	d := &decoder{path: name, pos: map[string]position{}, fields: l.fields}
	var root *yaml.Node
	if len(doc.Content) > 0 {
		d.walk(doc.Content[0], reflect.TypeFor[Config](), "")
		if doc.Content[0].Kind == yaml.MappingNode { // a top level of another kind was reported and nulled
			root = doc.Content[0]
		}
	}
	for _, p := range d.problems {
		l.report(via, p)
	}
	var exts []string
	if e := mapValue(root, "extends"); e != nil {
		_ = e.val.Decode(&exts) // the decoder checked it is a list of strings, or nulled it
	}
	if via < 0 {
		l.localPos, l.extends = d.pos, exts
	}
	for i, s := range exts {
		field := fmt.Sprintf("extends[%d]", i)
		if d.suppressed(field) {
			continue
		}
		entry := via // the local file's entry this file came through, or its own
		if via < 0 {
			entry = i
		}
		if msg := l.extend(s, entry); msg != "" {
			p := &Problem{Path: name, Field: field, Msg: msg}
			p.Line, p.Col = lookupPosition(d.pos, field)
			l.report(via, p)
			l.incomplete = true
		}
	}
	l.layers = append(l.layers, layer{name: name, display: display, root: root, pos: d.pos, bad: d.bad, via: via})
	return nil
}

// extend loads the file of extends entry s, reached through the local file's entry via;
// it returns why the entry cannot be loaded, or "".
func (l *loader) extend(s string, via int) string {
	e, err := ParseExtend(s)
	if err != nil {
		msg := err.Error()
		if renovatePresetRe.MatchString(s) {
			msg += " (Renovate presets are not supported; the common ones are built in, see docs/migrating-from-renovate.md#presets)"
		}
		return msg
	}
	key := e.String()
	if at := slices.Index(l.stack, key); at >= 0 {
		return "cycle: " + strings.Join(append(slices.Clone(l.stack[at:]), key), " -> ")
	}
	if l.seen[key] {
		return ""
	}
	l.seen[key] = true
	if l.fetch == nil {
		return "extends is not supported by this command"
	}
	data, err := l.fetch.Fetch(l.ctx, e)
	if err != nil {
		return fmt.Sprintf("%s: %v", key, err)
	}
	l.stack = append(l.stack, key)
	defer func() { l.stack = l.stack[:len(l.stack)-1] }()
	_ = l.add(data, key, key, via) // only the local file's syntax errors are returned
	return ""
}

// renovatePresetRe matches Renovate preset names (config:recommended, :semanticCommits,
// github>org/repo, group:monorepos), which extends entries are not.
var renovatePresetRe = regexp.MustCompile(`^(:|[A-Za-z][A-Za-z0-9-]*[:>])`)

// report records problem p of the file reached through the local file's extends entry via
// (-1: the local file itself).
func (l *loader) report(via int, p *Problem) { l.problems = append(l.problems, l.located(via, p)) }

// located returns p, a problem of the file reached through the local file's extends entry
// via (-1: the local file itself), as a problem of the local file: an extended file's
// problem is located at that entry, its message is the problem with its own file and
// position.
func (l *loader) located(via int, p *Problem) *Problem {
	if via < 0 {
		return p
	}
	out := &Problem{Path: l.path, Msg: p.Error()}
	out.Line, out.Col = lookupPosition(l.localPos, fmt.Sprintf("extends[%d]", via))
	return out
}

type mapEntry struct{ key, val *yaml.Node }

// mapValue returns the entry of key in the mapping n, or nil (also when n is no mapping).
func mapValue(n *yaml.Node, key string) *mapEntry {
	if n == nil || n.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return &mapEntry{n.Content[i], n.Content[i+1]}
		}
	}
	return nil
}

// listKeys are the top-level keys whose lists are concatenated across an extends chain,
// extended files first; mapKeys are merged by key, later files replacing entries.
var (
	listKeys = []string{"rules", "custom"}
	mapKeys  = []string{"customDatasources"}
)

// merge combines the layers into one document: a later file's top-level keys replace an
// earlier file's, except listKeys (concatenated) and mapKeys (merged by key). extends itself
// is dropped. The layers' nodes are not modified.
func merge(layers []layer) *yaml.Node {
	out := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	for _, ly := range layers {
		if ly.root == nil {
			continue
		}
		for i := 0; i+1 < len(ly.root.Content); i += 2 {
			k, v := ly.root.Content[i], ly.root.Content[i+1]
			if k.Value == "extends" {
				continue
			}
			prev := mapValue(out, k.Value)
			switch {
			case slices.Contains(listKeys, k.Value) && v.Kind == yaml.SequenceNode:
				if prev != nil && prev.val.Kind == yaml.SequenceNode {
					prev.val.Content = append(prev.val.Content, v.Content...)
					continue
				}
				v = &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: slices.Clone(v.Content)}
			case slices.Contains(mapKeys, k.Value) && v.Kind == yaml.MappingNode:
				if prev != nil && prev.val.Kind == yaml.MappingNode {
					for j := 0; j+1 < len(v.Content); j += 2 {
						if at := mapIndex(prev.val, v.Content[j].Value); at >= 0 {
							prev.val.Content[at+1] = v.Content[j+1]
						} else {
							prev.val.Content = append(prev.val.Content, v.Content[j], v.Content[j+1])
						}
					}
					continue
				}
				v = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: slices.Clone(v.Content)}
			}
			if at := mapIndex(out, k.Value); at >= 0 {
				out.Content[at+1] = v
			} else {
				out.Content = append(out.Content, k, v)
			}
		}
	}
	return out
}

// mapIndex returns the index of key's key node in the mapping n, or -1.
func mapIndex(n *yaml.Node, key string) int {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return i
		}
	}
	return -1
}

// Document returns the top-level mapping of the configuration as written: the config file's,
// or with extends the files of the chain merged as Load merges them (without extends); nil
// without a file. Its nodes are the configuration's own and must not be modified.
func (c *Config) Document() *yaml.Node {
	switch len(c.layers) {
	case 0:
		return nil
	case 1:
		return c.layers[0].root
	}
	return merge(c.layers)
}

// KeyFile returns the file the top-level key of the configuration comes from: the config
// file (Path), the name of an extended file (Extend.String()), or "" when no file sets it. Of
// the keys several files merge (rules, custom, customDatasources), it is the last such file.
func (c *Config) KeyFile(key string) string {
	for _, ly := range slices.Backward(c.layers) {
		if mapValue(ly.root, key) != nil {
			return ly.name
		}
	}
	return ""
}

// sequence returns the items of the top-level list key of a layer.
func (ly layer) sequence(key string) []*yaml.Node {
	if e := mapValue(ly.root, key); e != nil && e.val.Kind == yaml.SequenceNode {
		return e.val.Content
	}
	return nil
}

// written returns the layer field of the merged config is written in and the field as
// written there (see attribute); nil for a config not loaded from a file.
func (c *Config) written(field string) (*layer, string) {
	switch len(c.layers) {
	case 0:
		return nil, field
	case 1:
		return &c.layers[0], field
	}
	j, f := c.attribute(field)
	return &c.layers[j], f
}

// attribute returns the layer a validation field of the merged config comes from and the
// field as written in that file (rules[5] of the merged list may be rules[1] of the local
// file); -1 when the config has no extends.
func (c *Config) attribute(field string) (int, string) {
	if len(c.layers) < 2 {
		return -1, field
	}
	top := field
	if i := strings.IndexAny(top, ".["); i >= 0 {
		top = top[:i]
	}
	if slices.Contains(listKeys, top) && strings.HasPrefix(field, top+"[") {
		rest := field[len(top)+1:]
		end := strings.IndexByte(rest, ']')
		if n, err := strconv.Atoi(rest[:max(end, 0)]); end > 0 && err == nil {
			for j, ly := range c.layers {
				items := len(ly.sequence(top))
				if n < items {
					return j, fmt.Sprintf("%s[%d]%s", top, n, rest[end+1:])
				}
				n -= items
			}
		}
	}
	name := ""
	if slices.Contains(mapKeys, top) {
		name, _, _ = strings.Cut(strings.TrimPrefix(field, top+"."), ".")
	}
	for j, ly := range slices.Backward(c.layers) {
		e := mapValue(ly.root, top)
		if e == nil {
			continue
		}
		if name == "" || mapValue(e.val, name) != nil {
			return j, field
		}
	}
	return len(c.layers) - 1, field
}
