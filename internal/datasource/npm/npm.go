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

// abbreviated selects the abbreviated ("corgi") metadata document: versions without readmes,
// and no `time`.
const abbreviated = "application/vnd.npm.install-v1+json"

// Source fetches each package's abbreviated metadata once per (registry, name) per run, its
// full packument at most once and only for release times and the source repository, and
// parses each directory's .npmrc once; concurrent callers share the results.
type Source struct {
	client *http.Client
	sem    chan struct{}
	getenv func(string) string

	mu       sync.Mutex
	packages map[string]*entry    // registry + "\x00" + name: abbreviated metadata
	docs     map[string]*docEntry // registry + "\x00" + name: full packument
	located  map[string]location  // file + "\x00" + name -> registry of the last Releases lookup
	rcs      map[string]*rcEntry  // package.json directory -> effective .npmrc
	wsDirs   map[string]*wsEntry  // package.json directory -> names of its workspace's packages
	wsRoots  map[string]*wsEntry  // workspace root directory -> names of its packages
}

// location is the registry (with trailing slash) and token a dependency resolved against.
type location struct{ reg, token string }

// metadata is what deps keeps of a package's abbreviated metadata document.
type metadata struct {
	rels     []datasource.Release // versions that are not deprecated
	latest   string               // `dist-tags.latest`; "" when absent
	modified time.Time            // `modified`; zero when absent or not RFC 3339
}

// packument is what deps keeps of a package's full packument.
type packument struct {
	rels  []datasource.Release // versions that are not deprecated
	times map[string]string    // `time`: version -> RFC 3339 publish time
	repo  string               // top-level `repository` URL as published
}

type entry struct {
	once sync.Once
	metadata
	err error
}

type docEntry struct {
	once sync.Once
	packument
	err error
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

// New returns the npm datasource. sem bounds concurrent requests; it is npm's own pool
// (httpx.Limits.NPM). getenv supplies NPM_CONFIG_REGISTRY and the .npmrc ${VAR} expansions.
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
	root := datasource.RootOf(d.File, dir)
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
	s.mu.Lock()
	s.located[d.File+"\x00"+d.Name] = loc
	s.mu.Unlock()
	e := s.load(ctx, loc, d.Name)
	return e.rels, e.err
}

// load returns the abbreviated metadata of name in loc's registry, fetched once per
// (registry, name).
func (s *Source) load(ctx context.Context, loc location, name string) *entry {
	key := loc.reg + "\x00" + name
	s.mu.Lock()
	e := s.packages[key]
	if e == nil {
		e = &entry{}
		s.packages[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.metadata, e.err = s.fetchMetadata(ctx, loc, name) })
	return e
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

// ReleaseTime implements datasource.Timer from the full packument's `time` object. The
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

// ReleasedBy implements datasource.ReleaseBound with the `modified` time of d's earlier
// Releases lookup: every publish changes the document, so no version is newer. When it is old
// enough the engine skips the per-version minimumReleaseAge lookups, and with them the full
// packument.
func (s *Source) ReleasedBy(d dep.Dep) time.Time {
	if e := s.loaded(d); e != nil {
		return e.modified
	}
	return time.Time{}
}

// Latest implements datasource.LatestSource with `dist-tags.latest` of d's earlier Releases
// lookup; "" without one.
func (s *Source) Latest(d dep.Dep) string {
	if e := s.loaded(d); e != nil {
		return e.latest
	}
	return ""
}

// loaded returns the metadata of d's earlier Releases lookup, or nil when there was none or it
// failed. Releases of d completed e.once before the engine asks, so e is complete.
func (s *Source) loaded(d dep.Dep) *entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	loc, ok := s.located[d.File+"\x00"+d.Name]
	if !ok {
		return nil
	}
	if e := s.packages[loc.reg+"\x00"+d.Name]; e != nil && e.err == nil {
		return e
	}
	return nil
}

// SourceRepo implements datasource.SourceRepo with the full packument's top-level
// `repository` (the latest version's).
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	e := s.doc(ctx, d)
	return e.repo, e.err
}

// doc returns the full packument of d, fetched once per (registry, name), in the registry of
// d's earlier Releases lookup (else NPM_CONFIG_REGISTRY or the default): ReleaseTime and
// SourceRepo share the one request.
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
	e.once.Do(func() { e.packument, e.err = s.fetch(ctx, loc, d.Name) })
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

// fetchMetadata streams the abbreviated metadata of name from loc's registry.
func (s *Source) fetchMetadata(ctx context.Context, loc location, name string) (m metadata, err error) {
	start, deprecated := time.Now(), 0
	defer func() {
		slog.DebugContext(ctx, "npm metadata fetched", "name", name, "registry", loc.reg, "authenticated", loc.token != "",
			"versions", len(m.rels), "deprecated", deprecated, "latest", m.latest, "modified", m.modified, "duration", time.Since(start), "err", err)
	}()
	err = s.stream(ctx, loc, name, abbreviated, func(body io.Reader) (err error) {
		m, deprecated, err = decodeMetadata(body)
		return err
	})
	return m, err
}

// fetch streams the full packument of name from loc's registry.
func (s *Source) fetch(ctx context.Context, loc location, name string) (p packument, err error) {
	start, deprecated := time.Now(), 0
	defer func() {
		slog.DebugContext(ctx, "npm packument fetched", "name", name, "registry", loc.reg, "authenticated", loc.token != "",
			"versions", len(p.rels), "deprecated", deprecated, "times", len(p.times), "repository", p.repo, "duration", time.Since(start), "err", err)
	}()
	err = s.stream(ctx, loc, name, "application/json", func(body io.Reader) (err error) {
		p, deprecated, err = decodePackument(body)
		return err
	})
	return p, err
}

// stream GETs the document of package name from loc's registry as accept and hands its body
// to decode, holding one of the datasource's request slots.
func (s *Source) stream(ctx context.Context, loc location, name, accept string, decode func(io.Reader) error) error {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return err
	}
	defer release()
	target := loc.reg + escapeName(name)
	resp, err := s.get(ctx, target, name, accept, loc.token)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	if err := decode(resp.Body); err != nil {
		return fmt.Errorf("npm registry %s: malformed response: %w", target, err)
	}
	return nil
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

// skipMembers skips the next value. An object is skipped member by member with ReadValue,
// which scans a value much faster than SkipValue's token-by-token walk while buffering only
// one member (one version of `versions`) at a time.
func skipMembers(dec *jsontext.Decoder) error {
	if dec.PeekKind() != '{' {
		return dec.SkipValue()
	}
	if _, err := dec.ReadToken(); err != nil {
		return err
	}
	for dec.PeekKind() != '}' {
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		if _, err := dec.ReadValue(); err != nil {
			return err
		}
	}
	_, err := dec.ReadToken()
	return err
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

// decodePackument streams a packument and keeps the versions that are not deprecated, the
// `time` object and the top-level `repository`. Every other member, notably the `readme`, is
// skipped without being held in memory as a whole (see skipMembers); decoding stops as soon as
// the three were seen.
// Duplicate-name checking is off: it keeps a name set per object, which made up over 90% of
// the allocations on large packuments (typescript).
func decodePackument(r io.Reader) (p packument, deprecated int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectObject(dec); err != nil {
		return packument{}, 0, err
	}
	seenVersions, seenTime, seenRepo := false, false, false
	for dec.PeekKind() != '}' {
		if seenVersions && seenTime && seenRepo {
			return p, deprecated, nil
		}
		key, err := dec.ReadToken()
		if err != nil {
			return packument{}, 0, err
		}
		switch name := key.String(); {
		case name == "versions":
			seenVersions = true
			p.rels, deprecated, err = decodeVersions(dec)
		case name == "time" && dec.PeekKind() == '{':
			seenTime = true
			p.times, err = decodeTimes(dec)
		case name == "repository":
			seenRepo = true
			p.repo, err = decodeRepository(dec)
		default:
			err = skipMembers(dec)
		}
		if err != nil {
			return packument{}, 0, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return packument{}, 0, err
	}
	return p, deprecated, nil
}

// decodeMetadata streams an abbreviated metadata document and keeps the versions that are not
// deprecated, `dist-tags.latest` and the top-level `modified`; every other member is skipped
// (see skipMembers). Duplicate-name checking is off, see decodePackument.
func decodeMetadata(r io.Reader) (m metadata, deprecated int, err error) {
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectObject(dec); err != nil {
		return metadata{}, 0, err
	}
	for dec.PeekKind() != '}' {
		key, err := dec.ReadToken()
		if err != nil {
			return metadata{}, 0, err
		}
		switch name := key.String(); {
		case name == "versions":
			m.rels, deprecated, err = decodeVersions(dec)
		case name == "dist-tags" && dec.PeekKind() == '{':
			m.latest, err = decodeLatest(dec)
		case name == "modified" && dec.PeekKind() == '"':
			var tok jsontext.Token
			if tok, err = dec.ReadToken(); err == nil {
				m.modified, _ = time.Parse(time.RFC3339, tok.String()) // unparsable = unknown bound
			}
		default:
			err = skipMembers(dec)
		}
		if err != nil {
			return metadata{}, 0, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return metadata{}, 0, err
	}
	return m, deprecated, nil
}

// decodeLatest reads the `dist-tags` object and returns its `latest`; "" when that is absent
// or not a string.
func decodeLatest(dec *jsontext.Decoder) (latest string, err error) {
	if _, err := dec.ReadToken(); err != nil { // '{'
		return "", err
	}
	for dec.PeekKind() != '}' {
		k, err := dec.ReadToken()
		if err != nil {
			return "", err
		}
		if k.String() != "latest" || dec.PeekKind() != '"' {
			if err := dec.SkipValue(); err != nil {
				return "", err
			}
			continue
		}
		v, err := dec.ReadToken()
		if err != nil {
			return "", err
		}
		latest = v.String()
	}
	_, err = dec.ReadToken() // '}'
	return latest, err
}

// decodeVersions reads the `versions` object and returns the versions that are not
// deprecated; only one member of a version object is buffered at a time.
func decodeVersions(dec *jsontext.Decoder) (rels []datasource.Release, deprecated int, err error) {
	if err := expectObject(dec); err != nil {
		return nil, 0, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, 0, err
		}
		v := tok.String() // tokens are only valid until the next read
		gone, err := versionDeprecated(dec)
		if err != nil {
			return nil, 0, err
		}
		if gone {
			deprecated++
			continue
		}
		rels = append(rels, datasource.Release{Version: v})
	}
	_, err = dec.ReadToken()
	return rels, deprecated, err
}

// decodeRepository reads a `repository`: a string, or an object's `url`; "" for other kinds.
func decodeRepository(dec *jsontext.Decoder) (repo string, err error) {
	v, err := dec.ReadValue()
	if err != nil {
		return "", err
	}
	switch v.Kind() {
	case '"':
		err = json.Unmarshal(v, &repo)
	case '{':
		var ref struct {
			URL string `json:"url"`
		}
		err = json.Unmarshal(v, &ref)
		repo = ref.URL
	}
	return repo, err
}

// versionDeprecated reads one version object and reports whether its `deprecated` member marks
// it deprecated. The other members are consumed with ReadValue: unlike unmarshaling into a
// struct, which skips unknown members token by token, it scans a whole value at once.
func versionDeprecated(dec *jsontext.Decoder) (bool, error) {
	if dec.PeekKind() != '{' {
		var meta struct {
			Deprecated any `json:"deprecated"`
		}
		return false, json.UnmarshalDecode(dec, &meta) // null, or the unmarshal error
	}
	if _, err := dec.ReadToken(); err != nil {
		return false, err
	}
	var deprecated *any // allocated only for versions that have the member
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return false, err
		}
		if name.String() == "deprecated" {
			if deprecated == nil {
				deprecated = new(any)
			}
			err = json.UnmarshalDecode(dec, deprecated)
		} else {
			_, err = dec.ReadValue()
		}
		if err != nil {
			return false, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return false, err
	}
	return deprecated != nil && isDeprecated(*deprecated), nil
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

// expectObject reads the next token and fails unless it starts an object.
func expectObject(dec *jsontext.Decoder) error {
	tok, err := dec.ReadToken()
	if err != nil {
		return err
	}
	if tok.Kind() != '{' {
		return fmt.Errorf("expected {, got %v", tok.Kind())
	}
	return nil
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	_ = resp.Body.Close()
}
