package crate

import (
	"bufio"
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
	"strconv"
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
	blobs blobs                   // git: the cat-file processes reading the clone
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

// open returns the index file at p (IndexPath, or "config.json"). A git index is read through
// a `git cat-file --batch` of its clone while holding a request slot, as the sparse index's
// GET does: a blob of a blob-less clone is fetched from the index host on demand, so those
// reads are requests like any other and the registry's limit has to bound them.
func (s *Source) open(ctx context.Context, r *registry, p string) (io.ReadCloser, error) {
	if r.sparse {
		return s.get(ctx, r, r.index+"/"+p)
	}
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, err
	}
	defer release()
	dir, err := r.clone.Do(ctx, func() (string, error) { return s.clone(ctx, r.index) })
	if err != nil {
		return nil, err
	}
	out, err := r.blobs.read(ctx, dir, "HEAD:"+p)
	if err != nil {
		return nil, err
	}
	return io.NopCloser(bytes.NewReader(out)), nil
}

// blobs reads the files of a git index clone through `git cat-file --batch` processes that
// stay alive until Close: a crate then costs a request line and its answer on a running
// process, where every crate used to fork a `git cat-file` of its own. One process answers one
// read at a time, so a read that finds them all busy starts another; the request slots held by
// open bound how many that can be.
type blobs struct {
	mu     sync.Mutex
	free   []*catFile // processes ready for the next read
	all    []*catFile // every process started, ended by close
	closed bool
}

// read returns the blob at spec (`HEAD:<path>`) of the clone in dir, errNotFound when the
// index has no such file.
func (b *blobs) read(ctx context.Context, dir, spec string) ([]byte, error) {
	c, err := b.take(dir)
	if err != nil {
		return nil, err
	}
	out, err := c.read(ctx, spec)
	if err != nil && !errors.Is(err, errNotFound) {
		c.close() // the answer stopped halfway: what the stream holds next is unknown
		return nil, err
	}
	b.put(c)
	return out, err
}

// take returns a process ready for a read, starting one when every started process is busy.
func (b *blobs) take(dir string) (*catFile, error) {
	b.mu.Lock()
	if n := len(b.free); n > 0 && !b.closed {
		c := b.free[n-1]
		b.free = b.free[:n-1]
		b.mu.Unlock()
		return c, nil
	}
	b.mu.Unlock()
	c, err := startCatFile(dir)
	if err != nil {
		return nil, err
	}
	b.mu.Lock()
	b.all = append(b.all, c)
	closed := b.closed
	b.mu.Unlock()
	if closed {
		c.close()
		return nil, errClosed
	}
	return c, nil
}

// put hands the process to the next read.
func (b *blobs) put(c *catFile) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return // close already ended it
	}
	b.free = append(b.free, c)
}

// close ends every process of the clone.
func (b *blobs) close() {
	b.mu.Lock()
	all := b.all
	b.all, b.free, b.closed = nil, nil, true
	b.mu.Unlock()
	for _, c := range all {
		c.close()
	}
}

// errClosed is the read of a Source that was closed while the read's process started.
var errClosed = errors.New("crate index closed")

// catFile is one `git cat-file --batch` process of a clone: a request line `<rev>:<path>` is
// answered by `<oid> blob <size>`, the blob and a newline, or by `<spec> missing` for a path
// the commit does not have.
type catFile struct {
	cancel context.CancelFunc
	cmd    *exec.Cmd
	in     io.WriteCloser
	out    *bufio.Reader
	stderr *syncBuffer
}

// startCatFile starts the process in the clone directory dir. It outlives the context of the
// read that started it, so that the next read finds it running; close ends it.
func startCatFile(dir string) (*catFile, error) {
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "git", "cat-file", "--batch")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	stderr := &syncBuffer{}
	cmd.Stderr = stderr
	in, err := cmd.StdinPipe()
	if err == nil {
		var out io.ReadCloser
		if out, err = cmd.StdoutPipe(); err == nil {
			if err = cmd.Start(); err == nil {
				return &catFile{cancel: cancel, cmd: cmd, in: in, out: bufio.NewReader(out), stderr: stderr}, nil
			}
		}
	}
	cancel()
	return nil, err
}

// read returns the blob at spec; errNotFound when the commit has no such path. The caller
// must not use the process concurrently.
func (c *catFile) read(ctx context.Context, spec string) ([]byte, error) {
	// A read of a blob-less clone fetches the blob from the index host, which can take as
	// long as any other request: end it with the caller's context.
	defer context.AfterFunc(ctx, c.cancel)()
	if _, err := io.WriteString(c.in, spec+"\n"); err != nil {
		return nil, c.fail(ctx, err)
	}
	header, err := c.out.ReadString('\n')
	if err != nil {
		return nil, c.fail(ctx, err)
	}
	fields := strings.Fields(header)
	if len(fields) == 2 && (fields[1] == "missing" || fields[1] == "ambiguous") {
		return nil, errNotFound
	}
	if len(fields) != 3 || fields[1] != "blob" {
		return nil, fmt.Errorf("git cat-file %s: unexpected answer %q", spec, strings.TrimSuffix(header, "\n"))
	}
	size, err := strconv.Atoi(fields[2])
	if err != nil || size < 0 {
		return nil, fmt.Errorf("git cat-file %s: invalid size %q", spec, fields[2])
	}
	buf := make([]byte, size+1) // the blob and the newline git writes after it
	if _, err := io.ReadFull(c.out, buf); err != nil {
		return nil, c.fail(ctx, err)
	}
	return buf[:size], nil
}

// fail reports what ended a read: the caller's context, else git's stderr as the git helper
// reports it, else the read error itself.
func (c *catFile) fail(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if msg := strings.TrimSpace(c.stderr.String()); msg != "" {
		return errors.New(msg)
	}
	return err
}

// close ends the process: git exits at the end of its input, a hanging one is killed.
func (c *catFile) close() {
	_ = c.in.Close()
	c.cancel()
	_ = c.cmd.Wait()
}

// syncBuffer collects the stderr of a process, which os/exec copies from another goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
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

// Close ends the cat-file processes of the git indexes and removes their clones.
func (s *Source) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.registries {
		r.blobs.close()
	}
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
