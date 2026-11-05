// Package tfregistry lists provider and module versions from Terraform registries
// (registry.terraform.io, registry.opentofu.org, HCP Terraform and other implementations of the
// provider and module registry protocols), found through service discovery.
package tfregistry

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Datasource names.
const (
	ProviderName = "terraform-provider"
	ModuleName   = "terraform-module"
)

// DefaultRegistry is the registry of deps without a Source.
const DefaultRegistry = "https://registry.terraform.io"

// Services discovered at /.well-known/terraform.json.
const (
	providersService = "providers.v1"
	modulesService   = "modules.v1"
)

// Registry shares service discovery, version lists and version documents between the
// terraform-provider and terraform-module datasources.
type Registry struct {
	client *http.Client
	sem    chan struct{}
	creds  *credentials

	services datasource.Memo[string, map[string]string]    // registry base URL -> service -> absolute base URL with trailing slash
	lists    datasource.Memo[string, []datasource.Release] // service URL + name
	docs     datasource.Memo[string, versionDoc]           // service URL + name + "/" + version
}

// versionDoc is what deps reads of a version document: when it was published and its source
// repository.
type versionDoc struct {
	published time.Time
	source    string
}

// New returns the registry client. sem bounds concurrent requests and is shared with the other
// datasources; getenv supplies TF_TOKEN_<host>, TF_CLI_CONFIG_FILE and HOME (APPDATA on Windows)
// for the registry credentials.
func New(client *http.Client, sem chan struct{}, getenv func(string) string) *Registry {
	return &Registry{client: client, sem: sem, creds: newCredentials(getenv)}
}

// Providers returns the terraform-provider datasource: d.Name is `namespace/type`, d.Source
// the registry base URL (DefaultRegistry when empty).
func (r *Registry) Providers() *Source {
	return &Source{r: r, service: providersService, kind: "provider"}
}

// Modules returns the terraform-module datasource: d.Name is `namespace/name/provider`,
// d.Source the registry base URL (DefaultRegistry when empty).
func (r *Registry) Modules() *Source { return &Source{r: r, service: modulesService, kind: "module"} }

// Source is one of the registry datasources.
type Source struct {
	r       *Registry
	service string
	kind    string // "provider" or "module", for messages
}

// Releases lists every published version of d (prereleases included; the schemes drop them
// unless the current version is one). The slice is shared and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	base, err := s.base(ctx, d)
	if err != nil {
		return nil, err
	}
	return s.r.lists.Do(base+d.Name, func() ([]datasource.Release, error) { return s.fetchVersions(ctx, d, base) })
}

// ReleaseTime implements datasource.Timer with the `published_at` of the version document
// (`<service>/<name>/<version>`, served by registry.terraform.io). A partial version (`5.31` of
// `~> 5.31`) has the time of the first release of its line. Zero when the registry has no
// such document.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	doc, err := s.doc(ctx, d, version)
	return doc.published, err
}

// SourceRepo implements datasource.SourceRepo with the `source` of the version document.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	doc, err := s.doc(ctx, d, version)
	return doc.source, err
}

// Digest is unsupported: registry versions are not digest-pinned.
func (s *Source) Digest(_ context.Context, d dep.Dep, _ string) (string, error) {
	return "", fmt.Errorf("terraform-%s: digests are not supported for %s", s.kind, d.Name)
}

// base returns the service URL of d's registry.
func (s *Source) base(ctx context.Context, d dep.Dep) (string, error) {
	reg := strings.TrimSuffix(d.Source, "/")
	if reg == "" {
		reg = DefaultRegistry
	}
	svc, err := s.r.services.Do(reg, func() (map[string]string, error) { return s.r.discover(ctx, reg) })
	if err != nil {
		return "", err
	}
	b, ok := svc[s.service]
	if !ok {
		return "", fmt.Errorf("%s has no %s service", reg, s.service)
	}
	return b, nil
}

// doc returns the version document of d at version; zero when the version is partial and has
// no published release.
func (s *Source) doc(ctx context.Context, d dep.Dep, version string) (versionDoc, error) {
	version = strings.TrimPrefix(version, "v")
	if strings.Count(version, ".") < 2 && !strings.Contains(version, "-") {
		rels, err := s.Releases(ctx, d, "")
		if err != nil {
			return versionDoc{}, err
		}
		if version = firstOfLine(rels, version); version == "" {
			return versionDoc{}, nil
		}
	}
	base, err := s.base(ctx, d)
	if err != nil {
		return versionDoc{}, err
	}
	key := base + d.Name + "/" + version
	return s.r.docs.Do(key, func() (versionDoc, error) {
		published, source, err := s.fetchDoc(ctx, d, key)
		return versionDoc{published, source}, err
	})
}

// firstOfLine returns the lowest stable release of rels whose version starts with the parts
// of line ("5.31" → "5.31.0"), or "".
func firstOfLine(rels []datasource.Release, line string) string {
	var first []int
	best := ""
	for _, r := range rels {
		v := r.Version
		if strings.Contains(v, "-") || !strings.HasPrefix(v, line+".") {
			continue
		}
		parts, ok := numbers(v)
		if ok && (best == "" || less(parts, first)) {
			best, first = v, parts
		}
	}
	return best
}

func numbers(v string) ([]int, bool) {
	var out []int
	for p := range strings.SplitSeq(v, ".") {
		n := 0
		if p == "" {
			return nil, false
		}
		for _, c := range []byte(p) {
			if c < '0' || c > '9' {
				return nil, false
			}
			n = n*10 + int(c-'0')
		}
		out = append(out, n)
	}
	return out, true
}

func less(a, b []int) bool {
	for i := range min(len(a), len(b)) {
		if a[i] != b[i] {
			return a[i] < b[i]
		}
	}
	return len(a) < len(b)
}

func (s *Source) fetchVersions(ctx context.Context, d dep.Dep, base string) (rels []datasource.Release, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "terraform registry versions", "kind", s.kind, "name", d.Name, "service", base,
			"versions", len(rels), "duration", time.Since(start), "err", err)
	}()
	target := base + d.Name + "/versions"
	var doc struct {
		Versions []struct { // providers
			Version string `json:"version"`
		} `json:"versions"`
		Modules []struct { // modules
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}
	notFound := fmt.Errorf("%s %s not found in %s", s.kind, d.Name, registryOf(d))
	if err := s.r.getJSON(ctx, target, notFound, &doc); err != nil {
		return nil, err
	}
	for _, v := range doc.Versions {
		rels = append(rels, datasource.Release{Version: v.Version})
	}
	for _, m := range doc.Modules {
		for _, v := range m.Versions {
			rels = append(rels, datasource.Release{Version: v.Version})
		}
	}
	if len(rels) == 0 {
		return nil, notFound
	}
	return rels, nil
}

// fetchDoc reads `published_at` and `source` of a version document; a missing document is
// unknown, not an error.
func (s *Source) fetchDoc(ctx context.Context, d dep.Dep, target string) (time.Time, string, error) {
	var doc struct {
		PublishedAt string `json:"published_at"`
		Source      string `json:"source"`
	}
	if err := s.r.getJSON(ctx, target, errNoDoc, &doc); err != nil {
		if errors.Is(err, errNoDoc) {
			return time.Time{}, "", nil
		}
		return time.Time{}, "", err
	}
	if doc.PublishedAt == "" {
		return time.Time{}, doc.Source, nil
	}
	t, err := time.Parse(time.RFC3339Nano, doc.PublishedAt)
	if err != nil {
		return time.Time{}, doc.Source, fmt.Errorf("%s %s: published_at: %w", s.kind, d.Name, err)
	}
	return t, doc.Source, nil
}

var errNoDoc = errors.New("no version document")

// discover reads the services of the registry at base (`https://host`).
func (r *Registry) discover(ctx context.Context, base string) (map[string]string, error) {
	target := base + "/.well-known/terraform.json"
	var raw map[string]any
	if err := r.getJSON(ctx, target, fmt.Errorf("%s is not a Terraform registry (no service discovery)", base), &raw); err != nil {
		return nil, err
	}
	ref, err := url.Parse(target)
	if err != nil {
		return nil, err
	}
	svc := map[string]string{}
	for _, name := range []string{providersService, modulesService} {
		v, ok := raw[name].(string)
		if !ok {
			continue
		}
		u, err := ref.Parse(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", target, name, err)
		}
		svc[name] = strings.TrimSuffix(u.String(), "/") + "/"
	}
	slog.DebugContext(ctx, "terraform registry discovered", "registry", base, "services", svc)
	return svc, nil
}

// getJSON GETs target and decodes the JSON body into v; a 404 returns notFound.
func (r *Registry) getJSON(ctx context.Context, target string, notFound error, v any) error {
	if r.sem != nil {
		select {
		case r.sem <- struct{}{}:
			defer func() { <-r.sem }()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", httpx.UserAgent())
	tok, err := r.creds.token(ctx, req.URL.Hostname())
	if err != nil {
		return err
	}
	if tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return notFound
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GET %s: HTTP %d", target, resp.StatusCode)
	}
	if err := json.UnmarshalRead(resp.Body, v); err != nil {
		return fmt.Errorf("GET %s: malformed response: %w", target, err)
	}
	return nil
}

func registryOf(d dep.Dep) string {
	if d.Source == "" {
		return DefaultRegistry
	}
	return d.Source
}
