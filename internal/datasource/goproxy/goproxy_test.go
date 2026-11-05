package goproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/mod/module"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gotool"
)

// proxyServer serves "/<name>/<module>/@v/list": "ok" proxies answer with a list, "nf" with 404,
// "gone" with 410 and "err" with 500. hits counts requests per proxy name.
func proxyServer(t *testing.T, hits map[string]*atomic.Int32) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if c := hits[name]; c != nil {
			c.Add(1)
		}
		if !strings.HasSuffix(rest, "/@v/list") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		switch name {
		case "ok":
			io.WriteString(w, "v1.0.0\nv1.1.0 2024-01-01T00:00:00Z\n\n"+rest+"\n")
		case "nf":
			http.NotFound(w, r)
		case "gone":
			w.WriteHeader(http.StatusGone)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestVersions(t *testing.T) {
	listed := []string{"v0.1.0", "v0.2.0"}
	tests := []struct {
		name     string
		proxy    string // "{}" is replaced by the server URL
		noProxy  string
		modPath  string
		want     []string
		wantErr  string // substring
		wantHits map[string]int32
		wantList int32
	}{
		{
			name: "first proxy answers", proxy: "{}/ok,direct", modPath: "example.com/m",
			want: []string{"v1.0.0", "v1.1.0", "example.com/m/@v/list"}, wantHits: map[string]int32{"ok": 1},
		},
		{
			name: "404 falls through on comma", proxy: "{}/nf,{}/ok", modPath: "example.com/m",
			want: []string{"v1.0.0", "v1.1.0", "example.com/m/@v/list"}, wantHits: map[string]int32{"nf": 1, "ok": 1},
		},
		{
			name: "410 falls through on comma", proxy: "{}/gone,{}/ok", modPath: "example.com/m",
			want: []string{"v1.0.0", "v1.1.0", "example.com/m/@v/list"}, wantHits: map[string]int32{"gone": 1, "ok": 1},
		},
		{
			name: "500 stops on comma", proxy: "{}/err,{}/ok", modPath: "example.com/m",
			wantErr: "/err/example.com/m/@v/list: HTTP 500", wantHits: map[string]int32{"err": 1, "ok": 0},
		},
		{
			name: "500 falls through on pipe", proxy: "{}/err|{}/ok", modPath: "example.com/m",
			want: []string{"v1.0.0", "v1.1.0", "example.com/m/@v/list"}, wantHits: map[string]int32{"err": 1, "ok": 1},
		},
		{
			name: "404 then direct", proxy: "{}/nf,direct", modPath: "example.com/m",
			want: listed, wantHits: map[string]int32{"nf": 1}, wantList: 1,
		},
		{
			name: "noproxy uses go list", proxy: "{}/ok", noProxy: "corp.example/*,example.com/private",
			modPath: "example.com/private/x", want: listed, wantHits: map[string]int32{"ok": 0}, wantList: 1,
		},
		{
			name: "path escaping", proxy: "{}/ok/", modPath: "github.com/Masterminds/semver/v3",
			want: []string{"v1.0.0", "v1.1.0", "github.com/!masterminds/semver/v3/@v/list"},
		},
		{name: "off", proxy: "off", modPath: "example.com/m", wantErr: "GOPROXY=off"},
		{name: "exhausted", proxy: "{}/nf", modPath: "example.com/m", wantErr: "no proxy has example.com/m"},
		{
			name: "empty entries skipped", proxy: " ,{}/nf,, {}/ok", modPath: "example.com/m",
			want: []string{"v1.0.0", "v1.1.0", "example.com/m/@v/list"}, wantHits: map[string]int32{"nf": 1, "ok": 1},
		},
		{name: "invalid module path", proxy: "{}/ok", modPath: "example.com/a b", wantErr: "malformed module path", wantHits: map[string]int32{"ok": 0}},
		{name: "invalid proxy URL stops on comma", proxy: "http://%zz,{}/ok", modPath: "example.com/m", wantErr: "invalid URL escape", wantHits: map[string]int32{"ok": 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hits := map[string]*atomic.Int32{"ok": {}, "nf": {}, "gone": {}, "err": {}}
			srv := proxyServer(t, hits)
			var envCalls, listCalls atomic.Int32
			s := New(srv.Client(), make(chan struct{}, 1))
			s.env = func(context.Context) (gotool.GoEnv, error) {
				envCalls.Add(1)
				return gotool.GoEnv{Proxy: strings.ReplaceAll(tt.proxy, "{}", srv.URL), NoProxy: tt.noProxy}, nil
			}
			s.list = func(_ context.Context, dir, path string) ([]string, error) {
				listCalls.Add(1)
				if dir != "/mod/dir" || path != tt.modPath {
					t.Errorf("list(%q, %q)", dir, path)
				}
				return listed, nil
			}

			for range 2 { // second call must be served from the in-run cache
				got, err := s.Versions(context.Background(), tt.modPath, "/mod/dir")
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("err = %v, want %q", err, tt.wantErr)
					}
				} else if err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("Versions = %q, want %q", got, tt.want)
				}
			}
			for name, want := range tt.wantHits {
				if got := hits[name].Load(); got != want {
					t.Errorf("hits[%s] = %d, want %d", name, got, want)
				}
			}
			if got := listCalls.Load(); got != tt.wantList {
				t.Errorf("list calls = %d, want %d", got, tt.wantList)
			}
			if got := envCalls.Load(); got != 1 {
				t.Errorf("env calls = %d, want 1", got)
			}
		})
	}
}

func TestVersionsEnvError(t *testing.T) {
	s := New(http.DefaultClient, nil)
	s.env = func(context.Context) (gotool.GoEnv, error) { return gotool.GoEnv{}, errors.New("boom") }
	if _, err := s.Versions(context.Background(), "example.com/m", "."); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestDatasource(t *testing.T) {
	s := New(http.DefaultClient, nil)
	s.env = func(context.Context) (gotool.GoEnv, error) { return gotool.GoEnv{Proxy: "direct"}, nil }
	var gotDir string
	s.list = func(_ context.Context, dir, _ string) ([]string, error) {
		gotDir = dir
		return []string{"v1.0.0", "v1.1.0"}, nil
	}
	var ds datasource.Datasource = s
	d := dep.Dep{Name: "example.com/m"}
	rels, err := ds.Releases(t.Context(), d, "/mod")
	if err != nil || !reflect.DeepEqual(rels, []datasource.Release{{Version: "v1.0.0"}, {Version: "v1.1.0"}}) || gotDir != "/mod" {
		t.Fatalf("Releases = %+v, %v (dir %q)", rels, err, gotDir)
	}
	if _, err := ds.Digest(t.Context(), d, "v1.0.0"); err == nil || err.Error() != "example.com/m@v1.0.0: go modules have no digests" {
		t.Errorf("Digest err = %v", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type errReader struct{ err error }

func (r errReader) Read([]byte) (int, error) { return 0, r.err }

func TestVersionsTransportErrors(t *testing.T) {
	boom := errors.New("boom")
	const list = "https://proxy.test/example.com/m/@v/list"
	tests := []struct {
		name string
		rt   roundTripFunc
		want string
	}{
		{
			name: "request fails",
			rt:   func(*http.Request) (*http.Response, error) { return nil, boom },
			want: `Get "` + list + `": boom`,
		},
		{
			name: "body read fails",
			rt: func(*http.Request) (*http.Response, error) {
				body := io.MultiReader(strings.NewReader("v1.0.0\n"), errReader{boom})
				return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(body)}, nil
			},
			want: "GET " + list + ": boom",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(&http.Client{Transport: tt.rt}, nil)
			s.env = func(context.Context) (gotool.GoEnv, error) {
				return gotool.GoEnv{Proxy: "https://proxy.test,direct"}, nil
			}
			s.list = func(context.Context, string, string) ([]string, error) {
				t.Error("comma must not fall through on a transport error")
				return nil, nil
			}
			vs, err := s.Versions(t.Context(), "example.com/m", ".")
			if err == nil || err.Error() != tt.want || !errors.Is(err, boom) || vs != nil {
				t.Fatalf("Versions = %q, %v; want error %s", vs, err, tt.want)
			}
		})
	}
}

func TestVersionsCanceledWaitingForSlot(t *testing.T) {
	for _, proxy := range []string{"https://proxy.test", "direct"} {
		t.Run(proxy, func(t *testing.T) {
			sem := make(chan struct{}, 1)
			sem <- struct{}{}
			s := New(&http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("request sent without a slot")
				return nil, errors.New("unexpected")
			})}, sem)
			s.env = func(context.Context) (gotool.GoEnv, error) { return gotool.GoEnv{Proxy: proxy}, nil }
			s.list = func(context.Context, string, string) ([]string, error) {
				t.Error("go list run without a slot")
				return nil, nil
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			if _, err := s.Versions(ctx, "example.com/m", "."); !errors.Is(err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", err)
			}
		})
	}
}

func TestReleaseTime(t *testing.T) {
	when := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/p/github.com/!masterminds/semver/v3/@v/v3.5.0.info":
			io.WriteString(w, `{"Version":"v3.5.0","Time":"2026-10-30T12:00:00Z"}`)
		case "/p/example.com/notime/@v/v1.0.0.info":
			io.WriteString(w, `{"Version":"v1.0.0"}`)
		case "/p/example.com/bad/@v/v1.0.0.info":
			io.WriteString(w, `{`)
		case "/p/example.com/err/@v/v1.0.0.info":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	tests := []struct {
		name, proxy, noProxy, mod, version string
		want                               time.Time
		wantErr                            string
		wantInfo                           int32 // go list calls
	}{
		{name: "proxy info, escaped path", proxy: "{}/p", mod: "github.com/Masterminds/semver/v3", version: "v3.5.0", want: when},
		{name: "no time is unknown", proxy: "{}/p", mod: "example.com/notime", version: "v1.0.0"},
		{name: "404 falls through to direct", proxy: "{}/p,direct", mod: "example.com/private", version: "v1.0.0", want: when.Add(time.Hour), wantInfo: 1},
		{name: "GOPRIVATE uses go list", proxy: "{}/p", noProxy: "example.com/*", mod: "example.com/err", version: "v1.0.0", want: when.Add(time.Hour), wantInfo: 1},
		{name: "500 stops on comma", proxy: "{}/p,direct", mod: "example.com/err", version: "v1.0.0", wantErr: "HTTP 500"},
		{name: "malformed info", proxy: "{}/p", mod: "example.com/bad", version: "v1.0.0", wantErr: "GET " + srv.URL + "/p/example.com/bad/@v/v1.0.0.info"},
		{name: "invalid version", proxy: "{}/p", mod: "example.com/m", version: "v1!0", wantErr: "disallowed version string"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := New(srv.Client(), make(chan struct{}, 1))
			s.env = func(context.Context) (gotool.GoEnv, error) {
				return gotool.GoEnv{Proxy: strings.ReplaceAll(tt.proxy, "{}", srv.URL), NoProxy: tt.noProxy}, nil
			}
			s.list = func(context.Context, string, string) ([]string, error) { return []string{"v1.0.0"}, nil }
			var infoCalls atomic.Int32
			s.info = func(_ context.Context, dir, path, version string) (time.Time, error) {
				infoCalls.Add(1)
				if dir != "/mod/dir" || path != tt.mod || version != tt.version {
					t.Errorf("info(%q, %q, %q)", dir, path, version)
				}
				return when.Add(time.Hour), nil
			}
			d := dep.Dep{Name: tt.mod}
			if _, err := s.Releases(t.Context(), d, "/mod/dir"); err != nil && tt.wantErr == "" && tt.wantInfo > 0 {
				t.Fatal(err)
			}
			var ds datasource.Timer = s
			for range 2 { // deduped per module@version
				got, err := ds.ReleaseTime(t.Context(), d, tt.version)
				if tt.wantErr != "" {
					if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
						t.Fatalf("err = %v, want %q", err, tt.wantErr)
					}
					continue
				}
				if err != nil || !got.Equal(tt.want) {
					t.Fatalf("ReleaseTime = %v, %v; want %v", got, err, tt.want)
				}
			}
			if got := infoCalls.Load(); got != tt.wantInfo {
				t.Errorf("go list calls = %d, want %d", got, tt.wantInfo)
			}
		})
	}
}

func TestReleaseTimeDefaultDir(t *testing.T) {
	s := New(http.DefaultClient, nil)
	s.env = func(context.Context) (gotool.GoEnv, error) { return gotool.GoEnv{Proxy: "direct"}, nil }
	var gotDir string
	s.info = func(_ context.Context, dir, _, _ string) (time.Time, error) {
		gotDir = dir
		return time.Time{}, errors.New("boom")
	}
	if _, err := s.ReleaseTime(t.Context(), dep.Dep{Name: "example.com/m"}, "v1.0.0"); err == nil || err.Error() != "boom" || gotDir != "." {
		t.Fatalf("err = %v, dir %q", err, gotDir)
	}
}

// A version's .info in GOMODCACHE answers without any request, also for private modules;
// missing, unreadable or timeless cache entries fall back to the proxy.
func TestReleaseTimeModuleCache(t *testing.T) {
	cached := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
	remote := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	cache := t.TempDir()
	write := func(mod, file, body string) {
		esc, _ := module.EscapePath(mod)
		dir := filepath.Join(cache, "cache", "download", filepath.FromSlash(esc), "@v")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("github.com/Masterminds/semver/v3", "v3.5.0.info", `{"Version":"v3.5.0","Time":"2025-01-02T03:04:05Z"}`)
	write("example.com/m", "v1.0.0-rc.1.info", `{"Version":"v1.0.0-rc.1","Time":"2025-01-02T03:04:05Z"}`)
	write("example.com/notime", "v1.0.0.info", `{"Version":"v1.0.0"}`)
	write("example.com/broken", "v1.0.0.info", `{`)

	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		io.WriteString(w, `{"Time":"2026-10-30T12:00:00Z"}`)
	}))
	t.Cleanup(srv.Close)
	for _, tt := range []struct {
		mod, version, noProxy string
		want                  time.Time
		requests              int32
	}{
		{mod: "github.com/Masterminds/semver/v3", version: "v3.5.0", want: cached},           // escaped path
		{mod: "example.com/m", version: "v1.0.0-rc.1", noProxy: "example.com", want: cached}, // private: no go list either
		{mod: "example.com/m", version: "v1.1.0", want: remote, requests: 1},                 // not cached
		{mod: "example.com/notime", version: "v1.0.0", want: remote, requests: 1},
		{mod: "example.com/broken", version: "v1.0.0", want: remote, requests: 1},
	} {
		hits.Store(0)
		s := New(srv.Client(), nil)
		s.env = func(context.Context) (gotool.GoEnv, error) {
			return gotool.GoEnv{Proxy: srv.URL, NoProxy: tt.noProxy, ModCache: cache}, nil
		}
		s.info = func(context.Context, string, string, string) (time.Time, error) {
			t.Errorf("%s@%s: go list called", tt.mod, tt.version)
			return time.Time{}, nil
		}
		got, err := s.ReleaseTime(t.Context(), dep.Dep{Name: tt.mod}, tt.version)
		if err != nil || !got.Equal(tt.want) || hits.Load() != tt.requests {
			t.Errorf("%s@%s = %v, %v, %d requests; want %v, %d", tt.mod, tt.version, got, err, hits.Load(), tt.want, tt.requests)
		}
	}
}

// DeclaredPath reads the module directive of a version's .mod: from GOMODCACHE without a
// request, else from the proxy, once per module@version; private modules are unknown.
func TestDeclaredPath(t *testing.T) {
	cache := t.TempDir()
	dir := filepath.Join(cache, "cache", "download", "example.com", "cached", "@v")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "v1.0.0.mod"), []byte("module example.com/elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/github.com/!google/cel-go/@v/v0.32.0.mod": // escaped path
			io.WriteString(w, "module cel.dev/cel-go\n\ngo 1.23.0\n")
		case "/github.com/!google/cel-go/@v/v0.31.0.mod":
			io.WriteString(w, "module github.com/Google/cel-go\n")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(srv.Client(), nil)
	s.env = func(context.Context) (gotool.GoEnv, error) {
		return gotool.GoEnv{Proxy: srv.URL, NoProxy: "corp.example", ModCache: cache}, nil
	}
	for _, tt := range []struct {
		mod, version, want string
		wantErr            bool
		requests           int32
	}{
		{mod: "github.com/Google/cel-go", version: "v0.32.0", want: "cel.dev/cel-go", requests: 1},
		{mod: "github.com/Google/cel-go", version: "v0.32.0", want: "cel.dev/cel-go"}, // deduped
		{mod: "github.com/Google/cel-go", version: "v0.31.0", want: "github.com/Google/cel-go", requests: 1},
		{mod: "example.com/cached", version: "v1.0.0", want: "example.com/elsewhere"},
		{mod: "corp.example/m", version: "v1.0.0"}, // GONOPROXY: unknown
		{mod: "example.com/missing", version: "v1.0.0", wantErr: true, requests: 1},
	} {
		hits.Store(0)
		got, err := s.DeclaredPath(t.Context(), tt.mod, tt.version)
		if got != tt.want || (err != nil) != tt.wantErr || hits.Load() != tt.requests {
			t.Errorf("%s@%s = %q, %v, %d requests; want %q, %d", tt.mod, tt.version, got, err, hits.Load(), tt.want, tt.requests)
		}
	}
}

func TestHead(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		switch r.URL.Path {
		case "/p/k8s.io/kube-openapi/@latest":
			io.WriteString(w, `{"Version":"v0.0.0-20261014044854-ca3452a79603","Time":"2026-10-14T04:48:54Z","Origin":{"VCS":"git","Hash":"ca3452a7960344c2deae56f967aded124a7ca72c"}}`)
		case "/p/example.com/noorigin/@latest":
			io.WriteString(w, `{"Version":"v1.2.4-0.20240101000000-abcdefabcdef"}`)
		case "/p/example.com/bad/@latest":
			io.WriteString(w, `{`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(srv.Client(), make(chan struct{}, 2))
	s.env = func(context.Context) (gotool.GoEnv, error) {
		return gotool.GoEnv{Proxy: srv.URL + "/p,direct", NoProxy: "private.example"}, nil
	}
	var latestCalls atomic.Int32
	s.latest = func(_ context.Context, dir, path string) (gotool.Module, error) {
		latestCalls.Add(1)
		if dir != "/mod" {
			t.Errorf("latest dir %q", dir)
		}
		if path == "example.com/missing" {
			return gotool.Module{}, errors.New("no such module")
		}
		tm := time.Date(2025, 1, 2, 3, 4, 5, 0, time.UTC)
		return gotool.Module{Version: "v0.0.0-20250102030405-0123456789ab", Time: &tm}, nil
	}
	var hs datasource.HeadSource = s

	d := dep.Dep{Name: "k8s.io/kube-openapi"}
	for range 2 {
		rev, err := hs.Head(t.Context(), d, "/mod")
		want := datasource.Revision{Version: "v0.0.0-20261014044854-ca3452a79603", Digest: "ca3452a7960344c2deae56f967aded124a7ca72c", Time: time.Date(2026, 10, 14, 4, 48, 54, 0, time.UTC)}
		if err != nil || rev != want {
			t.Fatalf("Head = %+v, %v", rev, err)
		}
	}
	// The head's time answers ReleaseTime without another request.
	if tm, err := s.ReleaseTime(t.Context(), d, "v0.0.0-20261014044854-ca3452a79603"); err != nil || !tm.Equal(time.Date(2026, 10, 14, 4, 48, 54, 0, time.UTC)) {
		t.Errorf("ReleaseTime = %v, %v", tm, err)
	}
	if hits.Load() != 1 {
		t.Errorf("hits = %d, want 1", hits.Load())
	}

	rev, err := hs.Head(t.Context(), dep.Dep{Name: "example.com/noorigin"}, "/mod")
	if err != nil || rev.Digest != "abcdefabcdef" || !rev.Time.IsZero() {
		t.Errorf("no origin: %+v, %v", rev, err)
	}
	if _, err := hs.Head(t.Context(), dep.Dep{Name: "example.com/bad"}, "/mod"); err == nil || !strings.Contains(err.Error(), "/@latest") {
		t.Errorf("bad JSON: %v", err)
	}
	// 404 on the proxy falls through to direct (go list), as do GONOPROXY modules.
	rev, err = hs.Head(t.Context(), dep.Dep{Name: "private.example/m"}, "/mod")
	if err != nil || rev.Version != "v0.0.0-20250102030405-0123456789ab" || rev.Digest != "0123456789ab" || rev.Time.IsZero() {
		t.Errorf("direct: %+v, %v", rev, err)
	}
	if _, err := hs.Head(t.Context(), dep.Dep{Name: "example.com/missing"}, "/mod"); err == nil || err.Error() != "no such module" {
		t.Errorf("missing: %v", err)
	}
	if latestCalls.Load() != 2 {
		t.Errorf("latest calls = %d, want 2", latestCalls.Load())
	}
}

// A major path probe of a path that is a dependency itself reuses the dependency's `@v/list`,
// also while that lookup is still in flight. A lookup the go command answered, or one that
// failed, is not reused: the probe lists the path from the proxy itself.
func TestMajorPathsReuseLookup(t *testing.T) {
	var mu sync.Mutex
	hits := map[string]int{}
	first, unblock := make(chan struct{}), make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mod, _ := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/p/"), "/@v/list")
		mu.Lock()
		hits[mod]++
		n := hits[mod]
		mu.Unlock()
		switch {
		case mod == "example.com/m/v2" && n == 1:
			close(first)
			<-unblock
			io.WriteString(w, "v2.0.0\nv2.1.0\n")
		case mod == "example.com/m/v2", mod == "example.com/e/v2" && n > 1:
			io.WriteString(w, "v2.0.0\nv2.1.0\n")
		case mod == "example.com/e/v2":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(srv.Client(), nil)
	s.env = func(context.Context) (gotool.GoEnv, error) {
		return gotool.GoEnv{Proxy: srv.URL + "/p,direct"}, nil
	}
	s.list = func(context.Context, string, string) ([]string, error) { return []string{"v2.0.0"}, nil }
	names := func(ps []datasource.ModulePath) []string {
		var out []string
		for _, p := range ps {
			for _, r := range p.Releases {
				out = append(out, p.Name+"@"+r.Version)
			}
		}
		return out
	}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := s.Releases(t.Context(), dep.Dep{Name: "example.com/m/v2"}, "/mod"); err != nil {
			t.Errorf("Releases(m/v2): %v", err)
		}
	})
	<-first
	wg.Go(func() {
		got, err := s.MajorPaths(t.Context(), dep.Dep{Name: "example.com/m", Version: "v1.0.0"}, "/mod")
		if want := []string{"example.com/m/v2@v2.0.0", "example.com/m/v2@v2.1.0"}; err != nil || !reflect.DeepEqual(names(got), want) {
			t.Errorf("MajorPaths(m) = %q, %v; want %q", names(got), err, want)
		}
	})
	close(unblock)
	wg.Wait()

	// d/v2 is listed by the go command (`direct` after a 404), e/v2's lookup fails.
	if vs, err := s.Versions(t.Context(), "example.com/d/v2", "/mod"); err != nil || len(vs) != 1 {
		t.Fatalf("Versions(d/v2) = %q, %v", vs, err)
	}
	if got, err := s.MajorPaths(t.Context(), dep.Dep{Name: "example.com/d", Version: "v1.0.0"}, "/mod"); err != nil || got != nil {
		t.Errorf("MajorPaths(d) = %q, %v; want none (only proxies are probed)", names(got), err)
	}
	if _, err := s.Versions(t.Context(), "example.com/e/v2", "/mod"); err == nil {
		t.Fatal("Versions(e/v2): want the HTTP 500")
	}
	got, err := s.MajorPaths(t.Context(), dep.Dep{Name: "example.com/e", Version: "v1.0.0"}, "/mod")
	if want := []string{"example.com/e/v2@v2.0.0", "example.com/e/v2@v2.1.0"}; err != nil || !reflect.DeepEqual(names(got), want) {
		t.Errorf("MajorPaths(e) = %q, %v; want %q", names(got), err, want)
	}

	want := map[string]int{
		"example.com/m/v2": 1, "example.com/m/v3": 1,
		"example.com/d/v2": 2, "example.com/e/v2": 2, "example.com/e/v3": 1,
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(hits, want) {
		t.Errorf("@v/list requests = %v, want %v", hits, want)
	}
}

func TestMajorPaths(t *testing.T) {
	lists := map[string]string{
		"github.com/google/go-github/v93": "v93.0.0\nv93.1.0\n",
		"github.com/google/go-github/v94": "v94.0.0\n",
		"github.com/a/b/v2":               "v2.0.0\n",
		"github.com/a/b/v3":               "", // exists, no releases: stops
		"github.com/a/b/v4":               "v4.0.0\n",
		"gopkg.in/yaml.v4":                "v4.0.0\n",
		"github.com/docker/docker/v29":    "v29.0.0\n",
		"example.com/err/v2":              "!500",
	}
	for i := 2; i <= 10; i++ {
		lists["example.com/many/v"+strconv.Itoa(i)] = "v" + strconv.Itoa(i) + ".0.0\n"
	}
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		mod, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/p/"), "/@v/list")
		body, found := lists[mod]
		switch {
		case !ok || !found:
			http.NotFound(w, r)
		case body == "!500":
			w.WriteHeader(http.StatusInternalServerError)
		default:
			io.WriteString(w, body)
		}
	}))
	t.Cleanup(srv.Close)
	s := New(srv.Client(), make(chan struct{}, 2))
	s.env = func(context.Context) (gotool.GoEnv, error) {
		return gotool.GoEnv{Proxy: srv.URL + "/p,direct", NoProxy: "private.example"}, nil
	}
	s.list = func(context.Context, string, string) ([]string, error) {
		t.Error("major paths must not run go list")
		return nil, nil
	}
	var mp datasource.MajorPathSource = s
	names := func(ps []datasource.ModulePath) []string {
		var out []string
		for _, p := range ps {
			for _, r := range p.Releases {
				out = append(out, p.Name+"@"+r.Version)
			}
		}
		return out
	}
	tests := []struct {
		name, version string
		want          []string
		wantErr       string
	}{
		{"github.com/google/go-github/v92", "v92.0.0", []string{"github.com/google/go-github/v93@v93.0.0", "github.com/google/go-github/v93@v93.1.0", "github.com/google/go-github/v94@v94.0.0"}, ""},
		{"github.com/a/b", "v1.5.0", []string{"github.com/a/b/v2@v2.0.0"}, ""},
		{"gopkg.in/yaml.v3", "v3.0.1", []string{"gopkg.in/yaml.v4@v4.0.0"}, ""},
		{"github.com/docker/docker", "v28.5.2+incompatible", []string{"github.com/docker/docker/v29@v29.0.0"}, ""},
		{"example.com/none", "v1.0.0", nil, ""},
		{"private.example/m", "v1.0.0", nil, ""},
		{"example.com/many", "v1.0.0", []string{"example.com/many/v2@v2.0.0", "example.com/many/v3@v3.0.0", "example.com/many/v4@v4.0.0", "example.com/many/v5@v5.0.0", "example.com/many/v6@v6.0.0"}, ""},
		{"example.com/err", "v1.0.0", nil, "HTTP 500"},
		{"example.com/bad path", "v1.0.0", nil, "malformed module path"},
	}
	for _, tt := range tests {
		got, err := mp.MajorPaths(t.Context(), dep.Dep{Name: tt.name, Version: tt.version}, "/mod")
		if tt.wantErr != "" {
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("%s: err = %v, want %q", tt.name, err, tt.wantErr)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(names(got), tt.want) {
			t.Errorf("%s: MajorPaths = %q, %v; want %q", tt.name, names(got), err, tt.want)
		}
	}
	before := hits.Load()
	if _, err := mp.MajorPaths(t.Context(), dep.Dep{Name: "github.com/google/go-github/v92", Version: "v92.0.0"}, "/mod"); err != nil || hits.Load() != before {
		t.Errorf("probes must be deduped: %d new requests, %v", hits.Load()-before, err)
	}
}
