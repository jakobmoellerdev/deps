package crate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// The registry configuration follows the Cargo book (github.com/rust-lang/cargo@46a4439
// doc/book/src/reference/registries.md:26-27 and :105-106, with the example token), with the
// index URL pointed at a test server.
const bookToken = "854DvwSlUwEHtIo3kWy6x7UCPKHfzCmy"

func writeFiles(t *testing.T, dir string, files map[string]string) {
	t.Helper()
	for p, c := range files {
		abs := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// registryServer serves sparse indexes under /reg/ (token required), /open/ and /mirror/, and
// a web API under /api.
func registryServer(t *testing.T) *httptest.Server {
	t.Helper()
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/reg/") && r.Header.Get("Authorization") != bookToken {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/reg/config.json":
			fmt.Fprintf(w, `{"dl": "%s/dl", "api": "%s/api/", "auth-required": true}`, srv.URL, srv.URL)
		case "/reg/so/me/some-crate", "/open/so/me/some-crate":
			fmt.Fprintln(w, `{"name":"some-crate","vers":"1.0.0","yanked":false}`)
			fmt.Fprintln(w, `{"name":"some-crate","vers":"1.4.2","yanked":false}`)
		case "/mirror/se/rd/serde":
			fmt.Fprintln(w, `{"name":"serde","vers":"1.0.228","yanked":false,"pubtime":"2025-09-27T16:51:35Z"}`)
		case "/api/api/v1/crates/some-crate/1.4.2":
			if r.Header.Get("Authorization") != bookToken {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"version":{"repository":"https://git.example/some-crate"}}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func envOf(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestNamedRegistry(t *testing.T) {
	srv := registryServer(t)
	root, home := t.TempDir(), t.TempDir()
	writeFiles(t, root, map[string]string{
		".cargo/config.toml":        "[registries]\nmy-registry = { index = \"sparse+" + srv.URL + "/reg/\" }\n",
		"app/.cargo/config":         "[registries.other]\nindex = \"sparse+" + srv.URL + "/open\"\n", // legacy name, nearer
		"app/Cargo.toml":            "",
		"broken/.cargo/config.toml": "[registries\n",
	})
	writeFiles(t, home, map[string]string{"credentials.toml": "[registries.my-registry]\ntoken = \"" + bookToken + "\"\n"})
	s := New(srv.Client(), nil, nil, envOf(map[string]string{"CARGO_HOME": home}))
	d := dep.Dep{Name: "some-crate", Source: "my-registry", File: "app/Cargo.toml", Version: "1.0.0"}
	dir := filepath.Join(root, "app")
	rels, err := s.Releases(t.Context(), d, dir)
	if err != nil || versions(rels) != "1.0.0 1.4.2" {
		t.Fatalf("Releases = %s, %v", versions(rels), err)
	}
	if repo, err := s.SourceRepo(t.Context(), d, "1.4.2"); err != nil || repo != "https://git.example/some-crate" {
		t.Errorf("SourceRepo = %q, %v", repo, err)
	}
	if tm, err := s.ReleaseTime(t.Context(), d, "1.4.2"); err != nil || !tm.IsZero() {
		t.Errorf("ReleaseTime without pubtime = %v, %v", tm, err)
	}
	other := dep.Dep{Name: "some-crate", Source: "other", File: "app/Cargo.toml", Version: "1"}
	if rels, err := s.Releases(t.Context(), other, dir); err != nil || versions(rels) != "1.0.0 1.4.2 1.0 1 1.4" {
		t.Errorf("other = %s, %v", versions(rels), err)
	}
	if repo, err := s.SourceRepo(t.Context(), other, "1.4"); err != nil || repo != "" {
		t.Errorf("registry without api: %q, %v", repo, err)
	}
	// An unknown name is skipped, not an error (Cargo cannot resolve it either).
	var skip *datasource.SkipError
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "x", Source: "nowhere", File: "app/Cargo.toml"}, dir); !errors.As(err, &skip) || skip.Reason != "unknown registry nowhere" || err.Error() != skip.Reason {
		t.Errorf("unknown registry: %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "x", Source: "my-registry", File: "broken/Cargo.toml"}, filepath.Join(root, "broken")); err == nil || !strings.Contains(err.Error(), "config.toml: toml: line 1") {
		t.Errorf("broken config: %v", err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "missing", Source: "my-registry", File: "app/Cargo.toml"}, dir); err == nil || err.Error() != "crate missing not found in registry my-registry" {
		t.Errorf("missing crate: %v", err)
	}

	// The environment wins over the files, as in Cargo.
	env := New(srv.Client(), nil, nil, envOf(map[string]string{
		"CARGO_HOME":                         t.TempDir(),
		"CARGO_REGISTRIES_MY_REGISTRY_INDEX": "sparse+" + srv.URL + "/reg",
		"CARGO_REGISTRIES_MY_REGISTRY_TOKEN": bookToken,
	}))
	if rels, err := env.Releases(t.Context(), dep.Dep{Name: "some-crate", Source: "my-registry", File: "Cargo.toml", Version: "1.0.0"}, t.TempDir()); err != nil || len(rels) != 2 {
		t.Errorf("env registry = %s, %v", versions(rels), err)
	}
	noToken := New(srv.Client(), nil, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir(), "CARGO_REGISTRIES_MY_REGISTRY_INDEX": "sparse+" + srv.URL + "/reg"}))
	if _, err := noToken.Releases(t.Context(), dep.Dep{Name: "some-crate", Source: "my-registry", File: "Cargo.toml"}, t.TempDir()); err == nil || !strings.HasSuffix(err.Error(), "HTTP 401") {
		t.Errorf("without token: %v", err)
	}
	// A token in the config files (registries.md:105-106) works too.
	cfgToken := t.TempDir()
	writeFiles(t, cfgToken, map[string]string{".cargo/config.toml": "[registries.my-registry]\nindex = \"sparse+" + srv.URL + "/reg\"\ntoken = \"" + bookToken + "\"\n"})
	if rels, err := New(srv.Client(), nil, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir()})).Releases(t.Context(), dep.Dep{Name: "some-crate", Source: "my-registry", File: "Cargo.toml", Version: "1.0.0"}, cfgToken); err != nil || len(rels) != 2 {
		t.Errorf("config token = %s, %v", versions(rels), err)
	}
}

func TestRegistryIndexAndReplacement(t *testing.T) {
	srv := registryServer(t)
	s := New(srv.Client(), nil, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir()}))
	d := dep.Dep{Name: "some-crate", Source: "sparse+" + srv.URL + "/open/", File: "Cargo.toml", Version: "1.0.0"}
	if rels, err := s.Releases(t.Context(), d, t.TempDir()); err != nil || versions(rels) != "1.0.0 1.4.2" {
		t.Errorf("registry-index = %s, %v", versions(rels), err)
	}

	// [source.crates-io] replace-with, followed through a chain to a sparse mirror.
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".cargo/config.toml": "[source.crates-io]\nreplace-with = \"corp\"\n\n[source.corp]\nreplace-with = \"mirror\"\n\n[source.mirror]\nregistry = \"sparse+" + srv.URL + "/mirror/\"\n"})
	serde := dep.Dep{Name: "serde", File: "Cargo.toml", Version: "1.0.100"}
	if rels, err := s.Releases(t.Context(), serde, root); err != nil || versions(rels) != "1.0.228" {
		t.Errorf("replaced crates.io = %s, %v", versions(rels), err)
	}
	if tm, err := s.ReleaseTime(t.Context(), serde, "1.0.228"); err != nil || tm.Year() != 2025 {
		t.Errorf("mirror pubtime = %v, %v", tm, err)
	}

	// A vendored replacement (directory source) keeps crates.io.
	vendored := t.TempDir()
	writeFiles(t, vendored, map[string]string{".cargo/config.toml": "[source.crates-io]\nreplace-with = \"vendored-sources\"\n\n[source.vendored-sources]\ndirectory = \"vendor\"\n"})
	r, err := s.registryOf(dep.Dep{Name: "serde", File: "Cargo.toml"}, vendored)
	if err != nil || r.index != DefaultIndexURL || !r.sparse {
		t.Errorf("vendored = %+v, %v", r, err)
	}
}

// gitIndex makes a git registry index (Cargo's layout: config.json and the prefix paths)
// with the serde fixture lines.
func gitIndex(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	serde, err := os.ReadFile("testdata/serde")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	writeFiles(t, dir, map[string]string{"config.json": `{"dl": "https://dl.example/{crate}/{version}", "api": "https://api.example"}`, "se/rd/serde": string(serde)})
	for _, args := range [][]string{
		{"init", "-q", "-b", "master"}, {"add", "."},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "-m", "index"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_CONFIG_NOSYSTEM=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return "file://" + filepath.ToSlash(dir)
}

func TestGitIndex(t *testing.T) {
	index := gitIndex(t)
	root := t.TempDir()
	writeFiles(t, root, map[string]string{".cargo/config.toml": "[registries.corp]\nindex = \"" + index + "\"\n"})
	s := New(http.DefaultClient, nil, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir()}))
	d := dep.Dep{Name: "serde", Source: "corp", File: "Cargo.toml", Version: "1.0.171"}
	rels, err := s.Releases(t.Context(), d, root)
	if err != nil || len(rels) != 8 {
		t.Fatalf("git index = %s, %v", versions(rels), err)
	}
	if api, err := s.apiOf(t.Context(), s.locate(d)); err != nil || api != "https://api.example" {
		t.Errorf("git index api = %q, %v", api, err)
	}
	if _, err := s.Releases(t.Context(), dep.Dep{Name: "missing", Source: "corp", File: "Cargo.toml"}, root); err == nil || err.Error() != "crate missing not found in registry corp" {
		t.Errorf("missing crate: %v", err)
	}
	clones := len(s.clones)
	if clones != 1 {
		t.Errorf("%d clones, want 1", clones)
	}
	dir := s.clones[0]
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Errorf("clone left behind: %v", err)
	}
	bad := New(http.DefaultClient, nil, nil, envOf(map[string]string{"CARGO_HOME": t.TempDir()}))
	if _, err := bad.Releases(t.Context(), dep.Dep{Name: "serde", Source: "file:///nonexistent/index", File: "Cargo.toml"}, t.TempDir()); err == nil || !strings.HasPrefix(err.Error(), "git index file:///nonexistent/index: ") {
		t.Errorf("unreachable git index: %v", err)
	}
	if err := bad.Close(); err != nil {
		t.Error(err)
	}
}

// Credentials go to the index host only: the Cargo token is not sent to a config.json `api`
// on another host. Without a token, the platform token (Basic) goes to its host, and after a
// 401 the git credential helper's credentials.
func TestRegistryCredentialHosts(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]string{} // host + path -> Authorization
	handler := func(want string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen[r.Host+r.URL.Path] = r.Header.Get("Authorization")
			mu.Unlock()
			if want != "" && r.Header.Get("Authorization") != want {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			switch r.URL.Path {
			case "/idx/so/me/some-crate":
				fmt.Fprintln(w, `{"name":"some-crate","vers":"1.4.2"}`)
			case "/api/v1/crates/some-crate/1.4.2":
				fmt.Fprint(w, `{"version":{"repository":"https://git.example/some-crate"}}`)
			default:
				http.NotFound(w, r)
			}
		}
	}
	api := httptest.NewServer(handler(""))
	t.Cleanup(api.Close)
	authed := func(t *testing.T, want string) *httptest.Server {
		t.Helper()
		var srv *httptest.Server
		srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/idx/config.json" {
				fmt.Fprintf(w, `{"dl": "%s/dl", "api": "%s"}`, srv.URL, api.URL)
				return
			}
			handler(want)(w, r)
		}))
		t.Cleanup(srv.Close)
		return srv
	}
	d := dep.Dep{Name: "some-crate", Source: "corp", File: "Cargo.toml", Version: "1.0.0"}
	lookup := func(t *testing.T, srv *httptest.Server, auth *httpx.Auth, env map[string]string) {
		t.Helper()
		env["CARGO_HOME"], env["CARGO_REGISTRIES_CORP_INDEX"] = t.TempDir(), "sparse+"+srv.URL+"/idx/"
		s := New(http.DefaultClient, nil, auth, envOf(env))
		if rels, err := s.Releases(t.Context(), d, t.TempDir()); err != nil || versions(rels) != "1.4.2" {
			t.Fatalf("Releases = %s, %v", versions(rels), err)
		}
		if repo, err := s.SourceRepo(t.Context(), d, "1.4.2"); err != nil || repo != "https://git.example/some-crate" {
			t.Errorf("SourceRepo = %q, %v", repo, err)
		}
		mu.Lock()
		defer mu.Unlock()
		if got := seen[strings.TrimPrefix(api.URL, "http://")+"/api/v1/crates/some-crate/1.4.2"]; got != "" {
			t.Errorf("API on another host got Authorization %q", got)
		}
	}
	t.Run("cargo token", func(t *testing.T) {
		srv := authed(t, bookToken)
		lookup(t, srv, nil, map[string]string{"CARGO_REGISTRIES_CORP_TOKEN": bookToken})
	})
	t.Run("static token", func(t *testing.T) {
		srv := authed(t, "Basic eC1hY2Nlc3MtdG9rZW46cGxhdGZvcm0=") // x-access-token:platform
		lookup(t, srv, httpx.NewAuth(platform.Info{Host: "127.0.0.1"}, "platform", envOf(nil)), map[string]string{})
	})
	t.Run("credential helper after 401", func(t *testing.T) {
		srv := authed(t, "Basic dTpw") // u:p
		var asked []string
		auth := httpx.NewAuth(platform.Info{Host: "forge.example"}, "platform", envOf(nil), httpx.WithHelper(func(_ context.Context, host string) (string, string, bool) {
			asked = append(asked, host)
			return "u", "p", true
		}))
		lookup(t, srv, auth, map[string]string{})
		if want := strings.TrimPrefix(srv.URL, "http://"); len(asked) != 1 || asked[0] != want {
			t.Errorf("helper asked for %q, want [%s]", asked, want)
		}
	})
}
