package gittags

import (
	"encoding/json/v2"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strconv"
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

// TestReleaseListPages follows the Link rel="next" of the Gitea/Forgejo releases listing: a host
// that caps limit below the requested page size (MAX_RESPONSE_ITEMS) is not mistaken for a
// complete list, and a release on a later page is dated without a request of its own.
func TestReleaseListPages(t *testing.T) {
	for _, tc := range []struct {
		name           string
		pages, perPage int
		link           bool // the host sends a Link header, as Gitea and Forgejo do
		wantComplete   bool
		wantReqs       []string
		wantTag        string // dated from the listing alone
	}{
		{
			name: "capped page size", pages: 2, perPage: 10, link: true, wantComplete: true,
			wantReqs: []string{"/api/v1/repos/o/r/releases?limit=50", "/api/v1/repos/o/r/releases?limit=50&page=2"},
			wantTag:  "v2.0",
		},
		{
			name: "no link header", pages: 1, perPage: giteaListSize, wantComplete: false,
			wantReqs: []string{"/api/v1/repos/o/r/releases?limit=50"},
		},
		{
			name: "page cap", pages: maxReleasePages + 3, perPage: 2, link: true, wantComplete: false,
			wantReqs: nil, // filled below: maxReleasePages requests
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var (
				mu   sync.Mutex
				reqs []string
			)
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				reqs = append(reqs, r.URL.RequestURI())
				mu.Unlock()
				if r.URL.Path != "/api/v1/repos/o/r/releases" {
					http.NotFound(w, r)
					return
				}
				page := 1
				if p := r.URL.Query().Get("page"); p != "" {
					page, _ = strconv.Atoi(p)
				}
				switch {
				case !tc.link:
				case page < tc.pages:
					w.Header().Set("Link", fmt.Sprintf(`<%s?page=%d>; rel="next"`, r.URL.Path, page+1))
				case page > 1:
					w.Header().Set("Link", fmt.Sprintf(`<%s?page=1>; rel="first"`, r.URL.Path))
				}
				var b strings.Builder
				b.WriteString("[")
				for i := range tc.perPage {
					if i > 0 {
						b.WriteString(",")
					}
					fmt.Fprintf(&b, `{"tag_name":"v%d.%d","published_at":"2026-01-01T00:%02d:00Z"}`, page, i, page)
				}
				b.WriteString("]")
				io.WriteString(w, b.String())
			}))
			t.Cleanup(srv.Close)
			l := New(srv.Client(), httpx.NewAuth(platform.Info{}, "", func(string) string { return "" }), make(chan struct{}, 1), nil)
			le, err := l.giteaList(t.Context(), srv.URL+"/o/r")
			if err != nil {
				t.Fatal(err)
			}
			if !le.api || le.complete != tc.wantComplete {
				t.Errorf("api %v, complete %v; want true, %v", le.api, le.complete, tc.wantComplete)
			}
			want := tc.wantReqs
			if want == nil {
				want = []string{"/api/v1/repos/o/r/releases?limit=50"}
				for page := 2; page <= maxReleasePages; page++ {
					want = append(want, "/api/v1/repos/o/r/releases?limit=50&page="+strconv.Itoa(page))
				}
			}
			if !slices.Equal(reqs, want) {
				t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
			}
			if tc.wantTag == "" {
				return
			}
			var timer datasource.Timer = l
			got, err := timer.ReleaseTime(t.Context(), dep.Dep{Source: srv.URL + "/o/r"}, tc.wantTag)
			if w := time.Date(2026, 1, 1, 0, 2, 0, 0, time.UTC); err != nil || !got.Equal(w) {
				t.Errorf("ReleaseTime(%s) = %v, %v; want %v", tc.wantTag, got, err, w)
			}
			if len(reqs) != len(want) {
				t.Errorf("a listed release must not cost a request: %q", reqs[len(want):])
			}
		})
	}
}

// CommitTime dates the default branch head of a commit pin: GitHub over GraphQL with a token,
// else REST; Gitea/Forgejo via git/commits. Deduped per repository and commit.
func TestCommitTime(t *testing.T) {
	var (
		mu   sync.Mutex
		reqs []string
	)
	head, gone := strings.Repeat("a", 40), strings.Repeat("b", 40)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := strings.TrimSpace(r.Header.Get("Authorization") + " " + r.URL.RequestURI())
		switch r.URL.Path {
		case "/graphql":
			var q struct {
				Query     string         `json:"query"`
				Variables map[string]any `json:"variables"`
			}
			_ = json.UnmarshalRead(r.Body, &q)
			if !strings.Contains(q.Query, "object(oid:$oid)") {
				t.Errorf("query = %s", q.Query)
			}
			rec += " " + q.Variables["owner"].(string) + "/" + q.Variables["name"].(string) + " " + q.Variables["oid"].(string)
			if q.Variables["oid"] == head {
				io.WriteString(w, `{"data":{"repository":{"object":{"committedDate":"2026-11-01T10:00:00Z"}}}}`)
			} else {
				io.WriteString(w, `{"data":{"repository":{"object":null}}}`)
			}
		case "/repos/steipete/run-testbox/git/commits/" + head:
			io.WriteString(w, `{"sha":"`+head+`","committer":{"date":"2026-11-02T10:00:00Z"}}`)
		case "/api/v1/repos/o/r/git/commits/" + head:
			if q := r.URL.RawQuery; q != "stat=false&verification=false&files=false" {
				t.Errorf("gitea query = %q", q)
			}
			io.WriteString(w, `{"sha":"`+head+`","created":"2026-11-03T12:00:00+02:00"}`)
		default:
			http.NotFound(w, r)
		}
		mu.Lock()
		reqs = append(reqs, rec)
		mu.Unlock()
	}))
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	lister := func(ghToken string) *Lister {
		auth := httpx.NewAuth(platform.Info{Host: u.Hostname()}, "gitea-tok", func(k string) string {
			return map[string]string{"DEPS_GITHUB_COM_TOKEN": ghToken}[k]
		})
		gh := githubapi.New(srv.Client(), auth, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql"))
		return New(srv.Client(), auth, make(chan struct{}, 1), gh)
	}
	withToken, anonymous := lister("gh-tok"), lister("")
	tests := []struct {
		l              *Lister
		source, commit string
		want           time.Time
	}{
		{withToken, "https://github.com/useblacksmith/begin-testbox", head, time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)},
		{withToken, "https://github.com/useblacksmith/begin-testbox", gone, time.Time{}}, // unknown commit
		{anonymous, "https://github.com/steipete/run-testbox/", head, time.Date(2026, 11, 2, 10, 0, 0, 0, time.UTC)},
		{anonymous, "https://github.com/steipete/run-testbox", gone, time.Time{}}, // 404
		{withToken, srv.URL + "/o/r", head, time.Date(2026, 11, 3, 10, 0, 0, 0, time.UTC)},
		{withToken, srv.URL + "/o/r", gone, time.Time{}},
	}
	for _, tt := range tests {
		var ct datasource.CommitTimer = tt.l
		for range 2 {
			got, err := ct.CommitTime(t.Context(), dep.Dep{Source: tt.source}, tt.commit)
			if err != nil || !got.Equal(tt.want) {
				t.Fatalf("%s@%s = %v, %v; want %v", tt.source, tt.commit, got, err, tt.want)
			}
		}
	}
	want := []string{
		"Bearer gh-tok /graphql useblacksmith/begin-testbox " + head,
		"Bearer gh-tok /graphql useblacksmith/begin-testbox " + gone,
		"/repos/steipete/run-testbox/git/commits/" + head,
		"/repos/steipete/run-testbox/git/commits/" + gone,
		"token gitea-tok /api/v1/repos/o/r/git/commits/" + head + "?stat=false&verification=false&files=false",
		"token gitea-tok /api/v1/repos/o/r/git/commits/" + gone + "?stat=false&verification=false&files=false",
	}
	if !slices.Equal(reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", reqs, want)
	}
}
