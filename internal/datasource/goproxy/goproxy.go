// Package goproxy lists Go module versions from the GOPROXY `@v/list` endpoint, falling back to
// `go list` only for direct and GOPRIVATE/GONOPROXY modules.
package goproxy

import (
	"cmp"
	"context"
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
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/module"
	"golang.org/x/mod/semver"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Source resolves module versions once per module path per run.
type Source struct {
	getter       httpx.Getter
	env          func(ctx context.Context) (gotool.GoEnv, error)
	list         func(ctx context.Context, dir string, mods ...string) (map[string]gotool.ModuleVersions, error)
	info         func(ctx context.Context, dir, path, version string) (time.Time, error)
	latest       func(ctx context.Context, dir, path string) (gotool.Module, error)
	probeTimeout time.Duration // per proxy request of a major path probe; 0 = probeTimeout

	goEnv   datasource.Once[gotool.GoEnv]
	times   datasource.Memo[string, time.Time]           // module@version
	heads   datasource.Memo[string, datasource.Revision] // module path
	paths   datasource.Memo[string, string]              // module@version: declared module path
	cache   datasource.Memo[string, *entry]              // module path
	probes  datasource.Memo[string, []string]            // probed major module path: versions, nil = missing
	modules datasource.Memo[string, string]              // package path outside go.mod: its module path

	mu   sync.Mutex
	dirs map[string]string // module path -> directory of its first lookup, for `go list`
}

type entry struct {
	versions []string
	proxied  bool // versions came from a GOPROXY URL, not the go command
	rels     []datasource.Release
}

// New returns a Source using gotool.Env, gotool.ListVersions, gotool.ModuleTime and
// gotool.ModuleLatest. sem bounds concurrent requests (including `go list` runs) and is
// shared with other datasources.
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{
		getter: httpx.Getter{Client: client, Sem: sem}, env: gotool.Env, list: gotool.ListVersions, info: gotool.ModuleTime, latest: gotool.ModuleLatest,
		dirs: make(map[string]string),
	}
}

// Versions returns the known versions of modPath. dir is the module directory used when the
// go command has to be run. Results are deduped by modPath; the slice must not be modified.
func (s *Source) Versions(ctx context.Context, modPath, dir string) ([]string, error) {
	e, err := s.get(ctx, modPath, dir)
	return e.versions, err
}

// Releases implements datasource.Datasource for module d.Name; releases carry no digest.
// The slice is shared between callers and must not be modified. A dep outside go.mod (a mise
// `go:` tool, a pre-commit golang hook's additional dependency, a custom manager) may name a
// package, as `go install` does: it resolves to the longest prefix of d.Name that lists
// versions (see module).
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	e, err := s.get(ctx, s.module(ctx, d, dir), dir)
	return e.rels, err
}

// gomodManager is the manager whose requirements are module paths by construction.
const gomodManager = "gomod"

// module returns the module path that provides d: d.Name for go.mod requirements, else the
// longest prefix of d.Name (ending before a "/", keeping at least one "/") whose lookup lists
// versions, as `go install <package>@<version>` and mise's go backend resolve a package
// (`github.com/golangci/golangci-lint/v2/cmd/golangci-lint` → `…/golangci-lint/v2`). When no
// prefix lists any, d.Name's own lookup decides. Deduped per name; dir is the directory of
// the first lookup.
func (s *Source) module(ctx context.Context, d dep.Dep, dir string) string {
	if d.Manager == gomodManager {
		return d.Name
	}
	mod, _ := s.modules.Do(ctx, d.Name, func() (string, error) {
		prefixes := pathPrefixes(d.Name)
		s.prime(ctx, prefixes, dir)
		for _, p := range prefixes {
			if e, err := s.get(ctx, p, dir); err == nil && len(e.versions) > 0 {
				if p != d.Name {
					slog.DebugContext(ctx, "package resolved to module", "package", d.Name, "module", p)
				}
				return p, nil
			}
		}
		return d.Name, nil
	})
	return mod
}

// pathPrefixes returns pkg and the prefixes of it that can be module paths, longest first:
// those ending before a "/" that keep at least one "/".
func pathPrefixes(pkg string) []string {
	var out []string
	for p := pkg; strings.Contains(p, "/"); p = p[:strings.LastIndexByte(p, '/')] {
		out = append(out, p)
	}
	return out
}

// prime resolves paths in one `go list` run and shares each answer with their lookups, when
// the go command is what those would use anyway: a GONOPROXY/GOPRIVATE module, or a GOPROXY
// whose first entry is `direct`. A package path of five elements otherwise starts five go
// commands, each of which fetches from the module's repository. A path the run does not
// answer, an unreadable go environment and a failing run are left to the per-path lookup.
func (s *Source) prime(ctx context.Context, paths []string, dir string) {
	env, err := s.loadEnv(ctx)
	if err != nil {
		return
	}
	batch := make([]string, 0, len(paths))
	for _, p := range paths {
		if usesGoCommand(env, p) {
			batch = append(batch, p)
		}
	}
	if len(batch) < 2 {
		return
	}
	release, err := httpx.Acquire(ctx, s.getter.Sem)
	if err != nil {
		return
	}
	defer release()
	res, err := s.list(ctx, dir, batch...)
	if err != nil {
		return
	}
	for _, p := range batch {
		if m, ok := res[p]; ok {
			_, _ = s.entry(ctx, p, dir, func() ([]string, bool, error) { return m.Versions, false, m.Err })
		}
	}
}

// usesGoCommand reports whether the lookup of modPath ends at the go command: a
// GONOPROXY/GOPRIVATE module, or a GOPROXY whose first entry is `direct` and is not followed
// by a `|`, which would retry the next entry after a failure (see walk).
func usesGoCommand(env gotool.GoEnv, modPath string) bool {
	if module.MatchPrefixPatterns(env.NoProxy, modPath) {
		return true
	}
	for rest := env.Proxy; rest != ""; {
		entry, anyErr := rest, false
		if i := strings.IndexAny(rest, ",|"); i >= 0 {
			entry, anyErr, rest = rest[:i], rest[i] == '|', rest[i+1:]
		} else {
			rest = ""
		}
		if entry = strings.TrimSpace(entry); entry == "" {
			continue
		}
		return entry == "direct" && !anyErr
	}
	return false
}

// resolved returns the module an earlier Releases resolved d's package path to (see module),
// else d.Name; it looks nothing up.
func (s *Source) resolved(d dep.Dep) string {
	if d.Manager != gomodManager {
		if mod, ok := s.modules.Peek(d.Name); ok {
			return mod
		}
	}
	return d.Name
}

// Digest always fails: Go module versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, version string) (string, error) {
	return "", fmt.Errorf("%s@%s: go modules have no digests", d.Name, version)
}

// Head implements datasource.HeadSource: the module's `@latest` query (`go list -m -json
// <path>@latest` for direct and GONOPROXY/GOPRIVATE modules). For a module without a release
// newer than its pseudo-versions this is the pseudo-version of the default branch's tip.
// Digest is the commit hash (the proxy's Origin.Hash, else the pseudo-version's 12-digit
// revision). Deduped per module path; the time also answers ReleaseTime of that version.
func (s *Source) Head(ctx context.Context, d dep.Dep, dir string) (datasource.Revision, error) {
	return s.heads.Do(ctx, d.Name, func() (datasource.Revision, error) {
		rev, err := s.head(ctx, d.Name, dir)
		slog.DebugContext(ctx, "module head", "module", d.Name, "version", rev.Version, "time", rev.Time, "err", err)
		if err == nil && rev.Version != "" {
			_, _ = s.times.Do(ctx, d.Name+"@"+rev.Version, func() (time.Time, error) { return rev.Time, nil })
		}
		return rev, err
	})
}

func (s *Source) head(ctx context.Context, modPath, dir string) (datasource.Revision, error) {
	var m gotool.Module
	err := s.walk(ctx, modPath, func(base, escaped string) (notFound bool, err error) {
		if base == "" {
			release, err := httpx.Acquire(ctx, s.getter.Sem)
			if err != nil {
				return false, err
			}
			defer release()
			m, err = s.latest(ctx, dir, modPath)
			return false, err
		}
		m = gotool.Module{}
		return s.stream(ctx, base+"/"+escaped+"/@latest", maxInfo, func(r io.Reader) error { return json.UnmarshalRead(r, &m) })
	})
	if err != nil {
		return datasource.Revision{}, err
	}
	rev := datasource.Revision{Version: m.Version, Digest: m.Hash()}
	if m.Time != nil {
		rev.Time = *m.Time
	}
	if rev.Digest == "" && module.IsPseudoVersion(m.Version) {
		rev.Digest, _ = module.PseudoVersionRev(m.Version)
	}
	return rev, nil
}

// maxMajorProbes bounds the successive major module paths MajorPaths looks up per module.
const maxMajorProbes = 5

// probeTimeout bounds each proxy request of a major path probe (httpx.RequestTimeout:
// retries included, the wait for a request slot not). A probe only decides whether a major
// update is offered, while proxy.golang.org can hang on a missing path: on six large repos
// every listed path answered within 0.3 s and 99% of the 404s within 9 s, but some
// requests got no response headers in 30 s and were retried, for up to two minutes per path.
const probeTimeout = 10 * time.Second

// MajorPaths implements datasource.MajorPathSource: the module paths of the next majors of
// d (`<prefix>/v<N+1>`, … for a `/v<N>` path; `/v2`, … for an unsuffixed v0/v1 module, or
// `/v<M+1>` for one at vM.x.y+incompatible; `gopkg.in/x.v<N+1>` for gopkg.in), each listed
// from the proxies until one has no releases, at most maxMajorProbes. Only GOPROXY URLs are
// probed: `direct` and GONOPROXY/GOPRIVATE modules have no major path lookups, since each
// probe would clone the repository. Probes are deduped per path. A probe request that takes
// longer than probeTimeout is logged as a warning and counts as no release.
func (s *Source) MajorPaths(ctx context.Context, d dep.Dep, _ string) ([]datasource.ModulePath, error) {
	next, ok := nextMajorPath(d.Name, d.Version)
	var out []datasource.ModulePath
	for range maxMajorProbes {
		if !ok {
			break
		}
		vs, err := s.probe(ctx, next)
		if err != nil {
			return nil, err
		}
		if len(vs) == 0 {
			break
		}
		rels := make([]datasource.Release, len(vs))
		for i, v := range vs {
			rels[i] = datasource.Release{Version: v}
		}
		out = append(out, datasource.ModulePath{Name: next, Releases: rels})
		next, ok = nextMajorPath(next, "")
	}
	return out, nil
}

// nextMajorPath returns the module path of the major after modPath at version: `/v<N+1>`
// for a `/v<N>` (or gopkg.in `.v<N>`) path, else `/v2`, or `/v<M+1>` when version is
// vM.x.y+incompatible with M ≥ 2.
func nextMajorPath(modPath, version string) (string, bool) {
	prefix, pathMajor, ok := module.SplitPathVersion(modPath)
	if !ok {
		return "", false
	}
	sep, n := "/v", 1
	if pathMajor != "" {
		sep = pathMajor[:2]
		m, err := strconv.Atoi(pathMajor[2:])
		if err != nil {
			return "", false
		}
		n = m
	} else if strings.HasSuffix(version, "+incompatible") {
		if m, err := strconv.Atoi(strings.TrimPrefix(semver.Major(version), "v")); err == nil && m > n {
			n = m
		}
	}
	return prefix + sep + strconv.Itoa(n+1), true
}

// errNotProbed marks a `direct` GOPROXY entry, or a GONOPROXY module, during a probe.
var errNotProbed = errors.New("not probed")

// probe returns the versions of modPath from the GOPROXY URLs; nil when no proxy has it or
// a proxy did not answer within probeTimeout.
func (s *Source) probe(ctx context.Context, modPath string) ([]string, error) {
	return s.probes.Do(ctx, modPath, func() ([]string, error) {
		// A path that is a dependency itself has its own lookup of the same `@v/list`.
		if vs, ok := s.listed(ctx, modPath); ok {
			slog.DebugContext(ctx, "major module path probed", "module", modPath, "versions", len(vs), "listed", true)
			return vs, nil
		}
		var vs []string
		err := s.walk(ctx, modPath, func(base, escaped string) (notFound bool, err error) {
			if base == "" {
				return true, errNotProbed
			}
			timeout := cmp.Or(s.probeTimeout, probeTimeout)
			vs, notFound, err = s.fetch(httpx.RequestTimeout(ctx, timeout), base+"/"+escaped+"/@v/list")
			if timedOut(ctx, err) {
				slog.WarnContext(ctx, "major module path probe timed out; assuming no newer major", "module", modPath, "proxy", base, "timeout", timeout)
			}
			return notFound, err
		})
		if errors.Is(err, errNotProbed) || errors.Is(err, errNoProxy) || timedOut(ctx, err) {
			err = nil
		}
		slog.DebugContext(ctx, "major module path probed", "module", modPath, "versions", len(vs), "err", err)
		return vs, err
	})
}

// timedOut reports whether err ended a probe request at its probeTimeout (not at ctx's end).
func timedOut(ctx context.Context, err error) bool {
	return ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded)
}

// get returns the versions of modPath, resolved once; dir is used by the first lookup. The
// entry is never nil.
func (s *Source) get(ctx context.Context, modPath, dir string) (*entry, error) {
	return s.entry(ctx, modPath, dir, func() ([]string, bool, error) { return s.resolve(ctx, modPath, dir) })
}

// entry shares what resolve returns for modPath with every lookup of it, resolving once; dir
// is the directory of the first lookup. The entry is never nil.
func (s *Source) entry(ctx context.Context, modPath, dir string, resolve func() (vs []string, proxied bool, err error)) (*entry, error) {
	s.mu.Lock()
	if _, ok := s.dirs[modPath]; !ok {
		s.dirs[modPath] = dir
	}
	s.mu.Unlock()
	return s.cache.Do(ctx, modPath, func() (*entry, error) {
		e := &entry{}
		var err error
		e.versions, e.proxied, err = resolve()
		e.rels = make([]datasource.Release, len(e.versions))
		for i, v := range e.versions {
			e.rels[i] = datasource.Release{Version: v}
		}
		return e, err
	})
}

// listed returns the versions of modPath's own lookup (Versions, Releases), waiting for it when
// it is still running, if one was started and a GOPROXY URL answered it. A major path probe
// would GET the same `@v/list`; one that failed or came from the go command is not reused.
func (s *Source) listed(ctx context.Context, modPath string) ([]string, bool) {
	s.mu.Lock()
	dir, ok := s.dirs[modPath]
	s.mu.Unlock()
	if !ok {
		return nil, false
	}
	e, err := s.get(ctx, modPath, dir)
	return e.versions, err == nil && e.proxied
}

// ReleaseTime implements datasource.Timer: the Time of the version's `@v/<version>.info`, or
// `go list -m -json` for direct and GONOPROXY/GOPRIVATE modules (run in the directory of the
// module's earlier Releases lookup). Results are deduped per module@version.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	mod := s.resolved(d)
	s.mu.Lock()
	dir := cmp.Or(s.dirs[mod], ".")
	s.mu.Unlock()
	return s.times.Do(ctx, mod+"@"+version, func() (time.Time, error) {
		t, err := s.releaseTime(ctx, mod, version, dir)
		slog.DebugContext(ctx, "module release time", "module", mod, "version", version, "time", t, "err", err)
		return t, err
	})
}

func (s *Source) releaseTime(ctx context.Context, modPath, version, dir string) (time.Time, error) {
	escVersion, err := module.EscapeVersion(version)
	if err != nil {
		return time.Time{}, err
	}
	// A version's .info is immutable, so the local module download cache answers without a
	// request whenever the go command already fetched this version.
	if t, ok := s.cachedInfo(ctx, modPath, escVersion); ok {
		return t, nil
	}
	var t time.Time
	err = s.walk(ctx, modPath, func(base, escaped string) (notFound bool, err error) {
		if base == "" {
			release, err := httpx.Acquire(ctx, s.getter.Sem)
			if err != nil {
				return false, err
			}
			defer release()
			t, err = s.info(ctx, dir, modPath, version)
			return false, err
		}
		var info struct{ Time *time.Time }
		notFound, err = s.stream(ctx, base+"/"+escaped+"/@v/"+escVersion+".info", maxInfo, func(r io.Reader) error {
			return json.UnmarshalRead(r, &info)
		})
		if err == nil && info.Time != nil {
			t = *info.Time
		}
		return notFound, err
	})
	return t, err
}

// DeclaredPath implements datasource.DeclaredPathSource: the module directive of the version's
// go.mod (`@v/<version>.mod`) from the module download cache, else the GOPROXY URLs. It is ""
// for direct and GONOPROXY/GOPRIVATE modules, whose go.mod only the go command could fetch.
// Results are deduped per module@version.
func (s *Source) DeclaredPath(ctx context.Context, modPath, version string) (string, error) {
	return s.paths.Do(ctx, modPath+"@"+version, func() (string, error) {
		p, err := s.declaredPath(ctx, modPath, version)
		slog.DebugContext(ctx, "module declared path", "module", modPath, "version", version, "declared", p, "err", err)
		return p, err
	})
}

func (s *Source) declaredPath(ctx context.Context, modPath, version string) (string, error) {
	escVersion, err := module.EscapeVersion(version)
	if err != nil {
		return "", err
	}
	if b, ok := s.cached(ctx, modPath, escVersion+".mod"); ok {
		return modfile.ModulePath(b), nil
	}
	var declared string
	err = s.walk(ctx, modPath, func(base, escaped string) (notFound bool, err error) {
		if base == "" {
			return false, nil // unknown
		}
		return s.stream(ctx, base+"/"+escaped+"/@v/"+escVersion+".mod", maxMod, func(r io.Reader) error {
			b, err := io.ReadAll(r)
			if err != nil {
				return err
			}
			declared = modfile.ModulePath(b)
			return nil
		})
	})
	return declared, err
}

// cachedInfo reads the release time from the cached .info of a version (see cached).
func (s *Source) cachedInfo(ctx context.Context, modPath, escVersion string) (time.Time, bool) {
	b, ok := s.cached(ctx, modPath, escVersion+".info")
	if !ok {
		return time.Time{}, false
	}
	var info struct{ Time *time.Time }
	if json.Unmarshal(b, &info) != nil || info.Time == nil {
		return time.Time{}, false
	}
	return *info.Time, true
}

// cached reads <GOMODCACHE>/cache/download/<escaped path>/@v/<name>: a version's .info or
// .mod, both immutable, so the cache answers without a request whenever the go command already
// fetched the version. Version lists are not read from the cache: they only hold versions
// fetched locally, so the newest releases would be missing.
func (s *Source) cached(ctx context.Context, modPath, name string) ([]byte, bool) {
	env, err := s.loadEnv(ctx)
	if err != nil || env.ModCache == "" {
		return nil, false
	}
	escPath, err := module.EscapePath(modPath)
	if err != nil {
		return nil, false
	}
	p := filepath.Join(env.ModCache, "cache", "download", filepath.FromSlash(escPath), "@v", name)
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	slog.DebugContext(ctx, "module file from module cache", "module", modPath, "file", p)
	return b, true
}

// loadEnv resolves the go environment once per run.
func (s *Source) loadEnv(ctx context.Context) (gotool.GoEnv, error) {
	return s.goEnv.Do(ctx, func() (gotool.GoEnv, error) {
		env, err := s.env(ctx)
		slog.DebugContext(ctx, "go env", "goproxy", env.Proxy, "noProxy", env.NoProxy, "modCache", env.ModCache, "err", err)
		return env, err
	})
}

func (s *Source) resolve(ctx context.Context, modPath, dir string) (vs []string, proxied bool, err error) {
	err = s.walk(ctx, modPath, func(base, escaped string) (notFound bool, err error) {
		if base == "" {
			vs, err = s.direct(ctx, dir, modPath)
		} else {
			vs, notFound, err = s.fetch(ctx, base+"/"+escaped+"/@v/list")
		}
		proxied = base != ""
		slog.DebugContext(ctx, "module versions", "module", modPath, "source", cmp.Or(base, "direct"), "versions", len(vs), "err", err)
		return notFound, err
	})
	return vs, proxied, err
}

// walk runs step for modPath along GOPROXY until one succeeds. step gets a proxy base URL and
// the escaped module path, or base "" when the go command must be used (a `direct` entry, or a
// GONOPROXY/GOPRIVATE module); a notFound (404/410) result falls through to the next entry.
// When no entry succeeded, the first failure of one that was not a notFound is returned, so
// that a `|` list whose proxies were all unreachable fails instead of reporting no versions.
func (s *Source) walk(ctx context.Context, modPath string, step func(base, escaped string) (notFound bool, err error)) error {
	env, err := s.loadEnv(ctx)
	if err != nil {
		return err
	}
	if module.MatchPrefixPatterns(env.NoProxy, modPath) {
		_, err := step("", "")
		return err
	}
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return err
	}
	rest := env.Proxy
	var failed error // the first entry that failed with something else than a notFound
	for rest != "" {
		// Each entry is followed by "," (fall through only on 404/410) or "|" (fall through on any error).
		i := strings.IndexAny(rest, ",|")
		entry, anyErr := rest, false
		if i >= 0 {
			entry, anyErr, rest = rest[:i], rest[i] == '|', rest[i+1:]
		} else {
			rest = ""
		}
		entry = strings.TrimSpace(entry)
		var notFound bool
		switch entry {
		case "":
			continue
		case "off":
			return errors.New("GOPROXY=off")
		case "direct":
			_, err = step("", "")
		default:
			notFound, err = step(strings.TrimSuffix(entry, "/"), escaped)
		}
		if err == nil {
			return nil
		}
		if !notFound && !anyErr {
			return err
		}
		if !notFound && failed == nil {
			failed = err
		}
	}
	if failed != nil {
		return failed
	}
	return fmt.Errorf("%w %s", errNoProxy, modPath)
}

// errNoProxy ends a GOPROXY walk in which every entry reported not found.
var errNoProxy = errors.New("no proxy has")

func (s *Source) direct(ctx context.Context, dir, modPath string) ([]string, error) {
	release, err := httpx.Acquire(ctx, s.getter.Sem)
	if err != nil {
		return nil, err
	}
	defer release()
	res, err := s.list(ctx, dir, modPath)
	if err != nil {
		return nil, err
	}
	m, ok := res[modPath]
	switch {
	case !ok:
		return nil, fmt.Errorf("go list -m -versions %s: no answer", modPath)
	case m.Err != nil:
		return nil, m.Err
	}
	return m.Versions, nil
}

// Body caps of the proxy endpoints: an `@latest` or `.info` document, a `.mod` file. A version
// list (`@v/list`) gets httpx.DefaultMaxBody.
const (
	maxInfo = 1 << 20
	maxMod  = 16 << 20
)

// stream reads target with a body cap of limit bytes and passes the body to read: a `file://`
// entry from the directory it names, every other URL with a GET holding a request slot until
// the body is read and closed. notFound reports a 404 or 410, or a missing file.
func (s *Source) stream(ctx context.Context, target string, limit int64, read func(io.Reader) error) (notFound bool, err error) {
	if strings.HasPrefix(target, fileScheme) {
		return readFileProxy(target, limit, read)
	}
	g := s.getter
	g.MaxBody = limit
	err = g.Stream(ctx, target, read)
	se, ok := errors.AsType[*httpx.StatusError](err)
	return ok && (se.Code == http.StatusNotFound || se.Code == http.StatusGone), err
}

// fileScheme prefixes a GOPROXY entry served from a local directory, which the go command
// accepts as well (`GOPROXY=file:///var/cache/goproxy`): its layout is the one of the module
// cache's download directory, so the endpoints are files below it.
const fileScheme = "file://"

// readFileProxy serves target, a fileScheme URL, from the local file it names, capped at limit
// bytes (0 = httpx.DefaultMaxBody). A missing file is notFound, so that the GOPROXY walk falls
// through to the next entry as it does for a 404.
func readFileProxy(target string, limit int64, read func(io.Reader) error) (notFound bool, err error) {
	u, err := url.Parse(target)
	if err != nil {
		return false, fmt.Errorf("%s: %w", target, err)
	}
	if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
		return false, fmt.Errorf("%s: file proxy on another host", target)
	}
	path := u.Path
	// A Windows path arrives as /C:/dir, whose volume name follows the leading slash.
	if filepath.Separator == '\\' && len(path) > 2 && path[0] == '/' && path[2] == ':' {
		path = path[1:]
	}
	f, err := os.Open(filepath.FromSlash(path))
	if err != nil {
		return errors.Is(err, fs.ErrNotExist), err
	}
	defer f.Close()
	if limit <= 0 {
		limit = httpx.DefaultMaxBody
	}
	if err := read(io.LimitReader(f, limit)); err != nil {
		return false, fmt.Errorf("read %s: %w", path, err)
	}
	return false, nil
}

// fetch reads a proxy list endpoint; notFound reports a 404 or 410, or a missing file.
func (s *Source) fetch(ctx context.Context, target string) (versions []string, notFound bool, err error) {
	var data []byte
	notFound, err = s.stream(ctx, target, 0, func(r io.Reader) (err error) {
		data, err = io.ReadAll(r)
		return err
	})
	if err != nil {
		return nil, notFound, err
	}
	// One string for the whole body; versions are substrings of it.
	body := string(data)
	versions = make([]string, 0, strings.Count(body, "\n")+1)
	for line := range strings.Lines(body) {
		// Each line is "<version>[ <extra>]"; only the first field is the version.
		line = strings.TrimSpace(line)
		if i := strings.IndexAny(line, " \t"); i >= 0 {
			line = line[:i]
		}
		if line != "" {
			versions = append(versions, line)
		}
	}
	if len(versions) == 0 {
		return nil, false, nil
	}
	return versions, false, nil
}
