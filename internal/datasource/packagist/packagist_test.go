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
// (not minified, without release times), all fetched 2026-11-05. The Composer 1 repository
// asset-packagist.org (fetched 2026-11-09): testdata/asset-packagist-packages.json is its
// packages.json and testdata/asset-packagist-left-pad.json the provider file of
// npm-asset/left-pad verbatim, testdata/asset-packagist-provider-latest.json its provider
// listing (p/provider-latest/<sha256>.json, 21 MB) cut down to three entries.
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
	assetIndex, assetListing, leftPad := read("asset-packagist-packages.json"), read("asset-packagist-provider-latest.json"), read("asset-packagist-left-pad.json")
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
		case "/asset/packages.json":
			w.Write(assetIndex)
		case "/asset/p/provider-latest/94c8cdde70cbe4bf56d73b75bf2fffe24b0d0446d12166898c7ba5cb21e33eaf.json":
			w.Write(assetListing)
		case "/p/npm-asset/left-pad/703d74ebe5793865dcdbb77ef3f66c908fdb4942e49b8285b7236ab5f653309c.json":
			w.Write(leftPad)
		case "/v1/packages.json":
			io.WriteString(w, `{"packages":[],"providers-url":"/v1/p/%package%$%hash%.json","provider-includes":{"p/missing$%hash%.json":{"sha256":"def"}}}`)
		case "/v1ok/packages.json":
			io.WriteString(w, `{"packages":[],"providers-url":"/v1/p/%package%$%hash%.json","providers-lazy-url":"../v1/p/%package%.json",
				"providers":{"acme/hashed":{"sha256":"abc"}}}`)
		case "/v1/p/acme/hashed$abc.json":
			io.WriteString(w, `{"packages":{"acme/hashed":{"1.0.0":{"version":"1.0.0"},"1.1.0":{"version":"v1.1.0"}}}}`)
		case "/v1/p/acme/lazy.json":
			io.WriteString(w, `{"packages":{"acme/lazy":{"2.0.0":{"version":"2.0.0"}}}}`)
		case "/v1lazy/packages.json":
			io.WriteString(w, `{"providers-lazy-url":"/v1/p/%package%.json"}`)
		case "/badprov/packages.json":
			io.WriteString(w, `{"providers-url":"http://[::1/%package%"}`)
		case "/badlazy/packages.json":
			io.WriteString(w, `{"providers-lazy-url":"http://[::1/%package%"}`)
		case "/badlisting/packages.json":
			io.WriteString(w, `{"providers-url":"/p/%package%$%hash%.json","provider-includes":{"p/l$%hash%.json":{"sha256":"x"}}}`)
		case "/badlisting/p/l$x.json":
			io.WriteString(w, `{"providers":[]}`)
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

// TestComposer1Repositories: a repository serving only the Composer 1 API (asset-packagist.org)
// is read through its provider listings; a package in packages.json `providers` needs no
// listing, and providers-lazy-url serves the packages no listing names.
func TestComposer1Repositories(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	asset := srv.URL + "/asset"
	d := dep.Dep{Name: "npm-asset/left-pad", Source: asset + " https://packagist.org"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); len(got) != 15 || !slices.Contains(got, "1.3.0") || !slices.Contains(got, "0.0.0") {
		t.Errorf("versions = %v", got)
	}
	if n := hits.Load(); n != 3 {
		t.Errorf("requests = %d, want 3 (packages.json, listing, provider file)", n)
	}
	// The listing is read once; psr/log is outside the available-package-patterns.
	before := hits.Load()
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "psr/log", Source: asset + " https://packagist.org"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "npm-asset/unlisted", Source: asset}, ""); err == nil || err.Error() != "package npm-asset/unlisted not found" {
		t.Errorf("unlisted: %v", err)
	}
	if n := hits.Load() - before; n != 1 {
		t.Errorf("requests = %d, want 1 (psr/log on Packagist)", n)
	}

	// packages.json `providers` without a listing; providers-lazy-url for what it lacks.
	v1 := newSource(srv, nil)
	if rels, err := v1.Releases(t.Context(), dep.Dep{Name: "Acme/Hashed", Source: srv.URL + "/v1ok"}, ""); err != nil || !slices.Equal(versionsOf(rels), []string{"1.0.0", "v1.1.0"}) {
		t.Errorf("hashed = %v, %v", versionsOf(rels), err)
	}
	if rels, err := v1.Releases(t.Context(), dep.Dep{Name: "acme/lazy", Source: srv.URL + "/v1ok"}, ""); err != nil || !slices.Equal(versionsOf(rels), []string{"2.0.0"}) {
		t.Errorf("lazy fallback = %v, %v", versionsOf(rels), err)
	}
	lazy := newSource(srv, nil)
	if rels, err := lazy.Releases(t.Context(), dep.Dep{Name: "acme/lazy", Source: srv.URL + "/v1lazy"}, ""); err != nil || !slices.Equal(versionsOf(rels), []string{"2.0.0"}) {
		t.Errorf("lazy = %v, %v", rels, err)
	}
	if _, err := lazy.Releases(t.Context(), dep.Dep{Name: "acme/none", Source: srv.URL + "/v1lazy"}, ""); err == nil {
		t.Error("lazy provider file missing")
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
		{dep.Dep{Name: "a/b", Source: srv.URL + "/v1"}, "/v1/p/missing$def.json: provider listing not found"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badprov"}, "invalid providers-url"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badlazy"}, "invalid providers-lazy-url"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badlisting"}, "JSON array into Go map[string]packagist.providerHash"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/broken"}, "JSON array into Go packagist.packagesJSON"},
		{dep.Dep{Name: "broken/pkg"}, "not an object"},
		{dep.Dep{Name: "failing/pkg"}, "HTTP 500"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/missing"}, "no packages.json"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badmeta"}, "invalid metadata-url"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badinclude"}, "invalid include"},
		{dep.Dep{Name: "a/b", Source: srv.URL + "/badinline"}, "malformed packages"},
		{dep.Dep{Name: "a/b", Source: "ftp://x"}, "invalid composer repository URL"},
		{dep.Dep{Name: "acme/lib", Source: srv.URL + "/private"}, "HTTP 401"}, // COMPOSER_AUTH unreadable
		{dep.Dep{Name: "a/b", Source: "http://user:pw@127.0.0.1:1"}, `Get "http://user:***@127.0.0.1:1/packages.json"`},
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
