package goproxy

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
