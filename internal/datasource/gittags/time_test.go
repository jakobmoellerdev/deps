package gittags

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestReleaseTime(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	// o/full returns a full page (giteaListSize items), so tags missing from it fall back.
	var full strings.Builder
	full.WriteString(`[{"tag_name":"v0","draft":true,"published_at":"2026-01-01T00:00:00Z"}`)
	for range giteaListSize - 1 {
		full.WriteString(`,{"tag_name":"old","published_at":"2020-01-01T00:00:00Z"}`)
	}
	full.WriteString(`]`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, strings.TrimSpace(r.Header.Get("Authorization")+" "+r.URL.RequestURI()))
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/repos/actions/checkout/releases":
			io.WriteString(w, `[{"tag_name":"v7.0.1","published_at":"2026-10-30T12:00:00Z"},
				{"tag_name":"v6.1.0","published_at":"2026-10-01T12:00:00Z"},
				{"tag_name":"v8.0.0","draft":true,"published_at":null}]`)
		case "/api/v1/repos/o/r/releases":
			io.WriteString(w, `[{"tag_name":"sub/v1 1","published_at":"2026-10-30T12:00:00Z"}]`)
		case "/api/v1/repos/o/full/releases":
			io.WriteString(w, full.String())
		case "/api/v1/repos/o/full/releases/tags/v0", "/api/v1/repos/o/full/releases/tags/sub/v1%201":
			io.WriteString(w, `{"published_at":"2026-10-30T12:00:00Z"}`)
		case "/api/v1/repos/o/full/releases/tags/draft":
			io.WriteString(w, `{"published_at":null}`)
		case "/api/v1/repos/o/full/releases/tags/limited":
			w.WriteHeader(http.StatusForbidden)
		case "/api/v1/repos/o/web/releases":
			io.WriteString(w, `<html>not an API</html>`)
		case "/api/v1/repos/o/limited/releases":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	auth := httpx.NewAuth(platform.Info{Host: u.Hostname()}, "gitea-tok", func(k string) string {
		return map[string]string{"DEPS_GITHUB_COM_TOKEN": "gh-tok"}[k]
	})
	l := New(srv.Client(), auth, make(chan struct{}, 1))
	l.githubAPI = srv.URL
	var timer datasource.Timer = l
	when := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		source, tag, wantErr string
		want                 time.Time
	}{
		{source: "https://github.com/actions/checkout", tag: "v7.0.1", want: when},
		{source: "https://github.com/actions/checkout/", tag: "v6.1.0", want: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)},
		{source: "https://github.com/actions/checkout", tag: "v8.0.0"}, // draft, complete page
		{source: "https://github.com/actions/checkout", tag: "v6.0.0"}, // no release, complete page
		{source: srv.URL + "/o/r.git", tag: "sub/v1 1", want: when},
		{source: srv.URL + "/o/full", tag: "old", want: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
		{source: srv.URL + "/o/full", tag: "v0", want: when}, // draft in list: per-tag fallback
		{source: srv.URL + "/o/full", tag: "sub/v1 1", want: when},
		{source: srv.URL + "/o/full", tag: "draft"},
		{source: srv.URL + "/o/full", tag: "no-release"},
		{source: srv.URL + "/o/full", tag: "limited", wantErr: "release limited of o/full: HTTP 403"},
		{source: srv.URL + "/o/missing", tag: "v1"},
		{source: srv.URL + "/o/web", tag: "v1"},
		{source: srv.URL + "/o/limited", tag: "v1", wantErr: "releases of o/limited: HTTP 403"},
		{source: srv.URL + "/o/limited", tag: "v2", wantErr: "releases of o/limited: HTTP 403"},
		{source: "::bad", tag: "v1", wantErr: `releases: invalid repository "::bad"`},
		{source: "http://127.0.0.1:1/o/r", tag: "v1", wantErr: "releases of o/r: "},
	}
	for _, tt := range tests {
		for range 2 { // deduped per repository and per fallback tag
			got, err := timer.ReleaseTime(t.Context(), dep.Dep{Source: tt.source}, tt.tag)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("%s@%s: err = %v, want %q", tt.source, tt.tag, err, tt.wantErr)
				}
				continue
			}
			if err != nil || !got.Equal(tt.want) {
				t.Fatalf("%s@%s = %v, %v; want %v", tt.source, tt.tag, got, err, tt.want)
			}
		}
	}
	want := []string{
		"Bearer gh-tok /repos/actions/checkout/releases?per_page=100",
		"token gitea-tok /api/v1/repos/o/r/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/full/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/v0",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/sub/v1%201",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/draft",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/no-release",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/limited",
		"token gitea-tok /api/v1/repos/o/missing/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/web/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/limited/releases?limit=50",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
	}
}
