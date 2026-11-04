package dockerds

import (
	"encoding/json/v2"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/opencontainers/go-digest"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
)

// memRegistry is an in-memory OCI distribution registry serving tags/list, manifests (by tag
// or digest, HEAD and GET) and blobs. Repositories exist once a tag is set.
type memRegistry struct {
	mu      sync.Mutex
	content map[digest.Digest]ocispec.Descriptor
	data    map[digest.Digest][]byte
	tags    map[string]map[string]digest.Digest // repository → tag → manifest digest
}

func newMemRegistry() *memRegistry {
	return &memRegistry{
		content: map[digest.Digest]ocispec.Descriptor{},
		data:    map[digest.Digest][]byte{},
		tags:    map[string]map[string]digest.Digest{},
	}
}

// put stores v as JSON content of mediaType and returns its descriptor.
func (r *memRegistry) put(t *testing.T, mediaType string, v any) ocispec.Descriptor {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	desc := ocispec.Descriptor{MediaType: mediaType, Digest: digest.FromBytes(b), Size: int64(len(b))}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content[desc.Digest], r.data[desc.Digest] = desc, b
	return desc
}

func (r *memRegistry) tag(repo, tag string, desc ocispec.Descriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tags[repo] == nil {
		r.tags[repo] = map[string]digest.Digest{}
	}
	r.tags[repo][tag] = desc.Digest
}

// image stores a single-platform image whose config has created and labels.
func (r *memRegistry) image(t *testing.T, created time.Time, labels map[string]string) ocispec.Descriptor {
	t.Helper()
	cfg := r.put(t, ocispec.MediaTypeImageConfig, ocispec.Image{
		Created: &created,
		OS:      "linux", Architecture: "amd64",
		Config: ocispec.ImageConfig{Labels: labels},
		RootFS: ocispec.RootFS{Type: "layers"},
	})
	return r.put(t, ocispec.MediaTypeImageManifest, ocispec.Manifest{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageManifest,
		Config:        cfg,
		Layers:        []ocispec.Descriptor{},
	})
}

// index stores an image index of manifests (with their platforms set by the caller).
func (r *memRegistry) index(t *testing.T, manifests ...ocispec.Descriptor) ocispec.Descriptor {
	t.Helper()
	return r.put(t, ocispec.MediaTypeImageIndex, ocispec.Index{
		SchemaVersion: 2,
		MediaType:     ocispec.MediaTypeImageIndex,
		Manifests:     manifests,
	})
}

func withPlatform(desc ocispec.Descriptor, os, arch string) ocispec.Descriptor {
	desc.Platform = &ocispec.Platform{OS: os, Architecture: arch}
	return desc
}

func (r *memRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	if path == "" {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if repo, ok := strings.CutSuffix(path, "/tags/list"); ok {
		tags, ok := r.tags[repo]
		if !ok {
			registryError(w, http.StatusNotFound, "NAME_UNKNOWN")
			return
		}
		list := slices.Sorted(maps.Keys(tags))
		w.Header().Set("Content-Type", "application/json")
		_ = json.MarshalWrite(w, map[string]any{"name": repo, "tags": list})
		return
	}
	var dg digest.Digest
	if repo, ref, ok := strings.Cut(path, "/manifests/"); ok {
		dg = digest.Digest(ref)
		if !strings.Contains(ref, ":") {
			dg = r.tags[repo][ref]
		}
	} else if _, ref, ok := strings.Cut(path, "/blobs/"); ok {
		dg = digest.Digest(ref)
	}
	desc, ok := r.content[dg]
	if !ok {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
		return
	}
	w.Header().Set("Content-Type", desc.MediaType)
	w.Header().Set("Docker-Content-Digest", desc.Digest.String())
	w.Header().Set("Content-Length", strconv.FormatInt(desc.Size, 10))
	w.WriteHeader(http.StatusOK)
	if req.Method == http.MethodGet {
		_, _ = w.Write(r.data[dg])
	}
}

func registryError(w http.ResponseWriter, status int, code string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.MarshalWrite(w, map[string]any{"errors": []map[string]string{{"code": code, "message": strings.ToLower(code)}}})
}
