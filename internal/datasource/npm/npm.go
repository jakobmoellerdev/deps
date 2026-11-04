// Package npmds lists the versions of npm packages from an npm registry.
//
// The registry of a package is taken from the nearest .npmrc, walking up from the
// package.json directory to the scanned root (`@scope:registry=` for scoped packages, then
// `registry=`), then NPM_CONFIG_REGISTRY, then https://registry.npmjs.org. A
// `//host/path/:_authToken=` entry whose path prefixes the registry URL authenticates the
// request; `${VAR}` in .npmrc keys and values is expanded from the environment.
package npmds

import (
	"bufio"
	"cmp"
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/npm"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/nodetool"
)

// Name is the datasource name.
const Name = "npm"

// DefaultRegistry is used when neither .npmrc nor NPM_CONFIG_REGISTRY sets a registry.
const DefaultRegistry = "https://registry.npmjs.org/"

// abbreviated selects the abbreviated ("corgi") metadata document: versions without readmes.
const abbreviated = "application/vnd.npm.install-v1+json"

// Source fetches package metadata once per (registry, name) per run and parses each
// directory's .npmrc once; concurrent callers share the results.
type Source struct {
	client *http.Client
	sem    chan struct{}
	getenv func(string) string

	mu       sync.Mutex
	packages map[string]*entry    // registry + "\x00" + name
	docs     map[string]*docEntry // registry + "\x00" + name: full packument subset
	located  map[string]location  // file + "\x00" + name -> registry of the last Releases lookup
	rcs      map[string]*rcEntry  // package.json directory -> effective .npmrc
	wsDirs   map[string]*wsEntry  // package.json directory -> names of its workspace's packages
	wsRoots  map[string]*wsEntry  // workspace root directory -> names of its packages
}

// location is the registry (with trailing slash) and token a dependency resolved against.
type location struct{ reg, token string }

// docEntry is what deps keeps of a full packument: the top-level `time` and `repository`.
type docEntry struct {
	once  sync.Once
	times map[string]string // version -> RFC 3339 publish time
	repo  string            // repository URL as published
	err   error
}

type entry struct {
	once sync.Once
	rels []datasource.Release
	err  error
}

type rcEntry struct {
	once sync.Once
	rc   *npmrc
	err  error
}

type wsEntry struct {
	once  sync.Once
	names map[string]bool
	err   error
}

// New returns the npm datasource. sem bounds concurrent requests and is shared with the other
// datasources; getenv supplies NPM_CONFIG_REGISTRY and the .npmrc ${VAR} expansions.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) datasource.Datasource {
	return &Source{
		client: client, sem: sem, getenv: getenv,
		packages: make(map[string]*entry), docs: make(map[string]*docEntry), located: make(map[string]location),
		rcs: make(map[string]*rcEntry), wsDirs: make(map[string]*wsEntry), wsRoots: make(map[string]*wsEntry),
	}
}

// Releases lists the non-deprecated versions of d.Name. dir is the absolute directory of
// d.File; the scanned root is derived from it and d.File. A dependency on a package of the
// manifest's own workspace (linked locally whatever its range) has no releases. The slice is
// shared between callers and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	root := rootOf(d.File, dir)
	if d.DepType != npm.DepTypePackageManager {
		names, err := s.workspacePackages(dir, root)
		if err != nil {
			return nil, err
		}
		if names[d.Name] {
			slog.DebugContext(ctx, "workspace member", "name", d.Name, "file", d.File)
			return nil, nil
		}
	}
	rc, err := s.config(dir, root)
	if err != nil {
		return nil, err
	}
	loc := s.locate(rc, d.Name)
	key := loc.reg + "\x00" + d.Name
	s.mu.Lock()
	s.located[d.File+"\x00"+d.Name] = loc
	e := s.packages[key]
	if e == nil {
		e = &entry{}
		s.packages[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.rels, e.err = s.fetch(ctx, loc.reg, d.Name, loc.token) })
	return e.rels, e.err
}

// locate returns the registry of package name: rc's, then NPM_CONFIG_REGISTRY, then the default.
func (s *Source) locate(rc *npmrc, name string) location {
	reg := rc.registryFor(name)
	if reg == "" {
		reg = s.getenv("NPM_CONFIG_REGISTRY")
	}
	if reg == "" {
		reg = s.getenv("npm_config_registry")
	}
	if reg == "" {
		reg = DefaultRegistry
	}
	reg = strings.TrimSuffix(reg, "/") + "/"
	return location{reg: reg, token: rc.tokenFor(reg)}
}

// ReleaseTime implements datasource.Timer from the `time` object of the full packument. The
// registry is the one of d's earlier Releases lookup (else NPM_CONFIG_REGISTRY or the default).
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	e := s.doc(ctx, d)
	if e.err != nil {
		return time.Time{}, e.err
	}
	v, ok := e.times[version]
	if !ok {
		return time.Time{}, nil
	}
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		return time.Time{}, fmt.Errorf("package %s: time of %s: %w", d.Name, version, err)
	}
	return t, nil
}

// SourceRepo implements datasource.SourceRepo with the packument's top-level `repository`
// (the latest version's), fetched together with ReleaseTime.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	e := s.doc(ctx, d)
	return e.repo, e.err
}

// doc returns the packument subset of d, fetched once per (registry, name).
func (s *Source) doc(ctx context.Context, d dep.Dep) *docEntry {
	s.mu.Lock()
	loc, ok := s.located[d.File+"\x00"+d.Name]
	if !ok {
		loc = s.locate(&npmrc{}, d.Name)
	}
	key := loc.reg + "\x00" + d.Name
	e := s.docs[key]
	if e == nil {
		e = &docEntry{}
		s.docs[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.times, e.repo, e.err = s.fetchDoc(ctx, loc, d.Name) })
	return e
}

// Digest is unsupported: npm versions are not digest-pinned.
func (*Source) Digest(context.Context, dep.Dep, string) (string, error) {
	return "", errors.New("npm: digests are not supported")
}

// workspacePackages returns the package names of the workspace containing the package.json
// directory dir (nil outside a workspace); the workspace root is looked up once per directory
// and its packages are read once per root.
func (s *Source) workspacePackages(dir, root string) (map[string]bool, error) {
	e := s.wsEntry(s.wsDirs, dir)
	e.once.Do(func() {
		rel, err := filepath.Rel(root, dir)
		if err != nil {
			e.err = err
			return
		}
		ws, ok := nodetool.WorkspaceRoot(root, filepath.ToSlash(rel))
		if !ok {
			return
		}
		abs := filepath.Join(root, filepath.FromSlash(ws))
		re := s.wsEntry(s.wsRoots, abs)
		re.once.Do(func() {
			if re.names, re.err = nodetool.WorkspacePackages(abs); re.err != nil {
				re.err = fmt.Errorf("workspace %s: %w", abs, re.err)
			}
		})
		e.names, e.err = re.names, re.err
	})
	return e.names, e.err
}

func (s *Source) wsEntry(m map[string]*wsEntry, key string) *wsEntry {
	s.mu.Lock()
	defer s.mu.Unlock()
	e := m[key]
	if e == nil {
		e = &wsEntry{}
		m[key] = e
	}
	return e
}

// rootOf returns the scanned root: dir without the directory part of the slash path file.
func rootOf(file, dir string) string {
	rel := path.Dir(file)
	if rel == "." {
		return dir
	}
	suffix := string(filepath.Separator) + filepath.FromSlash(rel)
	if strings.HasSuffix(dir, suffix) {
		return dir[:len(dir)-len(suffix)]
	}
	return dir
}

// config returns the nearest .npmrc of dir, looking no higher than root; an empty config when
// there is none.
func (s *Source) config(dir, root string) (*npmrc, error) {
	s.mu.Lock()
	e := s.rcs[dir]
	if e == nil {
		e = &rcEntry{}
		s.rcs[dir] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		e.rc = &npmrc{}
		for cur := dir; ; {
			p := filepath.Join(cur, ".npmrc")
			f, err := os.Open(p)
			if err == nil {
				e.rc, e.err = parseNpmrc(f, s.getenv)
				_ = f.Close()
				if e.err != nil {
					e.err = fmt.Errorf("%s: %w", p, e.err)
				}
				slog.Debug("npmrc loaded", "path", p, "registry", e.rc.registry, "scopes", len(e.rc.scopes), "tokens", len(e.rc.tokens))
				return
			}
			if !errors.Is(err, fs.ErrNotExist) {
				e.err = err
				return
			}
			parent := filepath.Dir(cur)
			if cur == root || parent == cur {
				return
			}
			cur = parent
		}
	})
	return e.rc, e.err
}

// npmrc is the subset of .npmrc deps understands.
type npmrc struct {
	registry string
	scopes   map[string]string // "@scope" -> registry
	tokens   map[string]string // "//host/path/" -> token
}

var envRe = regexp.MustCompile(`\$\{([^}?]+)\??\}`)

// parseNpmrc reads the ini lines `key=value`, skipping blanks and ; or # comments. Values may
// be quoted; ${VAR} and ${VAR?} expand to the environment value (empty when unset).
func parseNpmrc(r io.Reader, getenv func(string) string) (*npmrc, error) {
	rc := &npmrc{scopes: map[string]string{}, tokens: map[string]string{}}
	expand := func(s string) string {
		return envRe.ReplaceAllStringFunc(s, func(m string) string { return getenv(envRe.FindStringSubmatch(m)[1]) })
	}
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line[0] == ';' || line[0] == '#' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = expand(strings.TrimSpace(k)), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		v = expand(v)
		switch {
		case k == "registry":
			rc.registry = v
		case strings.HasPrefix(k, "@") && strings.HasSuffix(k, ":registry"):
			rc.scopes[strings.TrimSuffix(k, ":registry")] = v
		case strings.HasPrefix(k, "//") && strings.HasSuffix(k, ":_authToken"):
			rc.tokens[strings.TrimSuffix(strings.TrimSuffix(k, ":_authToken"), "/")+"/"] = v
		}
	}
	return rc, sc.Err()
}

// registryFor returns the configured registry of package name, or "".
func (rc *npmrc) registryFor(name string) string {
	if scope, _, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		if r := rc.scopes[scope]; r != "" {
			return r
		}
	}
	return rc.registry
}

// tokenFor returns the token of the longest `//host/path/` key prefixing registry reg.
func (rc *npmrc) tokenFor(reg string) string {
	_, nerf, ok := strings.Cut(reg, ":")
	if !ok {
		return ""
	}
	var best, token string
	for k, t := range rc.tokens {
		if strings.HasPrefix(nerf, k) && len(k) > len(best) {
			best, token = k, t
		}
	}
	return token
}

// escapeName escapes a package name for the registry path: `@scope/name` → `@scope%2fname`.
func escapeName(name string) string {
	if scope, rest, ok := strings.Cut(name, "/"); ok && strings.HasPrefix(scope, "@") {
		return "@" + url.PathEscape(scope[1:]) + "%2f" + url.PathEscape(rest)
	}
	return url.PathEscape(name)
}

func (s *Source) fetch(ctx context.Context, reg, name, token string) (rels []datasource.Release, err error) {
	start, deprecated := time.Now(), 0
	defer func() {
		slog.DebugContext(ctx, "npm metadata fetched", "name", name, "registry", reg, "authenticated", token != "",
			"versions", len(rels), "deprecated", deprecated, "duration", time.Since(start), "err", err)
	}()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	target := reg + escapeName(name)
	resp, err := s.get(ctx, target, name, abbreviated, token)
	if err != nil {
		return nil, err
	}
	defer closeBody(resp)
	rels, deprecated, err = decodeVersions(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("npm registry %s: malformed response: %w", target, err)
	}
	return rels, nil
}

// get sends a GET for package name's document target and fails on a non-2xx status.
func (s *Source) get(ctx context.Context, target, name, accept, token string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("npm registry %s: %w", target, err)
	}
	req.Header.Set("Accept", accept)
	req.Header.Set("User-Agent", httpx.UserAgent())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("npm registry %s: %w", target, err)
	}
	switch {
	case resp.StatusCode == http.StatusNotFound:
		closeBody(resp)
		return nil, fmt.Errorf("package %s not found", name)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		closeBody(resp)
		return nil, fmt.Errorf("npm registry %s: HTTP %d", target, resp.StatusCode)
	}
	return resp, nil
}

// fetchDoc streams the full packument (the abbreviated one has no `time`) and keeps only its
// top-level `time` and `repository`.
func (s *Source) fetchDoc(ctx context.Context, loc location, name string) (times map[string]string, repo string, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "npm packument fetched", "name", name, "registry", loc.reg, "authenticated", loc.token != "",
			"times", len(times), "repository", repo, "duration", time.Since(start), "err", err)
	}()
	if err := s.acquire(ctx); err != nil {
		return nil, "", err
	}
	defer s.release()
	target := loc.reg + escapeName(name)
	resp, err := s.get(ctx, target, name, "application/json", loc.token)
	if err != nil {
		return nil, "", err
	}
	defer closeBody(resp)
	times, repo, err = decodeDoc(resp.Body)
	if err != nil {
		return nil, "", fmt.Errorf("npm registry %s: malformed response: %w", target, err)
	}
	return times, repo, nil
}

// decodeDoc reads the top-level `time` (version -> timestamp) and `repository` (string or
// {url}) of a packument. Every other member, notably the multi-megabyte `versions` and
// `readme`, is skipped token by token without being held in memory; decoding stops as soon as
// both members were seen.
func decodeDoc(r io.Reader) (times map[string]string, repo string, err error) {
	dec := jsontext.NewDecoder(r)
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '{' {
		return nil, "", cmp.Or(err, errors.New("not an object"))
	}
	seenTime, seenRepo := false, false
	for dec.PeekKind() != '}' && !(seenTime && seenRepo) {
		key, err := dec.ReadToken()
		if err != nil {
			return nil, "", err
		}
		switch name := key.String(); {
		case name == "time" && dec.PeekKind() == '{':
			seenTime = true
			if times, err = decodeTimes(dec); err != nil {
				return nil, "", err
			}
		case name == "repository":
			seenRepo = true
			v, err := dec.ReadValue()
			if err != nil {
				return nil, "", err
			}
			var ref struct {
				URL string `json:"url"`
			}
			if v.Kind() == '"' {
				err = json.Unmarshal(v, &repo)
			} else if v.Kind() == '{' {
				err = json.Unmarshal(v, &ref)
				repo = ref.URL
			}
			if err != nil {
				return nil, "", err
			}
		default:
			if err := dec.SkipValue(); err != nil {
				return nil, "", err
			}
		}
	}
	return times, repo, nil
}

// decodeTimes reads the `time` object; values other than strings (`unpublished` is an
// object) are skipped.
func decodeTimes(dec *jsontext.Decoder) (map[string]string, error) {
	if _, err := dec.ReadToken(); err != nil { // '{'
		return nil, err
	}
	times := map[string]string{}
	for dec.PeekKind() != '}' {
		k, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		version := k.String() // tokens are only valid until the next read
		if dec.PeekKind() != '"' {
			if err := dec.SkipValue(); err != nil {
				return nil, err
			}
			continue
		}
		v, err := dec.ReadToken()
		if err != nil {
			return nil, err
		}
		times[version] = v.String()
	}
	_, err := dec.ReadToken() // '}'
	return times, err
}

// decodeVersions streams the metadata document and returns the versions that are not
// deprecated; only one version object is buffered at a time.
func decodeVersions(r io.Reader) (rels []datasource.Release, deprecated int, err error) {
	dec := jsontext.NewDecoder(r)
	if err := expectDelim(dec, '{'); err != nil {
		return nil, 0, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, 0, err
		}
		if tok.String() != "versions" {
			if err := dec.SkipValue(); err != nil {
				return nil, 0, err
			}
			continue
		}
		if err := expectDelim(dec, '{'); err != nil {
			return nil, 0, err
		}
		for dec.PeekKind() != '}' {
			tok, err := dec.ReadToken()
			if err != nil {
				return nil, 0, err
			}
			v := tok.String() // tokens are only valid until the next read
			var meta struct {
				Deprecated any `json:"deprecated"`
			}
			if err := json.UnmarshalDecode(dec, &meta); err != nil {
				return nil, 0, err
			}
			if isDeprecated(meta.Deprecated) {
				deprecated++
				continue
			}
			rels = append(rels, datasource.Release{Version: v})
		}
		if _, err := dec.ReadToken(); err != nil {
			return nil, 0, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return nil, 0, err
	}
	return rels, deprecated, nil
}

// isDeprecated reports whether a version's `deprecated` value marks it deprecated: a
// non-empty message or true.
func isDeprecated(v any) bool {
	switch d := v.(type) {
	case string:
		return d != ""
	case bool:
		return d
	}
	return false
}

func expectDelim(dec *jsontext.Decoder, d jsontext.Kind) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != d {
		return fmt.Errorf("expected %v, got %v", d, tok.Kind())
	}
	return nil
}

func (s *Source) acquire(ctx context.Context) error {
	if s.sem == nil {
		return nil
	}
	select {
	case s.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *Source) release() {
	if s.sem != nil {
		<-s.sem
	}
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
