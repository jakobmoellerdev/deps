// Package dockerds lists container image tags and resolves tag digests from OCI registries.
//
// Tags come from the registry's tags/list endpoint (all pages), digests from a HEAD of the tag's
// manifest; for multi-platform images that is the index digest, which is what a pinned
// `image:tag@sha256:…` reference needs. Credentials come from the Docker config and credential
// helpers (authn.DefaultKeychain). Pullers are shared, so the registry ping and token exchange
// happen once per repository.
package dockerds

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/distribution/reference"
	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/remote/transport"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Source resolves image tags and digests. Every lookup runs once per run per key; concurrent
// callers share the result.
type Source struct {
	puller *remote.Puller // default page size
	hub    *remote.Puller // Docker Hub: hubPageSize
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
	return newSource(client, sem, authn.DefaultKeychain)
}

// hubPageSize is the tags/list page size for Docker Hub, which pages at 1000 by default but
// accepts larger pages: library/node (9270 tags) takes one 2s request instead of ten (7.7s).
// Other registries keep the default, since registries are free to cap or refuse a large n.
const hubPageSize = 10000

func newSource(client *http.Client, sem chan struct{}, kc authn.Keychain) *Source {
	tr := http.DefaultTransport
	if client != nil && client.Transport != nil {
		tr = client.Transport
	}
	return &Source{
		puller:  newPuller(tr, kc),
		hub:     newPuller(tr, kc, remote.WithPageSize(hubPageSize)),
		sem:     sem,
		tags:    map[string]*tagsEntry{},
		digests: map[string]*digestEntry{},
		configs: map[string]*configEntry{},
	}
}

// noRetry makes go-containerregistry send each request once: the shared httpx transport already
// retries, and nesting both would multiply attempts.
var noRetry = remote.WithRetryBackoff(remote.Backoff{Steps: 1})

func newPuller(tr http.RoundTripper, kc authn.Keychain, extra ...remote.Option) *remote.Puller {
	opts := make([]remote.Option, 0, 4+len(extra))
	opts = append(opts, remote.WithTransport(tr), remote.WithAuthFromKeychain(kc), remote.WithUserAgent(httpx.UserAgent()), noRetry)
	p, err := remote.NewPuller(append(opts, extra...)...)
	if err != nil {
		// Only conflicting auth options fail, and only a keychain is passed.
		panic(err)
	}
	return p
}

// pullerFor returns the puller for repo's registry.
func (s *Source) pullerFor(repo name.Repository) *remote.Puller {
	if repo.RegistryStr() == name.DefaultRegistry {
		return s.hub
	}
	return s.puller
}

// Releases lists every tag of the image (d.Source, or d.Name when Source is empty) without
// digests; the engine asks Digest for the versions it pins. The slice is shared between callers
// and must not be modified.
func (s *Source) Releases(ctx context.Context, d dep.Dep, _ string) ([]datasource.Release, error) {
	repo, err := repository(d)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	e := s.tags[repo.Name()]
	if e == nil {
		e = &tagsEntry{}
		s.tags[repo.Name()] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.rels, e.err = s.list(ctx, repo) })
	return e.rels, e.err
}

// Digest returns the manifest digest ("sha256:…") of tag version: the index digest for
// multi-platform images.
func (s *Source) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	repo, err := repository(d)
	if err != nil {
		return "", err
	}
	tag := repo.Tag(version)
	key := tag.Name()
	s.mu.Lock()
	e := s.digests[key]
	if e == nil {
		e = &digestEntry{}
		s.digests[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.digest, e.err = s.head(ctx, tag) })
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
	repo, err := repository(d)
	if err != nil {
		return &configEntry{err: err}
	}
	tag := repo.Tag(version)
	key := tag.Name()
	s.mu.Lock()
	e := s.configs[key]
	if e == nil {
		e = &configEntry{}
		s.configs[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		e.created, e.source, e.err = s.fetchConfig(ctx, tag)
		slog.DebugContext(ctx, "image config fetched", "ref", key, "created", e.created, "source", e.source, "err", e.err)
	})
	return e
}

// fetchConfig reads the config blob of tag: manifest, (index: first platform manifest), config.
func (s *Source) fetchConfig(ctx context.Context, tag name.Tag) (time.Time, string, error) {
	if err := s.acquire(ctx); err != nil {
		return time.Time{}, "", err
	}
	defer s.release()
	desc, err := s.pullerFor(tag.Repository).Get(ctx, tag)
	if err != nil {
		return time.Time{}, "", fmt.Errorf("image config %s: %w", tag.Name(), err)
	}
	var img v1.Image
	if desc.MediaType.IsIndex() {
		img, err = firstImage(desc)
	} else {
		img, err = desc.Image()
	}
	if err != nil {
		return time.Time{}, "", fmt.Errorf("image config %s: %w", tag.Name(), err)
	}
	cf, err := img.ConfigFile()
	if err != nil {
		return time.Time{}, "", fmt.Errorf("image config %s: %w", tag.Name(), err)
	}
	return cf.Created.Time, cf.Config.Labels[sourceLabel], nil
}

// firstImage returns the first platform image of an index, skipping attestation manifests
// (platform unknown/unknown).
func firstImage(desc *remote.Descriptor) (v1.Image, error) {
	idx, err := desc.ImageIndex()
	if err != nil {
		return nil, err
	}
	im, err := idx.IndexManifest()
	if err != nil {
		return nil, err
	}
	for _, m := range im.Manifests {
		if m.MediaType.IsImage() && (m.Platform == nil || m.Platform.OS != "unknown") {
			return idx.Image(m.Digest)
		}
	}
	return nil, errors.New("index has no platform image")
}

func (s *Source) list(ctx context.Context, repo name.Repository) (rels []datasource.Release, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "image tags listed", "repo", repo.Name(), "tags", len(rels), "duration", time.Since(start), "err", err)
	}()
	if err := s.acquire(ctx); err != nil {
		return nil, err
	}
	tags, err := s.pullerFor(repo).List(ctx, repo)
	s.release()
	if err != nil {
		return nil, fmt.Errorf("list tags %s: %w", repo.Name(), err)
	}
	rels = make([]datasource.Release, len(tags))
	for i, t := range tags {
		rels[i].Version = t
	}
	return rels, nil
}

// head resolves the digest with a HEAD request. A registry that omits the digest header gets a
// GET, whose digest is computed from the manifest; HTTP errors are not retried with GET, and a
// 404 becomes "tag <v> not found upstream".
func (s *Source) head(ctx context.Context, tag name.Tag) (digest string, err error) {
	start, method := time.Now(), http.MethodHead
	defer func() {
		slog.DebugContext(ctx, "image digest resolved", "ref", tag.Name(), "method", method, "digest", digest, "duration", time.Since(start), "err", err)
	}()
	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()
	p := s.pullerFor(tag.Repository)
	desc, err := p.Head(ctx, tag)
	if err == nil {
		return desc.Digest.String(), nil
	}
	if te := (*transport.Error)(nil); errors.As(err, &te) || ctx.Err() != nil {
		if te != nil && te.StatusCode == http.StatusNotFound {
			return "", fmt.Errorf("tag %s not found upstream", tag.TagStr())
		}
		return "", fmt.Errorf("digest %s: %w", tag.Name(), err)
	}
	method = http.MethodGet
	got, err := p.Get(ctx, tag)
	if err != nil {
		return "", fmt.Errorf("digest %s: %w", tag.Name(), err)
	}
	return got.Digest.String(), nil
}

// repository returns the canonical repository of d: d.Source, or d.Name normalized
// (`alpine` → `docker.io/library/alpine`) when Source is empty; tags and digests are dropped.
func repository(d dep.Dep) (name.Repository, error) {
	ref := d.Source
	if ref == "" {
		ref = d.Name
	}
	named, err := reference.ParseNormalizedNamed(ref)
	if err != nil {
		return name.Repository{}, fmt.Errorf("image %q: %w", ref, err)
	}
	repo, err := name.NewRepository(named.Name())
	if err != nil {
		return name.Repository{}, fmt.Errorf("image %q: %w", ref, err)
	}
	return repo, nil
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
