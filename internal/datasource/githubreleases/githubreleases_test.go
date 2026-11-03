package githubreleases

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// pageJSON renders n releases starting at index from; every 10th is a draft, every 7th a
// prerelease.
func pageJSON(from, n int) string {
	parts := make([]string, n)
	for i := range n {
		k := from + i
		parts[i] = fmt.Sprintf(`{"tag_name":"v1.%d.0","draft":%t,"prerelease":%t,"body":"long text","assets":[{"name":"x"}]}`,
			k, k%10 == 9, k%7 == 6)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func newSource(t *testing.T, h http.HandlerFunc, auth *httpx.Auth) (*Source, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	s := New(srv.Client(), auth, make(chan struct{}, 2)).(*Source)
	s.githubAPI = srv.URL
	return s, srv
}

func versions(rels []datasource.Release) []string {
	vs := make([]string, len(rels))
	for i, r := range rels {
		if r.Digest != "" {
			vs[i] = r.Version + "@" + r.Digest
		} else {
			vs[i] = r.Version
		}
	}
	return vs
}

func TestReleasesPagination(t *testing.T) {
	for _, c := range []struct {
		name  string
		total int // releases upstream
		pages int // expected requests
	}{
		{"empty", 0, 1},
		{"short page", 30, 1},
		{"exact page", 100, 2},
		{"three pages", 250, 3},
		{"capped at five", 900, 5},
	} {
		t.Run(c.name, func(t *testing.T) {
			var reqs atomic.Int32
			s, _ := newSource(t, func(w http.ResponseWriter, r *http.Request) {
				reqs.Add(1)
				if r.URL.Path != "/repos/o/r/releases" || r.URL.Query().Get("per_page") != "100" {
					t.Errorf("unexpected request %s", r.URL)
				}
				if r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2022-11-28" ||
					!strings.HasPrefix(r.Header.Get("User-Agent"), "deps/") || r.Header.Get("Authorization") != "" {
					t.Errorf("unexpected headers %v", r.Header)
				}
				page, _ := strconv.Atoi(r.URL.Query().Get("page"))
				from := (page - 1) * 100
				n := max(0, min(100, c.total-from))
				w.Header().Set("X-RateLimit-Remaining", "59")
				_, _ = w.Write([]byte(pageJSON(from, n)))
			}, nil)
			rels, err := s.Releases(context.Background(), dep.Dep{Name: "o/r"}, "")
			if err != nil {
				t.Fatal(err)
			}
			if int(reqs.Load()) != c.pages {
				t.Errorf("got %d requests, want %d", reqs.Load(), c.pages)
			}
			var want []string
			for k := range min(c.total, 500) {
				if k%10 != 9 && k%7 != 6 {
					want = append(want, fmt.Sprintf("v1.%d.0", k))
				}
			}
			if got := versions(rels); !slices.Equal(got, want) {
				t.Errorf("got %v, want %v", got, want)
			}
		})
	}
}

func TestReleasesDedupe(t *testing.T) {
	var reqs atomic.Int32
	s, _ := newSource(t, func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		_, _ = w.Write([]byte(`[{"tag_name":"v2.0.0"},{"tag_name":""}]`))
	}, nil)
	var wg sync.WaitGroup
	for _, name := range []string{"o/r", "O/R", "o/r", "https://github.com/o/r.git"} {
		wg.Go(func() {
			rels, err := s.Releases(context.Background(), dep.Dep{Name: name}, "")
			if err != nil || !slices.Equal(versions(rels), []string{"v2.0.0"}) {
				t.Errorf("%s: %v, %v", name, rels, err)
			}
		})
	}
	wg.Wait()
	if reqs.Load() != 1 {
		t.Errorf("got %d requests, want 1", reqs.Load())
	}
	if d, err := s.Digest(context.Background(), dep.Dep{Name: "o/r"}, "v2.0.0"); d != "" || err != nil {
		t.Errorf("Digest = %q, %v", d, err)
	}
}

func TestReleasesAuthAndEnterprise(t *testing.T) {
	var gotAuth, gotPath string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		_, _ = w.Write([]byte(`[]`))
	}
	t.Run("github.com token", func(t *testing.T) {
		auth := httpx.NewAuth(platform.Info{Host: "git.example"}, "", func(k string) string {
			if k == "DEPS_GITHUB_COM_TOKEN" {
				return "ghtok"
			}
			return ""
		})
		s, _ := newSource(t, h, auth)
		if _, err := s.Releases(context.Background(), dep.Dep{Name: "o/r"}, ""); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "Bearer ghtok" || gotPath != "/repos/o/r/releases" {
			t.Errorf("auth %q path %q", gotAuth, gotPath)
		}
	})
	t.Run("enterprise host", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(h))
		defer srv.Close()
		host := strings.TrimPrefix(srv.URL, "http://")
		hostname, _, _ := strings.Cut(host, ":")
		auth := httpx.NewAuth(platform.Info{Host: hostname}, "ghes", func(string) string { return "" })
		s := New(srv.Client(), auth, nil)
		if _, err := s.Releases(context.Background(), dep.Dep{Name: srv.URL + "/o/r/"}, ""); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "Bearer ghes" || gotPath != "/api/v3/repos/o/r/releases" {
			t.Errorf("auth %q path %q", gotAuth, gotPath)
		}
	})
	t.Run("anonymous for other hosts", func(t *testing.T) {
		auth := httpx.NewAuth(platform.Info{Host: "git.example"}, "tok", func(string) string { return "" })
		s, _ := newSource(t, h, auth)
		if _, err := s.Releases(context.Background(), dep.Dep{Name: "o/r"}, ""); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "" {
			t.Errorf("auth %q, want anonymous", gotAuth)
		}
	})
}

func TestReleasesErrors(t *testing.T) {
	for _, c := range []struct {
		name string
		h    http.HandlerFunc
		dep  string
		want string
	}{
		{name: "invalid name", dep: "o", want: `invalid github repository "o"`},
		{name: "nested name", dep: "o/r/x", want: `invalid github repository "o/r/x"`},
		{name: "bad url", dep: "ftp://h/o/r", want: `invalid github repository "ftp://h/o/r"`},
		{name: "url without repo", dep: "https://h/o", want: `invalid github repository "https://h/o"`},
		{name: "not found", dep: "o/r", want: "releases o/r: HTTP 404", h: func(w http.ResponseWriter, _ *http.Request) {
			http.NotFound(w, nil)
		}},
		{name: "rate limited", dep: "o/r", want: "releases o/r: HTTP 403: rate limit exceeded (resets 2026-11-03T12:00:00Z)", h: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1793707200")
			w.WriteHeader(http.StatusForbidden)
		}},
		{name: "rate limited bad reset", dep: "o/r", want: "HTTP 429: rate limit exceeded (resets soon)", h: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "soon")
			w.WriteHeader(http.StatusTooManyRequests)
		}},
		{name: "object", dep: "o/r", want: "releases o/r: malformed response: not an array", h: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{"message":"x"}`))
		}},
		{name: "empty body", dep: "o/r", want: "releases o/r: malformed response: EOF", h: func(http.ResponseWriter, *http.Request) {}},
		{name: "bad element", dep: "o/r", want: "releases o/r: malformed response", h: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"tag_name":1}]`))
		}},
		{name: "truncated", dep: "o/r", want: "releases o/r: malformed response", h: func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"tag_name":"v1"}`))
		}},
		{name: "second page fails", dep: "o/r", want: "releases o/r: HTTP 500", h: func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("page") == "2" {
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = w.Write([]byte(pageJSON(0, 100)))
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := c.h
			if h == nil {
				h = func(http.ResponseWriter, *http.Request) { t.Error("unexpected request") }
			}
			s, _ := newSource(t, h, nil)
			rels, err := s.Releases(context.Background(), dep.Dep{Name: c.dep}, "")
			if err == nil || rels != nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %v, %v; want error containing %q", rels, err, c.want)
			}
		})
	}
}

func TestReleasesTransportAndContext(t *testing.T) {
	s, srv := newSource(t, func(http.ResponseWriter, *http.Request) {}, nil)
	srv.Close()
	if _, err := s.Releases(context.Background(), dep.Dep{Name: "o/r"}, ""); err == nil || !strings.HasPrefix(err.Error(), "releases o/r: ") {
		t.Errorf("closed server: %v", err)
	}

	// A full semaphore and a canceled context fail fast without a request.
	s2 := New(http.DefaultClient, nil, make(chan struct{})).(*Source)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s2.Releases(ctx, dep.Dep{Name: "o/r"}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	s3 := New(http.DefaultClient, nil, nil).(*Source)
	s3.githubAPI = "http://[::1"
	if _, err := s3.Releases(context.Background(), dep.Dep{Name: "o/r"}, ""); err == nil {
		t.Error("bad api url: no error")
	}
}
