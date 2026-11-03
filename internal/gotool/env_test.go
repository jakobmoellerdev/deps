package gotool

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func needGo(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	t.Setenv("GOTOOLCHAIN", "local")
	t.Setenv("GOENV", "off")
}

func TestEnv(t *testing.T) {
	needGo(t)
	tests := []struct {
		name                    string
		proxy, noproxy, private string
		wantProxy, wantNoProxy  string
	}{
		{"gonoproxy wins", "https://p.example", "a.example", "b.example", "https://p.example", "a.example"},
		{"goprivate fallback", "https://p.example,direct", "", "b.example/*", "https://p.example,direct", "b.example/*"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GOPROXY", tt.proxy)
			t.Setenv("GONOPROXY", tt.noproxy)
			t.Setenv("GOPRIVATE", tt.private)
			e, err := Env(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if e.Proxy != tt.wantProxy || e.NoProxy != tt.wantNoProxy {
				t.Fatalf("Env = %+v; want proxy %q noproxy %q", e, tt.wantProxy, tt.wantNoProxy)
			}
		})
	}
}

func TestEnvWithoutGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	t.Setenv("GOPROXY", "")
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "corp.example")
	e, err := Env(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if e.Proxy != DefaultProxy || e.NoProxy != "corp.example" {
		t.Fatalf("Env = %+v", e)
	}

	t.Setenv("GOPROXY", "https://p.example")
	t.Setenv("GONOPROXY", "n.example")
	if e, _ := Env(t.Context()); e.Proxy != "https://p.example" || e.NoProxy != "n.example" {
		t.Fatalf("Env = %+v", e)
	}

	if _, err := ListVersions(t.Context(), t.TempDir(), "example.com/m"); err == nil {
		t.Fatal("ListVersions without go succeeded")
	}
}

func TestEnvError(t *testing.T) {
	tests := []struct{ name, script, want string }{
		{"go fails", "echo boom >&2; exit 1", "go env -json GOPROXY GONOPROXY GOPRIVATE GOMODCACHE: boom"},
		{"bad json", "echo not-json", "go env: "},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\n"+tt.script+"\n"), 0o755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin)
			if _, err := Env(t.Context()); err == nil || !strings.HasPrefix(err.Error(), tt.want) {
				t.Fatalf("err = %v, want prefix %q", err, tt.want)
			}
		})
	}
}

func TestListVersions(t *testing.T) {
	needGo(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/example.com/!my/mod/@v/list":
			fmt.Fprint(w, "v1.0.0\nv1.2.0\nv1.1.0\n")
		case "/example.com/!my/mod/@v/v1.2.0.info":
			fmt.Fprint(w, `{"Version":"v1.2.0","Time":"2026-01-01T00:00:00Z"}`)
		case "/example.com/!my/mod/@v/v1.2.0.mod":
			fmt.Fprint(w, "module example.com/My/mod\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("GOPROXY", srv.URL)
	t.Setenv("GONOPROXY", "")
	t.Setenv("GOPRIVATE", "")
	t.Setenv("GOSUMDB", "off")
	t.Setenv("GONOSUMDB", "")
	t.Setenv("GOFLAGS", "")
	t.Setenv("GOMODCACHE", t.TempDir())
	t.Setenv("GOPATH", t.TempDir())
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/x\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	vs, err := ListVersions(t.Context(), dir, "example.com/My/mod")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"v1.0.0", "v1.1.0", "v1.2.0"}; !slices.Equal(vs, want) {
		t.Fatalf("versions = %v, want %v", vs, want)
	}

	_, err = ListVersions(t.Context(), dir, "example.com/missing")
	if err == nil || !strings.HasPrefix(err.Error(), "go list -m -versions -json example.com/missing: ") {
		t.Fatalf("err = %v", err)
	}
	if strings.HasSuffix(err.Error(), ": ") {
		t.Fatalf("err lacks stderr tail: %v", err)
	}
}

func TestCmdErrorWithoutStderr(t *testing.T) {
	e := &cmdError{args: "mod tidy", err: fmt.Errorf("exit status 1")}
	if got := e.Error(); got != "go mod tidy: exit status 1" {
		t.Fatalf("Error() = %q", got)
	}
}

func TestEnvModCache(t *testing.T) {
	needGo(t)
	dir := t.TempDir()
	t.Setenv("GOMODCACHE", dir)
	if e, err := Env(t.Context()); err != nil || e.ModCache != dir {
		t.Fatalf("Env = %+v, %v; want ModCache %q", e, err, dir)
	}
}

func TestEnvModCacheWithoutGo(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	sep := string(os.PathListSeparator)
	home := t.TempDir()
	t.Setenv("HOME", home)
	for _, tt := range []struct{ gomodcache, gopath, want string }{
		{"/m", "/g", "/m"},
		{"", "/g1" + sep + "/g2", filepath.Join(string(filepath.Separator)+"g1", "pkg", "mod")},
		{"", "", filepath.Join(home, "go", "pkg", "mod")},
	} {
		t.Setenv("GOMODCACHE", tt.gomodcache)
		t.Setenv("GOPATH", tt.gopath)
		if e, err := Env(t.Context()); err != nil || e.ModCache != tt.want {
			t.Errorf("GOMODCACHE=%q GOPATH=%q: ModCache = %q, %v; want %q", tt.gomodcache, tt.gopath, e.ModCache, err, tt.want)
		}
	}
}
