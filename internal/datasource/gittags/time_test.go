package gittags

import (
	"encoding/json/v2"
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestReleaseTime(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	sha := func(c string) string { return strings.Repeat(c, 40) }
	// The tags of each repository (v0 ref advertisement lines), keyed by URL path.
	refs := map[string][]string{
		"/actions/checkout": {
			sha("a") + " refs/tags/v7.0.1\n", sha("a") + " refs/tags/v7\n", // v7 floats on v7.0.1
			sha("b") + " refs/tags/v6.1.0\n", sha("c") + " refs/tags/v8.0.0\n", sha("d") + " refs/tags/v6.0.0\n",
			sha("e") + " refs/tags/v5\n", sha("f") + " refs/tags/v5^{}\n",
		},
		"/big/repo": {sha("1") + " refs/tags/v2.0.0\n", sha("2") + " refs/tags/v1.0.0\n", sha("1") + " refs/tags/v4\n"},
		"/o/r.git":  {sha("3") + " refs/tags/v1.0.0\n", sha("3") + " refs/tags/latest\n", sha("4") + " refs/tags/v9\n"},
		"/o/full":   {sha("5") + " refs/tags/old\n", sha("6") + " refs/tags/draft\n", sha("7") + " refs/tags/no-release\n"},
	}
	// o/full returns a full page (giteaListSize items), so tags missing from it fall back.
	var full strings.Builder
	full.WriteString(`[{"tag_name":"v0","draft":true,"published_at":"2026-01-01T00:00:00Z"}`)
	for range giteaListSize - 1 {
		full.WriteString(`,{"tag_name":"old","published_at":"2020-01-01T00:00:00Z"}`)
	}
	full.WriteString(`]`)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if repo, ok := strings.CutSuffix(r.URL.Path, "/info/refs"); ok {
			lines, ok := refs[repo]
			if !ok {
				http.NotFound(w, r)
				return
			}
			head := sha("0") + " HEAD\x00agent=git/2.45\n"
			io.WriteString(w, pkts(slices.Concat([]string{serviceHeader, "0000", head}, lines, []string{"0000"})...))
			return
		}
		rec := strings.TrimSpace(r.Header.Get("Authorization") + " " + r.URL.RequestURI())
		if r.URL.Path == "/graphql" {
			var q struct {
				Variables map[string]any `json:"variables"`
			}
			_ = json.UnmarshalRead(r.Body, &q)
			v := map[string]string{}
			for k, x := range q.Variables {
				v[k], _ = x.(string)
			}
			rec += " " + v["owner"] + "/" + v["name"] + " " + v["tag"] + v["ref"]
			switch repo := v["owner"] + "/" + v["name"]; {
			case repo == "gone/repo":
				io.WriteString(w, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve"}]}`)
			case repo == "lim/repo":
				w.Header().Set("X-RateLimit-Reset", "1793707200")
				io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded"}]}`)
			case v["ref"] == "refs/tags/v6.0.0": // lightweight tag: the commit date
				io.WriteString(w, `{"data":{"repository":{"ref":{"target":{"committedDate":"2026-09-01T00:00:00Z"}}}}}`)
			case v["ref"] == "refs/tags/v5": // annotated tag: the tagger date
				io.WriteString(w, `{"data":{"repository":{"ref":{"target":{"tagger":{"date":"2026-08-01T00:00:00Z"},"target":{"committedDate":"2026-07-01T00:00:00Z"}}}}}}`)
			case v["ref"] != "":
				io.WriteString(w, `{"data":{"repository":{"ref":null}}}`)
			case v["tag"] == "v1.0.0":
				io.WriteString(w, `{"data":{"repository":{"release":{"isDraft":false,"publishedAt":"2026-10-30T12:00:00Z"}}}}`)
			case v["tag"] != "":
				io.WriteString(w, `{"data":{"repository":{"release":null}}}`)
			case repo == "actions/checkout":
				io.WriteString(w, `{"data":{"repository":{"releases":{"pageInfo":{"hasNextPage":false},"nodes":[
					{"tagName":"v7.0.1","publishedAt":"2026-10-30T12:00:00Z"},
					{"tagName":"v6.1.0","isPrerelease":true,"publishedAt":"2026-10-01T12:00:00Z"},
					{"tagName":"v8.0.0","isDraft":true,"publishedAt":null}]}}}}`)
			default: // a full first page: tags missing from it are looked up individually
				io.WriteString(w, `{"data":{"repository":{"releases":{"pageInfo":{"hasNextPage":true,"endCursor":"c1"},"nodes":[{"tagName":"v2.0.0","publishedAt":"2026-10-01T12:00:00Z"}]}}}}`)
			}
		}
		mu.Lock()
		reqs = append(reqs, rec)
		mu.Unlock()
		switch r.URL.EscapedPath() {
		case "/graphql":
		case "/api/v1/repos/o/r/releases":
			io.WriteString(w, `[{"tag_name":"sub/v1 1","published_at":"2026-10-30T12:00:00Z"},{"tag_name":"v1.0.0","published_at":"2026-10-20T00:00:00Z"}]`)
		case "/api/v1/repos/o/r/tags/v9":
			io.WriteString(w, `{"name":"v9","commit":{"sha":"`+sha("4")+`","created":"2026-09-15T10:00:00+02:00"}}`)
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
	// Git requests for github.com go to srv as well.
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == "github.com" {
			r = r.Clone(r.Context())
			r.URL.Scheme, r.URL.Host = "http", u.Host
		}
		return srv.Client().Transport.RoundTrip(r)
	})}
	gh := githubapi.New(srv.Client(), auth, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
	l := New(client, auth, make(chan struct{}, 1), gh)
	var timer datasource.Timer = l
	when := time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		source, tag, wantErr string
		want                 time.Time
	}{
		{source: "https://github.com/actions/checkout", tag: "v7.0.1", want: when},
		{source: "https://github.com/actions/checkout/", tag: "v6.1.0", want: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}, // prerelease
		{source: "https://github.com/actions/checkout", tag: "v7", want: when},                                               // floating: v7.0.1's release
		{source: "https://github.com/actions/checkout", tag: "v8.0.0"},                                                       // draft, complete list, tag not found
		{source: "https://github.com/actions/checkout", tag: "v6.0.0", want: time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)},    // commit date
		{source: "https://github.com/actions/checkout", tag: "v5", want: time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)},        // tagger date
		{source: "https://github.com/big/repo", tag: "v2.0.0", want: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)},
		{source: "https://github.com/big/repo", tag: "v1.0.0", want: when},                                      // per-tag fallback
		{source: "https://github.com/big/repo", tag: "v4", want: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}, // no release: v2.0.0's commit
		{source: "https://github.com/gone/repo", tag: "v1"},                                                     // NOT_FOUND: unknown
		{source: "https://github.com/lim/repo", tag: "v1", wantErr: "rate limited by github.com until 2026-11-03T12:00:00Z"},
		{source: srv.URL + "/o/r.git", tag: "sub/v1 1", want: when},
		{source: srv.URL + "/o/r.git", tag: "latest", want: time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)}, // v1.0.0's commit
		{source: srv.URL + "/o/r.git", tag: "v9", want: time.Date(2026, 9, 15, 8, 0, 0, 0, time.UTC)},      // tag date
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
		"Bearer gh-tok /graphql actions/checkout ",
		"Bearer gh-tok /graphql actions/checkout refs/tags/v8.0.0",
		"Bearer gh-tok /graphql actions/checkout refs/tags/v6.0.0",
		"Bearer gh-tok /graphql actions/checkout refs/tags/v5",
		"Bearer gh-tok /graphql big/repo ",
		"Bearer gh-tok /graphql big/repo v1.0.0",
		"Bearer gh-tok /graphql big/repo v4",
		"Bearer gh-tok /graphql gone/repo ",
		"Bearer gh-tok /graphql lim/repo ",
		"token gitea-tok /api/v1/repos/o/r/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/r/tags/v9",
		"token gitea-tok /api/v1/repos/o/full/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/v0",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/sub/v1%201",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/draft",
		"token gitea-tok /api/v1/repos/o/full/tags/draft",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/no-release",
		"token gitea-tok /api/v1/repos/o/full/tags/no-release",
		"token gitea-tok /api/v1/repos/o/full/releases/tags/limited",
		"token gitea-tok /api/v1/repos/o/missing/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/web/releases?limit=50",
		"token gitea-tok /api/v1/repos/o/limited/releases?limit=50",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
	}
}
