// Package npmds lists the versions of npm packages from an npm registry.
//
// The registry of a package is taken from the .npmrc files of the package.json directory up
// to the scanned root and then the user's, merged as npm merges them (nearer files win per
// key; `@scope:registry=` for scoped packages, then `registry=`), then NPM_CONFIG_REGISTRY,
// then https://registry.npmjs.org. The credentials of the longest `//host/path/` prefix of
// the registry URL authenticate the request: `_authToken` as a bearer token, `_auth` or
// `username` with `_password` as basic auth; `${VAR}` in .npmrc keys and values is expanded
// from the environment.
package npmds

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/base64"
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

// Source fetches each package's abbreviated metadata once per (registry, credential, name) per run, its
// full packument at most once and only for release times and the source repository, and
// parses each directory's .npmrc once; concurrent callers share the results.
type Source struct {
	getter httpx.Getter
	getenv func(string) string

	packages datasource.Memo[string, *metadata] // location.key: abbreviated metadata (nil = failed)
	docs     datasource.Memo[string, packument] // location.key: full packument

	mu      sync.Mutex
	located map[string]location // file + "\x00" + name -> registry of the last Releases lookup
	rcs     map[string]*rcEntry // package.json directory -> effective .npmrc
	wsDirs  map[string]*wsEntry // package.json directory -> names of its workspace's packages
	wsRoots map[string]*wsEntry // workspace root directory -> names of its packages
	wsConfs nodetool.WorkspaceConfigs
}

// location is the registry (with trailing slash) and the Authorization header a dependency
// resolved against.
type location struct{ reg, auth string }

// key is the memo key of a lookup of package name at l. It covers the credential: the memos
// cache failures, so without it the 401 of a directory without a token would be handed to
// every later directory that has one, and which of the two runs first is up to the scheduler.
// The credential is hashed so that no secret ends up in a map key.
func (l location) key(name string) string {
	return l.reg + "\x00" + credID(l.auth) + "\x00" + name
}

// credID identifies a credential without keeping it; "" stands for no credential.
func credID(secret string) string {
	if secret == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(secret))
	return string(sum[:])
}

// metadata is what deps keeps of a package's abbreviated metadata document.
type metadata struct {
	rels       []datasource.Release // versions that are not deprecated
	deprecated map[string]string    // deprecated versions -> message ("" = no message); nil = none
	latest     string               // `dist-tags.latest`; "" when absent
	modified   time.Time            // `modified`; zero when absent or not RFC 3339
}

// packument is what deps keeps of a package's full packument.
type packument struct {
	rels  []datasource.Release // versions that are not deprecated
	times map[string]string    // `time`: version -> RFC 3339 publish time
	repo  string               // top-level `repository` URL as published
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
// Every 2xx answer is read, and documents are not size-capped: they are decoded as they
// arrive, and the full packuments of busy packages run to hundreds of MB.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) datasource.Datasource {
	return &Source{
		getter: httpx.Getter{Client: client, Sem: sem, MaxBody: -1, Accept2xx: true}, getenv: getenv,
		located: make(map[string]location),
		rcs:     make(map[string]*rcEntry), wsDirs: make(map[string]*wsEntry), wsRoots: make(map[string]*wsEntry),
	}
}

// Releases lists the non-deprecated versions of d's package (packageName). dir is the absolute directory of
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
	name := packageName(d)
	loc := s.locate(rc, name)
	s.mu.Lock()
	s.located[d.File+"\x00"+name] = loc
	s.mu.Unlock()
	m, err := s.load(ctx, loc, name)
	if err != nil {
		return nil, err
	}
	return m.rels, nil
}

// berryPackage is the npm package of yarn 2 and later; the yarn package stops at 1.x.
const berryPackage = "@yarnpkg/cli-dist"

// packageName is the npm package d looks up: its name, except for yarn 2 and later as the
// packageManager, engines or volta tool (Renovate's packageName), which is berryPackage.
func packageName(d dep.Dep) string {
	switch d.DepType {
	case npm.DepTypePackageManager, npm.DepTypeEngines, npm.DepTypeVolta:
		if d.Manager == npm.Name && d.Name == "yarn" && npm.YarnBerry(d.Version) {
			return berryPackage
		}
	}
	return d.Name
}

// load returns the abbreviated metadata of name in loc's registry, fetched once per
// (registry, credential, name); nil on failure.
func (s *Source) load(ctx context.Context, loc location, name string) (*metadata, error) {
	return s.packages.Do(ctx, loc.key(name), func() (*metadata, error) {
		m, err := s.fetchMetadata(ctx, loc, name)
		if err != nil {
			return nil, err
		}
		return &m, nil
	})
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
	return location{reg: reg, auth: rc.authFor(reg)}
}

// ReleaseTime implements datasource.Timer from the full packument's `time` object. The
// registry is the one of d's earlier Releases lookup (else NPM_CONFIG_REGISTRY or the default).
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	p, err := s.doc(ctx, d)
	if err != nil {
		return time.Time{}, err
	}
	v, ok := p.times[version]
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

// Deprecation implements datasource.DeprecationSource from d's earlier Releases lookup: the
// package is deprecated when its `dist-tags.latest` version is (npm deprecate on the whole
// package marks every version), else version is when it carries a `deprecated` message.
func (s *Source) Deprecation(d dep.Dep, version string) string {
	e := s.loaded(d)
	if e == nil {
		return ""
	}
	if msg, ok := e.deprecated[e.latest]; ok && e.latest != "" {
		return "package deprecated" + message(msg)
	}
	if msg, ok := e.deprecated[version]; ok {
		return "version " + version + " deprecated" + message(msg)
	}
	return ""
}

// message renders a deprecation message as a ": <msg>" suffix, "" without one.
func message(msg string) string {
	if msg = strings.TrimSpace(msg); msg == "" {
		return ""
	}
	return ": " + msg
}

// loaded returns the metadata of d's earlier Releases lookup, or nil when there was none or it
// failed. Releases of d returned before the engine asks, so its result is shared by then.
func (s *Source) loaded(d dep.Dep) *metadata {
	s.mu.Lock()
	name := packageName(d)
	loc, ok := s.located[d.File+"\x00"+name]
	s.mu.Unlock()
	if !ok {
		return nil
	}
	m, _ := s.packages.Peek(loc.key(name))
	return m
}

// SourceRepo implements datasource.SourceRepo with the full packument's top-level
// `repository` (the latest version's).
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.doc(ctx, d)
	return p.repo, err
}

// doc returns the full packument of d, fetched once per (registry, credential, name), in the registry of
// d's earlier Releases lookup (else NPM_CONFIG_REGISTRY or the default): ReleaseTime and
// SourceRepo share the one request.
func (s *Source) doc(ctx context.Context, d dep.Dep) (packument, error) {
	name := packageName(d)
	s.mu.Lock()
	loc, ok := s.located[d.File+"\x00"+name]
	s.mu.Unlock()
	if !ok {
		loc = s.locate(&npmrc{}, name)
	}
	return s.docs.Do(ctx, loc.key(name), func() (packument, error) { return s.fetch(ctx, loc, name) })
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
		ws, ok := s.wsConfs.Root(root, filepath.ToSlash(rel))
		if !ok {
			return
		}
		abs := filepath.Join(root, filepath.FromSlash(ws))
		re := s.wsEntry(s.wsRoots, abs)
		re.once.Do(func() {
			if re.names, re.err = s.wsConfs.Packages(abs); re.err != nil {
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

// config returns the effective .npmrc of dir: the .npmrc files from dir up to root, then the
// user's (userConfig), merged as npm merges its configuration — the nearer file wins per key,
// so a project .npmrc naming a registry still picks up the credentials of the user's. An
// empty config when there is none.
func (s *Source) config(dir, root string) (*npmrc, error) {
	s.mu.Lock()
	e := s.rcs[dir]
	if e == nil {
		e = &rcEntry{}
		s.rcs[dir] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		e.rc = newNpmrc()
		files := 0
		add := func(p string) bool {
			rc, err := s.readNpmrc(p)
			if err != nil {
				e.rc, e.err = newNpmrc(), err
				return false
			}
			if rc != nil {
				e.rc.merge(rc)
				files++
			}
			return true
		}
		for cur := dir; ; {
			if !add(filepath.Join(cur, ".npmrc")) {
				return
			}
			parent := filepath.Dir(cur)
			if cur == root || parent == cur {
				break
			}
			cur = parent
		}
		if p := s.userConfigPath(); p != "" && !add(p) {
			return
		}
		slog.Debug("npmrc loaded", "dir", dir, "files", files, "registry", e.rc.registry, "scopes", len(e.rc.scopes), "credentials", len(e.rc.auth))
	})
	return e.rc, e.err
}

// readNpmrc parses the .npmrc at p, nil when there is none.
func (s *Source) readNpmrc(p string) (*npmrc, error) {
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	rc, err := parseNpmrc(f, s.getenv)
	_ = f.Close()
	if err != nil {
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	httpx.NoteSource("read config", "file", p, "datasource", Name)
	return rc, nil
}

// userConfigPath is NPM_CONFIG_USERCONFIG, else $HOME/.npmrc ("" without HOME): npm reads the
// user's configuration after the directory's.
func (s *Source) userConfigPath() string {
	if p := s.getenv("NPM_CONFIG_USERCONFIG"); p != "" {
		return p
	}
	if p := s.getenv("npm_config_userconfig"); p != "" {
		return p
	}
	if home := s.getenv("HOME"); home != "" {
		return filepath.Join(home, ".npmrc")
	}
	return ""
}

// npmrc is the subset of .npmrc deps understands.
type npmrc struct {
	registry string
	scopes   map[string]string     // "@scope" -> registry
	auth     map[string]credential // "//host/path/" -> its credentials
	global   credential            // the top-level _auth, username and _password
}

func newNpmrc() *npmrc {
	return &npmrc{scopes: map[string]string{}, auth: map[string]credential{}}
}

// credential authenticates requests to a registry: a bearer token (`_authToken`), a ready
// `user:password` in base64 (`_auth`) or a user name and password (`username`, `_password`).
type credential struct{ token, basic, user, pass string }

// header returns the Authorization header value of c, "" when c holds no credential.
func (c credential) header() string {
	switch {
	case c.token != "":
		return "Bearer " + c.token
	case c.basic != "":
		return "Basic " + c.basic
	case c.user != "" || c.pass != "":
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.user+":"+c.pass))
	}
	return ""
}

// fill takes from o the fields c does not have; c is the nearer configuration, whose keys win.
func (c *credential) fill(o credential) {
	if c.token == "" {
		c.token = o.token
	}
	if c.basic == "" {
		c.basic = o.basic
	}
	if c.user == "" {
		c.user = o.user
	}
	if c.pass == "" {
		c.pass = o.pass
	}
}

// merge adds the keys of the farther configuration o that rc does not have itself.
func (rc *npmrc) merge(o *npmrc) {
	if rc.registry == "" {
		rc.registry = o.registry
	}
	for scope, reg := range o.scopes {
		if _, ok := rc.scopes[scope]; !ok {
			rc.scopes[scope] = reg
		}
	}
	for prefix, c := range o.auth {
		own := rc.auth[prefix]
		own.fill(c)
		rc.auth[prefix] = own
	}
	rc.global.fill(o.global)
}

var envRe = regexp.MustCompile(`\$\{([^}?]+)\??\}`)

// parseNpmrc reads the ini lines `key=value`, skipping blanks and ; or # comments. Values may
// be quoted; ${VAR} and ${VAR?} expand to the environment value (empty when unset). Besides
// the registries it keeps the credentials, per `//host/path/` prefix and the top-level ones
// Artifactory and Nexus write.
func parseNpmrc(r io.Reader, getenv func(string) string) (*npmrc, error) {
	rc := newNpmrc()
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
		case strings.HasPrefix(k, "//"):
			// The prefix of a credential ends at its last colon: the host may carry a port.
			i := strings.LastIndex(k, ":")
			if i < 0 {
				continue
			}
			prefix := strings.TrimSuffix(k[:i], "/") + "/"
			c := rc.auth[prefix]
			if !c.set(k[i+1:], v) {
				continue
			}
			rc.auth[prefix] = c
		default:
			rc.global.set(k, v)
		}
	}
	return rc, sc.Err()
}

// set keeps the value of the credential field, reporting whether it is one deps understands.
// `_password` is base64 as npm writes it; a value that is not base64 is taken as written, so
// a hand-edited plain password still works.
func (c *credential) set(field, v string) bool {
	switch field {
	case "_authToken":
		c.token = v
	case "_auth":
		c.basic = v
	case "username":
		c.user = v
	case "_password":
		if b, err := base64.StdEncoding.DecodeString(v); err == nil {
			v = string(b)
		}
		c.pass = v
	default:
		return false
	}
	return true
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

// authFor returns the Authorization header of registry reg: the credentials of the longest
// `//host/path/` key prefixing it, else the top-level ones; "" when there are none.
func (rc *npmrc) authFor(reg string) string {
	_, nerf, ok := strings.Cut(reg, ":")
	if !ok {
		return ""
	}
	var best string
	var cred credential
	for k, c := range rc.auth {
		if strings.HasPrefix(nerf, k) && len(k) > len(best) {
			best, cred = k, c
		}
	}
	if h := cred.header(); h != "" {
		return h
	}
	return rc.global.header()
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
		slog.DebugContext(ctx, "npm metadata fetched", "name", name, "registry", loc.reg, "authenticated", loc.auth != "",
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
		slog.DebugContext(ctx, "npm packument fetched", "name", name, "registry", loc.reg, "authenticated", loc.auth != "",
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
	g := s.getter
	g.Prepare = func(req *http.Request) error {
		req.Header.Set("Accept", accept)
		if loc.auth != "" {
			req.Header.Set("Authorization", loc.auth)
		}
		return nil
	}
	err := g.Stream(ctx, loc.reg+escapeName(name), decode)
	if errors.Is(err, httpx.ErrNotFound) {
		return fmt.Errorf("package %s not found", name)
	}
	return err
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
	var gone map[string]string
	dec := jsontext.NewDecoder(r, jsontext.AllowDuplicateNames(true))
	if err := expectObject(dec); err != nil {
		return packument{}, 0, err
	}
	seenVersions, seenTime, seenRepo := false, false, false
	for dec.PeekKind() != '}' {
		if seenVersions && seenTime && seenRepo {
			return p, len(gone), nil
		}
		key, err := dec.ReadToken()
		if err != nil {
			return packument{}, 0, err
		}
		switch name := key.String(); {
		case name == "versions":
			seenVersions = true
			p.rels, gone, err = decodeVersions(dec)
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
	return p, len(gone), nil
}

// decodeMetadataTokens decodes an abbreviated metadata document like decodeMetadata, with the
// jsontext Decoder: the reference decodeMetadata replays errors and rare shapes with. Every
// member other than the kept ones is skipped (see skipMembers). Duplicate-name checking is off,
// see decodePackument.
func decodeMetadataTokens(r io.Reader) (m metadata, deprecated int, err error) {
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
			m.rels, m.deprecated, err = decodeVersions(dec)
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
	return m, len(m.deprecated), nil
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
// deprecated and, by version, the messages of those that are ("" for `deprecated: true`);
// only one member of a version object is buffered at a time.
func decodeVersions(dec *jsontext.Decoder) (rels []datasource.Release, deprecated map[string]string, err error) {
	if err := expectObject(dec); err != nil {
		return nil, nil, err
	}
	for dec.PeekKind() != '}' {
		tok, err := dec.ReadToken()
		if err != nil {
			return nil, nil, err
		}
		v := tok.String() // tokens are only valid until the next read
		msg, gone, err := versionDeprecated(dec)
		if err != nil {
			return nil, nil, err
		}
		if gone {
			if deprecated == nil {
				deprecated = make(map[string]string)
			}
			deprecated[v] = msg
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
// it deprecated, with the message. The other members are consumed with ReadValue: unlike
// unmarshaling into a struct, which skips unknown members token by token, it scans a whole
// value at once.
func versionDeprecated(dec *jsontext.Decoder) (string, bool, error) {
	if dec.PeekKind() != '{' {
		var meta struct {
			Deprecated any `json:"deprecated"`
		}
		return "", false, json.UnmarshalDecode(dec, &meta) // null, or the unmarshal error
	}
	if _, err := dec.ReadToken(); err != nil {
		return "", false, err
	}
	var deprecated *any // allocated only for versions that have the member
	for dec.PeekKind() != '}' {
		name, err := dec.ReadToken()
		if err != nil {
			return "", false, err
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
			return "", false, err
		}
	}
	if _, err := dec.ReadToken(); err != nil {
		return "", false, err
	}
	if deprecated == nil {
		return "", false, nil
	}
	msg, gone := isDeprecated(*deprecated)
	return msg, gone, nil
}

// isDeprecated reports whether a version's `deprecated` value marks it deprecated (a
// non-empty message or true) and returns the message.
func isDeprecated(v any) (string, bool) {
	switch d := v.(type) {
	case string:
		return d, d != ""
	case bool:
		return "", d
	}
	return "", false
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
