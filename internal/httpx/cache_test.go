package httpx

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// memCache is a Cache in memory, as Valkey is across replicas.
type memCache struct {
	mu      sync.Mutex
	entries map[string][]byte
	ttls    map[string]time.Duration
}

func (c *memCache) Get(_ context.Context, key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	b, ok := c.entries[key]
	return b, ok
}

func (c *memCache) Put(_ context.Context, key string, entry []byte, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[string][]byte{}
		c.ttls = map[string]time.Duration{}
	}
	c.entries[key] = entry
	c.ttls[key] = ttl
}

// cachingClient wires a caching transport in front of srv, without the retry chain, so the
// test sees exactly the cache's behavior.
func cachingClient(c Cache, scope string, next http.RoundTripper, observe func(string, CacheOutcome)) *http.Client {
	return &http.Client{Transport: &cacheTransport{next: next, cache: c, scope: scope, observe: observe}}
}

func get(t *testing.T, c *http.Client, url string) (int, string) {
	t.Helper()
	resp, err := c.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(b)
}

func TestCacheServesTheSecondRequest(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"versions":["1.0.0"]}`)
	}))
	defer srv.Close()

	cache := &memCache{}
	var outcomes []CacheOutcome
	c := cachingClient(cache, "scope-a", srv.Client().Transport, func(_ string, o CacheOutcome) {
		outcomes = append(outcomes, o)
	})
	url := srv.URL + "/mod/example.com/m/@v/list"
	for range 3 {
		if code, body := get(t, c, url); code != 200 || body != `{"versions":["1.0.0"]}` {
			t.Fatalf("response = %d %q", code, body)
		}
	}
	if hits != 1 {
		t.Errorf("upstream requests = %d, want 1", hits)
	}
	if len(outcomes) != 3 || outcomes[0] != CacheMiss || outcomes[1] != CacheHit || outcomes[2] != CacheHit {
		t.Errorf("outcomes = %v", outcomes)
	}

	// Another tenant must not read the first one's entry: the scope is part of the key.
	other := cachingClient(cache, "scope-b", srv.Client().Transport, nil)
	if code, _ := get(t, other, url); code != 200 {
		t.Fatalf("other tenant = %d", code)
	}
	if hits != 2 {
		t.Errorf("upstream requests after the second tenant = %d, want 2", hits)
	}
}

// Negative answers are cached on purpose: the Go major-path probes are mostly 404s.
func TestCacheStoresNotFound(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()
	c := cachingClient(&memCache{}, "s", srv.Client().Transport, nil)
	for range 2 {
		if code, _ := get(t, c, srv.URL+"/mod/example.com/v2/@v/list"); code != 404 {
			t.Fatalf("status = %d", code)
		}
	}
	if hits != 1 {
		t.Errorf("upstream requests = %d, want the 404 cached", hits)
	}
}

func TestCacheRevalidatesWithETag(t *testing.T) {
	var mu sync.Mutex
	full, conditional := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		if r.Header.Get("If-None-Match") == `"v1"` {
			conditional++
			w.WriteHeader(http.StatusNotModified)
			return
		}
		full++
		w.Header().Set("ETag", `"v1"`)
		_, _ = io.WriteString(w, "body")
	}))
	defer srv.Close()

	cache := &memCache{}
	var outcomes []CacheOutcome
	c := cachingClient(cache, "s", srv.Client().Transport, func(_ string, o CacheOutcome) {
		outcomes = append(outcomes, o)
	})
	url := srv.URL + "/mod/example.com/m/@v/list"
	if _, body := get(t, c, url); body != "body" {
		t.Fatalf("first body = %q", body)
	}
	// Age the entry past its listing TTL.
	for k, raw := range cache.entries {
		e, err := decodeEntry(raw)
		if err != nil {
			t.Fatal(err)
		}
		e.stored = time.Now().Add(-time.Hour)
		cache.entries[k] = e.encode()
	}
	if _, body := get(t, c, url); body != "body" {
		t.Fatalf("revalidated body = %q", body)
	}
	if full != 1 || conditional != 1 {
		t.Errorf("full=%d conditional=%d; want one of each", full, conditional)
	}
	if len(outcomes) != 2 || outcomes[1] != CacheRevalidated {
		t.Errorf("outcomes = %v", outcomes)
	}
}

func TestCacheSkipsForgeAPIsAndWrites(t *testing.T) {
	var mu sync.Mutex
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = io.WriteString(w, "[]")
	}))
	defer srv.Close()
	var outcomes []CacheOutcome
	c := cachingClient(&memCache{}, "s", srv.Client().Transport, func(_ string, o CacheOutcome) {
		outcomes = append(outcomes, o)
	})
	for range 2 {
		if code, _ := get(t, c, srv.URL+"/api/v1/repos/o/n/pulls?state=open"); code != 200 {
			t.Fatal("forge request failed")
		}
	}
	if hits != 2 {
		t.Errorf("forge API requests served from the cache: %d upstream, want 2", hits)
	}
	for _, o := range outcomes {
		if o != CacheUncacheable {
			t.Errorf("outcome = %v, want uncacheable", o)
		}
	}
	resp, err := c.Post(srv.URL+"/mod/x/@v/list", "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if hits != 3 {
		t.Errorf("a POST was cached")
	}
}

// A document larger than the limit must still reach the caller intact, uncached: the Helm
// index.yaml of a big repository is tens of megabytes and is read as a stream.
func TestCacheStreamsLargeBodies(t *testing.T) {
	body := strings.Repeat("y", maxCacheBody+4096)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, body)
	}))
	defer srv.Close()
	cache := &memCache{}
	c := cachingClient(cache, "s", srv.Client().Transport, nil)
	_, got := get(t, c, srv.URL+"/charts/index.yaml")
	if got != body {
		t.Fatalf("body length = %d, want %d", len(got), len(body))
	}
	if len(cache.entries) != 0 {
		t.Errorf("a %d byte body was cached", len(body))
	}
}

func TestCacheTTLClasses(t *testing.T) {
	for _, tc := range []struct {
		url  string
		want time.Duration
	}{
		{"https://proxy.golang.org/example.com/m/@v/v1.2.3.info", ttlImmutable},
		{"https://proxy.golang.org/example.com/m/@v/list", ttlListing},
		{"https://proxy.golang.org/example.com/m/@latest", ttlListing},
		{"https://registry-1.docker.io/v2/library/alpine/manifests/sha256:abc", ttlImmutable},
		{"https://registry-1.docker.io/v2/library/alpine/manifests/3.20", ttlListing},
		{"https://registry-1.docker.io/v2/library/alpine/tags/list", ttlListing},
		{"https://repo1.maven.org/maven2/g/a/maven-metadata.xml", ttlListing},
		{"https://repo1.maven.org/maven2/g/a/1.0/a-1.0.jar", ttlImmutable},
		{"https://api.osv.dev/v1/vulns/GHSA-xxxx", ttlAdvisory},
		{"https://api.osv.dev/v1/querybatch", 0},
		{"https://api.github.com/repos/o/n/pulls", 0},
		{"https://gitlab.com/api/v4/projects/1/merge_requests", 0},
	} {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tc.url, http.NoBody)
		if err != nil {
			t.Fatal(err)
		}
		if got := cacheTTL(req); got != tc.want {
			t.Errorf("cacheTTL(%s) = %v, want %v", tc.url, got, tc.want)
		}
	}
}

func TestEntryRoundTrip(t *testing.T) {
	e := entry{
		status: 200, stored: time.Now().Truncate(time.Millisecond),
		etag: `W/"abc"`, lastModified: "Mon, 02 Jan 2006 15:04:05 GMT",
		headers: map[string]string{"Content-Type": "application/json", "X-Checksum-Sha256": "deadbeef"},
		body:    []byte(`{"a":1}`),
	}
	got, err := decodeEntry(e.encode())
	if err != nil {
		t.Fatal(err)
	}
	if got.status != e.status || got.etag != e.etag || got.lastModified != e.lastModified ||
		!bytes.Equal(got.body, e.body) || !got.stored.Equal(e.stored) ||
		len(got.headers) != len(e.headers) || got.headers["X-Checksum-Sha256"] != "deadbeef" {
		t.Errorf("round trip = %+v, want %+v", got, e)
	}
	if _, err := decodeEntry([]byte{9, 9, 9}); err == nil {
		t.Error("decodeEntry accepted garbage")
	}
}
