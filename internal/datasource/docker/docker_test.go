package dockerds

import (
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/google/go-containerregistry/pkg/authn"
	"github.com/google/go-containerregistry/pkg/name"
	"github.com/google/go-containerregistry/pkg/registry"
	"github.com/google/go-containerregistry/pkg/v1/random"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// fixture is an in-memory registry with repository img: tags 1.0 and latest (images) and 2.0
// (a multi-platform index). Requests are counted per "METHOD path".
type fixture struct {
	srv           *httptest.Server
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
	f := &fixture{hits: map[string]int{}}
	reg := registry.New(registry.Logger(log.New(io.Discard, "", 0)))
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
		reg.ServeHTTP(w, r)
	}))
	t.Cleanup(f.srv.Close)
	f.host = strings.TrimPrefix(f.srv.URL, "http://")

	repo, err := name.NewRepository(f.host + "/img")
	if err != nil {
		t.Fatal(err)
	}
	img, err := random.Image(64, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, tag := range []string{"1.0", "latest"} {
		if err := remote.Write(repo.Tag(tag), img); err != nil {
			t.Fatal(err)
		}
	}
	idx, err := random.Index(64, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := remote.WriteIndex(repo.Tag("2.0"), idx); err != nil {
		t.Fatal(err)
	}
	d, _ := img.Digest()
	f.imageDigest = d.String()
	d, _ = idx.Digest()
	f.indexDigest = d.String()
	f.hits = map[string]int{}
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
var anon = authn.NewMultiKeychain()

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
	if n := f.count("GET /v2/"); n != 1 {
		t.Errorf("ping requests = %d, want 1 (shared puller)", n)
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

func TestKeychain(t *testing.T) {
	f := newFixture(t)
	f.set(func() { f.user, f.pass = "u", "p" })
	s := newSource(f.srv.Client(), nil, staticKeychain{f.host, authn.FromConfig(authn.AuthConfig{Username: "u", Password: "p"})})
	rels, err := s.Releases(t.Context(), dep.Dep{Name: f.host + "/img"}, "")
	if err != nil || len(rels) != 3 {
		t.Fatalf("Releases = %v, %v", rels, err)
	}
	if got, err := s.Digest(t.Context(), dep.Dep{Name: f.host + "/img"}, "1.0"); err != nil || got != f.imageDigest {
		t.Errorf("Digest = %q, %v", got, err)
	}
}

type staticKeychain struct {
	host string
	auth authn.Authenticator
}

func (k staticKeychain) Resolve(r authn.Resource) (authn.Authenticator, error) {
	if r.RegistryStr() == k.host {
		return k.auth, nil
	}
	return authn.Anonymous, nil
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
}

func TestPullerFor(t *testing.T) {
	s := newSource(nil, nil, anon)
	for ref, hub := range map[string]bool{"alpine": true, "docker.io/library/alpine": true, "ghcr.io/o/i": false, "127.0.0.1:5000/x": false} {
		repo, err := repository(dep.Dep{Name: ref})
		if err != nil {
			t.Fatal(err)
		}
		if got := s.pullerFor(repo) == s.hub; got != hub {
			t.Errorf("%s: hub puller = %v, want %v", ref, got, hub)
		}
	}
}
