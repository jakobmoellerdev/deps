// Package dockerds lists container image tags and resolves tag digests from OCI registries.
//
// Tags come from the registry's tags/list endpoint (all pages up to a bound, tagPageCap),
// digests from a HEAD of the tag's manifest; for multi-platform images that is the index digest,
// which is what a pinned `image:tag@sha256:…` reference needs. gcr.io and Artifact Registry list
// each tag's digest and times along with it, which then answer Digest and ReleaseTime without
// further requests. Credentials come from the Docker config and credential helpers. One registry
// client with a shared token cache serves every repository, so each token exchange happens once
// per run and scope; on Docker Hub and ghcr.io the first request to a repository is answered
// with the registry's last challenge instead of a 401 round trip (see challenger).
package dockerds

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
)

// Source resolves image tags and digests. Every lookup runs once per run per key; concurrent
// callers share the result.
type Source struct {
	client *client
	sem    chan struct{}
	// pullLimited is the registry that counts manifest GETs, but not HEADs, against a pull
	// limit per IP address or account: Docker Hub.
	pullLimited string
	// challenger answers the first request to each repository of Docker Hub and ghcr.io with
	// the registry's bearer challenge.
	challenger *challenger

	mu      sync.Mutex
	tags    map[string]*tagsEntry   // canonical repository
	digests map[string]*digestEntry // canonical repository + ":" + tag
	configs map[string]*configEntry // canonical repository + ":" + tag, or + "@" + digest
}

type tagsEntry struct {
	once sync.Once
	done atomic.Bool // set once rels, meta and err are final
	rels []datasource.Release
	meta map[string]tagMeta // tag → listed digest and time; nil unless the registry lists them
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

// hubPageSize is the tags/list page size for Docker Hub, which pages at 1000 by default but
// accepts larger pages: library/node (9270 tags) takes one 2s request instead of ten (7.7s).
// Hub's own API (hub.docker.com/v2/repositories/<repo>/tags?ordering=last_updated), which
// Renovate uses so that its page cap keeps the newest tags, pages at no more than 100 tags:
// library/node would take 93 requests.
const hubPageSize = 10000

// pageSize is the tags/list page size for other registries. ghcr.io pages at 100 by default
// and caps n at 1000 (spiffe/spire-agent, 1713 tags: 2 requests instead of 18); AWS ECR
// refuses n above 1000, so larger pages are left to Docker Hub. Registries that page smaller
// (quay.io) or ignore n (gcr.io) are unaffected.
const pageSize = 1000

// maxTagPages bounds a tags/list listing, as Renovate's dockerMaxPages (default 20) does: up to
// 200,000 tags on Docker Hub and 20,000 elsewhere. A repository with more tags (a per-commit CI
// image) would otherwise dominate a run; its tags beyond the cap are not considered.
const maxTagPages = 20

// maxTagPagesOldestFirst is maxTagPages for registries that do not list tags in lexical order
// but oldest first, so that a truncated listing would miss exactly the new releases. As in
// Renovate, they are listed whole up to a far higher bound.
const maxTagPagesOldestFirst = 1000

// oldestFirst are the registries of maxTagPagesOldestFirst (Renovate's hostsNeedingAllPages):
// ghcr.io and quay.io list oldest first, cgr.dev and the Red Hat registries sort lexically but
// publish a tag per build, so current versions sort past the cap.
var oldestFirst = map[string]bool{
	"ghcr.io":                    true,
	"quay.io":                    true,
	"cgr.dev":                    true,
	"registry.access.redhat.com": true,
	"registry.redhat.io":         true,
}

// tagPageCap returns the tags/list page bound for registry (host[:port]).
func tagPageCap(registry string) int {
	if oldestFirst[registry] {
		return maxTagPagesOldestFirst
	}
	return maxTagPages
}

// hubRegistry is the registry of normalized Docker Hub references.
const hubRegistry = "docker.io"

// maxMetadataBytes bounds a tags/list page, manifest or image config. It is well above 4 MiB
// for gcr.io, which ignores n and returns every tag with a digest map in one page
// (gcr.io/distroless/static: 29769 tags, 9.9 MB). Pages are stream-decoded, so the bound is on
// the bytes read, not on memory held.
const maxMetadataBytes = 64 << 20

// oauth2ClientID is the client_id of OAuth2 token requests.
const oauth2ClientID = "deps"

func newSource(client *http.Client, sem chan struct{}, cred credentialFunc) *Source {
	tr := http.DefaultTransport
	if client != nil && client.Transport != nil {
		tr = client.Transport
	}
	// The client has no retry layer of its own: the shared httpx transport already retries, and
	// nesting both would multiply attempts. Bearer tokens come from the distribution-spec token
	// flow (GET with basic credentials), as with the Docker CLI, not from an OAuth2 password
	// grant, which costs registries without an OAuth2 endpoint a rejected request per realm.
	ch := newChallenger(linkFixer{tr})
	c := newClient(&http.Client{Transport: ch}, http.Header{"User-Agent": {httpx.UserAgent()}}, cred, oauth2ClientID)
	return &Source{
		client:      c,
		sem:         sem,
		pullLimited: hubRegistry,
		challenger:  ch,
		tags:        map[string]*tagsEntry{},
		digests:     map[string]*digestEntry{},
		configs:     map[string]*configEntry{},
	}
}

// repository returns the registry repository of the canonical repository name (as returned by
// repoName). Tags are listed with hubPageSize on Docker Hub and pageSize elsewhere, up to
// tagPageCap pages; registries on loopback, private addresses and localhost are reached over
// plain HTTP.
func (s *Source) repository(name string) *repository {
	registry, path, _ := strings.Cut(name, "/")
	r := &repository{
		registry:  registry,
		path:      path,
		host:      registry,
		plainHTTP: plainHTTP(registry),
		pageSize:  pageSize,
		maxPages:  tagPageCap(registry),
	}
	if registry == hubRegistry {
		r.host = "registry-1.docker.io"
		r.pageSize = hubPageSize
	}
	return r
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

// Releases lists every tag of the image (d.Source, or d.Name when Source is empty). Only tags
// from a registry that lists digests (gcr.io, Artifact Registry) carry one; the engine asks
// Digest for the others it pins. The slice is shared between callers and must not be modified.
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
	e.once.Do(func() {
		e.rels, e.meta, e.err = s.list(ctx, name)
		e.done.Store(true)
	})
	return e.rels, e.err
}

// listed returns what the finished tag listing of the canonical repository name says about
// tag version; zero when the registry lists no digests and times, the listing has not
// finished, or it lacks the tag. It never lists itself: the engine resolves non-version tags
// (latest) without a listing.
func (s *Source) listed(name, version string) tagMeta {
	s.mu.Lock()
	e := s.tags[name]
	s.mu.Unlock()
	if e == nil || !e.done.Load() {
		return tagMeta{}
	}
	return e.meta[version]
}

// Digest returns the manifest digest ("sha256:…") of tag version: the index digest for
// multi-platform images. A digest from the tag listing answers without a request.
func (s *Source) Digest(ctx context.Context, d dep.Dep, version string) (string, error) {
	name, err := repoName(d)
	if err != nil {
		return "", err
	}
	if m := s.listed(name, version); m.digest != "" {
		return m.digest, nil
	}
	return s.digest(ctx, name, version)
}

// digest resolves the digest of tag in the canonical repository name once per run.
func (s *Source) digest(ctx context.Context, name, tag string) (string, error) {
	key := name + ":" + tag
	s.mu.Lock()
	e := s.digests[key]
	if e == nil {
		e = &digestEntry{}
		s.digests[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() { e.digest, e.err = s.head(ctx, name, tag) })
	return e.digest, e.err
}

// ReleaseTime implements datasource.Timer with the time the tag listing gives (gcr.io,
// Artifact Registry; see tagTime), else the `created` annotation of an index's first platform
// descriptor, else the `created` time of the image config of tag version (for an index: of its
// first platform image); zero when none has one.
func (s *Source) ReleaseTime(ctx context.Context, d dep.Dep, version string) (time.Time, error) {
	name, err := repoName(d)
	if err != nil {
		return time.Time{}, err
	}
	if m := s.listed(name, version); !m.time.IsZero() {
		return m.time, nil
	}
	e := s.config(ctx, name, version)
	return e.created, e.err
}

// SourceRepo implements datasource.SourceRepo with the image's
// `org.opencontainers.image.source` annotation or label, from the same fetch as ReleaseTime
// (which the tag listing does not replace here: it has no labels).
func (s *Source) SourceRepo(ctx context.Context, d dep.Dep, version string) (string, error) {
	name, err := repoName(d)
	if err != nil {
		return "", err
	}
	e := s.config(ctx, name, version)
	return e.source, e.err
}

// config fetches the image config of tag version of the canonical repository name once per
// (repository, tag). On the pull-limited registry the tag is first resolved with the HEAD that
// Digest shares, which is free, and the config is fetched once per (repository, digest): tags
// of one image (postgres:18 and postgres:18.6) cost one counted manifest GET.
func (s *Source) config(ctx context.Context, name, version string) *configEntry {
	key, ref := name+":"+version, version
	if s.limitsPulls(name) {
		dg, err := s.digest(ctx, name, version)
		if err != nil {
			return &configEntry{err: fmt.Errorf("image config %s: %w", key, err)}
		}
		key, ref = name+"@"+dg, dg
	}
	s.mu.Lock()
	e := s.configs[key]
	if e == nil {
		e = &configEntry{}
		s.configs[key] = e
	}
	s.mu.Unlock()
	e.once.Do(func() {
		e.created, e.source, e.err = s.fetchConfig(ctx, name, ref)
		if e.err != nil {
			e.err = s.pullLimitHint(name, fmt.Errorf("image config %s: %w", key, e.err))
		}
		slog.DebugContext(ctx, "image config fetched", "ref", key, "created", e.created, "source", e.source, "err", e.err)
	})
	return e
}

// limitsPulls reports whether the canonical repository name is on the pull-limited registry.
func (s *Source) limitsPulls(name string) bool {
	registry, _, _ := strings.Cut(name, "/")
	return registry == s.pullLimited
}

// pullLimitHint adds how to raise the limit to a rate-limit error of the pull-limited registry.
func (s *Source) pullLimitHint(name string, err error) error {
	if !s.limitsPulls(name) || !errors.Is(err, httpx.ErrRateLimited) {
		return err
	}
	return fmt.Errorf("%w (Docker Hub limits anonymous pulls per IP address; log in with `docker login`, "+
		"or add credentials for docker.io to $DOCKER_CONFIG/config.json, default ~/.docker/config.json)", err)
}

// fetchConfig reads the config blob of ref (tag or digest): manifest, (index: first platform
// manifest), config. Artifacts whose config is no image config (Helm charts) are not fetched
// further: their manifest's `org.opencontainers.image.created` and `…source` annotations answer
// instead. An image config without `created` falls back to the manifest annotation as well.
// An index whose first platform descriptor carries a `created` annotation (Docker official
// images, BuildKit with annotations) answers from the descriptor (source: descriptor, else index
// annotation) without fetching the platform manifest and config.
func (s *Source) fetchConfig(ctx context.Context, name, ref string) (time.Time, string, error) {
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return time.Time{}, "", err
	}
	defer release()
	repo := s.repository(name)
	desc, b, err := s.fetchManifest(ctx, repo, ref)
	if err != nil {
		return time.Time{}, "", err
	}
	if isIndex(desc.MediaType) {
		var idxAnnotations map[string]string
		if desc, idxAnnotations, err = firstImage(b); err != nil {
			return time.Time{}, "", err
		}
		if created, err := time.Parse(time.RFC3339, desc.Annotations[annotationCreated]); err == nil {
			return created, cmp.Or(desc.Annotations[sourceLabel], idxAnnotations[sourceLabel]), nil
		}
		if b, err = s.fetch(ctx, repo, desc); err != nil {
			return time.Time{}, "", err
		}
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return time.Time{}, "", fmt.Errorf("manifest %s: %w", desc.Digest, err)
	}
	created, _ := time.Parse(time.RFC3339, m.Annotations[annotationCreated])
	source := m.Annotations[sourceLabel]
	if m.Config.MediaType != mediaTypeOCIConfig && m.Config.MediaType != mediaTypeDockerConfig {
		return created, source, nil
	}
	if b, err = s.fetch(ctx, repo, m.Config); err != nil {
		return time.Time{}, "", err
	}
	var img imageConfig
	if err := json.Unmarshal(b, &img); err != nil {
		return time.Time{}, "", fmt.Errorf("config %s: %w", m.Config.Digest, err)
	}
	if img.Created != nil {
		created = *img.Created
	}
	return created, cmp.Or(img.Config.Labels[sourceLabel], source), nil
}

// isIndex reports whether mediaType is a multi-platform index (OCI or Docker manifest list).
func isIndex(mediaType string) bool {
	return mediaType == mediaTypeOCIIndex || mediaType == mediaTypeDockerManifestList
}

// firstImage returns the first platform image manifest of index b, skipping attestation
// manifests (platform unknown/unknown), and the index's annotations.
func firstImage(b []byte) (descriptor, map[string]string, error) {
	var idx index
	if err := json.Unmarshal(b, &idx); err != nil {
		return descriptor{}, nil, fmt.Errorf("index: %w", err)
	}
	for _, m := range idx.Manifests {
		image := m.MediaType == mediaTypeOCIManifest || m.MediaType == mediaTypeDockerManifest
		if image && (m.Platform == nil || m.Platform.OS != "unknown") {
			return m, idx.Annotations, nil
		}
	}
	return descriptor{}, nil, errors.New("index has no platform image")
}

func (s *Source) list(ctx context.Context, name string) (rels []datasource.Release, meta map[string]tagMeta, err error) {
	start := time.Now()
	defer func() {
		slog.DebugContext(ctx, "image tags listed", "repo", name, "tags", len(rels), "listed", len(meta), "duration", time.Since(start), "err", err)
	}()
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return nil, nil, err
	}
	defer release()
	repo := s.repository(name)
	meta, err = s.listTags(ctx, repo, func(t string) {
		rels = append(rels, datasource.Release{Version: t})
	})
	if errors.Is(err, errTooManyPages) {
		slog.WarnContext(ctx, "image tag listing truncated", "repo", name, "pages", repo.maxPages, "tags", len(rels))
		err = nil
	}
	if err != nil {
		return nil, nil, fmt.Errorf("list tags %s: %w", name, err)
	}
	for i := range rels {
		rels[i].Digest = meta[rels[i].Version].digest
	}
	return rels, meta, nil
}

// head resolves the digest with a HEAD request. A registry that omits the digest header gets a
// GET, whose digest is computed from the manifest; HTTP and auth errors are not retried with GET,
// and a 404 becomes "tag <v> not found upstream".
func (s *Source) head(ctx context.Context, name, tag string) (digest string, err error) {
	start, method := time.Now(), http.MethodHead
	defer func() {
		slog.DebugContext(ctx, "image digest resolved", "ref", name+":"+tag, "method", method, "digest", digest, "duration", time.Since(start), "err", err)
	}()
	release, err := httpx.Acquire(ctx, s.sem)
	if err != nil {
		return "", err
	}
	defer release()
	repo := s.repository(name)
	digest, err = s.resolve(ctx, repo, tag)
	if err == nil {
		return digest, nil
	}
	if errors.Is(err, errNotFound) {
		return "", fmt.Errorf("tag %s %w", tag, datasource.ErrVersionNotFound)
	}
	if httpErr := (*errorResponse)(nil); errors.As(err, &httpErr) || errors.Is(err, errBasicCredentialNotFound) ||
		errors.Is(err, httpx.ErrRateLimited) || ctx.Err() != nil {
		return "", s.pullLimitHint(name, fmt.Errorf("digest %s:%s: %w", name, tag, err))
	}
	method = http.MethodGet
	desc, _, err := s.fetchManifest(ctx, repo, tag)
	if err != nil {
		return "", fmt.Errorf("digest %s:%s: %w", name, tag, err)
	}
	return desc.Digest, nil
}

// repoName returns the canonical repository of d: d.Source, or d.Name normalized
// (`alpine` → `docker.io/library/alpine`) when Source is empty; tags and digests are dropped.
func repoName(d dep.Dep) (string, error) {
	ref := d.Source
	if ref == "" {
		ref = d.Name
	}
	r, err := imageref.Parse(ref)
	if err != nil {
		return "", fmt.Errorf("image %q: %w", ref, err)
	}
	return r.Name, nil
}
