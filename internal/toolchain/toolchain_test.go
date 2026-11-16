package toolchain

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
)

// tarGz builds a gzipped tar holding one executable at top/<name>.
func tarGz(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf strings.Builder
	zw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(zw)
	for _, e := range []struct {
		path string
		mode int64
		data string
	}{
		{"top/bin/" + name, 0o755, body},
		{"top/share/doc", 0o644, "doc"},
	} {
		if err := tw.WriteHeader(&tar.Header{Name: e.path, Mode: e.mode, Size: int64(len(e.data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e.data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := errors.Join(tw.Close(), zw.Close()); err != nil {
		t.Fatal(err)
	}
	return []byte(buf.String())
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// server serves the archive and its checksum file, counting the archive downloads.
func server(t *testing.T, archive []byte) (*httptest.Server, *int) {
	t.Helper()
	var mu sync.Mutex
	hits := 0
	mux := http.NewServeMux()
	mux.HandleFunc("/tool.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		hits++
		mu.Unlock()
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/tool.tar.gz.sha256", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  tool.tar.gz\n", sha256Hex(archive))
	})
	mux.HandleFunc("/wrong.tar.gz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(archive)
	})
	mux.HandleFunc("/wrong.tar.gz.sha256", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintln(w, strings.Repeat("0", 64)+"  wrong.tar.gz")
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, &hits
}

// testManager wires a Manager to a one-entry catalog served by s.
func testManager(t *testing.T, s *httptest.Server, file string) *Manager {
	t.Helper()
	root := t.TempDir()
	m := &Manager{Root: filepath.Join(root, "tools"), State: filepath.Join(root, "state"), Client: s.Client()}
	m.Entries = map[Tool]Entry{"demo": {
		Tool: "demo",
		Download: func(ctx context.Context, m *Manager, _, _, _ string) (Download, error) {
			url := s.URL + "/" + file
			sum, err := m.sibling(ctx, url, ".sha256", file)
			if err != nil {
				return Download{}, err
			}
			return Download{URL: url, Sum: sum, Hash: SHA256}, nil
		},
		Archive:  TarGz,
		Strip:    1,
		Provides: map[string]string{"demo": "bin/demo"},
		Env: func(m *Manager, dir string) []string {
			return []string{pathEnv(filepath.Join(dir, "bin")), "DEMO_HOME=" + dir}
		},
	}}
	return m
}

func TestEnsureInstallsOnceAndVerifies(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not installed")
	}
	archive := tarGz(t, "demo", "#!/bin/sh\necho demo\n")
	s, hits := server(t, archive)
	m := testManager(t, s, "tool.tar.gz")

	in, err := m.Ensure(t.Context(), "demo", "1.2.3")
	if err != nil {
		t.Fatal(err)
	}
	if got := in.Bins["demo"]; !strings.HasSuffix(got, filepath.Join("demo", "1.2.3", m.goos()+"-"+m.goarch(), "bin", "demo")) {
		t.Errorf("install path = %s", got)
	}
	st, err := os.Stat(in.Bins["demo"])
	if err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Fatalf("binary = %v, %v; want it executable", st, err)
	}

	// A second process sees the .ok marker and downloads nothing.
	fresh := testManager(t, s, "tool.tar.gz")
	fresh.Root, fresh.State = m.Root, m.State
	if _, err := fresh.Ensure(t.Context(), "demo", "1.2.3"); err != nil {
		t.Fatal(err)
	}
	if *hits != 1 {
		t.Errorf("downloads = %d, want 1", *hits)
	}
}

func TestEnsureConcurrentInstallsDownloadOnce(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not installed")
	}
	s, hits := server(t, tarGz(t, "demo", "x"))
	m := testManager(t, s, "tool.tar.gz")
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Go(func() {
			// Each goroutine uses its own Manager over the same directories, so the
			// in-process memo cannot hide a missing cross-process lock.
			n := testManager(t, s, "tool.tar.gz")
			n.Root, n.State = m.Root, m.State
			_, errs[i] = n.Ensure(t.Context(), "demo", "1.2.3")
		})
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("install %d: %v", i, err)
		}
	}
	if *hits != 1 {
		t.Errorf("downloads = %d, want 1", *hits)
	}
}

func TestEnsureRefusesAChecksumMismatch(t *testing.T) {
	s, _ := server(t, tarGz(t, "demo", "x"))
	m := testManager(t, s, "wrong.tar.gz")
	_, err := m.Ensure(t.Context(), "demo", "1.2.3")
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("err = %v, want a checksum mismatch", err)
	}
	if _, statErr := os.Stat(filepath.Join(m.Root, "demo", "1.2.3", m.goos()+"-"+m.goarch())); !os.IsNotExist(statErr) {
		t.Errorf("a failed install left an installation behind: %v", statErr)
	}
	entries, _ := os.ReadDir(m.Root)
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "download-") {
			t.Errorf("a failed install left the download %s behind", e.Name())
		}
	}
}

func TestEnsureWithoutAVersion(t *testing.T) {
	s, _ := server(t, tarGz(t, "demo", "x"))
	m := testManager(t, s, "tool.tar.gz")
	_, err := m.Ensure(t.Context(), "demo", "")
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("err = %v, want exec.ErrNotFound", err)
	}
	m.Pins = map[Tool]string{"demo": "1.2.3"}
	if _, err := m.Ensure(t.Context(), "demo", ""); err != nil {
		t.Errorf("with a pin: %v", err)
	}
}

func TestProviderEnvAndLookup(t *testing.T) {
	if _, err := exec.LookPath("tar"); err != nil {
		t.Skip("tar not installed")
	}
	s, _ := server(t, tarGz(t, "demo", "x"))
	m := testManager(t, s, "tool.tar.gz")
	p := m.For(map[Tool]string{"demo": "1.2.3"})
	if env := p.Env(t.Context()); env != nil {
		t.Errorf("Env before any lookup = %q, want none", env)
	}
	bin, err := p.Lookup(t.Context(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	env := p.Env(t.Context())
	if len(env) == 0 || !strings.HasPrefix(env[0], "PATH="+filepath.Dir(bin)+string(os.PathListSeparator)) {
		t.Errorf("Env = %q; want the tool's bin directory first on PATH", env)
	}
	if !strings.Contains(strings.Join(env, " "), "DEMO_HOME=") {
		t.Errorf("Env = %q; want the tool's own variables", env)
	}
	if _, err := p.Lookup(t.Context(), "other"); !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("unknown command = %v, want exec.ErrNotFound", err)
	}
}

func TestSumOf(t *testing.T) {
	list := "aa" + strings.Repeat("b", 62) + "  other.tar.gz\n" +
		"cc" + strings.Repeat("d", 62) + " *tool.tar.gz\n"
	got, err := sumOf([]byte(list), "tool.tar.gz")
	if err != nil || got != "cc"+strings.Repeat("d", 62) {
		t.Errorf("sumOf list = %q, %v", got, err)
	}
	bare := strings.Repeat("e", 64) + "\n"
	if got, err := sumOf([]byte(bare), "anything"); err != nil || got != strings.Repeat("e", 64) {
		t.Errorf("sumOf bare = %q, %v", got, err)
	}
	if _, err := sumOf([]byte(list), "missing.tar.gz"); err == nil {
		t.Error("sumOf accepted a name the list does not hold")
	}
}
