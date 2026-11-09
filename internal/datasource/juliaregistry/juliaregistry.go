// Package juliaregistry lists the versions of Julia packages from the General registry
// (JuliaRegistries/General), read file by file from its git tree: a package's Package.toml
// (its UUID and repository) and Versions.toml. Lookups run once per package and are shared by
// every dep of a run.
package juliaregistry

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// Name is the datasource name.
const Name = "julia-registry"

// DefaultURL is the raw file base of the General registry's default branch;
// DEPS_JULIA_REGISTRY_URL overrides it (a mirror, or another registry with the same layout).
const DefaultURL = "https://raw.githubusercontent.com/JuliaRegistries/General/HEAD"

// maxFile bounds a registry file read (the largest Versions.toml files are a few hundred KiB).
const maxFile = 16 << 20

// errNotFound marks a registry file that does not exist (HTTP 404).
var errNotFound = errors.New("not found")

// Source is the julia-registry datasource.
type Source struct {
	getter httpx.Getter
	base   string // without a trailing slash
	shown  string // base with its password masked, for messages

	pkgs datasource.Memo[string, *pkg] // by name
}

// pkg is what the registry holds for a package.
type pkg struct {
	uuid, repo string
	rels       []datasource.Release
}

// New returns the julia-registry datasource. sem bounds concurrent requests (nil = unbounded);
// getenv supplies DEPS_JULIA_REGISTRY_URL.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Source {
	base := strings.TrimSuffix(cmp.Or(getenv("DEPS_JULIA_REGISTRY_URL"), DefaultURL), "/")
	return &Source{getter: httpx.Getter{Client: client, Sem: sem, MaxBody: maxFile}, base: base, shown: httpx.Redact(base)}
}

// Path is the directory of the package name in a registry: `<first letter, upper-cased>/<name>`,
// and `jll/<letter>/<name>` for the `_jll` binary wrapper packages, as the General registry
// files every package (RegistryTools' package path).
func Path(name string) string {
	r, _ := utf8.DecodeRuneInString(name)
	p := string(unicode.ToUpper(r)) + "/" + name
	if strings.HasSuffix(name, "_jll") {
		return "jll/" + p
	}
	return p
}

// Releases lists the registered, not yanked versions of d (Name the package name, Source its
// UUID from the project's [deps], [weakdeps] or [extras]; "" = any). A package the registry
// does not have, or has under another UUID (a package of another registry with the same
// name), skips d. The result is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return nil, err
	}
	return p.rels, nil
}

// Digest is unsupported: Julia compat entries hold versions only.
func (*Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", datasource.UnsupportedDigest(Name, d)
}

// SourceRepo implements datasource.SourceRepo with the repository Package.toml names
// (`https://github.com/JuliaIO/JSON.jl.git`), the same for every version.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, _ string) (string, error) {
	p, err := s.lookup(ctx, d)
	if err != nil {
		return "", err
	}
	return p.repo, nil
}

// lookup returns the registry entry of d's package, checking its UUID.
func (s *Source) lookup(ctx context.Context, d dep.Dep) (*pkg, error) {
	p, err := s.pkgs.Do(ctx, d.Name, func() (*pkg, error) { return s.fetch(ctx, d.Name) })
	switch {
	case errors.Is(err, errNotFound):
		return nil, &dep.SkipError{Reason: fmt.Sprintf("package %s is not in the registry %s", d.Name, s.shown)}
	case err != nil:
		return nil, err
	case d.Source != "" && !strings.EqualFold(p.uuid, d.Source):
		return nil, &dep.SkipError{Reason: fmt.Sprintf("package %s of the registry %s has UUID %s, not %s", d.Name, s.shown, p.uuid, d.Source)}
	}
	return p, nil
}

// fetch reads Package.toml and Versions.toml of name, concurrently.
func (s *Source) fetch(ctx context.Context, name string) (p *pkg, err error) {
	start := time.Now()
	defer func() {
		n := 0
		if p != nil {
			n = len(p.rels)
		}
		slog.DebugContext(ctx, "julia package fetched", "name", name, "registry", s.shown, "versions", n, "duration", time.Since(start), "err", err)
	}()
	dir := s.base + "/" + Path(name)
	var (
		wg              sync.WaitGroup
		meta, versions  *tomlspan.Value
		metaErr, verErr error
	)
	wg.Go(func() { meta, metaErr = s.get(ctx, dir+"/Package.toml") })
	versions, verErr = s.get(ctx, dir+"/Versions.toml")
	wg.Wait()
	if err := cmp.Or(metaErr, verErr); err != nil {
		return nil, err
	}
	p = &pkg{uuid: str(meta, "uuid"), repo: str(meta, "repo")}
	if got := str(meta, "name"); got != name {
		return nil, fmt.Errorf("julia-registry %s: Package.toml names %q, not %q", httpx.Redact(dir), got, name)
	}
	for _, v := range versions.Keys {
		e := versions.Fields[v]
		if e.Kind != tomlspan.Table {
			continue
		}
		if y := e.Fields["yanked"]; y != nil && y.Kind == tomlspan.Scalar && y.Str == "true" {
			continue
		}
		p.rels = append(p.rels, datasource.Release{Version: v})
	}
	return p, nil
}

// str is the string field k of the table t, "" when absent.
func str(t *tomlspan.Value, k string) string {
	if v := t.Get(k); v != nil && v.Kind == tomlspan.String {
		return v.Str
	}
	return ""
}

// get GETs target and parses the body as TOML. A 404 is errNotFound. Credentials in the base
// URL's userinfo are sent as basic auth.
func (s *Source) get(ctx context.Context, target string) (*tomlspan.Value, error) {
	var t *tomlspan.Value
	err := s.getter.Stream(ctx, target, func(r io.Reader) error {
		b, err := io.ReadAll(r)
		if err != nil {
			return err
		}
		if t, err = tomlspan.Parse(b); err != nil {
			return fmt.Errorf("malformed TOML: %w", err)
		}
		return nil
	})
	switch {
	case errors.Is(err, httpx.ErrNotFound):
		return nil, errNotFound
	case err != nil:
		return nil, err
	}
	return t, nil
}
