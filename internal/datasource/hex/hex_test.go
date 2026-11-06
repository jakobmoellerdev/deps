package hex

import (
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
)

// serve answers /api/packages/<name> with the hex.pm responses in testdata (fetched
// 2026-11-06, reduced to the fields deps reads), /api/repos/acme/packages/secret only with the
// API key, an unknown package with 404 as hex.pm does, and /api/packages/bad with a non-object.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch name := strings.TrimPrefix(r.URL.Path, "/api/packages/"); {
		case r.URL.Path == "/api/repos/acme/packages/secret" && r.Header.Get("Authorization") == "key123":
			w.Write([]byte(`{"releases": [{"version": "0.1.0", "inserted_at": "2024-01-02T03:04:05Z"}], "meta": {"links": {}}}`))
		case name == "bad":
			w.Write([]byte(`["1.0.0"]`))
		case name == "empty":
			w.Write([]byte(`{"releases": [{"version": "1.0"}]}`))
		case name == "jason", name == "plug_cowboy", name == "phoenix", name == "gleam_stdlib":
			b, err := os.ReadFile("testdata/" + name + ".json")
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case name == "slow":
			http.Error(w, "busy", http.StatusServiceUnavailable)
		default:
			http.Error(w, `{"status":404,"message":"Page not found"}`, http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url, key string) func(string) string {
	return func(k string) string {
		switch k {
		case "DEPS_HEX_URL":
			return url
		case "HEX_API_KEY":
			return key
		}
		return ""
	}
}

func versions(rs []datasource.Release) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.Version
	}
	return out
}

func TestReleases(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/", ""))
	d := dep.Dep{Name: "jason"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"1.0.0-rc.1", "1.0.0-rc.2", "1.0.0-rc.3", "1.0.0", "1.0.1", "1.1.0", "1.1.1", "1.1.2", "1.2.0", "1.2.1",
		"1.2.2", "1.3.0", "1.4.0", "1.4.1", "1.4.2", "1.4.3", "1.4.4", "1.4.5", "1.5.0-alpha.1", "1.5.0-alpha.2"}
	if got := versions(rs); !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.4.4")
	if err != nil || !tm.Equal(time.Date(2024, 7, 26, 17, 51, 45, 963093000, time.UTC)) {
		t.Errorf("release time = %v, %v", tm, err)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "9.9.9"); err != nil || !tm.IsZero() {
		t.Errorf("unknown release time = %v, %v", tm, err)
	}
	if r, err := s.SourceRepo(t.Context(), d, "1.4.4"); err != nil || r != "https://github.com/michalmuskala/jason" {
		t.Errorf("source repo = %q, %v", r, err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("requests = %d, want one per package", n)
	}
	pc := dep.Dep{Name: "plug_cowboy"}
	if _, err := s.Releases(t.Context(), pc, ""); err != nil {
		t.Fatal(err)
	}
	if got := s.Deprecation(pc, "2.2.0"); got != "version 2.2.0 retired (invalid: Broken telemetry support)" {
		t.Errorf("deprecation = %q", got)
	}
	if got := s.Deprecation(pc, "2.7.2"); got != "" {
		t.Errorf("deprecation of a current version = %q", got)
	}
	if got := s.Deprecation(dep.Dep{Name: "nope"}, "1.0.0"); got != "" {
		t.Errorf("deprecation of a missing package = %q", got)
	}
}

func TestOrganization(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL, "key123"))
	rs, err := s.Releases(t.Context(), dep.Dep{Name: "secret:acme"}, "")
	if err != nil || !slices.Equal(versions(rs), []string{"0.1.0"}) {
		t.Fatalf("releases = %v, %v", versions(rs), err)
	}
	if r, err := s.SourceRepo(t.Context(), dep.Dep{Name: "secret:acme"}, ""); err != nil || r != "" {
		t.Errorf("source repo = %q, %v", r, err)
	}
	anon := New(srv.Client(), nil, env(srv.URL, ""))
	if _, err := anon.Releases(t.Context(), dep.Dep{Name: "secret:acme"}, ""); err == nil || !strings.Contains(err.Error(), "package not found") {
		t.Errorf("without a key: %v", err)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL, ""))
	for name, want := range map[string]string{
		"nope":       "GET " + srv.URL + "/api/packages/nope: package not found",
		"bad":        "GET " + srv.URL + "/api/packages/bad: ",
		"empty":      "no releases",
		"slow":       "HTTP 503",
		"Phoenix":    `invalid Hex package name "Phoenix"`,
		"a:b:c":      `invalid Hex package name "a:b:c"`,
		"phoenix/x":  `invalid Hex package name "phoenix/x"`,
		"x:Org-Name": `invalid Hex package name`,
	} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("%s: release time without error", name)
		}
		if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("%s: source repo without error", name)
		}
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "jason"}, "1.0.0"); err == nil {
		t.Error("digest without error")
	}
	if New(http.DefaultClient, nil, env("", "")).base != DefaultURL {
		t.Error("default URL")
	}
}
