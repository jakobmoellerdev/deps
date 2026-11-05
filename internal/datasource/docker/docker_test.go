package dockerds

import (
	"cmp"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// fixture is an in-memory registry with repository img: tags 1.0 and latest (images) and 2.0
// (a multi-platform index). Requests are counted per "METHOD path".
type fixture struct {
	srv           *httptest.Server
	reg           *memRegistry
	host          string
	imageDigest   string // of 1.0
	indexDigest   string // of 2.0
	mu            sync.Mutex
	hits          map[string]int
	stripDigest   bool // drop Docker-Content-Digest from HEAD responses
	failManifestG bool // GET manifests fails with 404
	onHead        func(r *http.Request)
	user, pass    string // required basic auth, "" = anonymous
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{hits: map[string]int{}, reg: newMemRegistry()}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.hits[r.Method+" "+r.URL.Path]++
		strip, failGet, onHead, user, pass := f.stripDigest, f.failManifestG, f.onHead, f.user, f.pass
		f.mu.Unlock()
		if user != "" {
			if u, p, ok := r.BasicAuth(); !ok || u != user || p != pass {
				w.Header().Set("WWW-Authenticate", `Basic realm="test"`)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
		}
		manifest := strings.Contains(r.URL.Path, "/manifests/")
		switch {
		case manifest && r.Method == http.MethodHead && onHead != nil:
			onHead(r)
		case manifest && r.Method == http.MethodGet && failGet:
			http.NotFound(w, r)
			return
		case manifest && r.Method == http.MethodHead && strip:
			w = stripWriter{w}
		}
		f.reg.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.host = strings.TrimPrefix(f.srv.URL, "http://")

	created := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	img := f.reg.image(t, created, nil)
	f.reg.tag("img", "1.0", img)
	f.reg.tag("img", "latest", img)
	idx := f.reg.index(t,
		withPlatform(f.reg.image(t, created.Add(time.Hour), nil), "linux", "amd64"),
		withPlatform(f.reg.image(t, created.Add(2*time.Hour), nil), "linux", "arm64"))
	f.reg.tag("img", "2.0", idx)
	f.imageDigest = img.Digest
	f.indexDigest = idx.Digest
	return f
}

func (f *fixture) count(key string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hits[key]
}

func (f *fixture) set(fn func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn()
}

type stripWriter struct{ http.ResponseWriter }

func (s stripWriter) WriteHeader(code int) {
	s.Header().Del("Docker-Content-Digest")
	s.ResponseWriter.WriteHeader(code)
}

// anon resolves every registry to anonymous access.
func anon(context.Context, string, string) (credential, error) {
	return credential{}, nil
}

// static returns cred for registry and anonymous access for others.
func static(registry string, cred credential) credentialFunc {
	return func(_ context.Context, r, _ string) (credential, error) {
		if r == registry {
			return cred, nil
		}
		return credential{}, nil
	}
}

func TestReleasesAndDigest(t *testing.T) {
	f := newFixture(t)
	s := newSource(f.srv.Client(), make(chan struct{}, 2), anon)
	d := dep.Dep{Name: f.host + "/img", Source: f.host + "/img"}
	other := dep.Dep{Name: f.host + "/img:1.0"} // Source empty: the name is normalized, tag dropped

	var wg sync.WaitGroup
	for _, dd := range []dep.Dep{d, d, other, d} {
		wg.Go(func() {
			rels, err := s.Releases(t.Context(), dd, "/ignored")
			if err != nil {
				t.Error(err)
				return
			}
			var got []string
			for _, r := range rels {
				if r.Digest != "" {
					t.Errorf("release %s has digest", r.Version)
				}
				got = append(got, r.Version)
			}
			if strings.Join(got, ",") != "1.0,2.0,latest" {
				t.Errorf("versions = %v", got)
			}
		})
	}
	wg.Wait()
	if n := f.count("GET /v2/img/tags/list"); n != 1 {
		t.Errorf("tags/list requests = %d, want 1 (deduped)", n)
	}

	for _, c := range []struct{ version, want string }{{"1.0", f.imageDigest}, {"2.0", f.indexDigest}, {"2.0", f.indexDigest}} {
		got, err := s.Digest(t.Context(), other, c.version)
		if err != nil || got != c.want {
			t.Errorf("Digest(%s) = %q, %v; want %q", c.version, got, err, c.want)
		}
	}
	if n := f.count("HEAD /v2/img/manifests/2.0"); n != 1 {
		t.Errorf("HEAD 2.0 requests = %d, want 1 (deduped)", n)
	}
	if _, err := s.Digest(t.Context(), d, "9.9"); err == nil || err.Error() != "tag 9.9 not found upstream" {
		t.Errorf("Digest(9.9) err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	f := newFixture(t)
	s := newSource(f.srv.Client(), nil, anon)
	if _, err := s.Releases(t.Context(), dep.Dep{Name: f.host + "/missing"}, ""); err == nil || !strings.HasPrefix(err.Error(), "list tags "+f.host+"/missing: ") {
		t.Errorf("missing repo err = %v", err)
	}
	for _, d := range []dep.Dep{{Name: "UPPER"}, {Name: "a:b:c"}} {
		if _, err := s.Releases(t.Context(), d, ""); err == nil || !strings.HasPrefix(err.Error(), `image "`+d.Name+`": `) {
			t.Errorf("Releases(%q) err = %v", d.Name, err)
		}
		if _, err := s.Digest(t.Context(), d, "1"); err == nil {
			t.Errorf("Digest(%q): want error", d.Name)
		}
	}
	// non-404 HTTP errors are reported, not retried with GET
	f.set(func() { f.user, f.pass = "u", "p" })
	if _, err := s.Digest(t.Context(), dep.Dep{Name: f.host + "/img"}, "1.0"); err == nil || !strings.HasPrefix(err.Error(), "digest "+f.host+"/img:1.0: ") {
		t.Errorf("unauthorized err = %v", err)
	}
	if n := f.count("GET /v2/img/manifests/1.0"); n != 0 {
		t.Errorf("GET fallback after HTTP error: %d requests", n)
	}
}

// TestNoRegistryRetry checks the registry client sends each request once: the shared httpx
// transport owns retries.
func TestNoRegistryRetry(t *testing.T) {
	var lists atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			return
		}
		lists.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	_, err := newSource(srv.Client(), nil, anon).Releases(t.Context(), dep.Dep{Name: host + "/img"}, "")
	if err == nil || !strings.HasPrefix(err.Error(), "list tags "+host+"/img: ") {
		t.Errorf("err = %v", err)
	}
	if n := lists.Load(); n != 1 {
		t.Errorf("tags/list requests = %d, want 1", n)
	}
}

// TestPullLimitExhausted checks that an exhausted Docker Hub quota (429 with
// `ratelimit-remaining: 0;w=3600`, no Retry-After) fails each lookup after one request, with no
// GET fallback for the digest, and that the error says how to raise the limit.
func TestPullLimitExhausted(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/" {
			return
		}
		mu.Lock()
		hits[r.Method]++
		mu.Unlock()
		w.Header().Set("Ratelimit-Limit", "100;w=3600")
		w.Header().Set("Ratelimit-Remaining", "0;w=3600")
		registryError(w, http.StatusTooManyRequests, "TOOMANYREQUESTS")
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	s := newSource(httpx.NewClient(), nil, anon)
	s.pullLimited = host
	d := dep.Dep{Name: host + "/img"}
	_, digestErr := s.Digest(t.Context(), d, "1.0")
	_, configErr := s.ReleaseTime(t.Context(), d, "2.0")
	for _, err := range []error{digestErr, configErr} {
		if !errors.Is(err, httpx.ErrRateLimited) || !strings.Contains(err.Error(), "`docker login`") {
			t.Errorf("err = %v; want ErrRateLimited with a docker login hint", err)
		}
	}
	if hits[http.MethodHead] != 2 || hits[http.MethodGet] != 0 {
		t.Errorf("requests = %v; want 2 HEAD, no GET", hits)
	}

	// Other registries get no Docker Hub hint.
	s.pullLimited = hubRegistry
	if _, err := s.Digest(t.Context(), d, "3.0"); !errors.Is(err, httpx.ErrRateLimited) || strings.Contains(err.Error(), "docker login") {
		t.Errorf("other registry err = %v", err)
	}
}

func TestLimitsPulls(t *testing.T) {
	s := newSource(nil, nil, anon)
	for ref, want := range map[string]bool{"alpine": true, "docker.io/o/i": true, "ghcr.io/o/i": false, "mirror.gcr.io/library/redis": false} {
		name, err := repoName(dep.Dep{Name: ref})
		if err != nil {
			t.Fatal(err)
		}
		if got := s.limitsPulls(name); got != want {
			t.Errorf("limitsPulls(%s) = %v, want %v", name, got, want)
		}
	}
}

// TestLargeTagList checks a single tags/list page above 4 MiB is read whole,
// as gcr.io returns one ~10 MB page for large repositories.
func TestLargeTagList(t *testing.T) {
	const n = 40000 // ~130 bytes per tag: a ~5 MB page from few allocations
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		tags := make([]string, n)
		pad := strings.Repeat("x", 120)
		for i := range tags {
			tags[i] = "1.0." + strconv.Itoa(i) + "-" + pad
		}
		_ = json.MarshalWrite(w, map[string]any{"name": "img", "tags": tags})
	}))
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	rels, err := newSource(srv.Client(), nil, anon).Releases(t.Context(), dep.Dep{Name: host + "/img"}, "")
	if err != nil || len(rels) != n {
		t.Fatalf("Releases = %d tags, %v; want %d", len(rels), err, n)
	}
}

func TestCredentials(t *testing.T) {
	f := newFixture(t)
	f.set(func() { f.user, f.pass = "u", "p" })
	cred := static(f.host, credential{Username: "u", Password: "p"})
	s := newSource(f.srv.Client(), nil, cred)
	rels, err := s.Releases(t.Context(), dep.Dep{Name: f.host + "/img"}, "")
	if err != nil || len(rels) != 3 {
		t.Fatalf("Releases = %v, %v", rels, err)
	}
	if got, err := s.Digest(t.Context(), dep.Dep{Name: f.host + "/img"}, "1.0"); err != nil || got != f.imageDigest {
		t.Errorf("Digest = %q, %v", got, err)
	}
}

func TestDigestGetFallback(t *testing.T) {
	f := newFixture(t)
	f.set(func() { f.stripDigest = true })
	s := newSource(f.srv.Client(), nil, anon)
	d := dep.Dep{Name: f.host + "/img"}
	if got, err := s.Digest(t.Context(), d, "2.0"); err != nil || got != f.indexDigest {
		t.Errorf("Digest = %q, %v; want %q", got, err, f.indexDigest)
	}
	if f.count("GET /v2/img/manifests/2.0") != 1 {
		t.Error("no GET fallback")
	}
	f.set(func() { f.failManifestG = true })
	if _, err := s.Digest(t.Context(), d, "1.0"); err == nil || !strings.HasPrefix(err.Error(), "digest "+f.host+"/img:1.0: ") {
		t.Errorf("failed GET err = %v", err)
	}
}

func TestCancel(t *testing.T) {
	f := newFixture(t)
	d := dep.Dep{Name: f.host + "/img"}

	// all semaphore slots taken: the lookup waits until ctx is done
	sem := make(chan struct{}, 1)
	sem <- struct{}{}
	s := newSource(f.srv.Client(), sem, anon)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Releases(ctx, d, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("Releases err = %v", err)
	}
	if _, err := s.Digest(ctx, d, "1.0"); !errors.Is(err, context.Canceled) {
		t.Errorf("Digest err = %v", err)
	}
	if _, err := newSource(f.srv.Client(), nil, anon).Digest(ctx, d, "1.0"); !errors.Is(err, context.Canceled) {
		t.Errorf("nil sem Digest err = %v", err)
	}

	// cancellation during HEAD is reported without a GET fallback
	ctx, cancel = context.WithCancel(t.Context())
	var heads atomic.Int32
	f.set(func() {
		f.onHead = func(r *http.Request) {
			heads.Add(1)
			cancel()
			<-r.Context().Done()
		}
	})
	if _, err := newSource(f.srv.Client(), nil, anon).Digest(ctx, d, "1.0"); err == nil || !errors.Is(err, context.Canceled) {
		t.Errorf("Digest during cancel err = %v", err)
	}
	if heads.Load() != 1 || f.count("GET /v2/img/manifests/1.0") != 0 {
		t.Errorf("heads = %d, GETs = %d", heads.Load(), f.count("GET /v2/img/manifests/1.0"))
	}
}

func TestNew(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir()) // no credentials: anonymous
	f := newFixture(t)
	for _, c := range []*http.Client{f.srv.Client(), nil} {
		ds := New(c, nil)
		if rels, err := ds.Releases(t.Context(), dep.Dep{Name: f.host + "/img"}, ""); err != nil || len(rels) != 3 {
			t.Errorf("Releases = %v, %v", rels, err)
		}
	}

	// credentials from the Docker config
	dir := t.TempDir()
	t.Setenv("DOCKER_CONFIG", dir)
	cfg := `{"auths":{"` + f.host + `":{"auth":"dTpw"}}}` // base64("u:p")
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	f.set(func() { f.user, f.pass = "u", "p" })
	if rels, err := New(f.srv.Client(), nil).Releases(t.Context(), dep.Dep{Name: f.host + "/img"}, ""); err != nil || len(rels) != 3 {
		t.Errorf("with docker config: Releases = %v, %v", rels, err)
	}

	// an unreadable config fails authenticated lookups with its error
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(f.srv.Client(), nil).Releases(t.Context(), dep.Dep{Name: f.host + "/img"}, ""); err == nil || !strings.Contains(err.Error(), "docker config: ") {
		t.Errorf("broken docker config: err = %v", err)
	}
}

func TestRepository(t *testing.T) {
	s := newSource(nil, nil, anon)
	for ref, want := range map[string]struct {
		pageSize, maxPages int
		plain              bool
	}{
		"alpine":                   {hubPageSize, maxTagPages, false},
		"docker.io/library/alpine": {hubPageSize, maxTagPages, false},
		"ghcr.io/o/i":              {pageSize, maxTagPagesOldestFirst, false},
		"quay.io/o/i":              {pageSize, maxTagPagesOldestFirst, false},
		"gcr.io/o/i":               {pageSize, maxTagPages, false},
		"127.0.0.1:5000/x":         {pageSize, maxTagPages, true},
		"localhost:5000/x":         {pageSize, maxTagPages, true},
		"10.1.2.3/x":               {pageSize, maxTagPages, true},
		"[::1]:5000/x":             {pageSize, maxTagPages, true},
	} {
		name, err := repoName(dep.Dep{Name: ref})
		if err != nil {
			t.Fatal(err)
		}
		repo := s.repository(name)
		if repo.pageSize != want.pageSize || repo.maxPages != want.maxPages || repo.plainHTTP != want.plain {
			t.Errorf("%s: page size %d, max pages %d, plain HTTP %v; want %d, %d, %v", ref, repo.pageSize, repo.maxPages, repo.plainHTTP, want.pageSize, want.maxPages, want.plain)
		}
	}
}

// TestTagPageCap checks a listing stops after maxTagPages pages and resolves with the tags seen,
// while one of exactly maxTagPages pages is read whole.
func TestTagPageCap(t *testing.T) {
	for _, tc := range []struct {
		name  string
		pages int // 0 = endless
	}{
		{"endless", 0},
		{"exactly the cap", maxTagPages},
		{"below the cap", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var lists atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v2/" {
					return
				}
				page := int(lists.Add(1))
				if n := r.URL.Query().Get("n"); n != strconv.Itoa(pageSize) {
					t.Errorf("page %d: n = %q, want %d", page, n, pageSize)
				}
				if tc.pages == 0 || page < tc.pages {
					w.Header().Set("Link", fmt.Sprintf(`</v2/img/tags/list?last=1.0.%d&n=%d>; rel="next"`, page, pageSize))
				}
				_ = json.MarshalWrite(w, map[string]any{"name": "img", "tags": []string{fmt.Sprintf("1.0.%d", page)}})
			}))
			t.Cleanup(srv.Close)
			host := strings.TrimPrefix(srv.URL, "http://")
			want := cmp.Or(tc.pages, maxTagPages)
			rels, err := newSource(srv.Client(), nil, anon).Releases(t.Context(), dep.Dep{Name: host + "/img"}, "")
			if err != nil || len(rels) != want || rels[want-1].Version != fmt.Sprintf("1.0.%d", want) {
				t.Errorf("Releases = %v, %v; want %d tags", rels, err, want)
			}
			if n := int(lists.Load()); n != want {
				t.Errorf("tags/list requests = %d, want %d", n, want)
			}
		})
	}
}
