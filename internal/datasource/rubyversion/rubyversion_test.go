package rubyversion

import (
	"context"
	"errors"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// testdata/releases.yml is _data/releases.yml of ruby/www.ruby-lang.org (2026-11-05) trimmed to
// eight entries: newest first per series, with previews, an rc and a patchlevel release.
func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/releases.yml" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(t *testing.T, body string, hits *atomic.Int32) *Source {
	srv := serve(t, body, hits)
	return New(srv.Client(), nil, func(k string) string {
		if k == "DEPS_RUBY_RELEASES_URL" {
			return srv.URL + "/releases.yml"
		}
		return ""
	})
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	b, err := os.ReadFile("testdata/releases.yml")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	s := newSource(t, string(b), &hits)
	ctx := context.Background()
	d := dep.Dep{Name: "ruby", Datasource: Name}
	rs, err := s.Releases(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"4.1.0-preview1", "4.0.9", "4.0.0", "4.0.0-preview3", "3.4.7", "3.4.0", "3.4.0-rc1", "2.0.0-p648", "4.0", "4", "3.4", "3"}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Fatalf("releases = %v, want %v", got, want)
	}
	for v, want := range map[string]string{"4.0.9": "2026-10-23", "4.0": "2025-12-25", "4": "2025-12-25", "3.4": "2024-12-25", "2.0.0-p648": "2015-12-16", "9.9.9": ""} {
		got, err := s.ReleaseTime(ctx, d, v)
		if err != nil {
			t.Fatal(err)
		}
		if w, _ := time.Parse(time.DateOnly, want); !got.Equal(w) {
			t.Errorf("ReleaseTime(%s) = %v, want %s", v, got, want)
		}
	}
	if _, err := s.Releases(ctx, d, ""); err != nil || hits.Load() != 1 {
		t.Errorf("second call: err %v, %d requests, want 1", err, hits.Load())
	}

	// The ruby scheme prefers the published 4.0.0 over the derived 4.0 and skips previews.
	cands := versions(rs)
	ruby, _ := versioning.Get("ruby")
	if got, _ := ruby.Best("3.4.7", cands, nil, nil); got != "4.0.9" {
		t.Errorf("ruby best from 3.4.7 = %q", got)
	}
	tag, _ := versioning.Get("tag")
	if got, _ := tag.Best("3.4", cands, nil, nil); got != "4.0" {
		t.Errorf("tag best from 3.4 = %q", got)
	}
}

// A first caller whose context ended does not leave its error to the dependencies looked up
// after it with a live context.
func TestReleasesAfterCanceledCaller(t *testing.T) {
	b, err := os.ReadFile("testdata/releases.yml")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	s := newSource(t, string(b), &hits)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.ReleaseTime(canceled, dep.Dep{}, "4.0.9"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v, want context.Canceled", err)
	}
	if rs, err := s.Releases(t.Context(), dep.Dep{}, ""); err != nil || len(rs) == 0 {
		t.Fatalf("live: %d releases, err %v", len(rs), err)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
}

func TestReleasesErrors(t *testing.T) {
	ctx := context.Background()
	var hits atomic.Int32
	for body, want := range map[string]string{
		"# nothing\n":                  "no releases",
		"- date: 2020-01-01\n  x: 1\n": "no releases",
	} {
		s := newSource(t, body, &hits)
		if _, err := s.Releases(ctx, dep.Dep{}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%q: err %v, want %q", body, err, want)
		}
	}
}

func TestMappingEntry(t *testing.T) {
	for l, want := range map[string][2]string{
		"  version: 3.4.7":             {"version", "3.4.7"},
		"  version: \"3.4.7\" # x":     {"version", "3.4.7"},
		"  date: '2024-12-25'":         {"date", "2024-12-25"},
		"  date: 2024-12-25 # note":    {"date", "2024-12-25"},
		"    gz: https://example/x.gz": {},
		"# 4.1 series":                 {},
	} {
		k, v, ok := mappingEntry(l)
		if ok != (want[0] != "") || k != want[0] || v != want[1] {
			t.Errorf("mappingEntry(%q) = %q, %q, %v", l, k, v, ok)
		}
	}
}
