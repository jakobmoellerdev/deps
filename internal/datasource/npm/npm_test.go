package npmds

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

const metadata = `{
  "name": "@scope/pkg",
  "modified": "2026-11-01T00:00:00.000Z",
  "dist-tags": {"latest": "1.2.0"},
  "versions": {
    "1.0.0": {"name": "@scope/pkg", "version": "1.0.0", "dependencies": {"a": "^1.0.0"}, "dist": {"tarball": "x"}},
    "1.1.0": {"name": "@scope/pkg", "version": "1.1.0", "deprecated": "use 1.2.0"},
    "1.1.1": {"name": "@scope/pkg", "version": "1.1.1", "deprecated": ""},
    "1.1.2": {"name": "@scope/pkg", "version": "1.1.2", "deprecated": true},
    "1.2.0": {"name": "@scope/pkg", "version": "1.2.0", "deprecated": false}
  }
}`

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func versions(rels []datasource.Release) []string {
	vs := make([]string, len(rels))
	for i, r := range rels {
		vs[i] = r.Version
	}
	return vs
}

// write creates the files (slash paths relative to root) and returns root.
func write(t *testing.T, root string, files map[string]string) string {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// registry serves metadata for every package and records "<auth> <escaped path>" per request.
type registry struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
}

func newRegistry(t *testing.T, h http.HandlerFunc) *registry {
	t.Helper()
	r := &registry{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Accept") != abbreviated || !strings.HasPrefix(req.Header.Get("User-Agent"), "deps/") {
			t.Errorf("unexpected headers %v", req.Header)
		}
		r.mu.Lock()
		r.reqs = append(r.reqs, strings.TrimSpace(req.Header.Get("Authorization")+" "+req.URL.EscapedPath()))
		r.mu.Unlock()
		if h != nil {
			h(w, req)
			return
		}
		_, _ = w.Write([]byte(metadata))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func TestReleasesAbbreviatedDeprecated(t *testing.T) {
	reg := newRegistry(t, nil)
	s := New(reg.srv.Client(), make(chan struct{}, 1), env(map[string]string{"NPM_CONFIG_REGISTRY": reg.srv.URL + "/base"}))
	root := t.TempDir()
	d := dep.Dep{Name: "@scope/pkg", File: "package.json"}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			rels, err := s.Releases(t.Context(), d, root)
			if err != nil {
				t.Error(err)
				return
			}
			if got := versions(rels); !slices.Equal(got, []string{"1.0.0", "1.1.1", "1.2.0"}) {
				t.Errorf("versions = %v", got)
			}
		})
	}
	wg.Wait()
	if !slices.Equal(reg.reqs, []string{"/base/@scope%2fpkg"}) {
		t.Errorf("requests = %v, want one deduped request with the escaped name", reg.reqs)
	}
	if _, err := s.Digest(t.Context(), d, "1.0.0"); err == nil {
		t.Error("Digest succeeded")
	}
}

func TestRegistryResolution(t *testing.T) {
	reg := newRegistry(t, nil)
	u := reg.srv.URL
	parent := t.TempDir()
	root := write(t, filepath.Join(parent, "repo"), map[string]string{
		".npmrc": "; comment\n# comment\n\nregistry = \"" + u + "/default/\"\n" +
			"@corp:registry='" + u + "/corp'\n" +
			"//" + strings.TrimPrefix(u, "http://") + "/corp/:_authToken=${CORP_TOKEN}\n" +
			"//" + strings.TrimPrefix(u, "http://") + "/:_authToken=short\n" +
			"no-equals-sign\n",
		"web/sub/package.json": "{}",
		"own/.npmrc":           "registry=" + u + "/own\n//${HOST}/own/:_authToken=own-${MISSING?}token\n",
		"own/package.json":     "{}",
	})
	// Above the root: never read.
	write(t, parent, map[string]string{".npmrc": "registry=http://127.0.0.1:1/outside\n"})
	other := write(t, t.TempDir(), map[string]string{"package.json": "{}"})

	getenv := env(map[string]string{"CORP_TOKEN": "corp-secret", "HOST": strings.TrimPrefix(u, "http://"), "npm_config_registry": u + "/lower"})
	s := New(reg.srv.Client(), nil, getenv)
	for _, c := range []struct {
		name, file, dir string
	}{
		{"plain", "web/sub/package.json", filepath.Join(root, "web", "sub")},
		{"@corp/lib", "web/sub/package.json", filepath.Join(root, "web", "sub")},
		{"@other/lib", "web/sub/package.json", filepath.Join(root, "web", "sub")},
		{"plain", "own/package.json", filepath.Join(root, "own")},
		{"plain", "package.json", other},
	} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: c.name, File: c.file}, c.dir); err != nil {
			t.Fatalf("%s in %s: %v", c.name, c.file, err)
		}
	}
	want := []string{
		"Bearer short /default/plain",
		"Bearer corp-secret /corp/@corp%2flib",
		"Bearer short /default/@other%2flib",
		"Bearer own-token /own/plain",
		"/lower/plain",
	}
	if !slices.Equal(reg.reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reg.reqs, want)
	}
}

func TestReleasesErrors(t *testing.T) {
	for _, c := range []struct {
		name, body string
		status     int
		want       string
	}{
		{"missing", "", http.StatusNotFound, "package missing not found"},
		{"server", "", http.StatusInternalServerError, "/server: HTTP 500"},
		{"array", "[]", 200, "malformed response: expected {"},
		{"versions array", `{"versions": []}`, 200, "malformed response: expected {"},
		{"truncated", `{"versions": {"1.0.0": {`, 200, "malformed response"},
		{"bad version", `{"versions": {"1.0.0": 5}}`, 200, "malformed response"},
		{"bad other", `{"name": }`, 200, "malformed response"},
		{"empty", ``, 200, "malformed response"},
	} {
		t.Run(c.name, func(t *testing.T) {
			reg := newRegistry(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(c.body))
			})
			s := New(reg.srv.Client(), nil, env(map[string]string{"NPM_CONFIG_REGISTRY": reg.srv.URL}))
			_, err := s.Releases(t.Context(), dep.Dep{Name: c.name, File: "package.json"}, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("err = %v, want %q", err, c.want)
			}
		})
	}
}

func TestReleasesTransportErrors(t *testing.T) {
	d := dep.Dep{Name: "x", File: "package.json"}
	t.Run("bad url", func(t *testing.T) {
		s := New(http.DefaultClient, nil, env(map[string]string{"NPM_CONFIG_REGISTRY": "http://[::1"}))
		if _, err := s.Releases(t.Context(), d, t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "npm registry ") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("refused", func(t *testing.T) {
		s := New(http.DefaultClient, nil, env(map[string]string{"NPM_CONFIG_REGISTRY": "http://127.0.0.1:1"}))
		if _, err := s.Releases(t.Context(), d, t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "npm registry http://127.0.0.1:1/x: ") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("canceled waiting for a slot", func(t *testing.T) {
		var calls atomic.Int32
		reg := newRegistry(t, func(http.ResponseWriter, *http.Request) { calls.Add(1) })
		sem := make(chan struct{}, 1)
		sem <- struct{}{}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		s := New(reg.srv.Client(), sem, env(map[string]string{"NPM_CONFIG_REGISTRY": reg.srv.URL}))
		if _, err := s.Releases(ctx, d, t.TempDir()); err == nil || calls.Load() != 0 {
			t.Errorf("err = %v, calls = %d", err, calls.Load())
		}
	})
	t.Run("unreadable npmrc", func(t *testing.T) {
		root := write(t, t.TempDir(), map[string]string{"package.json": "{}"})
		if err := os.Mkdir(filepath.Join(root, ".npmrc"), 0o755); err != nil {
			t.Fatal(err)
		}
		s := New(http.DefaultClient, nil, env(nil))
		if _, err := s.Releases(t.Context(), d, root); err == nil || !strings.Contains(err.Error(), ".npmrc") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("default registry", func(t *testing.T) {
		if got := rootOf("a/b/package.json", "/x/y"); got != "/x/y" {
			t.Errorf("rootOf outside = %q", got)
		}
		rc := &npmrc{}
		if rc.registryFor("@s/x") != "" || rc.tokenFor("no-scheme") != "" {
			t.Error("empty npmrc resolved something")
		}
	})
}
