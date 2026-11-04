// Package dockerds lists container image tags and resolves tag digests from OCI registries.
//
// Tags come from the registry's tags/list endpoint (all pages), digests from a HEAD of the tag's
// manifest; for multi-platform images that is the index digest, which is what a pinned
// `image:tag@sha256:…` reference needs. Credentials come from the Docker config and credential
// helpers. One auth client with a shared token cache serves every repository, so each token
// exchange happens once per run and scope.
package dockerds

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/distribution/reference"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/oras-project/oras-go/v3/content"
	"github.com/oras-project/oras-go/v3/errdef"
	"github.com/oras-project/oras-go/v3/registry/remote"
	"github.com/oras-project/oras-go/v3/registry/remote/auth"
	"github.com/oras-project/oras-go/v3/registry/remote/credentials"
	"github.com/oras-project/oras-go/v3/registry/remote/errcode"
	"github.com/oras-project/oras-go/v3/registry/remote/properties"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Source resolves image tags and digests. Every lookup runs once per run per key; concurrent
// callers share the result.
type Source struct {
	client *auth.Client
	sem    chan struct{}

	mu      sync.Mutex
	tags    map[string]*tagsEntry   // canonical repository
	digests map[string]*digestEntry // canonical repository + ":" + tag
	configs map[string]*configEntry // canonical repository + ":" + tag
}

type tagsEntry struct {
	once sync.Once
	rels []datasource.Release
	err  error
}

type digestEntry struct {
	once   sync.Once
	digest string
	err    error
}

// configEntry is what deps keeps of a tag's image config: its creation time and source label.
type configEntry struct {
	once    sync.Once
	created time.Time
	source  string
	err     error
}

// sourceLabel is the OCI annotation (and image label) naming the image's source repository.
const sourceLabel = "org.opencontainers.image.source"

// New returns the docker datasource. Requests go through client's transport; sem bounds
// concurrent registry operations and is shared with other datasources (nil = unbounded).
func New(client *http.Client, sem chan struct{}) datasource.Datasource {
	return newSource(client, sem, dockerCredentials())
}

// dockerCredentials reads credentials from the Docker config ($DOCKER_CONFIG or ~/.docker) and
// its credential helpers, as the Docker CLI does: a platform credential store is used only when
// the config names it. An unreadable config fails every authenticated lookup with its error.
func dockerCredentials() credentials.CredentialFunc {
	store, err := credentials.NewStoreFromDocker(credentials.StoreOptions{IgnoreDefaultNativeStore: true})
	if err != nil {
		return func(context.Context, properties.Resource) (credentials.Credential, error) {
			return credentials.EmptyCredential, fmt.Errorf("docker config: %w", err)
		}
	}
	return remote.NewCredentialFunc(store)
}

// hubPageSize is the tags/list page size for Docker Hub, which pages at 1000 by default but
// accepts larger pages: library/node (9270 tags) takes one 2s request instead of ten (7.7s).
// Other registries keep the default, since registries are free to cap or refuse a large n.
const hubPageSize = 10000

// hubRegistry is the registry of normalized Docker Hub references.
const hubRegistry = "docker.io"

// maxMetadataBytes bounds a tags/list page or manifest. oras-go's 4 MiB default is too small for
// gcr.io, which ignores n and returns every tag with a digest map in one page
// (gcr.io/distroless/static: 29769 tags, 9.9 MB).
const maxMetadataBytes = 64 << 20

func newSource(client *http.Client, sem chan struct{}, cred credentials.CredentialFunc) *Source {
	tr := http.DefaultTransport
	if client != nil && client.Transport != nil {
		tr = client.Transport
	}
	// The client has no retry layer of its own: the shared httpx transport already retries, and
	// nesting both would multiply attempts.
	c := &auth.Client{
		Client:         &http.Client{Transport: linkFixer{tr}},
		CredentialFunc: cred,
		Cache:          auth.NewCache(),
	}
	c.SetUserAgent(httpx.UserAgent())
	// Bearer tokens come from the distribution-spec token flow (GET with basic credentials), as
	// with the Docker CLI, instead of oras-go's default OAuth2 password grant with fallback, which
	// costs registries without an OAuth2 endpoint a rejected request per realm.
	c.TokenFetcher = auth.NewCompositeTokenFetcher(c.Client, c.Header, c.ClientID, true)
	return &Source{
		client:  c,
		sem:     sem,
		tags:    map[string]*tagsEntry{},
		digests: map[string]*digestEntry{},
		configs: map[string]*configEntry{},
	}
}

// repository returns the registry client for the canonical repository name (as returned by
// repoName). Docker Hub lists tags with hubPageSize; registries on loopback, private addresses
// and localhost are reached over plain HTTP.
func (s *Source) repository(name string) (*remote.Repository, error) {
	r, err := remote.NewRepository(name)
	if err != nil {
		return nil, err
	}
	r.Registry.Client = s.client
	r.Registry.PlainHTTP = plainHTTP(r.Registry.Reference.Registry)
	r.Registry.MaxMetadataBytes = maxMetadataBytes
	if r.Registry.Reference.Registry == hubRegistry {
		r.TagListPageSize = hubPageSize
	}
	return r, nil
}

// plainHTTP reports whether registry (host[:port]) is local: localhost, a loopback or a private
// address.
func plainHTTP(registry string) bool {
	host := registry
	if h, _, err := net.SplitHostPort(registry); err == nil {
		host = h
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate())
}

// Releases lists every tag of the image (d.Source, or d.Name when Source is empty) without
// digests; the engine asks Digest for the versions it pins. The slice is shared between callers
// and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	name, err := repoName(d)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	e := s.tags[name]
	if e == nil {
		e = &tagsEntry{}
		s.tags[name] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.rels, e.err = s.list(ctx, name) })
	return e.rels, e.err
}

// Digest returns the manifest digest ("sha256:…") of tag version: the index digest for
// multi-platform images.
func (s *Source) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	name, err := repoName(d)
	if err != nil {
		return "", err
	}
	key := name + ":" + version
	s.mu.Lock()
	e := s.digests[key]
	if e == nil {
		e = &digestEntry{}
		s.digests[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.digest, e.err = s.head(ctx, name, version) })
	return e.digest, e.err
}

// ReleaseTime implements datasource.Timer with the `created` time of the image config of tag
// version (for an index: of its first platform image); zero when the config has none.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	e := s.config(ctx, d, version)
	return e.created, e.err
}

// SourceRepo implements datasource.SourceRepo with the image's
// `org.opencontainers.image.source` label, from the same config fetch as ReleaseTime.
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	e := s.config(ctx, d, version)
	return e.source, e.err
}

// config fetches the image config of tag version once per (repository, tag).
func (s *Source) config(ctx context.Context, d dep.Dep, version string) *configEntry {
	name, err := repoName(d)
	if err != nil {
		return &configEntry{err: err}
	}
	key := name + ":" + version
	s.mu.Lock()
	e := s.configs[key]
	if e == nil {
		e = &configEntry{}
		s.configs[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		e.created, e.source, e.err = s.fetchConfig(ctx, name, version)
		if e.err != nil {
			e.err = fmt.Errorf("image config %s: %w", key, e.err)
		}
		slog.DebugContext(ctx, "image config fetched", "ref", key, "created", e.created, "source", e.source, "err", e.err)
	})
	return e
}

// fetchConfig reads the config blob of tag: manifest, (index: first platform manifest), config.
// Artifacts whose config is no image config (Helm charts) are not fetched further: their
// manifest's `org.opencontainers.image.created` and `…source` annotations answer instead. An
// image config without `created` falls back to the manifest annotation as well.
func (s *Source) fetchConfig(ctx context.Context, name, tag string) (time.Time, string, error) {
	if err := s.acquire(ctx); err != nil {
		return time.Time{}, "", err
	}
	defer s.release()
	repo, err := s.repository(name)
	if err != nil {
		return time.Time{}, "", err
	}
	desc, b, err := fetchManifest(ctx, repo, tag)
	if err != nil {
		return time.Time{}, "", err
	}
	if isIndex(desc.MediaType) {
		if desc, err = firstImage(b); err != nil {
			return time.Time{}, "", err
		}
		if b, err = content.FetchAll(ctx, repo, desc); err != nil {
			return time.Time{}, "", err
		}
	}
	var m ocispec.Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return time.Time{}, "", fmt.Errorf("manifest %s: %w", desc.Digest, err)
	}
	created, _ := time.Parse(time.RFC3339, m.Annotations[ocispec.AnnotationCreated])
	source := m.Annotations[sourceLabel]
	if m.Config.MediaType != ocispec.MediaTypeImageConfig && m.Config.MediaType != mediaTypeDockerConfig {
		return created, source, nil
	}
	if b, err = content.FetchAll(ctx, repo, m.Config); err != nil {
		return time.Time{}, "", err
	}
	var img ocispec.Image
	if err := json.Unmarshal(b, &img); err != nil {
		return time.Time{}, "", fmt.Errorf("config %s: %w", m.Config.Digest, err)
	}
	if img.Created != nil {
		created = *img.Created
	}
	return created, cmp.Or(img.Config.Labels[sourceLabel], source), nil
}

// fetchManifest GETs the manifest of tag; the descriptor's digest is the registry's, or computed
// from the body when the registry sends none.
func fetchManifest(ctx context.Context, repo *remote.Repository, tag string) (ocispec.Descriptor, []byte, error) {
	desc, rc, err := repo.FetchReference(ctx, tag)
	if err != nil {
		return ocispec.Descriptor{}, nil, err
	}
	defer rc.Close()
	b, err := content.ReadAll(rc, desc)
	return desc, b, err
}

// isIndex reports whether mediaType is a multi-platform index (OCI or Docker manifest list).
func isIndex(mediaType string) bool {
	return mediaType == ocispec.MediaTypeImageIndex || mediaType == mediaTypeDockerManifestList
}

const (
	mediaTypeDockerManifestList = "application/vnd.docker.distribution.manifest.list.v2+json"
	mediaTypeDockerManifest     = "application/vnd.docker.distribution.manifest.v2+json"
	mediaTypeDockerConfig       = "application/vnd.docker.container.image.v1+json"
)

// firstImage returns the first platform image manifest of index b, skipping attestation
// manifests (platform unknown/unknown).
func firstImage(b []byte) (ocispec.Descriptor, error) {
	var idx ocispec.Index
	if err := json.Unmarshal(b, &idx); err != nil {
		return ocispec.Descriptor{}, fmt.Errorf("index: %w", err)
	}
	for _, m := range idx.Manifests {
		image := m.MediaType == ocispec.MediaTypeImageManifest || m.MediaType == mediaTypeDockerManifest
		if image && (m.Platform == nil || m.Platform.OS != "unknown") {
			return m, nil
		}
	}
	return ocispec.Descriptor{}, errors.New("index has no platform image")
}

func (s *Source) list(ctx context.Context, name string) (rels []datasource.Release, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "image tags listed", "repo", name, "tags", len(rels), "duration", time.Since(start), "err", err)
	}()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	defer s.release()
	repo, err := s.repository(name)
	if err != nil {
		return nil, fmt.Errorf("list tags %s: %w", name, err)
	}
	err = repo.Tags(ctx, "", func(tags []string) error {
		for _, t := range tags {
			rels = append(rels, datasource.Release{Version: t})
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("list tags %s: %w", name, err)
	}
	return rels, nil
}

// head resolves the digest with a HEAD request. A registry that omits the digest header gets a
// GET, whose digest is computed from the manifest; HTTP and auth errors are not retried with GET,
// and a 404 becomes "tag <v> not found upstream".
func (s *Source) head(ctx context.Context, name, tag string) (digest string, err error) {
	start, method := time.Now(), http.MethodHead
	defer func() {
		slog.DebugContext(ctx, "image digest resolved", "ref", name+":"+tag, "method", method, "digest", digest, "duration", time.Since(start), "err", err)
	}()
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	repo, err := s.repository(name)
	if err != nil {
		return "", fmt.Errorf("digest %s:%s: %w", name, tag, err)
	}
	desc, err := repo.Resolve(ctx, tag)
	if err == nil {
		return desc.Digest.String(), nil
	}
	if errors.Is(err, errdef.ErrNotFound) {
		return "", fmt.Errorf("tag %s not found upstream", tag)
	}
	if httpErr := (*errcode.ErrorResponse)(nil); errors.As(err, &httpErr) || errors.Is(err, auth.ErrBasicCredentialNotFound) || ctx.Err() != nil {
		return "", fmt.Errorf("digest %s:%s: %w", name, tag, err)
	}
	method = http.MethodGet
	desc, rc, err := repo.FetchReference(ctx, tag)
	if err != nil {
		return "", fmt.Errorf("digest %s:%s: %w", name, tag, err)
	}
	rc.Close()
	return desc.Digest.String(), nil
}

// repoName returns the canonical repository of d: d.Source, or d.Name normalized
// (`alpine` → `docker.io/library/alpine`) when Source is empty; tags and digests are dropped.
func repoName(d dep.Dep) (string, error) {
	ref := d.Source
	if ref == "" {
		ref = d.Name
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", ref, err)
	}
	return named.Name(), nil
}

func (s *Source) acquire(ctx context.Context) error {
	if s.sem == nil {
		return ctx.Err()
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
