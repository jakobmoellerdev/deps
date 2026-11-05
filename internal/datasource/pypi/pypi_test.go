package pypi

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/pyreq"
)

// The fixtures in testdata are real responses fetched on 2026-11-04 and trimmed with jq to a
// few releases (and files):
//   - requests.json:              curl -s https://pypi.org/pypi/requests/json
//     (releases 0.0.1 without files, 2.31.0, 2.32.0 and 2.32.1 yanked, 2.32.2, 2.34.2)
//   - requests-simple.json:       curl -s -H 'Accept: application/vnd.pypi.simple.v1+json' https://pypi.org/simple/requests/
//   - zope.interface-simple.json: curl -s -H 'Accept: application/vnd.pypi.simple.v1+json' https://pypi.org/simple/zope-interface/
//     (eggs and sdists of 3.3.0b2 and 3.6.1, yanked wheels of 5.5.0.dev0, wheels and sdist of 8.7)
//   - requests-simple.html:       curl -s -H 'Accept: text/html' https://pypi.org/simple/requests/
//     (the anchors of 2.31.0, the yanked 2.32.0, 2.32.2 and 2.34.2)
//   - torch-simple.html:          curl -s https://download.pytorch.org/whl/cpu/torch/ (HTML only;
//     two files each of 2.15.0+cpu, 2.15.0, 2.4.0+cpu and 2.4.0)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// index serves body (with content type ct) for path, HTTP 500 for /simple/server-error/,
// text/plain for /simple/text/ and 404 otherwise, counting requests and recording the Accept
// header and basic auth of the last one.
type index struct {
	srv          *httptest.Server
	calls        atomic.Int32
	mu           sync.Mutex
	accept, auth string
}

func newIndex(t *testing.T, path, ct string, body []byte) *index {
	t.Helper()
	ix := &index{}
	ix.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ix.calls.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			t.Errorf("User-Agent = %q", r.Header.Get("User-Agent"))
		}
		ix.mu.Lock()
		ix.accept = r.Header.Get("Accept")
		if u, p, ok := r.BasicAuth(); ok {
			ix.auth = u + ":" + p
		}
		ix.mu.Unlock()
		switch r.URL.Path {
		case "/simple/server-error/":
			w.WriteHeader(http.StatusInternalServerError)
			return
		case "/simple/text/":
			w.Header().Set("Content-Type", "text/plain")
			_, _ = w.Write([]byte("hello"))
			return
		}
		if r.URL.Path != path {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(body)
	}))
	t.Cleanup(ix.srv.Close)
	return ix
}

// last returns the Accept header and basic auth of the last request.
func (ix *index) last() (accept, auth string) {
	ix.mu.Lock()
	defer ix.mu.Unlock()
	return ix.accept, ix.auth
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNormalizedIs(t *testing.T) {
	for _, name := range []string{"Django", "zope.interface", "typing_extensions", "A--b__c..d", "-a_", "a-", "", "-", "Foo.Bar-1"} {
		norm := pyreq.Normalize(name)
		if !normalizedIs(name, norm) {
			t.Errorf("normalizedIs(%q, %q) = false", name, norm)
		}
		for _, other := range []string{norm + "x", norm + "-", "x" + norm, strings.TrimSuffix(norm, "o")} {
			if other != norm && normalizedIs(name, other) {
				t.Errorf("normalizedIs(%q, %q) = true", name, other)
			}
		}
	}
}

func TestJSONAPI(t *testing.T) {
	ix := newIndex(t, "/pypi/requests/json", "application/json", fixture(t, "requests.json"))
	s := New(ix.srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_PYPI_URL": ix.srv.URL + "/"}))
	ctx := t.Context()
	var wg sync.WaitGroup
	for i := range 4 {
		// The PyPI simple index URL is served by the JSON API too, deduped with the default.
		d := dep.Dep{Name: []string{"requests", "Requests", "REQUESTS", "requests"}[i]}
		if i == 3 {
			d.Source = "https://pypi.org/simple/"
		}
		wg.Go(func() {
			rels, err := s.Releases(ctx, d, "")
			if err != nil {
				t.Error(err)
				return
			}
			if got := versions(rels); !slices.Equal(got, []string{"2.31.0", "2.32.2", "2.34.2"}) {
				t.Errorf("versions = %v, want the yanked 2.32.0/2.32.1 and the empty 0.0.1 dropped", got)
			}
		})
	}
	wg.Wait()
	d := dep.Dep{Name: "requests"}
	for v, want := range map[string]string{"2.31.0": "2023-05-22T15:12:42.313790Z", "2.34.2": "2026-05-14T19:25:26.443Z"} {
		if got, err := s.ReleaseTime(ctx, d, v); err != nil || !got.Equal(mustTime(t, want)) {
			t.Errorf("ReleaseTime(%s) = %v, %v; want %s", v, got, err, want)
		}
	}
	if got, err := s.ReleaseTime(ctx, d, "9.9.9"); err != nil || !got.IsZero() {
		t.Errorf("ReleaseTime(unknown) = %v, %v", got, err)
	}
	if got, err := s.SourceRepo(ctx, d, "2.31.0"); err != nil || got != "https://github.com/psf/requests" {
		t.Errorf("SourceRepo = %q, %v", got, err)
	}
	for v, want := range map[string]string{"2.32.0": "version 2.32.0 yanked", "2.31.0": ""} {
		if got := s.Deprecation(d, v); got != want {
			t.Errorf("Deprecation(%s) = %q, want %q", v, got, want)
		}
	}
	if n := ix.calls.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
	if accept, _ := ix.last(); accept != "application/json" {
		t.Errorf("Accept = %q", accept)
	}
	if _, err := s.Digest(ctx, d, "2.31.0"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestDefaultURL(t *testing.T) {
	if s := New(http.DefaultClient, nil, env(nil)); s.base != DefaultURL {
		t.Errorf("base = %q", s.base)
	}
}

func TestJSONAPIEdgeCases(t *testing.T) {
	body := `{"info": null, "releases": {
		"1.0": [{"yanked": true, "upload_time_iso_8601": "2020-01-02T00:00:00Z"}, {"yanked": false, "upload_time_iso_8601": "2020-01-01T00:00:00Z"}],
		"1.1": [{"yanked": false, "upload_time_iso_8601": "garbage"}],
		"1.2": [{"yanked": false}],
		"Nightly": [{"yanked": false, "upload_time_iso_8601": "2021-01-01T00:00:00Z"}],
		"2.0": []}, "urls": []}`
	ix := newIndex(t, "/pypi/x/json", "application/json", []byte(body))
	s := New(ix.srv.Client(), nil, env(map[string]string{"DEPS_PYPI_URL": ix.srv.URL}))
	d := dep.Dep{Name: "x"}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil || !slices.Equal(versions(rels), []string{"1.0", "1.1", "1.2", "Nightly"}) {
		t.Fatalf("Releases = %v, %v; want the partly yanked 1.0 kept", versions(rels), err)
	}
	if got, _ := s.ReleaseTime(t.Context(), d, "1.0"); !got.Equal(mustTime(t, "2020-01-01T00:00:00Z")) {
		t.Errorf("ReleaseTime(1.0) = %v, want the earliest file", got)
	}
	// Versions are matched in PEP 440 normal form.
	if got, _ := s.ReleaseTime(t.Context(), d, "V1.0"); got.IsZero() {
		t.Error("ReleaseTime(V1.0) is zero")
	}
	// Other versions case-insensitively.
	if got, _ := s.ReleaseTime(t.Context(), d, "nightly"); !got.Equal(mustTime(t, "2021-01-01T00:00:00Z")) {
		t.Errorf("ReleaseTime(nightly) = %v", got)
	}
	for _, v := range []string{"1.1", "1.2"} {
		if got, err := s.ReleaseTime(t.Context(), d, v); err != nil || !got.IsZero() {
			t.Errorf("ReleaseTime(%s) = %v, %v", v, got, err)
		}
	}
	if got, err := s.SourceRepo(t.Context(), d, ""); err != nil || got != "" {
		t.Errorf("SourceRepo = %q, %v", got, err)
	}
}

func TestSourceRepo(t *testing.T) {
	for _, c := range []struct {
		name string
		urls map[string]string
		home string
		want string
	}{
		{"source any host", map[string]string{"Source": "https://git.example.com/o/r", "Homepage": "https://github.com/o/home"}, "", "https://git.example.com/o/r"},
		{"source code label", map[string]string{"source-code": " https://hg.example.org/r ", "Documentation": "https://docs"}, "", "https://hg.example.org/r"},
		{"repository", map[string]string{"Repository": "https://example.org/r", "Code": "https://github.com/o/code"}, "", "https://example.org/r"},
		{"code on forge", map[string]string{"Code": "https://gitlab.com/o/r", "Homepage": "https://github.com/o/home"}, "", "https://gitlab.com/o/r"},
		{"code elsewhere", map[string]string{"Code": "https://example.org/r", "GitHub": "https://www.github.com/o/r"}, "", "https://www.github.com/o/r"},
		{"homepage on forge", map[string]string{"Home_Page": "https://codeberg.org/o/r"}, "https://github.com/o/other", "https://codeberg.org/o/r"},
		{"homepage elsewhere", map[string]string{"Homepage": "https://example.org"}, "https://github.com/o/r", "https://github.com/o/r"},
		{"home page elsewhere", nil, "https://example.org/o/r", ""},
		{"not http", map[string]string{"Homepage": "git@github.com:o/r"}, "%zz", ""},
		{"duplicate labels sorted", map[string]string{"source": "https://b.example/r", "Source": "https://a.example/r"}, "", "https://a.example/r"},
	} {
		if got := sourceRepo(c.urls, c.home); got != c.want {
			t.Errorf("%s: sourceRepo = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestSimpleAPI(t *testing.T) {
	for _, c := range []struct {
		name, fixture, path string
		want                []string
		times               map[string]string
	}{
		{"requests", "requests-simple.json", "/simple/requests/", []string{"2.31.0", "2.32.2", "2.34.2"},
			map[string]string{"2.31.0": "2023-05-22T15:12:42.313790Z"}},
		{"zope.interface", "zope.interface-simple.json", "/simple/zope-interface/", []string{"3.3.0b2", "3.6.1", "8.7"},
			map[string]string{"3.3.0b2": "2007-01-04T17:10:17Z", "3.6.1": "2010-05-03T15:13:11.689399Z", "8.7": "2026-10-15T07:23:45.197215Z", "5.5.0.dev0": ""}},
	} {
		for _, via := range []string{"source", "PIP_INDEX_URL"} {
			t.Run(c.name+" via "+via, func(t *testing.T) {
				ix := newIndex(t, c.path, simpleJSON+"; charset=utf-8", fixture(t, c.fixture))
				d := dep.Dep{Name: c.name}
				e := map[string]string{"DEPS_PYPI_URL": "http://127.0.0.1:1"}
				if via == "source" {
					d.Source = ix.srv.URL + "/simple"
					e["PIP_INDEX_URL"] = "http://127.0.0.1:1/ignored"
				} else {
					e["PIP_INDEX_URL"] = ix.srv.URL + "/simple/"
				}
				s := New(ix.srv.Client(), nil, env(e))
				rels, err := s.Releases(t.Context(), d, "")
				if err != nil {
					t.Fatal(err)
				}
				if got := versions(rels); !slices.Equal(got, c.want) {
					t.Errorf("versions = %v, want %v", got, c.want)
				}
				for v, want := range c.times {
					got, err := s.ReleaseTime(t.Context(), d, v)
					if err != nil || (want == "" && !got.IsZero()) || (want != "" && !got.Equal(mustTime(t, want))) {
						t.Errorf("ReleaseTime(%s) = %v, %v; want %q", v, got, err, want)
					}
				}
				if repo, err := s.SourceRepo(t.Context(), d, ""); err != nil || repo != "" {
					t.Errorf("SourceRepo = %q, %v", repo, err)
				}
				if accept, _ := ix.last(); accept != simpleAccept || ix.calls.Load() != 1 {
					t.Errorf("Accept = %q, calls = %d", accept, ix.calls.Load())
				}
			})
		}
	}
}

func TestSimpleAPIWithoutVersionsList(t *testing.T) {
	// api-version 1.0: no PEP 700 `versions`; the filenames' versions in order of appearance.
	body := `{"meta": {"api-version": "1.0"}, "name": "my-pkg", "files": [
		{"filename": "my_pkg-1.0-py3-none-any.whl", "yanked": false, "upload-time": "2020-01-02T00:00:00Z"},
		{"filename": "My.Pkg-1.0.tar.gz", "yanked": "broken", "upload-time": "2020-01-01T00:00:00Z"},
		{"filename": "my_pkg-1.1-py3-none-any.whl", "yanked": true},
		{"filename": "my_pkg-1.1.tar.gz", "yanked": ""},
		{"filename": "my-pkg-1.2.zip"},
		{"filename": "my_pkg-1.2.0-1-py3-none-any.whl", "upload-time": "bad"},
		{"filename": "my-pkg-1.3.win32.exe"},
		{"filename": "README"}
	]}`
	ix := newIndex(t, "/my-pkg/", simpleJSON, []byte(body))
	s := New(ix.srv.Client(), nil, env(nil))
	d := dep.Dep{Name: "My_Pkg", Source: ix.srv.URL}
	rels, err := s.Releases(t.Context(), d, "")
	if err != nil || !slices.Equal(versions(rels), []string{"1.0", "1.2", "1.2.0"}) {
		t.Fatalf("Releases = %v, %v", versions(rels), err)
	}
	if got, _ := s.ReleaseTime(t.Context(), d, "1.0"); !got.Equal(mustTime(t, "2020-01-01T00:00:00Z")) {
		t.Errorf("ReleaseTime(1.0) = %v, want the earliest file, yanked or not", got)
	}
	if got, _ := s.ReleaseTime(t.Context(), d, "1.2"); !got.IsZero() {
		t.Errorf("ReleaseTime(1.2) = %v", got)
	}
}

func TestFileVersion(t *testing.T) {
	for _, c := range []struct {
		file, name, want string
	}{
		{"requests-2.31.0-py3-none-any.whl", "requests", "2.31.0"},
		{"zope_interface-8.7-cp311-cp311-win_amd64.whl", "zope-interface", "8.7"},
		{"pkg-1.0-1build-py3-none-any.whl", "pkg", "1.0"},
		{"PKG-1.0-py3-none-any.WHL", "pkg", "1.0"},
		{"zope.interface-3.6.1-py2.6-win-amd64.egg", "zope-interface", "3.6.1"},
		{"setuptools-0.6c11-py2.7.egg", "setuptools", "0.6c11"},
		{"pkg-1.0.egg", "pkg", "1.0"},
		{"python-dateutil-2.8.2.tar.gz", "python-dateutil", "2.8.2"},
		{"python_dateutil-2.9.0.post0.tar.gz", "python-dateutil", "2.9.0.post0"},
		{"zope.interface-3.6.1.tar.gz", "zope-interface", "3.6.1"},
		{"Django-1.0.tar.bz2", "django", "1.0"},
		{"pkg-1.0-1.tgz", "pkg", "1.0-1"},
		{"pkg-1.0.zip", "pkg", "1.0"},
		{"pkg-1.0.tar", "pkg", "1.0"},
		{"pkg-1.0.tar.xz", "pkg", "1.0"},
		{"renamed-2.0.tar.gz", "pkg", "2.0"},
		{"pkg-.tar.gz", "pkg", ""},
		{"nodash.tar.gz", "pkg", ""},
		{"pkg-.whl", "pkg", ""},
		{"pkg.whl", "pkg", ""},
		{"pkg-1.0.win32.exe", "pkg", ""},
		{"pkg-1.0.msi", "pkg", ""},
	} {
		got, ok := fileVersion(c.file, c.name)
		if got != c.want || ok != (c.want != "") {
			t.Errorf("fileVersion(%q, %q) = %q, %v; want %q", c.file, c.name, got, ok, c.want)
		}
	}
}

func TestErrors(t *testing.T) {
	for _, c := range []struct {
		name, ct, body string
		status         int
		simple         bool
		want           string
	}{
		{"missing", "", "", http.StatusNotFound, false, "package missing not found"},
		{"missing simple", "", "", http.StatusNotFound, true, "package missing simple not found"},
		{"server", "", "", http.StatusInternalServerError, false, "/pypi/server/json: HTTP 500"},
		{"plain text", "text/plain", "x", 200, true, `/simple/plain%20text/: unsupported content type "text/plain"`},
		{"no content type", "", "{}", 200, true, `unsupported content type ""`},
		{"not acceptable", "", "", http.StatusNotAcceptable, true, "/simple/ serves neither the JSON nor the HTML simple API"},
		{"array", "application/json", "[]", 200, false, "malformed response: expected {"},
		{"releases array", "application/json", `{"releases": []}`, 200, false, "malformed response: expected {"},
		{"files object", "application/json", `{"releases": {"1.0": {}}}`, 200, false, "malformed response: expected ["},
		{"bad file", "application/json", `{"releases": {"1.0": [5]}}`, 200, false, "malformed response"},
		{"truncated", "application/json", `{"releases": {"1.0": [`, 200, false, "malformed response"},
		{"truncated releases", "application/json", `{"releases": {`, 200, false, "malformed response"},
		{"bad info", "application/json", `{"info": 5}`, 200, false, "malformed response"},
		{"bad other", "application/json", `{"urls": }`, 200, false, "malformed response"},
		{"empty", "application/json", ``, 200, false, "malformed response"},
		{"unterminated", "application/json", `{"urls": []`, 200, false, "malformed response"},
		{"simple array", simpleJSON, `[]`, 200, true, "malformed response: expected {"},
		{"simple files object", simpleJSON, `{"files": {}}`, 200, true, "malformed response: expected ["},
		{"simple bad file", simpleJSON, `{"files": [5]}`, 200, true, "malformed response"},
		{"simple truncated", simpleJSON, `{"files": [`, 200, true, "malformed response"},
		{"simple bad versions", simpleJSON, `{"versions": {}}`, 200, true, "malformed response"},
		{"simple bad other", simpleJSON, `{"meta": }`, 200, true, "malformed response"},
		{"simple unterminated", simpleJSON, `{"meta": {}`, 200, true, "malformed response"},
		{"simple bad key", simpleJSON, `{5: 1}`, 200, true, "malformed response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if c.ct != "" {
					w.Header().Set("Content-Type", c.ct)
				} else {
					w.Header()["Content-Type"] = nil // no sniffing
				}
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			}))
			defer srv.Close()
			d := dep.Dep{Name: c.name}
			if c.simple {
				d.Source = srv.URL + "/simple"
			}
			s := New(srv.Client(), nil, env(map[string]string{"DEPS_PYPI_URL": srv.URL}))
			_, err := s.Releases(t.Context(), d, "")
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
			// ReleaseTime and SourceRepo report the shared lookup's error.
			if _, err2 := s.ReleaseTime(t.Context(), d, "1.0"); err2 == nil || err2.Error() != err.Error() {
				t.Errorf("ReleaseTime err = %v", err2)
			}
			if _, err2 := s.SourceRepo(t.Context(), d, "1.0"); err2 == nil {
				t.Error("SourceRepo succeeded")
			}
		})
	}
}

func TestInvalidIndex(t *testing.T) {
	for _, idx := range []string{"ftp://host/simple", "/relative/simple", "http://user:s3cret@[::1/simple"} {
		s := New(http.DefaultClient, nil, env(map[string]string{"PIP_INDEX_URL": idx}))
		d := dep.Dep{Name: "x"}
		_, err := s.Releases(t.Context(), d, "")
		if err == nil || err.Error() != "pypi: invalid index URL for package x" {
			t.Errorf("%s: err = %v", idx, err)
		}
		if _, err := s.ReleaseTime(t.Context(), d, "1"); err == nil {
			t.Errorf("%s: ReleaseTime succeeded", idx)
		}
		if _, err := s.SourceRepo(t.Context(), d, "1"); err == nil {
			t.Errorf("%s: SourceRepo succeeded", idx)
		}
	}
	s := New(http.DefaultClient, nil, env(map[string]string{"DEPS_PYPI_URL": "http://u:s3cret@[::1"}))
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "x"}, ""); err == nil || !strings.HasPrefix(err.Error(), "pypi <invalid URL>: ") || strings.Contains(err.Error(), "s3cret") {
		t.Errorf("bad DEPS_PYPI_URL: err = %v", err)
	}
}

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

func TestBasicAuthRedacted(t *testing.T) {
	logs := &syncBuffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ix := newIndex(t, "/simple/requests/", simpleJSON, fixture(t, "requests-simple.json"))
	authURL := strings.Replace(ix.srv.URL, "http://", "http://user:s3cret@", 1) + "/simple/"
	s := New(ix.srv.Client(), nil, env(map[string]string{"PIP_INDEX_URL": authURL}))
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "requests"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, auth := ix.last(); auth != "user:s3cret" {
		t.Errorf("basic auth = %q, want the index URL's userinfo", auth)
	}
	for _, name := range []string{"server-error", "text"} {
		_, err := s.Releases(t.Context(), dep.Dep{Name: name}, "")
		if err == nil || strings.Contains(err.Error(), "s3cret") || !strings.Contains(err.Error(), "user:xxxxx@") {
			t.Errorf("%s: err = %v, want the password redacted", name, err)
		}
	}
	refused := New(http.DefaultClient, nil, env(map[string]string{"PIP_INDEX_URL": "http://user:s3cret@127.0.0.1:1/simple"}))
	if _, err := refused.Releases(t.Context(), dep.Dep{Name: "x"}, ""); err == nil || strings.Contains(err.Error(), "s3cret") ||
		!strings.HasPrefix(err.Error(), "pypi http://user:xxxxx@127.0.0.1:1/simple/x/: ") {
		t.Errorf("refused: err = %v", err)
	}
	if out := logs.String(); strings.Contains(out, "s3cret") || !strings.Contains(out, "pypi project fetched") {
		t.Errorf("logs leak the password or miss the fetch:\n%s", out)
	}
}

func TestSemaphore(t *testing.T) {
	var inFlight, peak atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		n := inFlight.Add(1)
		defer inFlight.Add(-1)
		for {
			p := peak.Load()
			if n <= p || peak.CompareAndSwap(p, n) {
				break
			}
		}
		time.Sleep(10 * time.Millisecond)
		_, _ = w.Write([]byte(`{"releases": {"1.0": [{"yanked": false}]}}`))
	}))
	defer srv.Close()
	s := New(srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_PYPI_URL": srv.URL}))
	var wg sync.WaitGroup
	for _, name := range []string{"a", "b", "c", "d"} {
		wg.Go(func() {
			if _, err := s.Releases(t.Context(), dep.Dep{Name: name}, ""); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if p := peak.Load(); p != 1 {
		t.Errorf("peak concurrent requests = %d, want 1", p)
	}
}

func TestCancellation(t *testing.T) {
	t.Run("waiting for a slot", func(t *testing.T) {
		for _, d := range []dep.Dep{{Name: "x"}, {Name: "x", Source: "http://127.0.0.1:1/simple"}} {
			sem := make(chan struct{}, 1)
			sem <- struct{}{}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			s := New(http.DefaultClient, sem, env(map[string]string{"DEPS_PYPI_URL": "http://127.0.0.1:1"}))
			if _, err := s.Releases(ctx, d, ""); !errors.Is(err, context.Canceled) {
				t.Errorf("%+v: err = %v", d, err)
			}
		}
	})
	t.Run("in flight", func(t *testing.T) {
		started := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
			close(started)
			<-r.Context().Done()
		}))
		defer srv.Close()
		s := New(srv.Client(), nil, env(map[string]string{"DEPS_PYPI_URL": srv.URL}))
		ctx, cancel := context.WithCancel(t.Context())
		go func() {
			<-started
			cancel()
		}()
		if _, err := s.Releases(ctx, dep.Dep{Name: "x"}, ""); !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	})
}

// server serves each path's content type and body, 404 otherwise, counting requests per path.
func server(t *testing.T, routes map[string][2]string) (*httptest.Server, *sync.Map) {
	t.Helper()
	var calls sync.Map
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n, _ := calls.LoadOrStore(r.URL.Path, new(atomic.Int32))
		n.(*atomic.Int32).Add(1)
		rt, ok := routes[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		if rt[0] == "500" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", rt[0])
		_, _ = w.Write([]byte(rt[1]))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func calls(m *sync.Map, path string) int32 {
	if n, ok := m.Load(path); ok {
		return n.(*atomic.Int32).Load()
	}
	return 0
}

func TestHTMLSimpleAPI(t *testing.T) {
	for _, c := range []struct {
		name, fixture, ct string
		want              []string
		times             map[string]string
	}{
		{"requests", "requests-simple.html", "text/html", []string{"2.31.0", "2.32.2", "2.34.2"}, map[string]string{"2.31.0": ""}},
		{"requests", "requests-simple.html", simpleHTML, []string{"2.31.0", "2.32.2", "2.34.2"}, nil},
		{"torch", "torch-simple.html", "text/html", []string{"2.15.0+cpu", "2.15.0", "2.4.0+cpu", "2.4.0"},
			map[string]string{"2.4.0": "2024-07-24T22:46:27Z", "2.4.0+cpu": "2024-07-24T22:45:20Z", "2.15.0": "2026-10-28T13:17:02Z"}},
	} {
		t.Run(c.name+" "+c.ct, func(t *testing.T) {
			ix := newIndex(t, "/whl/cpu/"+c.name+"/", c.ct, fixture(t, c.fixture))
			s := New(ix.srv.Client(), nil, env(nil))
			d := dep.Dep{Name: c.name, Source: ix.srv.URL + "/whl/cpu"}
			rels, err := s.Releases(t.Context(), d, "")
			if err != nil || !slices.Equal(versions(rels), c.want) {
				t.Fatalf("Releases = %v, %v; want %v", versions(rels), err, c.want)
			}
			for v, want := range c.times {
				got, err := s.ReleaseTime(t.Context(), d, v)
				if err != nil || (want == "" && !got.IsZero()) || (want != "" && !got.Equal(mustTime(t, want))) {
					t.Errorf("ReleaseTime(%s) = %v, %v; want %q", v, got, err, want)
				}
			}
			if accept, _ := ix.last(); accept != simpleAccept {
				t.Errorf("Accept = %q", accept)
			}
		})
	}
}

func TestHTMLNotAcceptable(t *testing.T) {
	var accepts []string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		accepts = append(accepts, r.Header.Get("Accept"))
		mu.Unlock()
		if r.Header.Get("Accept") != "text/html" {
			w.WriteHeader(http.StatusNotAcceptable)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<a href="p-1.0.tar.gz">p-1.0.tar.gz</a>`))
	}))
	defer srv.Close()
	s := New(srv.Client(), nil, env(nil))
	rels, err := s.Releases(t.Context(), dep.Dep{Name: "p", Source: srv.URL}, "")
	if err != nil || !slices.Equal(versions(rels), []string{"1.0"}) {
		t.Fatalf("Releases = %v, %v", versions(rels), err)
	}
	if !slices.Equal(accepts, []string{simpleAccept, "text/html"}) {
		t.Errorf("Accept headers = %q", accepts)
	}
}

func TestDecodeHTMLFiles(t *testing.T) {
	body := `<!DOCTYPE html><html><head><meta name="pypi:repository-version" content="1.0"></head><body>
<abbr>not an anchor</abbr>
<A HREF='../../packages/pkg-1.0.tar.gz#sha256=abc' data-requires-python="&gt;=3.8,&lt;4" DATA-YANKED>pkg-1.0.tar.gz</A><br>
<a href=pkg-1.1-py3-none-any.whl#md5=x data-yanked="">  </a>
<a
  href="https://files.example/pkg-1.2.tar.gz#sha256=def" data-upload-time = "2024-01-01T00:00:00Z" >pkg&#45;1.2.tar.gz</a><br/>
<a href="pkg-1.3.tar.gz`
	base, _ := url.Parse("https://idx.example/simple/pkg/")
	var got []simpleFile
	if err := decodeHTMLFiles(strings.NewReader(body), base, func(f simpleFile) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}
	want := []simpleFile{
		{Filename: "pkg-1.0.tar.gz", SHA256: "abc", RequiresPython: ">=3.8,<4", Yanked: true},
		{Filename: "pkg-1.1-py3-none-any.whl", Yanked: true},
		{Filename: "pkg-1.2.tar.gz", SHA256: "def", Upload: "2024-01-01T00:00:00Z"},
		{Filename: "pkg-1.3.tar.gz"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("files =\n%+v\nwant\n%+v", got, want)
	}
	p, yanked, err := decodeHTML(strings.NewReader(body), nil, "pkg")
	if err != nil || !slices.Equal(versions(p.rels), []string{"1.2", "1.3"}) || yanked != 2 {
		t.Errorf("decodeHTML = %v, %d, %v", versions(p.rels), yanked, err)
	}
}

func TestExtraIndexes(t *testing.T) {
	extra := `{"files": [
		{"filename": "requests-2.31.0.tar.gz", "upload-time": "2020-01-01T00:00:00Z"},
		{"filename": "requests-2.35.0.tar.gz", "upload-time": "2026-11-01T00:00:00Z"}]}`
	srv, n := server(t, map[string][2]string{
		"/pypi/requests/json": {"application/json", string(fixture(t, "requests.json"))},
		"/extra/requests/":    {simpleJSON, extra},
		"/html/requests/":     {"text/html", string(fixture(t, "requests-simple.html"))},
		"/broken/requests/":   {"500", ""},
	})
	s := New(srv.Client(), nil, env(map[string]string{
		"DEPS_PYPI_URL":       srv.URL,
		"PIP_EXTRA_INDEX_URL": " " + srv.URL + "/missing/  " + srv.URL + "/extra ",
	}))
	ctx := t.Context()
	d := dep.Dep{Name: "Requests", ExtraSources: []string{srv.URL + "/extra/", "https://pypi.org/simple"}}
	rels, err := s.Releases(ctx, d, "")
	if want := []string{"2.31.0", "2.32.2", "2.34.2", "2.35.0"}; err != nil || !slices.Equal(versions(rels), want) {
		t.Fatalf("Releases = %v, %v; want %v", versions(rels), err, want)
	}
	for v, want := range map[string]string{"2.31.0": "2020-01-01T00:00:00Z", "2.35.0": "2026-11-01T00:00:00Z", "2.34.2": "2026-05-14T19:25:26.443Z"} {
		if got, err := s.ReleaseTime(ctx, d, v); err != nil || !got.Equal(mustTime(t, want)) {
			t.Errorf("ReleaseTime(%s) = %v, %v; want the earliest on any index", v, got, err)
		}
	}
	if repo, err := s.SourceRepo(ctx, d, ""); err != nil || repo != "https://github.com/psf/requests" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	// Another dep with the extra index as primary reuses the fetched projects.
	if rels, err := s.Releases(ctx, dep.Dep{Name: "requests", Source: srv.URL + "/extra"}, ""); err != nil || !slices.Equal(versions(rels), []string{"2.31.0", "2.35.0"}) {
		t.Errorf("extra as primary: %v, %v", versions(rels), err)
	}
	for _, p := range []string{"/pypi/requests/json", "/extra/requests/", "/missing/requests/"} {
		if got := calls(n, p); got != 1 {
			t.Errorf("%s: %d requests, want 1", p, got)
		}
	}
	// requires-python of the primary's files wins for a version on several indexes.
	d.PythonConstraint = ">=3.9"
	if rels, err := s.Releases(ctx, d, ""); err != nil || !slices.Equal(versions(rels), []string{"2.31.0", "2.32.2", "2.35.0"}) {
		t.Errorf("with requires-python: %v, %v", versions(rels), err)
	}

	only := New(srv.Client(), nil, env(map[string]string{"DEPS_PYPI_URL": srv.URL + "/none"}))
	for _, c := range []struct {
		name   string
		d      dep.Dep
		want   []string
		errMsg string
	}{
		{"html extra", dep.Dep{Name: "requests", Source: srv.URL + "/missing", ExtraSources: []string{srv.URL + "/html"}}, []string{"2.31.0", "2.32.2", "2.34.2"}, ""},
		{"failing extra", dep.Dep{Name: "requests", Source: srv.URL + "/html", ExtraSources: []string{srv.URL + "/broken"}}, nil, "/broken/requests/: HTTP 500"},
		{"missing everywhere", dep.Dep{Name: "requests", ExtraSources: []string{srv.URL + "/missing"}}, nil, "package requests not found"},
		{"invalid extra", dep.Dep{Name: "requests", ExtraSources: []string{"ftp://x/simple"}}, nil, "pypi: invalid index URL for package requests"},
	} {
		rels, err := only.Releases(ctx, c.d, "")
		if c.errMsg != "" {
			if err == nil || !strings.Contains(err.Error(), c.errMsg) {
				t.Errorf("%s: err = %v, want %q", c.name, err, c.errMsg)
			}
			continue
		}
		if err != nil || !slices.Equal(versions(rels), c.want) {
			t.Errorf("%s: %v, %v", c.name, versions(rels), err)
		}
	}
}

func TestRequiresPython(t *testing.T) {
	mixed := `{"releases": {
		"1.0": [{"requires_python": ">=3.12"}, {"requires_python": null}],
		"1.1": [{"requires_python": ">=3.12"}, {"requires_python": ">=2.7", "yanked": true}],
		"1.2": [{"requires_python": ">=3.12"}, {"requires_python": ">=3.11"}],
		"1.3": [{"requires_python": "not a specifier"}],
		"1.4": [{"requires_python": " >=3.9 "}, {"requires_python": ">=3.9"}]}}`
	srv, _ := server(t, map[string][2]string{
		"/pypi/requests/json": {"application/json", string(fixture(t, "requests.json"))},
		"/json/requests/":     {simpleJSON, string(fixture(t, "requests-simple.json"))},
		"/html/requests/":     {"text/html", string(fixture(t, "requests-simple.html"))},
		"/pypi/mixed/json":    {"application/json", mixed},
	})
	s := New(srv.Client(), nil, env(map[string]string{"DEPS_PYPI_URL": srv.URL}))
	for _, c := range []struct {
		python string
		want   []string
	}{
		{"", []string{"2.31.0", "2.32.2", "2.34.2"}},
		{">=3.9", []string{"2.31.0", "2.32.2"}},
		{"^3.10", []string{"2.31.0", "2.32.2", "2.34.2"}},
		{">=3.8,<4", []string{"2.31.0", "2.32.2"}},
		{"==3.7.*", []string{"2.31.0"}},
		{">=3.6", nil},
		{">=x", []string{"2.31.0", "2.32.2", "2.34.2"}}, // invalid: not filtered
	} {
		for _, src := range []string{"", srv.URL + "/json", srv.URL + "/html"} {
			d := dep.Dep{Name: "requests", Source: src, PythonConstraint: c.python}
			rels, err := s.Releases(t.Context(), d, "")
			if err != nil || !slices.Equal(versions(rels), c.want) {
				t.Errorf("%q via %q: %v, %v; want %v", c.python, src, versions(rels), err, c.want)
			}
		}
	}
	rels, err := s.Releases(t.Context(), dep.Dep{Name: "mixed", PythonConstraint: ">=3.9"}, "")
	if want := []string{"1.0", "1.2", "1.3", "1.4"}; err != nil || !slices.Equal(versions(rels), want) {
		t.Errorf("mixed: %v, %v; want %v", versions(rels), err, want)
	}
}

// TestDeprecationInactive flags a project classified `Development Status :: 7 - Inactive`.
func TestDeprecationInactive(t *testing.T) {
	ix := newIndex(t, "/pypi/nose/json", "application/json",
		[]byte(`{"info":{"classifiers":["Development Status :: 7 - Inactive","License :: OSI Approved"]},"releases":{"1.3.7":[{"yanked":false}]}}`))
	s := New(ix.srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_PYPI_URL": ix.srv.URL + "/"}))
	d := dep.Dep{Name: "nose"}
	if _, err := s.Releases(t.Context(), d, ""); err != nil {
		t.Fatal(err)
	}
	if got, want := s.Deprecation(d, "1.3.7"), "project inactive (Development Status :: 7 - Inactive)"; got != want {
		t.Errorf("Deprecation = %q, want %q", got, want)
	}
	if n := ix.calls.Load(); n != 1 {
		t.Errorf("requests = %d, want 1", n)
	}
}
