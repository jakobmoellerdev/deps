// Package goproxy lists Go module versions from the GOPROXY `@v/list` endpoint, falling back to
// `go list` only for direct and GOPRIVATE/GONOPROXY modules.
package goproxy

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Source resolves module versions once per module path per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	env    func(ctx context.Context) (gotool.GoEnv, error)
	list   func(ctx context.Context, dir, path string) ([]string, error)
	info   func(ctx context.Context, dir, path, version string) (time.Time, error)

	envOnce sync.Once
	goEnv   gotool.GoEnv
	envErr  error

	mu    sync.Mutex
	cache map[string]*entry     // module path
	times map[string]*timeEntry // module@version
}

type entry struct {
	dir      string // directory of the first lookup, for `go list`
	once     sync.Once
	versions []string
	rels     []datasource.Release
	err      error
}

type timeEntry struct {
	once sync.Once
	t    time.Time
	err  error
}

// New returns a Source using gotool.Env, gotool.ListVersions and gotool.ModuleTime. sem bounds
// concurrent requests (including `go list` runs) and is shared with other datasources.
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{
		client: client, sem: sem, env: gotool.Env, list: gotool.ListVersions, info: gotool.ModuleTime,
		cache: make(map[string]*entry), times: make(map[string]*timeEntry),
	}
}

// Versions returns the known versions of modPath. dir is the module directory used when the
// go command has to be run. Results are deduped by modPath; the slice must not be modified.
func (s *Source) Versions(ctx context.Context, modPath, dir string) ([]string, error) {
	e := s.get(ctx, modPath, dir)
	return e.versions, e.err
}

// Releases implements datasource.Datasource for module d.Name; releases carry no digest.
// The slice is shared between callers and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	e := s.get(ctx, d.Name, dir)
	return e.rels, e.err
}

// Digest always fails: Go module versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, version string) (string, error) {
	return "", fmt.Errorf("%s@%s: go modules have no digests", d.Name, version)
}

func (s *Source) get(ctx context.Context, modPath, dir string) *entry {
	s.mu.Lock()
	e := s.cache[modPath]
	if e == nil {
		e = &entry{dir: dir}
		s.cache[modPath] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		e.versions, e.err = s.resolve(ctx, modPath, dir)
		e.rels = make([]datasource.Release, len(e.versions))
		for i, v := range e.versions {
			e.rels[i] = datasource.Release{Version: v}
		}
	})
	return e
}

// ReleaseTime implements datasource.Timer: the Time of the version's `@v/<version>.info`, or
// `go list -m -json` for direct and GONOPROXY/GOPRIVATE modules (run in the directory of the
// module's earlier Releases lookup). Results are deduped per module@version.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	key := d.Name + "@" + version
	s.mu.Lock()
	te := s.times[key]
	if te == nil {
		te = &timeEntry{}
		s.times[key] = te
	}
	dir := "."
	if e := s.cache[d.Name]; e != nil && e.dir != "" {
		dir = e.dir
	}
	s.mu.Unlock()
	te.once.Do(func() {
		te.t, te.err = s.releaseTime(ctx, d.Name, version, dir)
		slog.DebugContext(ctx, "module release time", "module", d.Name, "version", version, "time", te.t, "err", te.err)
	})
	return te.t, te.err
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
			if err := s.acquire(ctx); err != nil {
				return false, err
			}
			defer s.release()
			t, err = s.info(ctx, dir, modPath, version)
			return false, err
		}
		target := base + "/" + escaped + "/@v/" + escVersion + ".info"
		resp, notFound, err := s.get2xx(ctx, target)
		if err != nil {
			return notFound, err
		}
		defer resp.Body.Close()
		var info struct{ Time *time.Time }
		if err := json.UnmarshalRead(io.LimitReader(resp.Body, 1<<20), &info); err != nil {
			return false, fmt.Errorf("GET %s: %w", target, err)
		}
		if info.Time != nil {
			t = *info.Time
		}
		return false, nil
	})
	return t, err
}

// cachedInfo reads <GOMODCACHE>/cache/download/<escaped path>/@v/<escaped version>.info.
// Version lists are not read from the cache: they only hold versions fetched locally, so the
// newest releases would be missing.
func (s *Source) cachedInfo(ctx context.Context, modPath, escVersion string) (time.Time, bool) {
	if s.loadEnv(ctx) != nil || s.goEnv.ModCache == "" {
		return time.Time{}, false
	}
	escPath, err := module.EscapePath(modPath)
	if err != nil {
		return time.Time{}, false
	}
	p := filepath.Join(s.goEnv.ModCache, "cache", "download", filepath.FromSlash(escPath), "@v", escVersion+".info")
	b, err := os.ReadFile(p)
	if err != nil {
		return time.Time{}, false
	}
	var info struct{ Time *time.Time }
	if json.Unmarshal(b, &info) != nil || info.Time == nil {
		return time.Time{}, false
	}
	slog.DebugContext(ctx, "module release time from module cache", "module", modPath, "file", p)
	return *info.Time, true
}

// loadEnv resolves the go environment once per run.
func (s *Source) loadEnv(ctx context.Context) error {
	s.envOnce.Do(func() {
		s.goEnv, s.envErr = s.env(ctx)
		slog.DebugContext(ctx, "go env", "goproxy", s.goEnv.Proxy, "noProxy", s.goEnv.NoProxy, "modCache", s.goEnv.ModCache, "err", s.envErr)
	})
	return s.envErr
}

func (s *Source) resolve(ctx context.Context, modPath, dir string) ([]string, error) {
	var vs []string
	err := s.walk(ctx, modPath, func(base, escaped string) (notFound bool, err error) {
		if base == "" {
			vs, err = s.direct(ctx, dir, modPath)
		} else {
			vs, notFound, err = s.fetch(ctx, base+"/"+escaped+"/@v/list")
		}
		slog.DebugContext(ctx, "module versions", "module", modPath, "source", cmp.Or(base, "direct"), "versions", len(vs), "err", err)
		return notFound, err
	})
	return vs, err
}

// walk runs step for modPath along GOPROXY until one succeeds. step gets a proxy base URL and
// the escaped module path, or base "" when the go command must be used (a `direct` entry, or a
// GONOPROXY/GOPRIVATE module); a notFound (404/410) result falls through to the next entry.
func (s *Source) walk(ctx context.Context, modPath string, step func(base, escaped string) (notFound bool, err error)) error {
	if err := s.loadEnv(ctx); err != nil {
		return err
	}
	if module.MatchPrefixPatterns(s.goEnv.NoProxy, modPath) {
		_, err := step("", "")
		return err
	}
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return err
	}
	rest := s.goEnv.Proxy
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
	}
	return fmt.Errorf("no proxy has %s", modPath)
}

func (s *Source) direct(ctx context.Context, dir, modPath string) ([]string, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	return s.list(ctx, dir, modPath)
}

// get2xx GETs url holding a semaphore slot until the response headers arrive; notFound reports
// a 404 or 410. The caller closes the body of a successful response.
func (s *Source) get2xx(ctx context.Context, url string) (resp *http.Response, notFound bool, err error) {
	if err := s.acquire(ctx); err != nil {
		return nil, false, err
	}
	defer s.release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err = s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		code := resp.StatusCode
		return nil, code == http.StatusNotFound || code == http.StatusGone, fmt.Errorf("GET %s: HTTP %d", url, code)
	}
	return resp, false, nil
}

// fetch GETs a proxy list endpoint; notFound reports a 404 or 410.
func (s *Source) fetch(ctx context.Context, url string) (versions []string, notFound bool, err error) {
	resp, notFound, err := s.get2xx(ctx, url)
	if err != nil {
		return nil, notFound, err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		// Each line is "<version>[ <extra>]"; only the first field is the version.
		line := bytes.TrimSpace(sc.Bytes())
		if i := bytes.IndexAny(line, " \t"); i >= 0 {
			line = line[:i]
		}
		if len(line) > 0 {
			versions = append(versions, string(line))
		}
	}
	if err := sc.Err(); err != nil {
		return nil, false, fmt.Errorf("GET %s: %w", url, err)
	}
	return versions, false, nil
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
