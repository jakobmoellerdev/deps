package bazel

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// testdata/rules_go.json and testdata/blake3.json are https://bcr.bazel.build/modules/<name>/metadata.json
// verbatim (fetched 2026-11-05); rules_go has five yanked versions (0.33.0 to 0.37.0).
func registry(t *testing.T, hits *atomic.Int32, auth *string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if !strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") {
			http.Error(w, "no user agent", http.StatusForbidden)
			return
		}
		if auth != nil {
			*auth = r.Header.Get("Authorization")
		}
		name, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/modules/"), "/metadata.json")
		if !ok {
			http.NotFound(w, r)
			return
		}
		switch name {
		case "broken":
			_, _ = w.Write([]byte("{"))
			return
		case "down":
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		b, err := os.ReadFile(filepath.Join("testdata", name+".json"))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func versions(rels []datasource.Release) []string {
	out := make([]string, len(rels))
	for i, r := range rels {
		out[i] = r.Version
	}
	return out
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestReleasesDefaultRegistry(t *testing.T) {
	var hits atomic.Int32
	srv := registry(t, &hits, nil)
	s := New(srv.Client(), nil, nil, env(map[string]string{"DEPS_BAZEL_REGISTRY_URL": srv.URL + "/"}))
	dir := t.TempDir()
	d := dep.Dep{Name: "rules_go", File: "MODULE.bazel"}
	rels, err := s.Releases(t.Context(), d, dir)
	if err != nil {
		t.Fatal(err)
	}
	got := versions(rels)
	if len(got) != 45 || got[0] != "0.38.0" || got[len(got)-1] != "0.65.0" || slices.Contains(got, "0.35.0") {
		t.Errorf("versions = %v", got)
	}
	if _, err := s.Releases(t.Context(), d, dir); err != nil || hits.Load() != 1 {
		t.Errorf("second lookup: %v, %d requests", err, hits.Load())
	}
	if repo, err := s.SourceRepo(t.Context(), d, "0.65.0"); err != nil || repo != "https://github.com/bazel-contrib/rules_go" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if repo, _ := s.SourceRepo(t.Context(), dep.Dep{Name: "never", File: "MODULE.bazel"}, "1"); repo != "" {
		t.Errorf("SourceRepo of an unlooked-up module = %q", repo)
	}
	if _, err := s.Digest(t.Context(), d, "0.65.0"); err == nil {
		t.Error("Digest succeeded")
	}
	_, err = s.Releases(t.Context(), dep.Dep{Name: "missing", File: "MODULE.bazel"}, dir)
	if err == nil || err.Error() != "bazel module missing not found in "+srv.URL {
		t.Errorf("missing module: %v", err)
	}
	for name, want := range map[string]string{"broken": "unexpected EOF", "down": "HTTP 502"} {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: name, File: "MODULE.bazel"}, dir); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want %q", name, err, want)
		}
	}
}

// The .bazelrc of brpc and github/codeql name several registries; the first that has the
// module answers. Platform tokens go to custom registry hosts only.
func TestReleasesBazelrcRegistries(t *testing.T) {
	var hits atomic.Int32
	var authz string
	other := registry(t, &hits, &authz)
	empty := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(empty.Close)
	root := t.TempDir()
	ws := filepath.Join(root, "app")
	local := filepath.Join(ws, "misc", "registry", "modules", "blake3")
	if err := os.MkdirAll(local, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(local, "metadata.json"), `{"versions": ["1.8.2", "9.9.9"], "yanked_versions": {"9.9.9": "bad"}, "homepage": "https://codeberg.org/x/blake3"}`)
	write(t, filepath.Join(ws, "MODULE.bazel"), "")
	write(t, filepath.Join(ws, ".bazelrc"), "# registries\n"+
		"common --registry=file:///%workspace%/misc/registry\n"+
		"common:ci --registry=https://ci.invalid\n"+
		"startup --registry=https://startup.invalid\n"+
		"build --enable_bzlmod \\\n  --registry "+empty.URL+" --registry=\""+other.URL+"\" # trailing\n"+
		"try-import %workspace%/user.bazelrc\n"+
		"import %workspace%/missing.bazelrc\n"+
		"import /etc/outside.bazelrc\n")
	write(t, filepath.Join(ws, "user.bazelrc"), "common --registry="+other.URL+"\ntry-import %workspace%/.bazelrc\n")

	auth := httpx.NewAuth(platform.Info{Host: "127.0.0.1"}, "tok", env(nil))
	s := New(other.Client(), nil, auth, env(nil))
	segment := dep.Dep{Name: "rules_go", File: "app/sub/deps.MODULE.bazel"}
	rels, err := s.Releases(t.Context(), segment, filepath.Join(ws, "sub"))
	if err != nil || len(rels) != 45 {
		t.Fatalf("rules_go: %d releases, %v", len(rels), err)
	}
	if authz == "" {
		t.Error("no platform token for the custom registry")
	}
	d := dep.Dep{Name: "blake3", File: "app/MODULE.bazel"}
	rels, err = s.Releases(t.Context(), d, ws)
	if err != nil || !slices.Equal(versions(rels), []string{"1.8.2"}) {
		t.Errorf("blake3 from the local registry = %v, %v", versions(rels), err)
	}
	if repo, _ := s.SourceRepo(t.Context(), d, "1.8.2"); repo != "https://codeberg.org/x/blake3" {
		t.Errorf("SourceRepo = %q", repo)
	}
	// A single_version_override registry replaces the workspace's.
	d.Source = other.URL
	if rels, err := s.Releases(t.Context(), d, ws); err != nil || len(rels) != 8 {
		t.Errorf("override registry: %d releases, %v", len(rels), err)
	}
	_, err = s.Releases(t.Context(), dep.Dep{Name: "missing", File: "app/MODULE.bazel"}, ws)
	if want := "/misc/registry, " + empty.URL + ", " + other.URL; err == nil || !strings.HasSuffix(err.Error(), want) {
		t.Errorf("missing: %v, want suffix %s", err, want)
	}
	// A file:// registry outside the repository is not read.
	s = New(other.Client(), nil, nil, env(nil))
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "blake3", File: "MODULE.bazel", Source: "file:///etc"}, ws); err == nil || !strings.Contains(err.Error(), "outside the repository") {
		t.Errorf("outside registry: %v", err)
	}
	write(t, filepath.Join(local, "metadata.json"), "{")
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "blake3", File: "MODULE.bazel", Source: "file://%workspace%/misc/registry"}, ws); err == nil || !strings.Contains(err.Error(), "metadata.json") {
		t.Errorf("malformed local registry: %v", err)
	}
}

func write(t *testing.T, p, s string) {
	t.Helper()
	if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestRcWords(t *testing.T) {
	for in, want := range map[string][]string{
		`build --a=b  --c 'd e' "f#g" # comment`: {"build", "--a=b", "--c", "d e", "f#g"},
		`common --x=a\ b`:                        {"common", "--x=a b"},
		"  # only a comment":                     nil,
		`build --flag=""`:                        {"build", "--flag="},
	} {
		if got := rcWords(in); !slices.Equal(got, want) {
			t.Errorf("rcWords(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRepoURL(t *testing.T) {
	for _, c := range []struct {
		repos    []string
		homepage string
		want     string
	}{
		{[]string{"github:bazelbuild/rules_go"}, "", "https://github.com/bazelbuild/rules_go"},
		{[]string{"gitlab:o/r"}, "", "https://gitlab.com/o/r"},
		{[]string{"other:x", "https://git.example.com/o/r"}, "", "https://git.example.com/o/r"},
		{nil, "https://github.com/o/r", "https://github.com/o/r"},
		{nil, "https://example.com", ""},
		{nil, "::", ""},
	} {
		if got := repoURL(c.repos, c.homepage); got != c.want {
			t.Errorf("repoURL(%v, %q) = %q, want %q", c.repos, c.homepage, got, c.want)
		}
	}
}
