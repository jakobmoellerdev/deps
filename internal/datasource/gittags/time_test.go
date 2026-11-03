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
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		reqs = append(reqs, strings.TrimSpace(r.Header.Get("Authorization")+" "+r.URL.EscapedPath()))
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/repos/actions/checkout/releases/tags/v7.0.1", "/api/v1/repos/o/r/releases/tags/sub/v1%201":
			io.WriteString(w, `{"tag_name":"x","published_at":"2026-10-30T12:00:00Z"}`)
		case "/api/v1/repos/o/r/releases/tags/draft":
			io.WriteString(w, `{"published_at":null}`)
		case "/api/v1/repos/o/web/releases/tags/v1":
			io.WriteString(w, `<html>not an API</html>`)
		case "/api/v1/repos/o/limited/releases/tags/v1":
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
		{source: srv.URL + "/o/r.git", tag: "sub/v1 1", want: when},
		{source: srv.URL + "/o/r", tag: "draft"},
		{source: srv.URL + "/o/r", tag: "no-release"},
		{source: srv.URL + "/o/web", tag: "v1"},
		{source: srv.URL + "/o/limited", tag: "v1", wantErr: "release v1 of o/limited: HTTP 403"},
		{source: "::bad", tag: "v1", wantErr: `release v1: invalid repository "::bad"`},
		{source: "http://127.0.0.1:1/o/r", tag: "v1", wantErr: "release v1 of o/r: "},
	}
	for _, tt := range tests {
		for range 2 { // deduped per (repository, tag)
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
		"Bearer gh-tok /repos/actions/checkout/releases/tags/v7.0.1",
		"token gitea-tok /api/v1/repos/o/r/releases/tags/sub/v1%201",
		"token gitea-tok /api/v1/repos/o/r/releases/tags/draft",
		"token gitea-tok /api/v1/repos/o/r/releases/tags/no-release",
		"token gitea-tok /api/v1/repos/o/web/releases/tags/v1",
		"token gitea-tok /api/v1/repos/o/limited/releases/tags/v1",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
	}
}
