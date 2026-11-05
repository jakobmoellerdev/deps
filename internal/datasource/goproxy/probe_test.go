package goproxy_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/goproxy"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/gomod"
)

// hangingProxy serves lists, keyed by "<proxy>/<module>", and never answers any other
// "/@v/list" (a missing major path proxy.golang.org hangs on), counting requests per path.
type hangingProxy struct {
	lists map[string]string
	mu    sync.Mutex
	hits  map[string]int
}

func (p *hangingProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/")
	p.mu.Lock()
	p.hits[path]++
	p.mu.Unlock()
	mod, ok := strings.CutSuffix(path, "/@v/list")
	if !ok {
		http.NotFound(w, r)
		return
	}
	if body, found := p.lists[mod]; found {
		_, _ = io.WriteString(w, body)
		return
	}
	<-r.Context().Done()
}

func (p *hangingProxy) count(path string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits[path]
}

func serveHanging(t *testing.T, lists map[string]string) (*hangingProxy, string) {
	t.Helper()
	p := &hangingProxy{lists: lists, hits: map[string]int{}}
	srv := httptest.NewServer(p)
	t.Cleanup(srv.Close)
	return p, srv.URL
}

// captureWarnings installs a default slog logger at warn level writing to the returned buffer.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

// TestProbeTimeout: a proxy that never answers a major path probe costs one request of at
// most the probe timeout, without a retry, and the dep keeps its in-major update.
func TestProbeTimeout(t *testing.T) {
	const budget = 300 * time.Millisecond
	p, url := serveHanging(t, map[string]string{"p/example.com/m": "v1.0.0\nv1.1.0\n"})
	logs := captureWarnings(t)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/x\n\nrequire example.com/m v1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	s := goproxy.NewForProxy(httpx.NewClient(), url+"/p", budget)
	start := time.Now()
	res, err := engine.Resolve(t.Context(), root, cfg, manager.Env{DefaultHost: "github.com"},
		engine.Options{Managers: []manager.Manager{gomod.New()}, Datasources: map[string]engine.Datasource{"go": s}})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 5*budget {
		t.Errorf("resolve took %v, probe budget %v", d, budget)
	}
	if len(res.Errors) > 0 {
		t.Errorf("errors = %v, want none (a timed-out probe is no newer major)", res.Errors)
	}
	if len(res.Updates) != 1 || res.Updates[0].NewVersion != "v1.1.0" || res.Updates[0].NewName != "" {
		t.Errorf("updates = %+v, want the in-major v1.1.0", res.Updates)
	}
	if n := p.count("p/example.com/m/v2/@v/list"); n != 1 {
		t.Errorf("%d probe requests, want 1 (no retry of a timed-out probe)", n)
	}
	out := logs.String()
	for _, want := range []string{"level=WARN", `msg="major module path probe timed out; assuming no newer major"`, "module=example.com/m/v2", "timeout=300ms"} {
		if !strings.Contains(out, want) {
			t.Errorf("log lacks %q:\n%s", want, out)
		}
	}
}

// TestProbeTimeoutFallback: with `|` the next proxy answers a probe the first one timed
// out on; the run's own cancellation still fails the probe.
func TestProbeTimeoutFallback(t *testing.T) {
	const budget = 200 * time.Millisecond
	p, url := serveHanging(t, map[string]string{"b/example.com/m/v2": "v2.0.0\n"})
	captureWarnings(t)
	s := goproxy.NewForProxy(httpx.NewClient(), url+"/a|"+url+"/b", budget)
	var mp datasource.MajorPathSource = s
	got, err := mp.MajorPaths(t.Context(), dep.Dep{Name: "example.com/m", Version: "v1.0.0"}, "")
	if err != nil || len(got) != 1 || got[0].Name != "example.com/m/v2" {
		t.Fatalf("MajorPaths = %+v, %v; want example.com/m/v2 from the second proxy", got, err)
	}
	if a, b := p.count("a/example.com/m/v2/@v/list"), p.count("b/example.com/m/v2/@v/list"); a != 1 || b != 1 {
		t.Errorf("requests a=%d b=%d, want 1 each", a, b)
	}

	ctx, cancel := context.WithTimeout(t.Context(), budget/2)
	defer cancel()
	if _, err := goproxy.NewForProxy(httpx.NewClient(), url+"/a", budget).MajorPaths(ctx, dep.Dep{Name: "example.com/n", Version: "v1.0.0"}, ""); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("MajorPaths after the run's deadline = %v, want its error", err)
	}
}
