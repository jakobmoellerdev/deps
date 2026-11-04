package githubreleases

import (
	"context"
	"encoding/json/v2"
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
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
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

// newGH returns a githubapi client whose github.com endpoints are served by srv.
func newGH(srv *httptest.Server, auth *httpx.Auth, sem chan struct{}) *githubapi.Client {
	return githubapi.New(srv.Client(), auth, sem, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
}

func newSource(t *testing.T, h http.HandlerFunc, auth *httpx.Auth) (*Source, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(newGH(srv, auth, make(chan struct{}, 2))), srv
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

// Pages are fetched only until the dep's current version is listed: newer releases precede
// it. Callers with older versions page further; the list is shared.
func TestReleasesStopAtCurrentVersion(t *testing.T) {
	var reqs atomic.Int32
	s, _ := newSource(t, func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		_, _ = w.Write([]byte(pageJSON((page-1)*100, 100)))
	}, nil)
	for _, c := range []struct {
		version  string
		requests int32
		releases bool // the result must contain the version
	}{
		{"v1.50.0", 1, true},
		{"1.50.0", 1, true},   // v-less match, no new request
		{"v1.150.0", 2, true}, // page 2
		{"v1.21.0", 2, true},  // already listed
		{"v9.0.0", 5, false},  // not released: all pages up to the cap
		{"v1.450.0", 5, true}, // within the five pages already read
	} {
		rels, err := s.Releases(t.Context(), dep.Dep{Name: "o/r", Version: c.version}, "")
		if err != nil {
			t.Fatal(err)
		}
		if got := reqs.Load(); got != c.requests {
			t.Errorf("%s: %d requests, want %d", c.version, got, c.requests)
		}
		v := "v" + strings.TrimPrefix(c.version, "v")
		if has := slices.ContainsFunc(rels, func(r datasource.Release) bool { return r.Version == v }); has != c.releases {
			t.Errorf("%s: listed = %v, want %v", c.version, has, c.releases)
		}
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
	var gotAuth, gotPath, gotMethod string
	h := func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod = r.Header.Get("Authorization"), r.URL.Path, r.Method
		if strings.HasSuffix(r.URL.Path, "graphql") {
			_, _ = w.Write([]byte(`{"data":{"repository":{"releases":{"pageInfo":{"hasNextPage":false},"nodes":[]}}}}`))
			return
		}
		_, _ = w.Write([]byte(`[]`))
	}
	t.Run("github.com token uses graphql", func(t *testing.T) {
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
		if gotAuth != "Bearer ghtok" || gotPath != "/graphql" || gotMethod != http.MethodPost {
			t.Errorf("auth %q %s %s", gotAuth, gotMethod, gotPath)
		}
	})
	t.Run("enterprise host", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(h))
		defer srv.Close()
		host := strings.TrimPrefix(srv.URL, "http://")
		hostname, _, _ := strings.Cut(host, ":")
		for _, c := range []struct{ token, path string }{{"ghes", "/api/graphql"}, {"", "/api/v3/repos/o/r/releases"}} {
			auth := httpx.NewAuth(platform.Info{Host: hostname}, c.token, func(string) string { return "" })
			s := New(githubapi.New(srv.Client(), auth, nil))
			if _, err := s.Releases(context.Background(), dep.Dep{Name: srv.URL + "/o/r/"}, ""); err != nil {
				t.Fatal(err)
			}
			if want := strings.TrimSpace("Bearer " + c.token); gotPath != c.path || c.token != "" && gotAuth != want || c.token == "" && gotAuth != "" {
				t.Errorf("token %q: auth %q path %q", c.token, gotAuth, gotPath)
			}
		}
	})
	t.Run("anonymous for other hosts", func(t *testing.T) {
		auth := httpx.NewAuth(platform.Info{Host: "git.example"}, "tok", func(string) string { return "" })
		s, _ := newSource(t, h, auth)
		if _, err := s.Releases(context.Background(), dep.Dep{Name: "o/r"}, ""); err != nil {
			t.Fatal(err)
		}
		if gotAuth != "" || gotPath != "/repos/o/r/releases" {
			t.Errorf("auth %q path %q, want anonymous REST", gotAuth, gotPath)
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
	s2 := New(githubapi.New(http.DefaultClient, nil, make(chan struct{})))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s2.Releases(ctx, dep.Dep{Name: "o/r"}, ""); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
	s3 := New(githubapi.New(http.DefaultClient, nil, nil, githubapi.WithAPI("http://[::1", "")))
	if _, err := s3.Releases(context.Background(), dep.Dep{Name: "o/r"}, ""); err == nil {
		t.Error("bad api url: no error")
	}
}

func TestAssetDigest(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var reqs atomic.Int32
	s, _ := newSource(t, func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		_, _ = w.Write([]byte(`[
{"tag_name":"v2.60.0","assets":[
  {"name":"gh_2.60.0_linux_amd64.tar.gz","digest":"sha256:` + hex + `","browser_download_url":"https://x/gh.tgz"},
  {"name":"old.tar.gz","digest":null},
  {"name":"md5.tar.gz","digest":"md5:abc"}]},
{"tag_name":"1.0.0","assets":[{"name":"a","digest":"sha256:` + hex + `"}]},
{"tag_name":"v3.0.0-rc1","prerelease":true,"assets":[{"name":"a","digest":"sha256:` + hex + `"}]}]`))
	}, nil)
	ctx := context.Background()
	for _, c := range []struct{ version, asset, want, err string }{
		{"2.60.0", "gh_2.60.0_linux_amd64.tar.gz", hex, ""},
		{"v2.60.0", "gh_2.60.0_linux_amd64.tar.gz", hex, ""},
		{"v1.0.0", "a", hex, ""},
		{"1.0.0", "a", hex, ""},
		{"2.60.0", "missing", "", "release v2.60.0 has no digest for asset missing"},
		{"2.60.0", "old.tar.gz", "", "release v2.60.0 has no digest for asset old.tar.gz"},
		{"2.60.0", "md5.tar.gz", "", "release v2.60.0 has no digest for asset md5.tar.gz"},
		{"3.0.0-rc1", "a", "", "no release 3.0.0-rc1 in o/r"},
	} {
		got, err := s.AssetDigest(ctx, "o/r", c.version, c.asset)
		if got != c.want || (err == nil) != (c.err == "") || err != nil && err.Error() != c.err {
			t.Errorf("AssetDigest(%s, %s) = %q, %v; want %q, %q", c.version, c.asset, got, err, c.want, c.err)
		}
	}
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1 (cached)", reqs.Load())
	}
	rels, err := s.Releases(ctx, dep.Dep{Name: "o/r"}, "")
	if err != nil || len(rels) != 2 || reqs.Load() != 1 {
		t.Errorf("Releases after AssetDigest: %v, %v, reqs %d", rels, err, reqs.Load())
	}
	if _, err := s.AssetDigest(ctx, "bad", "1", "a"); err == nil {
		t.Error("invalid repo: no error")
	}
}

func TestReleaseTime(t *testing.T) {
	var reqs atomic.Int32
	s, _ := newSource(t, func(w http.ResponseWriter, _ *http.Request) {
		reqs.Add(1)
		_, _ = w.Write([]byte(`[
{"tag_name":"v2.60.0","published_at":"2026-10-30T12:00:00Z"},
{"tag_name":"1.0.0","published_at":null},
{"tag_name":"v3.0.0-rc1","prerelease":true,"published_at":"2026-11-01T00:00:00Z"}]`))
	}, nil)
	var timer datasource.Timer = s
	d := dep.Dep{Name: "o/r"}
	for _, c := range []struct{ version, want string }{
		{"2.60.0", "2026-10-30T12:00:00Z"},
		{"v2.60.0", "2026-10-30T12:00:00Z"},
		{"1.0.0", ""},
		{"3.0.0-rc1", ""}, // prereleases are not kept
		{"9.9.9", ""},
	} {
		got, err := timer.ReleaseTime(t.Context(), d, c.version)
		str := ""
		if !got.IsZero() {
			str = got.Format(time.RFC3339)
		}
		if err != nil || str != c.want {
			t.Errorf("ReleaseTime(%s) = %v, %v; want %q", c.version, got, err, c.want)
		}
	}
	if reqs.Load() != 1 {
		t.Errorf("requests = %d, want 1 (cached)", reqs.Load())
	}
	if _, err := timer.ReleaseTime(t.Context(), dep.Dep{Name: "bad"}, "1"); err == nil {
		t.Error("invalid repo: no error")
	}
}

// gqlReq is a decoded GraphQL request.
type gqlReq struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// With a github.com token the list, release times and asset digests come from GraphQL: pages
// follow the cursor only until the version is listed and asset digests are looked up once.
func TestGraphQL(t *testing.T) {
	const hex = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	var (
		mu   sync.Mutex
		reqs []string
	)
	s, _ := newSource(t, func(w http.ResponseWriter, r *http.Request) {
		var q gqlReq
		if r.Method != http.MethodPost || r.URL.Path != "/graphql" || r.Header.Get("Authorization") != "Bearer ghtok" ||
			json.UnmarshalRead(r.Body, &q) != nil {
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
		}
		v := q.Variables
		mu.Lock()
		reqs = append(reqs, fmt.Sprintf("%v/%v %v %v %v", v["owner"], v["name"], v["cursor"], v["tag"], v["asset"]))
		mu.Unlock()
		switch {
		case v["name"] == "missing":
			_, _ = w.Write([]byte(`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve to a Repository"}]}`))
		case strings.Contains(q.Query, "releaseAssets"):
			digest := `"sha256:` + hex + `"`
			if v["asset"] == "nodigest" {
				digest = "null"
			}
			fmt.Fprintf(w, `{"data":{"repository":{"release":{"releaseAssets":{"nodes":[{"name":%q,"digest":%s}]}}}}}`, v["asset"], digest)
		default:
			page := 1
			if c, ok := v["cursor"].(string); ok {
				page, _ = strconv.Atoi(strings.TrimPrefix(c, "c"))
				page++
			}
			nodes := make([]string, 100)
			for i := range nodes {
				k := (page-1)*100 + i
				nodes[i] = fmt.Sprintf(`{"tagName":"v1.%d.0","isDraft":%t,"isPrerelease":%t,"publishedAt":"2026-10-01T00:00:00Z"}`, k, k%10 == 9, k%7 == 6)
			}
			fmt.Fprintf(w, `{"data":{"repository":{"releases":{"pageInfo":{"hasNextPage":true,"endCursor":"c%d"},"nodes":[%s]}}}}`, page, strings.Join(nodes, ","))
		}
	}, httpx.NewAuth(platform.Info{Host: "git.example"}, "", func(k string) string {
		return map[string]string{"DEPS_GITHUB_COM_TOKEN": "ghtok"}[k]
	}))
	ctx := t.Context()
	rels, err := s.Releases(ctx, dep.Dep{Name: "o/r", Version: "v1.150.0"}, "")
	// Two pages of 100 without drafts (every 10th) and prereleases (every 7th).
	if err != nil || len(rels) != 154 || rels[0].Version != "v1.0.0" || slices.ContainsFunc(rels, func(r datasource.Release) bool { return r.Version == "v1.6.0" || r.Version == "v1.9.0" }) {
		t.Fatalf("Releases = %d releases, %v", len(rels), err)
	}
	if got, err := s.ReleaseTime(ctx, dep.Dep{Name: "o/r"}, "1.150.0"); err != nil || got.Format(time.DateOnly) != "2026-10-01" {
		t.Errorf("ReleaseTime = %v, %v", got, err)
	}
	for range 2 {
		if got, err := s.AssetDigest(ctx, "o/r", "1.50.0", "a.tgz"); err != nil || got != hex {
			t.Errorf("AssetDigest = %q, %v", got, err)
		}
	}
	if _, err := s.AssetDigest(ctx, "o/r", "v1.50.0", "nodigest"); err == nil || err.Error() != "release v1.50.0 has no digest for asset nodigest" {
		t.Errorf("no digest: %v", err)
	}
	if _, err := s.Releases(ctx, dep.Dep{Name: "o/missing"}, ""); !errors.Is(err, githubapi.ErrNotFound) || !strings.Contains(err.Error(), "releases o/missing: Could not resolve") {
		t.Errorf("missing repo: %v", err)
	}
	want := []string{
		"o/r <nil> <nil> <nil>", "o/r c1 <nil> <nil>",
		"o/r <nil> v1.50.0 a.tgz", "o/r <nil> v1.50.0 nodigest",
		"o/missing <nil> <nil> <nil>",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
	}
}
