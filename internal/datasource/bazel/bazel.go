// Package bazel lists the versions of Bazel modules from Bazel registries: the Bazel Central
// Registry, the registries a workspace's `.bazelrc` names with `--registry`, and the registry
// of a `single_version_override`. Each (registry, module) metadata.json is read once per run.
package bazel

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Name is the datasource name (Renovate's).
const Name = "bazel"

// DefaultURL is the Bazel Central Registry, Bazel's default `--registry`;
// DEPS_BAZEL_REGISTRY_URL overrides it (a mirror).
const DefaultURL = "https://bcr.bazel.build"

// errNotFound marks a registry that does not have the module (HTTP 404, no metadata.json).
var errNotFound = errors.New("not found")

// Source is the bazel datasource.
type Source struct {
	client *http.Client
	sem    chan struct{}
	auth   *httpx.Auth // credentials for registry hosts other than the default; nil = none
	base   string      // the default registry, without a trailing slash

	modules datasource.Memo[string, *module]  // registry + "\x00" + name
	rcs     datasource.Memo[string, []string] // workspace directory -> its .bazelrc registries

	mu      sync.Mutex
	located map[string]*module // file + "\x00" + name -> module of the Releases lookup
}

// module is a module's metadata.json: the versions that are not yanked, in registry order, and
// its source repository.
type module struct {
	rels []datasource.Release
	repo string
}

// New returns the bazel datasource. sem bounds concurrent requests (nil = unbounded); auth
// supplies the platform token of custom registry hosts (nil = none); getenv supplies
// DEPS_BAZEL_REGISTRY_URL.
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth, getenv func(string) string) *Source {
	return &Source{
		client: client, sem: sem, auth: auth,
		base:    strings.TrimSuffix(cmp.Or(getenv("DEPS_BAZEL_REGISTRY_URL"), DefaultURL), "/"),
		located: map[string]*module{},
	}
}

// Releases lists the versions of module d.Name that are not yanked, from the first of its
// registries that has the module: d.Source (whitespace-separated, a single_version_override
// registry), else the `--registry` flags of the workspace's .bazelrc (registries), else the
// Bazel Central Registry. dir is the absolute directory of d.File. The result is shared and
// must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, dir string) ([]datasource.Release, error) {
	m, err := s.lookup(ctx, d, dir)
	if err != nil {
		return nil, err
	}
	return m.rels, nil
}

// Digest is unsupported: module versions are not digest-pinned.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo with the first `repository` entry of the
// module's metadata.json (`github:<owner>/<repo>`, `gitlab:<owner>/<repo>`, or a URL), else its
// `homepage` when that is on a forge; "" without either. The metadata describes every version.
func (s *Source) SourceRepo(_ context.Context, d dep.Dep, _ string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if m := s.located[d.File+"\x00"+d.Name]; m != nil {
		return m.repo, nil
	}
	return "", nil
}

// lookup returns the module of d from the first of its registries that has it.
func (s *Source) lookup(ctx context.Context, d dep.Dep, dir string) (*module, error) {
	ws := workspace(d.File, dir)
	regs := strings.Fields(d.Source)
	for i, r := range regs {
		regs[i] = expandWorkspace(r, ws)
	}
	if len(regs) == 0 {
		var err error
		if regs, err = s.rcs.Do(ctx, ws, func() ([]string, error) {
			return readBazelrc(ws, datasource.RootOf(d.File, dir))
		}); err != nil {
			return nil, err
		}
	}
	if len(regs) == 0 {
		regs = []string{s.base}
	}
	root := datasource.RootOf(d.File, dir)
	for _, reg := range regs {
		if reg = strings.TrimSuffix(reg, "/"); reg == DefaultURL {
			reg = s.base // DEPS_BAZEL_REGISTRY_URL mirrors the BCR also where .bazelrc names it
		}
		m, err := s.modules.Do(ctx, reg+"\x00"+d.Name, func() (*module, error) { return s.fetch(ctx, root, reg, d.Name) })
		if errors.Is(err, errNotFound) {
			continue
		}
		if err == nil {
			s.mu.Lock()
			s.located[d.File+"\x00"+d.Name] = m
			s.mu.Unlock()
		}
		return m, err
	}
	return nil, fmt.Errorf("bazel module %s not found in %s", d.Name, strings.Join(redactAll(regs), ", "))
}

// workspace returns the absolute directory of the workspace d.File belongs to: the nearest
// directory from dir (d.File's) up to the scanned root holding a MODULE.bazel, else dir (a
// `*.MODULE.bazel` segment outside any module).
func workspace(file, dir string) string {
	root := datasource.RootOf(file, dir)
	for d := dir; ; d = filepath.Dir(d) {
		if st, err := os.Stat(filepath.Join(d, "MODULE.bazel")); err == nil && st.Mode().IsRegular() {
			return d
		}
		if d == root || filepath.Dir(d) == d {
			return dir
		}
	}
}

// metadata is a registry's modules/<name>/metadata.json; other fields are skipped.
type metadata struct {
	Homepage       string            `json:"homepage"`
	Repository     []string          `json:"repository"`
	Versions       []string          `json:"versions"`
	YankedVersions map[string]string `json:"yanked_versions"`
}

// fetch reads `<reg>/modules/<name>/metadata.json`: over HTTP(S), or from the file system for
// a `file://` registry inside the scanned root.
func (s *Source) fetch(ctx context.Context, root, reg, name string) (m *module, err error) {
	start := time.Now()
	defer func() {
		n := 0
		if m != nil {
			n = len(m.rels)
		}
		slog.DebugContext(ctx, "bazel module fetched", "name", name, "registry", httpx.Redact(reg), "versions", n, "duration", time.Since(start), "err", err)
	}()
	var meta metadata
	rel := "modules/" + name + "/metadata.json"
	if p, ok := strings.CutPrefix(reg, "file://"); ok {
		err = readLocal(root, p, rel, &meta)
	} else {
		err = s.get(ctx, reg+"/modules/"+url.PathEscape(name)+"/metadata.json", func(r io.Reader) error {
			return json.UnmarshalRead(r, &meta)
		})
	}
	if err != nil {
		return nil, err
	}
	m = &module{repo: repoURL(meta.Repository, meta.Homepage)}
	for _, v := range meta.Versions {
		if _, yanked := meta.YankedVersions[v]; v != "" && !yanked {
			m.rels = append(m.rels, datasource.Release{Version: v})
		}
	}
	return m, nil
}

// readLocal decodes the file rel of the local registry at dir (a file:// URL path) into meta.
// Only registries inside root are read; a missing file is errNotFound.
func readLocal(root, dir, rel string, meta *metadata) error {
	dir = filepath.Clean(filepath.FromSlash(dir))
	if r, err := filepath.Rel(root, dir); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) || !filepath.IsAbs(dir) {
		return fmt.Errorf("bazel registry file://%s: outside the repository", filepath.ToSlash(dir))
	}
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
	if errors.Is(err, os.ErrNotExist) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, meta); err != nil {
		return fmt.Errorf("bazel registry file://%s: %s: %w", filepath.ToSlash(dir), rel, err)
	}
	return nil
}

// repoURL returns the source repository named by a metadata.json `repository` list
// (`github:o/r`, `gitlab:o/r`, an http(s) URL), else homepage when it is on a forge.
func repoURL(repos []string, homepage string) string {
	for _, r := range repos {
		switch kind, p, _ := strings.Cut(r, ":"); kind {
		case "github":
			return "https://github.com/" + p
		case "gitlab":
			return "https://gitlab.com/" + p
		case "https", "http":
			return r
		}
	}
	if u, err := url.Parse(homepage); err == nil && (u.Scheme == "https" || u.Scheme == "http") {
		switch strings.ToLower(u.Hostname()) {
		case "github.com", "gitlab.com", "codeberg.org":
			return homepage
		}
	}
	return ""
}

// get GETs target while holding a slot of the semaphore and passes a 200 body to read; a 404
// is errNotFound. Hosts other than the default registry's get the configured token of their
// host (the platform token) as basic auth, unless the URL has userinfo.
func (s *Source) get(ctx context.Context, target string, read func(io.Reader) error) error {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return err
	}
	defer release()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return fmt.Errorf("bazel registry %s: invalid URL", httpx.Redact(target))
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	if req.URL.User == nil && !strings.HasPrefix(target, s.base+"/") {
		if user, pass, ok := s.auth.Static(req.URL.Hostname()); ok {
			req.SetBasicAuth(user, pass)
		}
	}
	resp, err := s.client.Do(req) // a *url.Error carries the URL without its password
	if err != nil {
		return fmt.Errorf("bazel registry %s: %w", httpx.Redact(target), err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return errNotFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("bazel registry %s: HTTP %d", httpx.Redact(target), resp.StatusCode)
	}
	if err := read(resp.Body); err != nil {
		return fmt.Errorf("bazel registry %s: malformed metadata.json: %w", httpx.Redact(target), err)
	}
	return nil
}

func redactAll(regs []string) []string {
	out := make([]string, len(regs))
	for i, r := range regs {
		out[i] = httpx.Redact(r)
	}
	return out
}

// expandWorkspace replaces Bazel's %workspace% in a registry URL or import path with the
// workspace directory (slash-separated, as Bazel does).
func expandWorkspace(s, ws string) string {
	return strings.ReplaceAll(s, "%workspace%", path.Clean(filepath.ToSlash(ws)))
}
