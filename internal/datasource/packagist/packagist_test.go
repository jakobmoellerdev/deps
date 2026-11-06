package packagist

import (
	"fmt"
	"io"
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

// testdata/psr-log.json is https://repo.packagist.org/p2/psr/log.json verbatim (minified
// composer/2.0 metadata), testdata/drupal-packages.json https://packages.drupal.org/8/packages.json
// and testdata/drupal-token.json https://packages.drupal.org/files/packages/8/p2/drupal/token.json
// (not minified, without release times), all fetched 2026-11-05.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile("testdata/" + name)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	psrLog, drupalIndex, token := read("psr-log.json"), read("drupal-packages.json"), read("drupal-token.json")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		switch r.URL.Path {
		case "/p2/psr/log.json":
			w.Write(psrLog)
		case "/8/packages.json":
			w.Write(drupalIndex)
		case "/files/packages/8/p2/drupal/token.json":
			w.Write(token)
		case "/private/packages.json":
			if u, p, ok := r.BasicAuth(); !ok || u != "bot" || p != "s3cret" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			io.WriteString(w, `{"metadata-url":"p2/%package%.json","available-packages":["acme/Lib"]}`)
		case "/private/p2/acme/lib.json":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"minified":"composer/2.0","packages":{"acme/lib":[
				{"name":"acme/lib","version":"2.0.0","time":"2026-10-01T10:00:00+00:00","source":{"type":"git","url":"git@git.example.com:acme/lib.git"}},
				{"version":"1.1.0"},
				{"version":"1.0.0","time":"__unset"},
				{"version":"dev-main","time":"2026-11-01T00:00:00+00:00"}]}}`)
		case "/bearer/packages.json":
			if r.Header.Get("Authorization") != "Bearer tok" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"packages":{"acme/tool":{"1.0.0":{"version":"1.0.0","time":"2026-01-01T00:00:00+00:00"},"dev-main":{"version":"dev-main"}}},
				"includes":{"include/all$1.json":{"sha1":"x"}}}`)
		case "/bearer/include/all$1.json":
			fmt.Fprint(w, `{"packages":{"acme/tool":{"1.1.0":{"version":"1.1.0"},"1.0.0":{"version":"1.0.0"}},"acme/other":{"3.0.0":{"version":"v3.0.0"}}}}`)
		case "/v1/packages.json":
			io.WriteString(w, `{"packages":[],"providers-url":"/p/%package%$%hash%.json"}`)
		case "/broken/packages.json", "/p2/broken/pkg.json":
			fmt.Fprint(w, `[1]`)
		case "/p2/failing/pkg.json":
			http.Error(w, "down", http.StatusInternalServerError)
		case "/badmeta/packages.json":
			fmt.Fprint(w, `{"metadata-url":"http://[::1"}`)
		case "/badinclude/packages.json":
			fmt.Fprint(w, `{"includes":{"http://[::1":{}}}`)
		case "/badinline/packages.json":
			fmt.Fprint(w, `{"packages":{"a/b":3}}`)
		case "/direct.json":
			fmt.Fprint(w, `{"packages":{"acme/direct":[{"version":"0.1.0"}]}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func newSource(srv *httptest.Server, env map[string]string) *Source {
	if env == nil {
		env = map[string]string{}
	}
	env["DEPS_PACKAGIST_URL"] = srv.URL + "/"
	return New(srv.Client(), nil, func(k string) string { return env[k] })
}

func versionsOf(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestPackagist(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	d := dep.Dep{Name: "PSR/Log"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"3.0.2", "3.0.1", "3.0.0", "2.0.0", "1.1.4", "1.1.3", "1.1.2", "1.1.1", "1.1.0", "1.0.2", "1.0.1", "1.0.0"}
	if got := versionsOf(rels); !slices.Equal(got, want) {
		t.Errorf("versions = %v", got)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "3.0.2"); err != nil || !tm.Equal(time.Date(2024, 9, 11, 13, 17, 53, 0, time.UTC)) {
		t.Errorf("time = %v, %v", tm, err)
	}
	if repo, err := s.SourceRepo(t.Context(), d, ""); err != nil || repo != "https://github.com/php-fig/log.git" {
		t.Errorf("source = %q, %v", repo, err)
	}
	// Packagist named explicitly is the same lookup.
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "psr/log", Source: "https://repo.packagist.org"}, ""); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "nope/nope"}, ""); err == nil || err.Error() != "package nope/nope not found" {
		t.Errorf("missing: %v", err)
	}
	if _, err := s.Digest(t.Context(), d, "3.0.2"); err == nil {
		t.Error("digest")
	}
}

func TestComposerRepositories(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, map[string]string{"COMPOSER_AUTH": `{"http-basic":{"127.0.0.1":{"username":"bot","password":"s3cret"}}}`})
	drupal := srv.URL + "/8"
	// drupal/* is on packages.drupal.org; psr/log does not match its patterns and falls through.
	d := dep.Dep{Name: "drupal/token", Source: drupal + " https://packagist.org"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); len(got) != 24 || got[0] != "1.18.0" || got[23] != "1.0.0-alpha1" {
		t.Errorf("versions = %v", got)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "1.18.0"); err != nil || !tm.IsZero() {
		t.Errorf("time = %v, %v", tm, err)
	}
	if repo, _ := s.SourceRepo(t.Context(), d, ""); repo != "https://git.drupalcode.org/project/token.git" {
		t.Errorf("source = %q", repo)
	}
	before := hits.Load()
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "psr/log", Source: drupal + " https://packagist.org"}, ""); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load() - before; n != 1 {
		t.Errorf("psr/log requests = %d, want 1 (packages.json read once, no drupal lookup)", n)
	}

	// metadata-url relative to the repository, credentials from COMPOSER_AUTH, minified
	// entries inheriting the previous time.
	priv := dep.Dep{Name: "acme/lib", Source: srv.URL + "/private/"}
	rels, err = s.Releases(t.Context(), priv, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); !slices.Equal(got, []string{"2.0.0", "1.1.0", "1.0.0"}) {
		t.Errorf("versions = %v", got)
	}
	if tm, _ := s.ReleaseTime(t.Context(), priv, "1.1.0"); !tm.Equal(time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)) {
		t.Errorf("inherited time = %v", tm)
	}
	if tm, _ := s.ReleaseTime(t.Context(), priv, "1.0.0"); !tm.IsZero() {
		t.Errorf("unset time = %v", tm)
	}
	if repo, _ := s.SourceRepo(t.Context(), priv, ""); repo != "" {
		t.Errorf("ssh source = %q", repo)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "acme/unlisted", Source: srv.URL + "/private"}, ""); err == nil {
		t.Error("a package outside available-packages")
	}

	// Satis: inline packages plus includes, merged; bearer tokens.
	b := newSource(srv, map[string]string{"COMPOSER_AUTH": `{"bearer":{"127.0.0.1":"tok"}}`})
	rels, err = b.Releases(t.Context(), dep.Dep{Name: "acme/tool", Source: srv.URL + "/bearer"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); !slices.Equal(got, []string{"1.0.0", "1.1.0"}) {
		t.Errorf("satis versions = %v", got)
	}
	if rels, err := b.Releases(t.Context(), dep.Dep{Name: "acme/direct", Source: srv.URL + "/direct.json"}, ""); err != nil || len(rels) != 1 {
		t.Errorf("packages.json URL: %v, %v", rels, err)
	}
}

func TestErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, map[string]string{"COMPOSER_AUTH": "{"})
	for _, tt := range []struct {
		d    dep.Dep
		want string
	}{
		{dep.Dep{Name: "a/b", Source: srv.URL + "/v1"}, "only the Composer 1 providers API"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/broken"}, "malformed response"},
		{dep.Dep{Name: "broken/pkg"}, "malformed response"},
		{dep.Dep{Name: "failing/pkg"}, "HTTP 500"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/missing"}, "no packages.json"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badmeta"}, "invalid metadata-url"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badinclude"}, "invalid include"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badinline"}, "malformed packages"},
		{dep.Dep{Name: "a/b", Source: "ftp://x"}, "invalid composer repository URL"},
		{dep.Dep{Name: "acme/lib", Source: srv.URL + "/private"}, "HTTP 401"}, // COMPOSER_AUTH unreadable
		{dep.Dep{Name: "a/b", Source: "http://user:pw@127.0.0.1:1"}, "packagist http://user:xxxxx@127.0.0.1:1/packages.json"},
	} {
		_, err := s.Releases(t.Context(), tt.d, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%+v: %v, want %q", tt.d, err, tt.want)
		}
		if _, err := s.ReleaseTime(t.Context(), tt.d, "1.0.0"); err == nil {
			t.Errorf("%+v: ReleaseTime without error", tt.d)
		}
		if _, err := s.SourceRepo(t.Context(), tt.d, "1.0.0"); err == nil {
			t.Errorf("%+v: SourceRepo without error", tt.d)
		}
	}
	if err := s.get(t.Context(), "http://[::1", nil); err == nil {
		t.Error("invalid URL")
	}
}

func TestReadPackages(t *testing.T) {
	for _, in := range []string{`[]`, `{"packages":{"a/b":[1]}}`, `{"packages":{"a/b":"x"}}`, `{"packages":{"a/b":[`, `{`} {
		if err := addInline(map[string]*pkg{}, strings.NewReader(in)); err == nil {
			t.Errorf("%s: no error", in)
		}
	}
	for v, want := range map[string]bool{"dev-main": true, "2.x-dev": true, "1.0.0-dev": true, "1.0.0": false, "v1.0.0-RC1": false} {
		if branch(v) != want {
			t.Errorf("branch(%s) != %v", v, want)
		}
	}
	if sourceURL([]byte(`{"url":1}`)) != "" || sourceURL([]byte(`"__unset"`)) != "" {
		t.Error("sourceURL")
	}
	if isDefault("https://user@packagist.org") || isDefault("https://packagist.org/sub") || !isDefault("http://www.packagist.org/") {
		t.Error("isDefault")
	}
}
