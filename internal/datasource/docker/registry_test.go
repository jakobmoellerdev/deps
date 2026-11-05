package dockerds

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"maps"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// memRegistry is an in-memory OCI distribution registry serving tags/list, manifests (by tag
// or digest, HEAD and GET) and blobs. Repositories exist once a tag is set.
type memRegistry struct {
	mu      sync.Mutex
	content map[string]descriptor
	data    map[string][]byte
	tags    map[string]map[string]string // repository → tag → manifest digest
}

func newMemRegistry() *memRegistry {
	return &memRegistry{
		content: map[string]descriptor{},
		data:    map[string][]byte{},
		tags:    map[string]map[string]string{},
	}
}

// sha256Digest returns the sha256 digest of b.
func sha256Digest(b []byte) string {
	sum := sha256.Sum256(b)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// put stores v as JSON content of mediaType and returns its descriptor.
func (r *memRegistry) put(t *testing.T, mediaType string, v any) descriptor {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	desc := descriptor{MediaType: mediaType, Digest: sha256Digest(b), Size: int64(len(b))}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.content[desc.Digest], r.data[desc.Digest] = desc, b
	return desc
}

func (r *memRegistry) tag(repo, tag string, desc descriptor) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.tags[repo] == nil {
		r.tags[repo] = map[string]string{}
	}
	r.tags[repo][tag] = desc.Digest
}

// image stores a single-platform image whose config has created and labels.
func (r *memRegistry) image(t *testing.T, created time.Time, labels map[string]string) descriptor {
	t.Helper()
	cfg := imageConfig{Created: &created}
	cfg.Config.Labels = labels
	return r.put(t, mediaTypeOCIManifest, manifest{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIManifest,
		Config:        r.put(t, mediaTypeOCIConfig, cfg),
		Layers:        []descriptor{},
	})
}

// index stores an image index of manifests (with their platforms set by the caller).
func (r *memRegistry) index(t *testing.T, manifests ...descriptor) descriptor {
	t.Helper()
	return r.put(t, mediaTypeOCIIndex, index{
		SchemaVersion: 2,
		MediaType:     mediaTypeOCIIndex,
		Manifests:     manifests,
	})
}

func withPlatform(desc descriptor, os, arch string) descriptor {
	desc.Platform = &platform{OS: os, Architecture: arch}
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
	var dg string
	if repo, ref, ok := strings.Cut(path, "/manifests/"); ok {
		dg = ref
		if !strings.Contains(ref, ":") {
			dg = r.tags[repo][ref]
		}
	} else if _, ref, ok := strings.Cut(path, "/blobs/"); ok {
		dg = ref
	}
	desc, ok := r.content[dg]
	if !ok {
		registryError(w, http.StatusNotFound, "MANIFEST_UNKNOWN")
		return
	}
	w.Header().Set("Content-Type", desc.MediaType)
	w.Header().Set("Docker-Content-Digest", desc.Digest)
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
