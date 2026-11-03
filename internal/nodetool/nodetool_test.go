package nodetool

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// tarball builds the npm package tarball of tiny@version.
func tarball(t *testing.T, version string) []byte {
	t.Helper()
	pj := []byte(`{"name":"tiny","version":"` + version + `"}`)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(pj))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(pj); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// registry serves the package "tiny" in versions 1.0.0 and 1.1.0 (metadata and tarballs).
func registry(t *testing.T) *httptest.Server {
	t.Helper()
	tgz := map[string][]byte{}
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tiny" {
			versions := map[string]any{}
			for v, b := range tgz {
				s512, s1 := sha512.Sum512(b), sha1.Sum(b)
				versions[v] = map[string]any{"name": "tiny", "version": v, "dist": map[string]string{
					"tarball":   srv.URL + "/tiny/-/tiny-" + v + ".tgz",
					"integrity": "sha512-" + base64.StdEncoding.EncodeToString(s512[:]),
					"shasum":    hex.EncodeToString(s1[:]),
				}}
			}
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]any{"name": "tiny", "dist-tags": map[string]string{"latest": "1.1.0"}, "versions": versions})
			return
		}
		for v, b := range tgz {
			if r.URL.Path == "/tiny/-/tiny-"+v+".tgz" {
				_, _ = w.Write(b)
				return
			}
		}
		http.NotFound(w, r)
	}))
	for _, v := range []string{"1.0.0", "1.1.0"} {
		tgz[v] = tarball(t, v)
	}
	t.Cleanup(srv.Close)
	return srv
}

// isolate keeps npm away from the user's config, cache and registry settings.
func isolate(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("npm_config_userconfig", filepath.Join(home, "npmrc"))
	t.Setenv("npm_config_cache", filepath.Join(home, "cache"))
	for _, k := range []string{"npm_config_registry", "NPM_CONFIG_REGISTRY"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}
}

func manifest(rng string) []byte {
	return []byte(`{"name":"app","version":"1.0.0","dependencies":{"tiny":"` + rng + `"}}` + "\n")
}

// TestProcessRealNpm regenerates a lockfile with the real npm against a local registry.
func TestProcessRealNpm(t *testing.T) {
	if _, err := exec.LookPath("npm"); err != nil {
		t.Skip("npm not in PATH")
	}
	isolate(t)
	srv := registry(t)
	rc := []byte("registry=" + srv.URL + "/\n")
	t.Run("update", func(t *testing.T) {
		dir := t.TempDir()
		// Create the lockfile at 1.0.0 with npm itself, so the format is npm's.
		first := `{"name":"app","version":"1.0.0","lockfileVersion":3,"requires":true,"packages":{"":{"name":"app","version":"1.0.0"}}}`
		out, err := Locker{}.Process(t.Context(), dir, manifest("1.0.0"), map[string][]byte{PackageLock: []byte(first), npmrc: rc})
		if err != nil {
			t.Fatal(err)
		}
		lock := out[PackageLock]
		if !strings.Contains(string(lock), "tiny-1.0.0.tgz") {
			t.Fatalf("initial lock = %s", lock)
		}

		// Unchanged manifest: nothing to write.
		out, err = Locker{}.Process(t.Context(), dir, manifest("1.0.0"), map[string][]byte{PackageLock: lock, npmrc: rc})
		if err != nil || out != nil {
			t.Fatalf("unchanged: out = %v, err = %v", out, err)
		}

		out, err = Locker{}.Process(t.Context(), dir, manifest("^1.1.0"), map[string][]byte{PackageLock: lock, npmrc: rc})
		if err != nil {
			t.Fatal(err)
		}
		if len(out) != 1 || !strings.Contains(string(out[PackageLock]), "tiny-1.1.0.tgz") || !strings.Contains(string(out[PackageLock]), `"tiny": "^1.1.0"`) {
			t.Errorf("updated lock = %s", out[PackageLock])
		}
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("dir was written: %v", entries)
		}
	})

	t.Run("npm failure", func(t *testing.T) {
		dir := t.TempDir()
		_, err := Locker{}.Process(t.Context(), dir, []byte("{"), map[string][]byte{PackageLock: []byte("{}"), npmrc: rc})
		if err == nil || !strings.HasPrefix(err.Error(), "npm install in "+dir+": ") || !strings.Contains(err.Error(), "JSON") {
			t.Errorf("err = %v", err)
		}
	})
}

func TestProcessWithoutNpm(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	dir := t.TempDir()
	out, err := Locker{}.Process(t.Context(), dir, manifest("1.0.0"), map[string][]byte{npmrc: []byte("x=y\n")})
	if err != nil || out != nil {
		t.Errorf("no lockfile: out = %v, err = %v", out, err)
	}
	_, err = Locker{}.Process(t.Context(), dir, manifest("1.0.0"), map[string][]byte{PackageLock: []byte("{}")})
	if err == nil || err.Error() != "npm not found in PATH (needed to update package-lock.json)" {
		t.Errorf("err = %v", err)
	}
	for _, n := range unsupported {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, n), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := Locker{}.Process(t.Context(), d, manifest("1.0.0"), map[string][]byte{PackageLock: []byte("{}")})
		if err == nil || err.Error() != "lockfile "+n+" not supported" {
			t.Errorf("%s: err = %v", n, err)
		}
	}
	if got := (Locker{}).Siblings(); strings.Join(got, ",") != "package-lock.json,.npmrc" {
		t.Errorf("Siblings = %v", got)
	}
}

func TestTail(t *testing.T) {
	for _, tt := range []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb\nc", 5, "a\nb\nc"},
		{"", 3, ""},
	} {
		if got := tail([]byte(tt.in), tt.n); got != tt.want {
			t.Errorf("tail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}
