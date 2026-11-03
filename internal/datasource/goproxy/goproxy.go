// Package goproxy lists Go module versions from the GOPROXY `@v/list` endpoint, falling back to
// `go list` only for direct and GOPRIVATE/GONOPROXY modules.
package goproxy

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"

	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Source resolves module versions once per module path per run.
type Source struct {
	client *http.Client
	sem    chan struct{}
	env    func(ctx context.Context) (gotool.GoEnv, error)
	list   func(ctx context.Context, dir, path string) ([]string, error)

	envOnce sync.Once
	goEnv   gotool.GoEnv
	envErr  error

	mu    sync.Mutex
	cache map[string]*entry
}

type entry struct {
	once     sync.Once
	versions []string
	err      error
}

// New returns a Source using gotool.Env and gotool.ListVersions. sem bounds concurrent requests
// (including `go list` runs) and is shared with other datasources.
func New(client *http.Client, sem chan struct{}) *Source {
	return &Source{client: client, sem: sem, env: gotool.Env, list: gotool.ListVersions, cache: make(map[string]*entry)}
}

// Versions returns the known versions of modPath. dir is the module directory used when the
// go command has to be run. Results are deduped by modPath; the slice must not be modified.
func (s *Source) Versions(ctx context.Context, modPath, dir string) ([]string, error) {
	s.mu.Lock()
	e := s.cache[modPath]
	if e == nil {
		e = &entry{}
		s.cache[modPath] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.versions, e.err = s.resolve(ctx, modPath, dir) })
	return e.versions, e.err
}

func (s *Source) resolve(ctx context.Context, modPath, dir string) ([]string, error) {
	s.envOnce.Do(func() {
		s.goEnv, s.envErr = s.env(ctx)
		slog.DebugContext(ctx, "go env", "goproxy", s.goEnv.Proxy, "noProxy", s.goEnv.NoProxy, "err", s.envErr)
	})
	if s.envErr != nil {
		return nil, s.envErr
	}
	if module.MatchPrefixPatterns(s.goEnv.NoProxy, modPath) {
		vs, err := s.direct(ctx, dir, modPath)
		slog.DebugContext(ctx, "module versions", "module", modPath, "source", "direct (GONOPROXY/GOPRIVATE)", "versions", len(vs), "err", err)
		return vs, err
	}
	escaped, err := module.EscapePath(modPath)
	if err != nil {
		return nil, err
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
		var (
			vs       []string
			err      error
			notFound bool
		)
		switch entry {
		case "":
			continue
		case "off":
			return nil, errors.New("GOPROXY=off")
		case "direct":
			vs, err = s.direct(ctx, dir, modPath)
		default:
			vs, notFound, err = s.fetch(ctx, strings.TrimSuffix(entry, "/")+"/"+escaped+"/@v/list")
		}
		slog.DebugContext(ctx, "module versions", "module", modPath, "source", entry, "versions", len(vs), "err", err)
		if err == nil {
			return vs, nil
		}
		if !notFound && !anyErr {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no proxy has %s", modPath)
}

func (s *Source) direct(ctx context.Context, dir, modPath string) ([]string, error) {
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	return s.list(ctx, dir, modPath)
}

// fetch GETs a proxy list endpoint; notFound reports a 404 or 410.
func (s *Source) fetch(ctx context.Context, url string) (versions []string, notFound bool, err error) {
	if err := s.acquire(ctx); err != nil {
		return nil, false, err
	}
	defer s.release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, http.NoBody)
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		code := resp.StatusCode
		return nil, code == http.StatusNotFound || code == http.StatusGone, fmt.Errorf("GET %s: HTTP %d", url, code)
	}
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
