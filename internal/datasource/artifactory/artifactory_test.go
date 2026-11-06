package artifactory

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

// server serves path → body; other paths are 404. It counts requests.
func server(t *testing.T, pages map[string][]byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var n atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.Add(1)
		b, ok := pages[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func TestReleasesFolders(t *testing.T) {
	srv, n := server(t, map[string][]byte{"/artifactory/generic/project/": fixture(t, "releases-as-folders.html")})
	s := New(srv.Client(), nil, nil)
	d := dep.Dep{Name: "project", Source: srv.URL + "/artifactory/generic", Datasource: Name}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rels), []string{"1.0.0", "1.0.1", "1.0.2", "1.0.3"}; !slices.Equal(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.0.1")
	if err != nil || !tm.Equal(time.Date(2021, 8, 23, 20, 3, 0, 0, time.UTC)) {
		t.Fatalf("ReleaseTime = %v, %v", tm, err)
	}
	if tm, _ := s.ReleaseTime(t.Context(), d, "9.9.9"); !tm.IsZero() {
		t.Fatalf("unknown version time = %v", tm)
	}
	if n.Load() != 1 {
		t.Fatalf("requests = %d, want 1", n.Load())
	}
	if _, err := s.Digest(t.Context(), d, "1.0.0"); err == nil {
		t.Fatal("Digest: want error")
	}
}

func TestReleasesFilesFullURL(t *testing.T) {
	srv, _ := server(t, map[string][]byte{"/artifactory/generic/project/": fixture(t, "releases-as-files.html")})
	s := New(srv.Client(), nil, nil)
	d := dep.Dep{Name: srv.URL + "/artifactory/generic/project", Datasource: Name}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rels), []string{"1.0.0", "1.0.1", "1.0.2", "1.0.3"}; !slices.Equal(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	tm, _ := s.ReleaseTime(t.Context(), d, "1.0.3")
	if !tm.Equal(time.Date(2021, 2, 6, 9, 54, 0, 0, time.UTC)) {
		t.Fatalf("ReleaseTime = %v", tm)
	}
}

func TestReleasesMergesRegistries(t *testing.T) {
	srv, _ := server(t, map[string][]byte{
		"/a/p/": []byte(`<pre><a href="../">../</a>
<a href="1.0.0/">1.0.0/</a>  21-Jul-2021 20:08    -
<a href="1.1.0/">1.1.0/</a>  22-Jul-2021 20:08    -</pre>`),
		"/b/p/": []byte(`<pre><a href="1.1.0/">1.1.0/</a>  01-Jan-2020 00:00    -
<a href="2.0.0/">2.0.0/</a>  2022-03-04 05:06    -
<a href="x&amp;y">x&amp;y</a></pre>`),
	})
	s := New(srv.Client(), nil, nil)
	d := dep.Dep{Name: "/p/", Source: srv.URL + "/missing " + srv.URL + "/a/ " + srv.URL + "/b", Datasource: Name}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rels), []string{"1.0.0", "1.1.0", "2.0.0", "x&y"}; !slices.Equal(got, want) {
		t.Fatalf("versions = %v, want %v", got, want)
	}
	for v, want := range map[string]time.Time{
		"1.1.0": time.Date(2021, 7, 22, 20, 8, 0, 0, time.UTC), // first registry wins
		"2.0.0": time.Date(2022, 3, 4, 5, 6, 0, 0, time.UTC),
		"x&y":   {},
	} {
		if tm, _ := s.ReleaseTime(t.Context(), d, v); !tm.Equal(want) {
			t.Errorf("ReleaseTime(%s) = %v, want %v", v, tm, want)
		}
	}
}

func TestReleasesErrors(t *testing.T) {
	srv, _ := server(t, map[string][]byte{"/empty/p/": []byte("<html><a href=\"../\">../</a></html>")})
	s := New(srv.Client(), nil, nil)
	for name, d := range map[string]dep.Dep{
		"HTTP 404":       {Name: "p", Source: srv.URL + "/missing"},
		"no versions":    {Name: "p", Source: srv.URL + "/empty"},
		"no versions in": {Name: "p", Source: srv.URL + "/missing " + srv.URL + "/empty"},
		"unsupported":    {Name: "ftp://x/p"},
		"invalid URL":    {Name: "http://[::1/p"},
	} {
		_, err := s.Releases(t.Context(), d, "")
		if err == nil {
			t.Errorf("%s: want error", name)
			continue
		}
		if name != "unsupported" && name != "invalid URL" && !strings.Contains(err.Error(), name) {
			t.Errorf("%s: err = %v", name, err)
		}
		if _, err := s.ReleaseTime(t.Context(), d, "1"); err == nil {
			t.Errorf("%s: ReleaseTime: want error", name)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := New(srv.Client(), make(chan struct{}), nil).Releases(ctx, dep.Dep{Name: "p", Source: srv.URL + "/empty"}, ""); err == nil {
		t.Error("canceled: want error")
	}
}

func TestAuth(t *testing.T) {
	var gotAuth []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, _ := r.BasicAuth()
		gotAuth = append(gotAuth, user+":"+pass)
		if pass != "helper" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`<a href="1.0.0/">1.0.0/</a>  21-Jul-2021 20:08    -`))
	}))
	defer srv.Close()
	host, _, _ := strings.Cut(strings.TrimPrefix(srv.URL, "http://"), ":")
	auth := httpx.NewAuth(platform.Info{Host: host}, "tok", func(string) string { return "" },
		httpx.WithHelper(func(_ context.Context, h string) (string, string, bool) { return "u", "helper", h == host }))
	s := New(srv.Client(), nil, auth)
	rels, err := s.Releases(t.Context(), dep.Dep{Name: srv.URL + "/p"}, "")
	if err != nil || len(rels) != 1 {
		t.Fatalf("Releases = %v, %v", rels, err)
	}
	if want := []string{"x-access-token:tok", "u:helper"}; !slices.Equal(gotAuth, want) {
		t.Fatalf("auth = %v, want %v", gotAuth, want)
	}
	// Without helper credentials a 401 is an error.
	gotAuth = nil
	s = New(srv.Client(), nil, httpx.NewAuth(platform.Info{}, "", func(string) string { return "" },
		httpx.WithHelper(func(context.Context, string) (string, string, bool) { return "", "", false })))
	if _, err := s.Releases(t.Context(), dep.Dep{Name: srv.URL + "/p"}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("err = %v", err)
	}
}
