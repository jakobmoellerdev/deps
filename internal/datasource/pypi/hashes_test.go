package pypi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// requests-2.34.2.json: curl -s https://pypi.org/pypi/requests/2.34.2/json (fetched
// 2026-11-04), trimmed with jq to info.name/version and urls[].filename/digests/yanked.
var requests2342 = []string{
	"2a0d60c172f83ac6ab31e4554906c0f3b3588d37b5cb939b1c061f4907e278e0", // wheel
	"f288924cae4e29463698d6d60bc6a4da69c89185ad1e0bcc4104f584e960b9ed", // sdist
}

func TestHashes(t *testing.T) {
	release := fixture(t, "requests-2.34.2.json")
	simple, html := fixture(t, "requests-simple.json"), fixture(t, "requests-simple.html")
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		switch r.URL.Path {
		case "/pypi/requests/2.34.2/json":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write(release)
		case "/pypi/nosha/1.0/json":
			_, _ = w.Write([]byte(`{"urls":[{"filename":"nosha-1.0.tar.gz","digests":{"md5":"x"}}]}`))
		case "/pypi/bad/1.0/json":
			_, _ = w.Write([]byte(`{"urls":`))
		case "/simple/requests/":
			w.Header().Set("Content-Type", simpleJSON)
			_, _ = w.Write(simple)
		case "/html/requests/":
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(html)
		case "/text/requests/":
			w.Header().Set("Content-Type", "text/plain")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	s := New(srv.Client(), make(chan struct{}, 1), env(map[string]string{"DEPS_PYPI_URL": srv.URL}))
	for _, c := range []struct {
		name    string
		d       dep.Dep
		version string
		want    []string
		err     string
	}{
		{"json api", dep.Dep{Name: "Requests"}, "2.34.2", requests2342, ""},
		{"pypi simple url uses the json api", dep.Dep{Name: "requests", Source: "https://pypi.org/simple/"}, "2.34.2", requests2342, ""},
		{"json simple api", dep.Dep{Name: "requests", Source: srv.URL + "/simple"}, "2.34.2", requests2342, ""},
		{"html simple api", dep.Dep{Name: "requests", Source: srv.URL + "/html/"}, "2.34.2", requests2342, ""},
		{"simple api other version", dep.Dep{Name: "requests", Source: srv.URL + "/simple"}, "2.31.0", []string{
			"58cd2187c01e70e6e26505bca751777aa9f2ee0b7f4300988b709f44e013003f",
			"942c5a758f98d790eaed1a29cb6eefc7ffb0d1cf7af05c3d2791656dbd6ad1e1",
		}, ""},
		{"simple api missing version", dep.Dep{Name: "requests", Source: srv.URL + "/simple"}, "9.9", nil, "requests 9.9 has no files with a sha256 digest"},
		{"not found", dep.Dep{Name: "requests"}, "9.9", nil, "package requests 9.9 not found"},
		{"no sha256", dep.Dep{Name: "nosha"}, "1.0", nil, "nosha 1.0 has no files with a sha256 digest"},
		{"malformed", dep.Dep{Name: "bad"}, "1.0", nil, "/pypi/bad/1.0/json: malformed response"},
		{"content type", dep.Dep{Name: "requests", Source: srv.URL + "/text"}, "1.0", nil, `unexpected content type "text/plain"`},
		{"invalid index", dep.Dep{Name: "requests", Source: "ftp://x"}, "1.0", nil, "invalid index URL"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := s.Hashes(t.Context(), c.d, c.version)
			if c.err != "" {
				if err == nil || !strings.Contains(err.Error(), c.err) {
					t.Fatalf("err = %v, want %q", err, c.err)
				}
				return
			}
			if err != nil || !slices.Equal(got, c.want) {
				t.Errorf("got %v, %v; want %v", got, err, c.want)
			}
		})
	}
	if !slices.Contains(paths, "/pypi/requests/2.34.2/json") {
		t.Errorf("paths = %v", paths)
	}
}

func TestHashesCanceled(t *testing.T) {
	s := New(http.DefaultClient, make(chan struct{}), env(nil)) // a full semaphore
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := s.Hashes(ctx, dep.Dep{Name: "x"}, "1"); err == nil {
		t.Error("no error")
	}
}
