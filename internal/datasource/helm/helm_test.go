package helmds

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	dockerds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/docker"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// testIndex is laid out as `helm repo index` writes it.
const testIndex = `apiVersion: v1
entries:
  cert-manager:
  - annotations:
      artifacthub.io/changes: |
        - kind: fixed
          description: a block scalar
    apiVersion: v2
    appVersion: v1.15.0
    created: "2024-06-01T10:00:00.123456789Z"
    description: x
    digest: aaa
    name: cert-manager
    urls:
    - charts/cert-manager-v1.15.0.tgz
    version: v1.15.0

  - appVersion: v1.14.0
    created: "2024-01-01T10:00:00Z"
    name: cert-manager
    urls:
    - charts/cert-manager-v1.14.0.tgz
    version: v1.14.0
  - name: cert-manager
    version: v1.16.0-alpha.0
    urls: []
  "quoted":
  - name: quoted
    version: 2.0.0
    urls: [x]
  # a comment
  unwanted:
  - name: unwanted
    version: 9.9.9
    urls: [x]
generated: "2024-06-01T10:00:00Z"
serverInfo: {}
`

type indexServer struct {
	*httptest.Server
	mu   sync.Mutex
	hits int
	auth string // Authorization header of the last request
	body string
	code int
}

func newIndexServer(t *testing.T, body string) *indexServer {
	t.Helper()
	s := &indexServer{body: body, code: http.StatusOK}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		if r.URL.Path != "/charts/index.yaml" {
			http.NotFound(w, r)
			return
		}
		s.hits++
		s.auth = r.Header.Get("Authorization")
		w.WriteHeader(s.code)
		_, _ = w.Write([]byte(s.body))
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *indexServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.hits
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestClassicRepository(t *testing.T) {
	srv := newIndexServer(t, testIndex)
	repo := srv.URL + "/charts"
	auth := httpx.NewAuth(platform.Info{Host: "127.0.0.1"}, "tok", func(string) string { return "" })
	s := New(srv.Client(), make(chan struct{}, 2), auth, nil)
	cm := dep.Dep{Datasource: Name, Name: "cert-manager", Source: repo}
	quoted := dep.Dep{Datasource: Name, Name: "quoted", Source: repo}
	s.Plan([]dep.Dep{cm, quoted, {Name: "oci-only", Source: "oci://x/y"}, {Name: "no-source"}})

	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			rels, err := s.Releases(t.Context(), cm, "")
			if err != nil || !slices.Equal(versions(rels), []string{"v1.15.0", "v1.14.0"}) {
				t.Errorf("cert-manager = %v, %v", versions(rels), err)
			}
		})
	}
	wg.Wait()
	rels, err := s.Chart(t.Context(), repo, "quoted")
	if err != nil || len(rels) != 1 || rels[0].Version != "2.0.0" {
		t.Fatalf("quoted = %+v, %v", rels, err)
	}
	if n := srv.count(); n != 1 {
		t.Errorf("index fetched %d times, want 1", n)
	}
	if want := "Basic " + basic("x-access-token", "tok"); srv.auth != want {
		t.Errorf("Authorization = %q, want %q", srv.auth, want)
	}

	rels, _ = s.Chart(t.Context(), repo, "cert-manager")
	if r := rels[0]; r.AppVersion != "v1.15.0" || r.Digest != "aaa" || !r.Created.Equal(time.Date(2024, 6, 1, 10, 0, 0, 123456789, time.UTC)) {
		t.Errorf("release = %+v", r)
	}
	for v, want := range map[string]time.Time{
		"v1.14.0": time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC),
		"1.14.0":  time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC), // the engine writes candidates in cur's "v" style
		"v9.0.0":  {},
	} {
		if got, err := s.ReleaseTime(t.Context(), cm, v); err != nil || !got.Equal(want) {
			t.Errorf("ReleaseTime(%s) = %v, %v; want %v", v, got, err, want)
		}
	}

	// A chart nobody planned for costs one more scan, once.
	for range 2 {
		rels, err := s.Chart(t.Context(), repo, "unwanted")
		if err != nil || len(rels) != 1 {
			t.Fatalf("unwanted = %+v, %v", rels, err)
		}
	}
	if n := srv.count(); n != 2 {
		t.Errorf("index fetched %d times, want 2", n)
	}
	if _, err := s.Chart(t.Context(), repo, "missing"); err == nil || err.Error() != "chart missing not found in "+repo {
		t.Errorf("missing chart: %v", err)
	}
	if _, err := s.Digest(t.Context(), cm, "v1.15.0"); err == nil {
		t.Error("Digest: no error")
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "x"}, ""); err == nil {
		t.Error("no repository: no error")
	}
}

func basic(user, pass string) string {
	req, _ := http.NewRequest(http.MethodGet, "http://x", http.NoBody)
	req.SetBasicAuth(user, pass)
	return strings.TrimPrefix(req.Header.Get("Authorization"), "Basic ")
}

func TestClassicErrors(t *testing.T) {
	srv := newIndexServer(t, "entries: [\n")
	s := New(srv.Client(), nil, nil, nil)
	if _, err := s.Chart(t.Context(), srv.URL+"/charts", "x"); err == nil || !strings.Contains(err.Error(), "index.yaml") {
		t.Errorf("malformed index: %v", err)
	}
	srv2 := newIndexServer(t, "")
	srv2.code = http.StatusInternalServerError
	s = New(srv2.Client(), nil, nil, nil)
	if _, err := s.Chart(t.Context(), srv2.URL+"/charts", "x"); err == nil || !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("HTTP 500: %v", err)
	}
	if _, err := s.Chart(t.Context(), srv2.URL+"/other", "x"); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("HTTP 404: %v", err)
	}
	if _, err := s.Chart(t.Context(), "http://127.0.0.1:1/charts", "x"); err == nil {
		t.Error("unreachable: no error")
	}
	if _, err := New(nil, nil, nil, nil).Chart(t.Context(), "://bad", "x"); err == nil {
		t.Error("bad URL: no error")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(srv.Client(), make(chan struct{}, 1), nil, nil).Chart(ctx, srv.URL+"/charts", "x"); err == nil {
		t.Error("canceled: no error")
	}
}

// ociRegistry serves tags/list and manifests for chart repositories on 127.0.0.1.
type ociRegistry struct {
	tags      map[string][]string
	manifests map[string][]byte // repository:tag
}

func (r *ociRegistry) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	path := strings.TrimPrefix(req.URL.Path, "/v2/")
	if repo, ok := strings.CutSuffix(path, "/tags/list"); ok {
		tags, ok := r.tags[repo]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.MarshalWrite(w, map[string]any{"name": repo, "tags": tags})
		return
	}
	if repo, tag, ok := strings.Cut(path, "/manifests/"); ok {
		b, ok := r.manifests[repo+":"+tag]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		sum := sha256.Sum256(b)
		w.Header().Set("Content-Type", "application/vnd.oci.image.manifest.v1+json")
		w.Header().Set("Docker-Content-Digest", "sha256:"+hex.EncodeToString(sum[:]))
		w.Header().Set("Content-Length", fmt.Sprint(len(b)))
		_, _ = w.Write(b)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func TestOCIRepository(t *testing.T) {
	t.Setenv("DOCKER_CONFIG", t.TempDir())
	created := time.Date(2025, 3, 1, 12, 0, 0, 0, time.UTC)
	manifest, err := json.Marshal(map[string]any{
		"schemaVersion": 2, "mediaType": "application/vnd.oci.image.manifest.v1+json",
		"config":      map[string]any{"mediaType": "application/vnd.cncf.helm.config.v1+json", "digest": "sha256:" + strings.Repeat("0", 64), "size": 2},
		"layers":      []any{},
		"annotations": map[string]string{"org.opencontainers.image.created": created.Format(time.RFC3339)},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := &ociRegistry{
		tags:      map[string][]string{"charts/redis": {"17.0.0", "18.0.0_build.1", "latest"}},
		manifests: map[string][]byte{"charts/redis:18.0.0_build.1": manifest},
	}
	srv := httptest.NewServer(reg)
	t.Cleanup(srv.Close)
	host := strings.TrimPrefix(srv.URL, "http://")
	s := New(srv.Client(), make(chan struct{}, 2), nil, dockerds.New(srv.Client(), make(chan struct{}, 2)))
	d := dep.Dep{Datasource: Name, Name: "redis", Source: "oci://" + host + "/charts"}
	s.Plan([]dep.Dep{d})
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil || !slices.Equal(versions(rels), []string{"17.0.0", "18.0.0+build.1", "latest"}) {
		t.Fatalf("Releases = %v, %v", versions(rels), err)
	}
	if got, err := s.ReleaseTime(t.Context(), d, "18.0.0+build.1"); err != nil || !got.Equal(created) {
		t.Errorf("ReleaseTime = %v, %v", got, err)
	}
	if got, err := s.ReleaseTime(t.Context(), d, "19.0.0"); err != nil || !got.IsZero() {
		t.Errorf("ReleaseTime(unknown) = %v, %v", got, err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "missing", Source: d.Source}, ""); err == nil {
		t.Error("missing repository: no error")
	}
	if _, err := New(nil, nil, nil, nil).Chart(t.Context(), d.Source, "redis"); err == nil {
		t.Error("no OCI datasource: no error")
	}
	// Without release times from the OCI datasource, times are unknown.
	s = New(nil, nil, nil, staticTags{"1.0.0"})
	if got, err := s.ReleaseTime(t.Context(), d, "1.0.0"); err != nil || !got.IsZero() {
		t.Errorf("ReleaseTime without Timer = %v, %v", got, err)
	}
	if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "x"}, "1.0.0"); err == nil {
		t.Error("ReleaseTime without repository: no error")
	}
}

type staticTags []string

func (s staticTags) Releases(context.Context, dep.Dep, string) ([]datasource.Release, error) {
	out := make([]datasource.Release, len(s))
	for i, v := range s {
		out[i] = datasource.Release{Version: v}
	}
	return out, nil
}

func (staticTags) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func TestOCIDep(t *testing.T) {
	for repo, want := range map[string]string{
		"oci://registry-1.docker.io/bitnamicharts": "docker.io/bitnamicharts/redis",
		"oci://index.docker.io/bitnamicharts":      "docker.io/bitnamicharts/redis",
		"oci://ghcr.io/o/charts":                   "ghcr.io/o/charts/redis",
	} {
		if d := ociDep(repo, "redis"); d.Name != want || d.Source != want || d.Datasource != "docker" {
			t.Errorf("ociDep(%s) = %+v, want %s", repo, d, want)
		}
	}
}

// planningTags is an OCI datasource that records what it is planned for.
type planningTags struct {
	staticTags
	planned []dep.Dep
}

func (p *planningTags) Plan(deps []dep.Dep) { p.planned = append(p.planned, deps...) }

// TestPlanOCI checks Plan hands the OCI charts, as docker-datasource deps, to an OCI datasource
// that plans (the docker datasource batches Docker Hub tokens), and nothing when there are none.
func TestPlanOCI(t *testing.T) {
	oci := &planningTags{}
	s := New(nil, nil, nil, oci)
	s.Plan([]dep.Dep{{Name: "cert-manager", Source: "https://example.com/charts"}, {Name: "no-source"}})
	if oci.planned != nil {
		t.Errorf("planned without OCI charts: %+v", oci.planned)
	}
	s.Plan([]dep.Dep{
		{Datasource: Name, Name: "redis", Source: "oci://registry-1.docker.io/bitnamicharts"},
		{Datasource: Name, Name: "cert-manager", Source: "https://example.com/charts"},
		{Datasource: Name, Name: "app", Source: "oci://ghcr.io/o/charts"},
	})
	want := []dep.Dep{ociDep("oci://registry-1.docker.io/bitnamicharts", "redis"), ociDep("oci://ghcr.io/o/charts", "app")}
	same := func(a, b dep.Dep) bool {
		return a.Datasource == b.Datasource && a.Name == b.Name && a.Source == b.Source
	}
	if !slices.EqualFunc(oci.planned, want, same) {
		t.Errorf("planned = %+v, want %+v", oci.planned, want)
	}
}
