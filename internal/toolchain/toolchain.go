// Package toolchain installs the ecosystem tools a repository needs on demand, verified
// against the publisher's checksum, into a cache directory shared by every cycle of a
// long-running deps process.
//
// It exists so the container image can ship only git and the deps binary: a fat image with
// every ecosystem preinstalled would be gigabytes and still wrong for a repository that pins
// another version. The version installed is the one the repository declares (Detect), else a
// pin from the configuration; a tool with neither is treated as absent, which is the CLI's
// behaviour on a machine without it.
//
// Verification is mandatory. A tool is in the catalog only when its publisher ships a
// checksum artifact, and an install whose hash does not match fails; there is no unverified
// path.
package toolchain

import (
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Tool is a catalog row: one distribution that provides one or more commands.
type Tool string

// Hash is the digest a publisher's checksum is of.
type Hash string

const (
	SHA256 Hash = "sha256"
	SHA512 Hash = "sha512"
)

func (h Hash) new() hash.Hash {
	if h == SHA512 {
		return sha512.New()
	}
	return sha256.New()
}

// Archive is the shape of a download.
type Archive string

const (
	TarGz Archive = "tar.gz" // extracted with the image's tar
	TarXz Archive = "tar.xz" // extracted with the image's tar (needs xz)
	Zip   Archive = "zip"    // extracted in process
	Raw   Archive = "raw"    // a single executable
)

// Download is one verified artifact.
type Download struct {
	URL  string
	Sum  string // lower-case hex of Hash over the artifact
	Hash Hash
	// Name is the file name a Raw download is stored under; "" uses the tool's name.
	Name string
}

// Entry is one catalog row.
type Entry struct {
	Tool Tool
	// Resolve maps the version a repository declares (which may name only a feature version,
	// "21", or a line, "1.24") to one Download accepts. nil takes versions as they are.
	Resolve func(ctx context.Context, m *Manager, version string) (string, error)
	// Download returns the artifact of version for goos/goarch and its publisher checksum.
	Download func(ctx context.Context, m *Manager, version, goos, goarch string) (Download, error)
	Archive  Archive
	// Strip is how many leading path components the archive's entries carry.
	Strip int
	// Setup runs in the install directory after extraction, before it is marked complete.
	Setup func(ctx context.Context, m *Manager, dir, version string) error
	// Provides maps a command name to its path below the install directory.
	Provides map[string]string
	// Env are the entries a run of one of the commands needs, given the install directory.
	Env func(m *Manager, dir string) []string
}

// Installed is a finished installation.
type Installed struct {
	Tool    Tool
	Version string
	Dir     string
	Env     []string
	// Bins maps each command the entry provides to its absolute path.
	Bins map[string]string
}

// Manager installs tools into Root and keeps their own caches (the Go module cache, the npm
// cache, the uv cache) in State, so the work a cycle does is reused by the next one.
type Manager struct {
	Root   string // <cacheDir>/tools
	State  string // <cacheDir>/state
	Client *http.Client
	// Pins are the versions used for a tool the repository does not declare; a tool with
	// neither is reported as not found.
	Pins map[Tool]string
	// GOOS and GOARCH override the target; "" uses the running one.
	GOOS, GOARCH string
	// Entries replaces the package Catalog; nil uses it.
	Entries map[Tool]Entry
	// OnInstall is called once per Ensure that had to install something.
	OnInstall func(tool Tool, err error)

	mu        sync.Mutex
	installed map[string]Installed // "<tool>@<version>"
	resolved  map[string]string    // "<tool>@<declared>" -> concrete version
	json      map[string][]byte    // small metadata documents fetched once per process
}

// ErrNoVersion means neither the repository nor a pin names a version of the tool, so it
// cannot be installed. It wraps exec.ErrNotFound: to a caller the tool is simply absent.
var ErrNoVersion = fmt.Errorf("no version declared and no pin configured: %w", exec.ErrNotFound)

// ErrUnsupported means the catalog has no build of the tool for this platform.
var ErrUnsupported = fmt.Errorf("no build for this platform: %w", exec.ErrNotFound)

func (m *Manager) goos() string {
	if m.GOOS != "" {
		return m.GOOS
	}
	return runtime.GOOS
}

func (m *Manager) goarch() string {
	if m.GOARCH != "" {
		return m.GOARCH
	}
	return runtime.GOARCH
}

func (m *Manager) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return http.DefaultClient
}

// state returns the directory of a tool's own cache, created on demand.
func (m *Manager) state(name string) string {
	d := filepath.Join(m.State, name)
	_ = os.MkdirAll(d, 0o700)
	return d
}

// catalog is the Manager's own entries, else the package Catalog.
func (m *Manager) catalog() map[Tool]Entry {
	if m.Entries != nil {
		return m.Entries
	}
	return Catalog
}

// toolOf returns the tool of the Manager's catalog providing the command name.
func (m *Manager) toolOf(name string) (Tool, bool) {
	if m.Entries == nil {
		return ToolOf(name)
	}
	for tool, e := range m.Entries {
		if _, ok := e.Provides[name]; ok {
			return tool, true
		}
	}
	return "", false
}

// Ensure installs tool at version, or returns the installation that is there already.
// Concurrent calls for the same tool and version, in this process and in another one sharing
// the directory, install it once.
func (m *Manager) Ensure(ctx context.Context, tool Tool, version string) (Installed, error) {
	e, ok := m.catalog()[tool]
	if !ok {
		return Installed{}, fmt.Errorf("%s: not in the toolchain catalog: %w", tool, exec.ErrNotFound)
	}
	if version == "" {
		version = m.Pins[tool]
	}
	if version == "" {
		return Installed{}, fmt.Errorf("toolchain %s: %w", tool, ErrNoVersion)
	}
	version, err := m.resolve(ctx, e, version)
	if err != nil {
		return Installed{}, fmt.Errorf("toolchain %s %s: %w", tool, version, err)
	}
	key := string(tool) + "@" + version
	m.mu.Lock()
	in, done := m.installed[key]
	m.mu.Unlock()
	if done {
		return in, nil
	}
	dir := filepath.Join(m.Root, string(tool), version, m.goos()+"-"+m.goarch())
	err = m.install(ctx, e, dir, version)
	if m.OnInstall != nil {
		m.OnInstall(tool, err)
	}
	if err != nil {
		return Installed{}, err
	}
	in = Installed{Tool: tool, Version: version, Dir: dir, Bins: map[string]string{}}
	for name, rel := range e.Provides {
		in.Bins[name] = filepath.Join(dir, filepath.FromSlash(rel))
	}
	if e.Env != nil {
		in.Env = e.Env(m, dir)
	}
	m.mu.Lock()
	if m.installed == nil {
		m.installed = map[string]Installed{}
	}
	m.installed[key] = in
	m.mu.Unlock()
	return in, nil
}

// resolve maps a declared version to the concrete one, once per process.
func (m *Manager) resolve(ctx context.Context, e Entry, version string) (string, error) {
	if e.Resolve == nil {
		return version, nil
	}
	key := string(e.Tool) + "@" + version
	m.mu.Lock()
	v, ok := m.resolved[key]
	m.mu.Unlock()
	if ok {
		return v, nil
	}
	v, err := e.Resolve(ctx, m, version)
	if err != nil {
		return version, err
	}
	m.mu.Lock()
	if m.resolved == nil {
		m.resolved = map[string]string{}
	}
	m.resolved[key] = v
	m.mu.Unlock()
	return v, nil
}

// okFile marks a complete installation; it is written last.
const okFile = ".ok"

// install downloads, verifies and unpacks the tool into dir unless it is complete already.
func (m *Manager) install(ctx context.Context, e Entry, dir, version string) error {
	if _, err := os.Stat(filepath.Join(dir, okFile)); err == nil {
		return nil
	}
	unlock, err := lockDir(ctx, dir)
	if err != nil {
		return err
	}
	defer unlock()
	// Another worker, here or in a replica sharing the volume, may have finished meanwhile.
	if _, err := os.Stat(filepath.Join(dir, okFile)); err == nil {
		return nil
	}
	if err := os.RemoveAll(dir); err != nil {
		return err
	}
	d, err := e.Download(ctx, m, version, m.goos(), m.goarch())
	if err != nil {
		return fmt.Errorf("toolchain %s %s: %w", e.Tool, version, err)
	}
	start := time.Now()
	art, err := m.fetchVerified(ctx, d)
	if err != nil {
		return fmt.Errorf("toolchain %s %s: %w", e.Tool, version, err)
	}
	defer os.Remove(art)
	tmp := dir + ".tmp-" + filepath.Base(art)
	defer os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return err
	}
	name := d.Name
	if name == "" {
		name = string(e.Tool)
	}
	if err := unpack(ctx, art, tmp, e.Archive, e.Strip, name); err != nil {
		return fmt.Errorf("toolchain %s %s: %w", e.Tool, version, err)
	}
	if e.Setup != nil {
		if err := e.Setup(ctx, m, tmp, version); err != nil {
			return fmt.Errorf("toolchain %s %s: %w", e.Tool, version, err)
		}
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	if err := os.Rename(tmp, dir); err != nil {
		return err
	}
	f, err := os.Create(filepath.Join(dir, okFile))
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	slog.InfoContext(ctx, "toolchain installed", "tool", e.Tool, "version", version, "duration", time.Since(start), "dir", dir)
	return nil
}

// fetchVerified downloads d into a temporary file and checks its digest; it returns the file
// path, which the caller removes. A mismatch is an error and the file is removed, so an
// unverified artifact is never unpacked.
func (m *Manager) fetchVerified(ctx context.Context, d Download) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.URL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	resp, err := m.client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		httpx.DrainBody(resp)
		return "", &httpx.StatusError{URL: httpx.Redact(d.URL), Code: resp.StatusCode}
	}
	if err := os.MkdirAll(m.Root, 0o755); err != nil {
		return "", err
	}
	f, err := os.CreateTemp(m.Root, "download-*")
	if err != nil {
		return "", err
	}
	h := d.Hash.new()
	_, copyErr := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	if got := hex.EncodeToString(h.Sum(nil)); !strings.EqualFold(got, d.Sum) {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("checksum mismatch for %s: %s is %s, want %s", httpx.Redact(d.URL), d.Hash, got, d.Sum)
	}
	return f.Name(), nil
}

// lockDir takes a cross-process lock for dir, so two workers sharing the cache volume do not
// install the same tool at once. The lock is a directory next to the installation; one left
// behind by a killed process is taken over after staleLock.
func lockDir(ctx context.Context, dir string) (func(), error) {
	lock := dir + ".lock"
	if err := os.MkdirAll(filepath.Dir(lock), 0o755); err != nil {
		return nil, err
	}
	for {
		switch err := os.Mkdir(lock, 0o700); {
		case err == nil:
			return func() { _ = os.Remove(lock) }, nil
		case !errors.Is(err, fs.ErrExist):
			return nil, err
		}
		if st, err := os.Stat(lock); err == nil && time.Since(st.ModTime()) > staleLock {
			slog.WarnContext(ctx, "taking over a stale toolchain install lock", "lock", lock)
			if err := os.Remove(lock); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// staleLock is when an install lock counts as abandoned.
const staleLock = 30 * time.Minute

// Size is the size of the installed tools on disk, for the cache metric.
func (m *Manager) Size() int64 {
	var total int64
	_ = filepath.WalkDir(m.Root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil //nolint:nilerr // an unreadable entry only costs accuracy
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}

// GC removes whole tool versions, least recently used first, until the cache is under max,
// which 0 turns off. A version in use by a running cycle keeps its files: removal only
// happens between installs, and the caller runs it from the same loop that starts them.
func (m *Manager) GC(max int64) (removed int, freed int64) {
	if max <= 0 || m.Size() <= max {
		return 0, 0
	}
	type entry struct {
		dir  string
		size int64
		used time.Time
	}
	var all []entry
	var total int64
	tools, _ := os.ReadDir(m.Root)
	for _, t := range tools {
		if !t.IsDir() {
			continue
		}
		versions, _ := os.ReadDir(filepath.Join(m.Root, t.Name()))
		for _, v := range versions {
			dir := filepath.Join(m.Root, t.Name(), v.Name())
			e := entry{dir: dir}
			if st, err := os.Stat(filepath.Join(dir, okFile)); err == nil {
				e.used = st.ModTime()
			}
			_ = filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
				if err == nil && !d.IsDir() {
					if info, err := d.Info(); err == nil {
						e.size += info.Size()
					}
				}
				return nil
			})
			total += e.size
			all = append(all, e)
		}
	}
	slices.SortFunc(all, func(a, b entry) int { return a.used.Compare(b.used) })
	for _, e := range all {
		if total <= max {
			break
		}
		m.mu.Lock()
		for key, in := range m.installed {
			if strings.HasPrefix(in.Dir, e.dir+string(filepath.Separator)) {
				delete(m.installed, key)
			}
		}
		m.mu.Unlock()
		if err := os.RemoveAll(e.dir); err != nil {
			continue
		}
		removed, freed, total = removed+1, freed+e.size, total-e.size
	}
	return removed, freed
}
