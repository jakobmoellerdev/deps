package nuget

import (
	"bytes"
	"compress/gzip"
	"errors"
	"fmt"
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

// testdata holds nuget.org responses (fetched 2026-11-05): index.json is
// https://api.nuget.org/v3/index.json verbatim; newtonsoft.json.index.json and
// newtonsoft.json.page2.json are https://api.nuget.org/v3/registration5-gz-semver2/newtonsoft.json/index.json
// cut to nine versions (dependency groups and vulnerabilities dropped), with its second page
// moved out of line as nuget.org serves large packages; 13.0.1-beta1 is unlisted there.
// newtonsoft.json.13.0.3.nuspec is the flat container's nuspec verbatim.
func serve(t *testing.T, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	files := map[string][]byte{}
	for _, f := range []string{"index.json", "newtonsoft.json.index.json", "newtonsoft.json.page2.json", "newtonsoft.json.13.0.3.nuspec"} {
		b, err := os.ReadFile("testdata/" + f)
		if err != nil {
			t.Fatal(err)
		}
		files[f] = b
	}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		local := func(name string) []byte {
			return bytes.ReplaceAll(files[name], []byte("https://api.nuget.org"), []byte(srv.URL))
		}
		switch r.URL.Path {
		case "/v3/index.json":
			w.Write(local("index.json"))
		case "/v3/registration5-gz-semver2/newtonsoft.json/index.json":
			// registration5-gz is gzip-encoded whether or not the client asked for it.
			w.Header().Set("Content-Encoding", "gzip")
			zw := gzip.NewWriter(w)
			zw.Write(local("newtonsoft.json.index.json"))
			zw.Close()
		case "/v3/registration5-gz-semver2/newtonsoft.json/page/12.0.1/14.0.1-beta2.json":
			w.Write(local("newtonsoft.json.page2.json"))
		case "/v3-flatcontainer/newtonsoft.json/13.0.3/newtonsoft.json.nuspec":
			w.Write(files["newtonsoft.json.13.0.3.nuspec"])
		case "/private/index.json":
			if u, p, ok := r.BasicAuth(); !ok || u != "bot" || p != "s3cret" {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			fmt.Fprintf(w, `{"version":"3.0.0","resources":[{"@id":"%s/private/flat","@type":"PackageBaseAddress/3.0.0"}]}`, srv.URL)
		case "/private/flat/internal.lib/index.json":
			if _, _, ok := r.BasicAuth(); !ok {
				http.Error(w, "auth", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"versions":["1.0.0","1.1.0+sha.abc","13.0.5"]}`)
		case "/private/flat/internal.lib/1.1.0/internal.lib.nuspec":
			http.NotFound(w, r)
		case "/empty/index.json":
			fmt.Fprint(w, `{"version":"3.0.0","resources":[{"@id":"x","@type":"SearchQueryService"}]}`)
		case "/broken/index.json":
			fmt.Fprint(w, `[1`)
		case "/failing/index.json":
			http.Error(w, "down", http.StatusInternalServerError)
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
	env["DEPS_NUGET_URL"] = srv.URL + "/v3/index.json"
	return New(srv.Client(), nil, func(k string) string { return env[k] })
}

func versionsOf(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func TestReleasesNuGetOrg(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	d := dep.Dep{Name: "Newtonsoft.Json"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"11.0.2", "12.0.1", "12.0.3", "13.0.1", "13.0.2", "13.0.3", "13.0.4", "14.0.1-beta2"} // 13.0.1-beta1 is unlisted
	if got := versionsOf(rels); !slices.Equal(got, want) {
		t.Errorf("releases = %v, want %v", got, want)
	}
	tm, err := s.ReleaseTime(t.Context(), d, "13.0.3")
	if err != nil || !tm.Equal(time.Date(2023, 3, 8, 7, 42, 54, 647e6, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	repo, err := s.SourceRepo(t.Context(), d, "13.0.3")
	if err != nil || repo != "https://github.com/JamesNK/Newtonsoft.Json" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	// One service index, the registration index and its out-of-line page, the nuspec; the
	// second dep with another spelling of the id shares them.
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "newtonsoft.json", Source: srv.URL + "/v3/index.json"}, ""); err != nil {
		t.Fatal(err)
	}
	if n := hits.Load(); n != 4 {
		t.Errorf("hits = %d, want 4", n)
	}
	if _, err := s.Digest(t.Context(), d, "13.0.3"); err == nil {
		t.Error("Digest: no error")
	}
}

func TestReleasesFeeds(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	env := map[string]string{CredentialsEnv + "my feed": `"Username=bot;Password=s3cret"`}
	s := newSource(srv, env)
	// The private feed (flat container only) and nuget.org are merged, private first.
	d := dep.Dep{Name: "Internal.Lib", Source: "my+feed=" + srv.URL + "/private/index.json " + srv.URL + "/v3/index.json"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil {
		t.Fatal(err)
	}
	if got := versionsOf(rels); !slices.Equal(got, []string{"1.0.0", "1.1.0", "13.0.5"}) {
		t.Errorf("releases = %v", got)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "1.1.0"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if repo, err := s.SourceRepo(t.Context(), d, "1.1.0"); err != nil || repo != "" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	both := dep.Dep{Name: "Newtonsoft.Json", Source: d.Source}
	if rels, err := s.Releases(t.Context(), both, ""); err != nil || len(rels) != 8 {
		t.Errorf("merged = %v, %v", versionsOf(rels), err)
	}

	// Without credentials the private feed fails.
	_, err = New(srv.Client(), nil, func(string) string { return "" }).Releases(t.Context(), d, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 401") {
		t.Errorf("no credentials: %v", err)
	}
}

func TestReleasesErrors(t *testing.T) {
	var hits atomic.Int32
	srv := serve(t, &hits)
	s := newSource(srv, nil)
	for _, tt := range []struct{ source, name, want string }{
		{"", "missing", "package missing not found"},
		{srv.URL + "/empty/index.json", "x", "has no registration or package base address"},
		{srv.URL + "/broken/index.json", "x", "malformed response"},
		{srv.URL + "/failing/index.json", "x", "HTTP 500"},
		{srv.URL + "/gone/index.json", "x", "service index not found"},
		{"https://user:pw@[bad/index.json", "x", "invalid URL"},
	} {
		_, err := s.Releases(t.Context(), dep.Dep{Name: tt.name, Source: tt.source}, "")
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s %s: err = %v, want %q", tt.source, tt.name, err, tt.want)
		}
		if err != nil && strings.Contains(err.Error(), "pw") {
			t.Errorf("password leaked: %v", err)
		}
	}
	_, err := s.Releases(t.Context(), dep.Dep{Name: "x", Source: "PSGallery=https://www.powershellgallery.com/api/v2"}, "")
	if skip, ok := errors.AsType[*dep.SkipError](err); !ok || !strings.Contains(skip.Reason, "v2 feed") {
		t.Errorf("v2 feed: %v", err)
	}
	if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "missing"}, "1.0"); err == nil {
		t.Error("ReleaseTime: no error")
	}
	if _, err := s.SourceRepo(t.Context(), dep.Dep{Name: "missing"}, "1.0"); err == nil {
		t.Error("SourceRepo: no error")
	}
}

func TestCredentialsAndForge(t *testing.T) {
	for in, want := range map[string][2]string{
		"Username=u;Password=p": {"u", "p"}, `"username=u; password=p=q"`: {"u", "p=q"}, "Password=t": {"", "t"},
	} {
		if u, p, ok := credentials(in); !ok || u != want[0] || p != want[1] {
			t.Errorf("credentials(%q) = %q %q %v", in, u, p, ok)
		}
	}
	if _, _, ok := credentials("Username=u"); ok {
		t.Error("credentials without password")
	}
	for raw, want := range map[string]bool{
		"https://github.com/o/r": true, "http://gitlab.com/g/s/p": true, "https://github.com/o": false,
		"https://www.newtonsoft.com/json": false, "ftp://github.com/o/r": false, "%": false,
	} {
		if got := onForge(raw); got != want {
			t.Errorf("onForge(%q) = %v", raw, got)
		}
	}
	if redact("%") != "<invalid URL>" || redact("https://u:p@h/x") != "https://u:xxxxx@h/x" {
		t.Error("redact")
	}
}
