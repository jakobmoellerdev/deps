package conda

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// testdata/conda-forge-tomli.json is https://api.anaconda.org/package/conda-forge/tomli
// verbatim, testdata/conda-forge-six.json https://api.anaconda.org/package/conda-forge/six with
// only the name, version, dev_url and file basename, version, labels and upload_time fields
// (both fetched 2026-11-05). six 1.11.0 has a file only under `broken`, tomli 2.0.0 one under
// `main` and `broken`.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	files := map[string][]byte{}
	for _, n := range []string{"six", "tomli"} {
		b, err := os.ReadFile("testdata/conda-forge-" + n + ".json")
		if err != nil {
			t.Fatal(err)
		}
		files["/package/conda-forge/"+n] = b
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		if b, ok := files[r.URL.Path]; ok {
			w.Write(b)
			return
		}
		switch r.URL.Path {
		case "/package/bioconda/samtools":
			fmt.Fprint(w, `{"versions":["1.9","1.21"],"dev_url":"not a url","files":[]}`)
		case "/package/conda-forge/broken":
			fmt.Fprint(w, `{"files":7}`)
		case "/package/conda-forge/failing":
			http.Error(w, "down", http.StatusInternalServerError)
		default:
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(srv *httptest.Server) *Source {
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_CONDA_URL" {
			return srv.URL + "/"
		}
		return ""
	})
}

func versionsOf(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits))
	ctx := context.Background()
	rels, err := s.Releases(ctx, dep.Dep{Name: "six"}, "")
	if err != nil {
		t.Fatal(err)
	}
	got := versionsOf(rels)
	slices.Sort(got)
	if want := []string{"1.10.0", "1.11.0", "1.12.0", "1.13.0", "1.14.0", "1.15.0", "1.16.0", "1.17.0"}; !slices.Equal(got, want) {
		t.Errorf("six = %v, want %v", got, want)
	}
	// The broken-only file of 1.11.0 (uploaded a day earlier) does not count.
	for v, want := range map[string]string{"1.11.0": "2017-09-21T14:06:08.93Z", "1.17.0": "2024-12-05T06:44:15.7Z", "1.17": "2024-12-05T06:44:15.7Z"} {
		tm, err := s.ReleaseTime(ctx, dep.Dep{Name: "six"}, v)
		if err != nil || tm.UTC().Format(time.RFC3339Nano) != want {
			t.Errorf("ReleaseTime(%s) = %v, %v, want %s", v, tm, err, want)
		}
	}
	if tm, err := s.ReleaseTime(ctx, dep.Dep{Name: "six"}, "9.9"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime(9.9) = %v, %v", tm, err)
	}
	if repo, err := s.SourceRepo(ctx, dep.Dep{Name: "six"}, "1.17.0"); err != nil || repo != "https://github.com/benjaminp/six" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1 (memoized)", n)
	}

	// pytorch has no tomli; 2.0.0 stays a release through its other, unbroken main file.
	rels, err = s.Releases(ctx, dep.Dep{Name: "tomli", Source: "pytorch conda-forge"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); len(got) != 18 || !slices.Contains(got, "2.0.0") || !slices.Contains(got, "2.5.0") {
		t.Errorf("tomli = %v", got)
	}
}

func TestReleasesFallbackAndErrors(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits))
	ctx := context.Background()
	rels, err := s.Releases(ctx, dep.Dep{Name: "bioconda/samtools"}, "")
	if err != nil || !slices.Equal(versionsOf(rels), []string{"1.9", "1.21"}) {
		t.Errorf("samtools = %v, %v", rels, err)
	}
	if repo, _ := s.SourceRepo(ctx, dep.Dep{Name: "bioconda/samtools"}, ""); repo != "" {
		t.Errorf("SourceRepo = %q", repo)
	}
	if tm, err := s.ReleaseTime(ctx, dep.Dep{Name: "bioconda/samtools"}, "1.21"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	for _, tt := range []struct {
		d    dep.Dep
		want string
	}{
		{dep.Dep{Name: "nope", Source: "pytorch conda-forge"}, "conda package nope not found in pytorch, conda-forge"},
		{dep.Dep{Name: "broken"}, "JSON number into Go slice"},
		{dep.Dep{Name: "failing"}, "HTTP 500"},
	} {
		_, err := s.Releases(ctx, tt.d, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want %q", tt.d.Name, err, tt.want)
		}
		if _, err := s.ReleaseTime(ctx, tt.d, "1"); err == nil {
			t.Errorf("%s: ReleaseTime without error", tt.d.Name)
		}
		if _, err := s.SourceRepo(ctx, tt.d, "1"); err == nil {
			t.Errorf("%s: SourceRepo without error", tt.d.Name)
		}
	}
	if _, err := s.Digest(ctx, dep.Dep{Name: "six"}, "1.17.0"); err == nil {
		t.Error("Digest supported")
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := New(http.DefaultClient, make(chan struct{}), func(string) string { return "" }).Releases(canceled, dep.Dep{Name: "x"}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	if _, err := New(http.DefaultClient, nil, func(string) string { return "http://[::1]:namedport" }).Releases(ctx, dep.Dep{Name: "x"}, ""); err == nil {
		t.Error("invalid base URL accepted")
	}
	if _, err := New(http.DefaultClient, nil, func(string) string { return "http://127.0.0.1:1" }).Releases(ctx, dep.Dep{Name: "x"}, ""); err == nil {
		t.Error("unreachable host accepted")
	}
}

func TestReleasesConcurrent(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits))
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.Releases(context.Background(), dep.Dep{Name: "six", Source: "conda-forge"}, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestChannel(t *testing.T) {
	for in, want := range map[string]string{
		"conda-forge": "conda-forge", "\tbioconda\t": "bioconda", "defaults": "anaconda", "conda-forge/label/rc": "conda-forge",
		"conda-forge/linux-64": "conda-forge", "https://conda.anaconda.org/conda-forge": "conda-forge",
		"https://conda.anaconda.org/pytorch/": "pytorch", "https://anaconda.org/nvidia": "nvidia", "https://repo.anaconda.com/pkgs/main": "anaconda",
		"nodefaults": "", "https://repo.anaconda.com/pkgs/r": "", "https://prefix.dev/conda-forge": "", "file:///opt/channel": "",
		"/opt/channel": "", "./channel": "", "local": "", "": "",
	} {
		got, ok := Channel(in)
		if got != want || ok != (want != "") {
			t.Errorf("Channel(%q) = %q, %v, want %q", in, got, ok, want)
		}
	}
}
