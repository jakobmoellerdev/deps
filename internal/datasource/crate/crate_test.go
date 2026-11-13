package crate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// testdata/serde holds nine lines of https://index.crates.io/se/rd/serde verbatim (fetched
// 2026-11-04): prereleases, 1.0.95 yanked, and 0.9.15 published after 1.0.0.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	serde, err := os.ReadFile("testdata/serde")
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/index/se/rd/serde":
			w.Write(serde)
		case "/index/2/cc":
			fmt.Fprintln(w, `{"name":"cc","vers":"1.0.0","yanked":false}`)
			fmt.Fprintln(w, `{"name":"cc","vers":"1.1.0","yanked":true}`)
		case "/private/2/cc":
			fmt.Fprintln(w, `{"name":"cc","vers":"1.0.0","yanked":false}`)
			fmt.Fprintln(w, `{"name":"cc","vers":"1.1.0","yanked":false}`)
		case "/index/1/a", "/index/3/a/abc":
			fmt.Fprintln(w, `{"name":"a","vers":"0.1.0","yanked":true}`)
		case "/index/br/ok/broken":
			fmt.Fprintln(w, `{"vers":`)
		case "/index/fa/il/failing":
			http.Error(w, "down", http.StatusInternalServerError)
		case "/index/go/ne/gone":
			http.Error(w, "gone", http.StatusGone)
		case "/api/api/v1/crates/serde/1.0.229", "/api/api/v1/crates/serde/1.0.228":
			fmt.Fprintf(w, `{"version":{"num":%q,"repository":"https://github.com/serde-rs/serde","license":"MIT OR Apache-2.0"}}`, strings.TrimPrefix(r.URL.Path, "/api/api/v1/crates/serde/"))
		case "/api/api/v1/crates/serde/0.9.15":
			fmt.Fprint(w, `{"version":{"num":"0.9.15","repository":null}}`)
		case "/api/api/v1/crates/serde/1.0.171":
			fmt.Fprint(w, `{"version":`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(srv *httptest.Server) *Source {
	home := os.TempDir() + "/deps-crate-test-no-cargo-home"
	return New(srv.Client(), make(chan struct{}, 1), nil, func(k string) string {
		switch k {
		case "DEPS_CRATES_INDEX_URL":
			return srv.URL + "/index/"
		case "DEPS_CRATES_API_URL":
			return srv.URL + "/api"
		case "CARGO_HOME":
			return home
		}
		return ""
	})
}

func versions(rs []datasource.Release) string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return strings.Join(out, " ")
}

func TestIndexPath(t *testing.T) {
	for name, want := range map[string]string{
		"a": "1/a", "cc": "2/cc", "syn": "3/s/syn", "serde": "se/rd/serde", "Serde_JSON": "se/rd/serde_json", "tokio-util": "to/ki/tokio-util",
	} {
		if got, ok := IndexPath(name); !ok || got != want {
			t.Errorf("IndexPath(%q) = %q, %v, want %q", name, got, ok, want)
		}
	}
	for _, name := range []string{"", "a/b", "../x", "naïve", strings.Repeat("x", 65)} {
		if got, ok := IndexPath(name); ok {
			t.Errorf("IndexPath(%q) = %q", name, got)
		}
	}
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits))
	full := dep.Dep{Name: "serde", Version: "1.0.171"}
	partial := dep.Dep{Name: "Serde", Version: "1.0"} // names are case-insensitive
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if _, err := s.Releases(t.Context(), full, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	rels, err := s.Releases(t.Context(), full, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(rels), "0.9.0-rc4 0.9.0 1.0.0 0.9.15 1.0.171 1.0.172-alpha.0 1.0.228 1.0.229"; got != want {
		t.Errorf("full: %s, want %s", got, want)
	}
	lines, err := s.Releases(t.Context(), partial, "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := versions(lines), versions(rels)+" 0.9 0 1.0 1"; got != want {
		t.Errorf("partial: %s, want %s", got, want)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}

	// The schemes the cargo manager picks keep the written precision.
	for _, tc := range []struct {
		cur, scheme, want string
		rels              []datasource.Release
	}{
		{"1.0.171", "semver", "1.0.229", rels},
		{"0.9.0", "semver", "1.0.229", rels},
		{"1.0", "tag", "", lines}, // no 1.x line beyond 1.0
		{"0.9", "tag", "1.0", lines},
		{"0", "tag", "1", lines},
	} {
		s, _ := versioning.Get(tc.scheme)
		cands := make([]string, len(tc.rels))
		for i, r := range tc.rels {
			cands[i] = r.Version
		}
		if got, _ := s.Best(tc.cur, cands, nil, nil); got != tc.want {
			t.Errorf("%s %s: best = %q, want %q", tc.scheme, tc.cur, got, tc.want)
		}
	}
}

// TestDeprecationPerRegistry pins that two deps of the same manifest and crate name but
// different registries (`cc = "1"` under [dependencies] and `cc = { version = "1", registry =
// "private" }` under [build-dependencies]) keep their own index: the located registry is keyed
// by the source too, so the order of the Releases lookups cannot swap their yanked versions.
func TestDeprecationPerRegistry(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv)
	dir := t.TempDir()
	cratesIO := dep.Dep{Name: "cc", File: "Cargo.toml", Version: "1.0.0"}
	private := dep.Dep{Name: "cc", File: "Cargo.toml", Version: "1.0.0", Source: "sparse+" + srv.URL + "/private/"}
	for _, d := range []dep.Dep{cratesIO, private} {
		if _, err := s.Releases(t.Context(), d, dir); err != nil {
			t.Fatal(err)
		}
	}
	// 1.1.0 is yanked on crates.io and published on the private registry.
	if got := s.Deprecation(cratesIO, "1.1.0"); got != "version 1.1.0 yanked" {
		t.Errorf("Deprecation(crates.io) = %q, want the yanked 1.1.0", got)
	}
	if got := s.Deprecation(private, "1.1.0"); got != "" {
		t.Errorf("Deprecation(private) = %q, want none", got)
	}
}

func TestReleaseTimeAndSourceRepo(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits))
	d := dep.Dep{Name: "serde", Version: "1.0"}
	for v, want := range map[string]string{
		"1.0.229": "2026-07-18T23:05:13Z",
		"1.0":     "2017-04-20T15:26:44Z", // the line's first release
		"0.9":     "2017-01-25T20:58:01Z",
		"1":       "2017-04-20T15:26:44Z",
		"1.0.95":  "0001-01-01T00:00:00Z", // yanked
		"9.9.9":   "0001-01-01T00:00:00Z",
	} {
		got, err := s.ReleaseTime(t.Context(), d, v)
		if err != nil || got.Format(time.RFC3339) != want {
			t.Errorf("ReleaseTime(%s) = %v, %v, want %s", v, got, err, want)
		}
	}
	for v, want := range map[string]string{
		"1.0.228": "https://github.com/serde-rs/serde",
		"1":       "https://github.com/serde-rs/serde", // the line's newest version, 1.0.229
		"0.9":     "",                                  // 0.9.15 has none
		"7.7.7":   "https://github.com/serde-rs/serde", // unknown: the newest published version
	} {
		got, err := s.SourceRepo(t.Context(), d, v)
		if err != nil || got != want {
			t.Errorf("SourceRepo(%s) = %q, %v, want %q", v, got, err, want)
		}
	}
	before := hits.Load()
	if _, err := s.SourceRepo(t.Context(), d, "1.0.229"); err != nil || hits.Load() != before {
		t.Errorf("SourceRepo is not cached: %v, %d requests", err, hits.Load()-before)
	}
	if _, err := s.SourceRepo(t.Context(), d, "1.0.171"); err == nil || !strings.Contains(err.Error(), "/api/v1/crates/serde/1.0.171: ") {
		t.Errorf("malformed version document: %v", err)
	}
	if got, err := s.SourceRepo(t.Context(), d, "1.0.172-alpha.0"); err != nil || got != "" {
		t.Errorf("version without API document = %q, %v", got, err)
	}
	if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: "missing"}, "1.0.0"); err == nil || err.Error() != "crate missing not found" {
		t.Errorf("missing crate: %v", err)
	}
	if _, err := s.Digest(t.Context(), d, "1.0.0"); err == nil || err.Error() != "crate: digests are not supported for serde" {
		t.Errorf("Digest err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv)
	for name, want := range map[string]string{
		"missing": "crate missing not found",
		"gone":    "crate gone not found",
		"a":       "crate a has no releases",
		"abc":     "crate abc has no releases",
		"broken":  srv.URL + "/index/br/ok/broken: ",
		"failing": "GET " + srv.URL + "/index/fa/il/failing: HTTP 500",
		"a/b":     `invalid crate name "a/b"`,
	} {
		_, err := s.Releases(t.Context(), dep.Dep{Name: name, Version: "1.0.0"}, "")
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%s: err = %v, want %s", name, err, want)
		}
	}
	if rels, err := s.Releases(t.Context(), dep.Dep{Name: "cc", Version: "1"}, ""); err != nil || versions(rels) != "1.0.0 1.0 1" {
		t.Errorf("cc = %s, %v", versions(rels), err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	blocked := New(srv.Client(), make(chan struct{}), nil, func(string) string { return srv.URL + "/index" })
	if _, err := blocked.Releases(ctx, dep.Dep{Name: "serde"}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	unbounded := New(srv.Client(), nil, nil, func(string) string { return "http://127.0.0.1:1" })
	if _, err := unbounded.Releases(t.Context(), dep.Dep{Name: "serde"}, ""); err == nil {
		t.Error("unreachable index accepted")
	}
	bad := New(srv.Client(), nil, nil, func(string) string { return "http://[::1" })
	if _, err := bad.Releases(t.Context(), dep.Dep{Name: "serde"}, ""); err == nil {
		t.Error("invalid index URL accepted")
	}
	if d := New(srv.Client(), nil, nil, func(string) string { return "" }); d.index != DefaultIndexURL || d.api != DefaultAPIURL {
		t.Errorf("defaults = %s, %s", d.index, d.api)
	}
}

// TestNewerParity pins newer to the strings.Split formulation it replaced.
func TestNewerParity(t *testing.T) {
	split := func(a, b string) (r bool, panicked bool) {
		defer func() { panicked = recover() != nil }()
		pa, pb := strings.Split(a, "."), strings.Split(b, ".")
		for i := range pa {
			if len(pa[i]) != len(pb[i]) {
				return len(pa[i]) > len(pb[i]), false
			}
			if pa[i] != pb[i] {
				return pa[i] > pb[i], false
			}
		}
		return false, false
	}
	cut := func(a, b string) (r bool, panicked bool) {
		defer func() { panicked = recover() != nil }()
		return newer(a, b), false
	}
	corpus := []string{"", ".", "..", "1", "1.", "1.0", "1.0.0", "1.0.1", "1.0.10", "1.1.0", "1.10.0", "2.0.0",
		"0.9.15", "10.0.0", "9.99.99", "01.0.0", "1.0.229", "1.0.228", "1.2.3.4", "a.b.c", "1..0"}
	for _, a := range corpus {
		for _, b := range corpus {
			wr, wp := split(a, b)
			gr, gp := cut(a, b)
			if wr != gr || wp != gp {
				t.Errorf("newer(%q, %q) = %v (panic %v), want %v (panic %v)", a, b, gr, gp, wr, wp)
			}
		}
	}
}

// TestDeprecation reports a yanked version from the index lines Releases read, without a
// request of its own.
func TestDeprecation(t *testing.T) {
	var hits atomic.Int32
	s := newSource(serve(t, &hits))
	d := dep.Dep{Name: "cc", Version: "1.0.0", File: "Cargo.toml"}
	if got := s.Deprecation(d, "1.1.0"); got != "" {
		t.Errorf("Deprecation before Releases = %q, want empty", got)
	}
	if _, err := s.Releases(t.Context(), d, t.TempDir()); err != nil {
		t.Fatal(err)
	}
	n := hits.Load()
	if got := s.Deprecation(d, "1.1.0"); got != "version 1.1.0 yanked" {
		t.Errorf("Deprecation(1.1.0) = %q", got)
	}
	if got := s.Deprecation(d, "1.0.0"); got != "" {
		t.Errorf("Deprecation(1.0.0) = %q, want empty", got)
	}
	if hits.Load() != n {
		t.Errorf("Deprecation made %d requests", hits.Load()-n)
	}
}
