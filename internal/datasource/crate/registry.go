package crate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// registry is one package index: sparse (HTTP, index is the base URL without "sparse+") or
// git (index is the repository URL, read from a blob-less shallow clone).
type registry struct {
	name   string // registry name ("crates-io" for the default), for errors
	index  string
	sparse bool
	token  string // Authorization header, only sent to the index host (owns); "" = none
	cred   string // identity of token, for memo keys (see key); "" = none
	api    string // web API base for source repositories; "" = read from config.json

	clone datasource.Once[string] // git: the clone's directory
}

// key is the memo key of a lookup of suffix in r. It covers the token: the memos cache
// failures, so without it the 401 of a directory without a token would be handed to every
// later directory that has one, and which of the two runs first is up to the scheduler.
func (r *registry) key(suffix string) string {
	return r.index + "\x00" + r.cred + "\x00" + suffix
}

// credID identifies a token without keeping it; "" stands for no token.
func credID(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return string(sum[:])
}

// owns reports whether u is on r's index host: Cargo credentials belong to the index, so only
// those requests carry them (a config.json `api` on another host gets none).
func (r *registry) owns(u *url.URL) bool {
	idx, err := url.Parse(r.index)
	return err == nil && strings.EqualFold(u.Host, idx.Host)
}

// cargoConfig is the merged Cargo configuration of a manifest directory: the
// .cargo/config.toml files from the directory up to the scanned root (nearer files win), then
// $CARGO_HOME/config.toml, and $CARGO_HOME/credentials.toml for tokens.
type cargoConfig struct {
	docs  []*tomlspan.Value // nearest first
	creds *tomlspan.Value
}

// str returns the first string at keys in the config files, nearest first.
func (c *cargoConfig) str(keys ...string) string {
	for _, d := range c.docs {
		if v := d.Get(keys...); v != nil && v.Kind == tomlspan.String {
			return v.Str
		}
	}
	return ""
}

// envName is Cargo's environment spelling of a registry name: CARGO_REGISTRIES_<NAME>_INDEX.
func envName(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// loadConfig reads the Cargo configuration for the manifest directory dir below root.
func (s *Source) loadConfig(dir, root string) (*cargoConfig, error) {
	c := &cargoConfig{}
	read := func(file string) (*tomlspan.Value, error) {
		b, err := os.ReadFile(file)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		v, err := tomlspan.Parse(b)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file, err)
		}
		httpx.NoteSource("read config", "file", file, "datasource", Name)
		return v, nil
	}
	var dirs []string
	for d := dir; d != ""; d = filepath.Dir(d) {
		dirs = append(dirs, filepath.Join(d, ".cargo"))
		if d == root || !strings.HasPrefix(d, root) || filepath.Dir(d) == d {
			break
		}
	}
	home := s.getenv("CARGO_HOME")
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".cargo")
		}
	}
	if home != "" {
		dirs = append(dirs, home)
	}
	for _, d := range dirs {
		for _, n := range []string{"config.toml", "config"} {
			v, err := read(filepath.Join(d, n))
			if err != nil {
				return nil, err
			}
			if v != nil {
				c.docs = append(c.docs, v)
				break
			}
		}
	}
	if home != "" {
		for _, n := range []string{"credentials.toml", "credentials"} {
			v, err := read(filepath.Join(home, n))
			if err != nil {
				return nil, err
			}
			if v != nil {
				c.creds = v
				break
			}
		}
	}
	return c, nil
}

// registryOf resolves the index of d: d.Source is "" (crates.io, or its [source.crates-io]
// replacement), an index URL (registry-index), or a registry name (registry = "<name>", from
// CARGO_REGISTRIES_<NAME>_INDEX or [registries.<name>] index). A name without an index is a
// *dep.SkipError, as Cargo cannot resolve it either.
func (s *Source) registryOf(d dep.Dep, dir string) (*registry, error) {
	cfg, err := s.config(dir, datasource.RootOf(d.File, dir))
	if err != nil {
		return nil, err
	}
	switch {
	case d.Source == "":
		// Source replacement ([source.crates-io] replace-with = "<mirror>"), followed to a
		// remote registry; local-registry and directory sources vendor crates.io itself.
		name := "crates-io"
		for range 8 {
			next := cfg.str("source", name, "replace-with")
			if next == "" {
				break
			}
			name = next
		}
		if idx := cfg.str("source", name, "registry"); name != "crates-io" && idx != "" {
			return s.registry(name, idx, s.tokenOf(cfg, name), ""), nil
		}
		return s.registry("crates-io", "sparse+"+s.index, "", s.api), nil
	case strings.Contains(d.Source, "://"):
		return s.registry(d.Source, d.Source, "", ""), nil
	}
	name := d.Source
	idx := s.getenv("CARGO_REGISTRIES_" + envName(name) + "_INDEX")
	if idx == "" {
		idx = cfg.str("registries", name, "index")
	}
	if idx == "" {
		return nil, &dep.SkipError{Reason: "unknown registry " + name}
	}
	return s.registry(name, idx, s.tokenOf(cfg, name), ""), nil
}

// tokenOf returns the token of the registry name: CARGO_REGISTRIES_<NAME>_TOKEN, else
// [registries.<name>] token of credentials.toml, else of the config files.
func (s *Source) tokenOf(cfg *cargoConfig, name string) string {
	if t := s.getenv("CARGO_REGISTRIES_" + envName(name) + "_TOKEN"); t != "" {
		return t
	}
	if cfg.creds != nil {
		if v := cfg.creds.Get("registries", name, "token"); v != nil && v.Kind == tomlspan.String {
			return v.Str
		}
	}
	return cfg.str("registries", name, "token")
}

// registry returns the shared registry of the index URL ("sparse+https://…" or a git URL).
func (s *Source) registry(name, index, token, api string) *registry {
	key := index + "\x00" + token
	s.mu.Lock()
	defer s.mu.Unlock()
	if r := s.registries[key]; r != nil {
		return r
	}
	r := &registry{name: name, token: token, cred: credID(token), api: api}
	if u, ok := strings.CutPrefix(index, "sparse+"); ok {
		r.index, r.sparse = strings.TrimSuffix(u, "/"), true
	} else {
		r.index = strings.TrimPrefix(index, "registry+")
	}
	s.registries[key] = r
	return r
}

// config returns the Cargo configuration of dir, read once per directory.
func (s *Source) config(dir, root string) (*cargoConfig, error) {
	s.mu.Lock()
	e := s.configs[dir]
	if e == nil {
		e = &configEntry{}
		s.configs[dir] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.cfg, e.err = s.loadConfig(dir, root) })
	return e.cfg, e.err
}

type configEntry struct {
	once sync.Once
	cfg  *cargoConfig
	err  error
}

// open returns the index file at p (IndexPath, or "config.json").
func (s *Source) open(ctx context.Context, r *registry, p string) (io.ReadCloser, error) {
	if r.sparse {
		return s.get(ctx, r, r.index+"/"+p)
	}
	dir, err := r.clone.Do(ctx, func() (string, error) { return s.clone(ctx, r.index) })
	if err != nil {
		return nil, err
	}
	out, err := git(ctx, dir, "cat-file", "blob", "HEAD:"+p)
	if err != nil {
		if strings.Contains(err.Error(), "does not exist") || strings.Contains(err.Error(), "Not a valid object name") {
			return nil, errNotFound
		}
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

// clone makes a shallow, blob-less clone of a git index; blobs are fetched on demand by
// cat-file. The clone is removed by Close.
func (s *Source) clone(ctx context.Context, index string) (string, error) {
	dir, err := os.MkdirTemp("", "deps-crate-index-*")
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.clones = append(s.clones, dir)
	s.mu.Unlock()
	start := time.Now()
	_, err = git(ctx, "", "clone", "--quiet", "--depth=1", "--filter=blob:none", "--no-checkout", index, dir)
	slog.DebugContext(ctx, "crate git index cloned", "url", index, "duration", time.Since(start), "err", err)
	if err != nil {
		return "", fmt.Errorf("git index %s: %w", index, err)
	}
	return dir, nil
}

// Close removes the clones of git indexes.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	errs := make([]error, 0, len(s.clones))
	for _, d := range s.clones {
		errs = append(errs, os.RemoveAll(d))
	}
	s.clones = nil
	return errors.Join(errs...)
}

// git runs git without prompts and returns stdout; a failure carries its stderr.
func git(ctx context.Context, dir string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
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
