package rustversion

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

// testdata/manifests.txt is https://static.rust-lang.org/manifests.txt (2026-11-05) trimmed to
// 31 lines: the first manifests of 2016 (1.8.0 published twice), the 1.82.0 release with the
// 1.83 betas around it, 1.83.0, 1.99.0 and 1.99.1, and the newest lines with a numbered 1.100
// beta; channel manifests (stable, beta, nightly) and release-line aliases (1.82, 1.83-beta,
// 1.83.0-beta) sit between them.
func serve(t *testing.T, body string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.URL.Path != "/manifests.txt" || !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
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
		if k == "DEPS_RUST_MANIFESTS_URL" {
			return srv.URL + "/manifests.txt"
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
	b, err := os.ReadFile("testdata/manifests.txt")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	s := newSource(t, string(b), &hits)
	ctx := context.Background()
	d := dep.Dep{Name: "rust", Datasource: Name}
	rs, err := s.Releases(ctx, d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"nightly-2016-04-12", "1.8.0", "nightly-2016-04-13", "nightly-2016-04-14", "1.83.0-beta.1", "nightly-2024-10-17", "1.82.0",
		"nightly-2024-10-18", "nightly-2024-10-19", "1.83.0-beta.2", "1.83.0", "1.99.0", "1.99.1", "1.100.0-beta.10", "nightly-2026-11-05",
		"1.8", "1.82", "1.83", "1.99",
	}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Fatalf("releases = %v, want %v", got, want)
	}
	for v, want := range map[string]string{
		"1.8.0": "2016-04-12", "1.82.0": "2024-10-17", "1.82": "2024-10-17", "1.99.1": "2026-10-15", "1.99": "2026-10-01",
		"1.100.0-beta.10": "2026-11-05", "nightly-2026-11-05": "2026-11-05", "stable": "", "nightly": "",
	} {
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
	if repo, err := s.SourceRepo(ctx, d, "1.99.1"); repo != Repo || err != nil {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if _, err := s.Digest(ctx, d, "1.99.1"); err == nil {
		t.Error("Digest: no error")
	}

	// semver skips the betas, nightlies and the derived lines; tag keeps the precision of a
	// line; NightlyVersioning moves a dated nightly to the newest, a patch update.
	cands := versions(rs)
	semver, _ := versioning.Get("semver")
	if got, _ := semver.Best("1.82.0", cands, nil, nil); got != "1.99.1" {
		t.Errorf("semver best from 1.82.0 = %q", got)
	}
	tag, _ := versioning.Get("tag")
	if got, _ := tag.Best("1.82", cands, nil, nil); got != "1.99" {
		t.Errorf("tag best from 1.82 = %q", got)
	}
	nightly, err := versioning.Parse(NightlyVersioning)
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := nightly.Best("nightly-2016-04-13", cands, nil, nil); got != "nightly-2026-11-05" {
		t.Errorf("nightly best from nightly-2016-04-13 = %q", got)
	}
	if k := nightly.Kind("nightly-2016-04-13", "nightly-2026-11-05"); k != dep.Patch {
		t.Errorf("nightly kind = %s, want patch", k)
	}
	if nightly.Valid("1.82.0") || nightly.Valid("nightly") {
		t.Error("nightly versioning accepts a non-nightly")
	}
}

// A first caller whose context ended (a canceled sibling, a passed deadline) does not leave
// its error to the dependencies looked up after it with a live context.
func TestReleasesAfterCanceledCaller(t *testing.T) {
	b, err := os.ReadFile("testdata/manifests.txt")
	if err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	s := newSource(t, string(b), &hits)
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Releases(canceled, dep.Dep{}, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled: err = %v, want context.Canceled", err)
	}
	if rs, err := s.Releases(t.Context(), dep.Dep{}, ""); err != nil || len(rs) == 0 {
		t.Fatalf("live: %d releases, err %v", len(rs), err)
	}
	if ts, err := s.ReleaseTime(t.Context(), dep.Dep{}, "1.82.0"); err != nil || ts.IsZero() {
		t.Errorf("ReleaseTime = %v, %v", ts, err)
	}
	if hits.Load() != 1 {
		t.Errorf("%d requests, want 1", hits.Load())
	}
}

func TestReleasesErrors(t *testing.T) {
	ctx := context.Background()
	var hits atomic.Int32
	s := newSource(t, "static.rust-lang.org/dist/2026-11-05/channel-rust-stable.toml\n", &hits)
	if _, err := s.Releases(ctx, dep.Dep{}, ""); err == nil || !strings.Contains(err.Error(), "no releases") {
		t.Errorf("err %v, want no releases", err)
	}
	if _, err := s.ReleaseTime(ctx, dep.Dep{}, "1.82.0"); err == nil {
		t.Error("ReleaseTime: no error")
	}
	s = New(http.DefaultClient, nil, func(string) string { return "" })
	if s.url != DefaultURL {
		t.Errorf("url = %q", s.url)
	}
}
