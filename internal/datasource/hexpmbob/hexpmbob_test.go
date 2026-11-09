package hexpmbob

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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// serve answers /builds/elixir/builds.txt and /builds/otp/ubuntu-22.04/builds.txt with
// excerpts of bob's lists in testdata (fetched 2026-11-06), /builds/otp/empty-1.0/builds.txt
// with branch builds only, and anything else with 404 as builds.hex.pm does.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		file := map[string]string{
			"/builds/elixir/builds.txt":           "testdata/elixir-builds.txt",
			"/builds/otp/ubuntu-22.04/builds.txt": "testdata/otp-ubuntu-22.04-builds.txt",
		}[r.URL.Path]
		switch {
		case file != "":
			b, err := os.ReadFile(file)
			if err != nil {
				t.Error(err)
			}
			w.Write(b)
		case r.URL.Path == "/builds/otp/empty-1.0/builds.txt":
			w.Write([]byte("maint 0123 2024-01-01T00:00:00Z\nshort line\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func env(url string) func(string) string {
	return func(k string) string {
		if k == "DEPS_HEXPM_BOB_URL" {
			return url
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

func TestElixir(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), make(chan struct{}, 1), env(srv.URL+"/"))
	d := dep.Dep{Name: "elixir"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	got := versions(rs)
	for _, v := range []string{"1.17.3", "1.17.3-otp-27", "1.18.0-rc.0", "1.18.0-rc.0-otp-27", "1.18.4", "1.18.4-otp-28"} {
		if !slices.Contains(got, v) {
			t.Errorf("releases lack %s: %v", v, got)
		}
	}
	for _, v := range got {
		if strings.HasPrefix(v, "main") || strings.HasPrefix(v, "v") || strings.Count(v, ".") < 2 {
			t.Errorf("branch build or prefixed version %s in releases", v)
		}
	}
	tm, err := s.ReleaseTime(t.Context(), d, "1.17.3")
	if err != nil || tm.IsZero() || tm.Location() != time.UTC {
		t.Errorf("release time = %v, %v", tm, err)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "9.9.9"); err != nil || !tm.IsZero() {
		t.Errorf("unknown release time = %v, %v", tm, err)
	}
	if r, _ := s.SourceRepo(t.Context(), d, ""); r != "https://github.com/elixir-lang/elixir" {
		t.Errorf("source repo = %q", r)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("requests = %d, want one per name", n)
	}
}

// ElixirVersioning keeps a build's OTP release and proposes release candidates only from one.
func TestElixirVersioning(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	rs, err := New(srv.Client(), nil, env(srv.URL)).Releases(t.Context(), dep.Dep{Name: "elixir"}, "")
	if err != nil {
		t.Fatal(err)
	}
	s, err := versioning.Parse(ElixirVersioning)
	if err != nil {
		t.Fatal(err)
	}
	for cur, want := range map[string]string{
		"1.17.3-otp-27": "1.19.7-otp-27", "1.17.3-otp-25": "1.18.6-otp-25", "1.17.3": "1.19.7",
		"1.18.0-rc.0-otp-27": "1.19.7-otp-27",
	} {
		if got, _ := s.Best(cur, versions(rs), nil, nil); got != want {
			t.Errorf("best from %s = %q, want %q", cur, got, want)
		}
	}
	if s.Valid("1.18") || s.Valid("main-otp-27") || !s.Valid("1.18.0-rc.0") {
		t.Error("ElixirVersioning validity")
	}
}

func TestOTP(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	d := dep.Dep{Name: "otp/ubuntu-22.04"}
	rs, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	got := versions(rs)
	for _, v := range []string{"26.2.5", "27.0-rc1", "27.3.4.1", "28.0"} {
		if !slices.Contains(got, v) {
			t.Errorf("releases lack %s: %v", v, got)
		}
	}
	if slices.ContainsFunc(got, func(v string) bool { return strings.HasPrefix(v, "maint") || strings.HasPrefix(v, "OTP") }) {
		t.Errorf("branch builds in %v", got)
	}
	if r, _ := s.SourceRepo(t.Context(), d, ""); r != "https://github.com/erlang/otp" {
		t.Errorf("source repo = %q", r)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := New(srv.Client(), nil, env(srv.URL))
	for name, want := range map[string]string{
		"erlang":           `invalid hexpm-bob name "erlang"`,
		"otp/ubuntu":       `invalid hexpm-bob name`,
		"otp/ubuntu-99.04": "HTTP 404",
		"otp/empty-1.0":    "no releases",
	} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: err = %v, want %q", name, err, want)
		}
		if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: name}, "1.0.0"); err == nil {
			t.Errorf("%s: release time without error", name)
		}
	}
	if r, _ := s.SourceRepo(t.Context(), dep.Dep{Name: "erlang"}, ""); r != "" {
		t.Errorf("source repo of an invalid name = %q", r)
	}
	if _, err := s.Digest(t.Context(), dep.Dep{Name: "elixir"}, "1.18.4"); err == nil {
		t.Error("digest without error")
	}
	if New(http.DefaultClient, nil, env("")).base != DefaultURL {
		t.Error("default URL")
	}
}
