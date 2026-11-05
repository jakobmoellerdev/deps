package dotnetversion

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// testdata holds the release metadata of https://builds.dotnet.microsoft.com/dotnet/release-metadata
// (fetched 2026-11-05): releases-index.json cut to the 9.0, 8.0 and 7.0 channels, and the 8.0 and
// 9.0 releases.json cut to their two newest and two oldest releases with only the fields read
// (release-date, release-version, security, runtime, sdk, sdks). 7.0's releases.json is absent.
func serve(t *testing.T) (*httptest.Server, *sync.Map) {
	t.Helper()
	hits := &sync.Map{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := hits.LoadOrStore(r.URL.Path, new(int))
		*n.(*int)++
		name := map[string]string{
			"/dotnet/release-metadata/releases-index.json": "releases-index.json",
			"/dotnet/release-metadata/8.0/releases.json":   "8.0-releases.json",
			"/dotnet/release-metadata/9.0/releases.json":   "9.0-releases.json",
		}[r.URL.Path]
		if name == "" {
			http.NotFound(w, r)
			return
		}
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Error(err)
		}
		w.Write(bytes.ReplaceAll(b, []byte("https://builds.dotnet.microsoft.com"), []byte(srv.URL)))
	}))
	t.Cleanup(srv.Close)
	return srv, hits
}

func newSource(srv *httptest.Server) *Source {
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_DOTNET_RELEASES_URL" {
			return srv.URL + "/dotnet/release-metadata/releases-index.json"
		}
		return ""
	})
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	srv, hits := serve(t)
	s := newSource(srv)
	rels, err := s.Releases(t.Context(), dep.Dep{Name: SDK, Version: "8.0.100"}, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"9.0.320", "9.0.123", "9.0.319", "9.0.122", "9.0.100-preview.2.24157.14", "9.0.100-preview.1.24101.2",
		"8.0.427", "8.0.133", "8.0.426", "8.0.132", "8.0.100-preview.2.23157.25", "8.0.100-preview.1.23115.2"}
	if got := versions(rels); !slices.Equal(got, want) {
		t.Errorf("sdk = %v\nwant %v", got, want)
	}
	rels, err = s.Releases(t.Context(), dep.Dep{Name: Runtime, Version: "9.0.0"}, "")
	if err != nil || !slices.Equal(versions(rels), []string{"9.0.22", "9.0.21", "9.0.0-preview.2.24128.5", "9.0.0-preview.1.24080.9"}) {
		t.Errorf("runtime = %v, %v", versions(rels), err)
	}
	tm, err := s.ReleaseTime(t.Context(), dep.Dep{Name: SDK}, "8.0.133")
	if err != nil || !tm.Equal(time.Date(2026, 10, 25, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if tm, err := s.ReleaseTime(t.Context(), dep.Dep{Name: SDK}, "9.9.999"); err != nil || !tm.IsZero() {
		t.Errorf("unknown ReleaseTime = %v, %v", tm, err)
	}
	// The index and each channel once; 7.0 is below every version asked for.
	for path, want := range map[string]int{"/dotnet/release-metadata/releases-index.json": 1, "/dotnet/release-metadata/8.0/releases.json": 1, "/dotnet/release-metadata/9.0/releases.json": 1} {
		if n, ok := hits.Load(path); !ok || *n.(*int) != want {
			t.Errorf("%s fetched %v times, want %d", path, n, want)
		}
	}
	if _, ok := hits.Load("/dotnet/release-metadata/7.0/releases.json"); ok {
		t.Error("7.0 fetched")
	}
	// Without a version every channel is read: 7.0 is missing upstream.
	if _, err := s.Releases(t.Context(), dep.Dep{Name: SDK}, ""); err == nil || !strings.Contains(err.Error(), "HTTP 404") {
		t.Errorf("all channels: %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "aspnet"}, ""); err == nil || !strings.Contains(err.Error(), "unknown name") {
		t.Errorf("unknown name: %v", err)
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: SDK}, "8.0.100"); err == nil {
		t.Error("Digest: no error")
	}
}

func TestIndexErrors(t *testing.T) {
	for body, want := range map[string]string{`{"releases-index":[]}`: "no channels", `[`: "GET "} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, body) }))
		s := New(srv.Client(), nil, func(string) string { return srv.URL })
		if _, err := s.Releases(t.Context(), dep.Dep{Name: SDK, Version: "8.0.100"}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", body, err)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: SDK}, "8.0.100"); err == nil {
			t.Errorf("%s: ReleaseTime no error", body)
		}
		srv.Close()
	}
}
