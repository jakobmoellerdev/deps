package githubapi

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

var oR = Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: "r"}

// tokenAuth has a token for github.com.
func tokenAuth() *httpx.Auth {
	return httpx.NewAuth(platform.Info{Host: "git.example"}, "", func(k string) string {
		return map[string]string{"DEPS_GITHUB_COM_TOKEN": "tok"}[k]
	})
}

// gql is a recorded GraphQL request.
type gql struct {
	Query     string         `json:"query"`
	Variables map[string]any `json:"variables"`
}

// server records GraphQL requests and answers them with h.
type server struct {
	mu   sync.Mutex
	reqs []gql
	rest []string
}

func newServer(t *testing.T, auth *httpx.Auth, h func(w http.ResponseWriter, q gql)) (*Client, *server) {
	t.Helper()
	s := &server{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/graphql" {
			s.mu.Lock()
			s.rest = append(s.rest, r.URL.RequestURI())
			s.mu.Unlock()
			h(w, gql{Query: "REST " + r.URL.RequestURI()})
			return
		}
		var q gql
		if r.Method != http.MethodPost || r.Header.Get("Authorization") != "Bearer tok" || r.Header.Get("Content-Type") != "application/json" ||
			json.UnmarshalRead(r.Body, &q) != nil {
			t.Errorf("bad GraphQL request %s %v", r.Method, r.Header)
		}
		s.mu.Lock()
		s.reqs = append(s.reqs, q)
		s.mu.Unlock()
		h(w, q)
	}))
	t.Cleanup(srv.Close)
	return New(srv.Client(), auth, make(chan struct{}, 2), WithAPI(srv.URL, srv.URL+"/graphql")), s
}

// releasesPage renders a GraphQL releases page of tags v<from>..v<from+n-1>.
func releasesPage(from, n int, next bool, cursor string) string {
	nodes := make([]string, n)
	for i := range n {
		nodes[i] = fmt.Sprintf(`{"tagName":"v%d","isDraft":%t,"isPrerelease":%t,"publishedAt":"2026-10-%02dT00:00:00Z"}`, from+i, i == 1, i == 2, 1+i%28)
	}
	return fmt.Sprintf(`{"data":{"repository":{"releases":{"pageInfo":{"hasNextPage":%t,"endCursor":%q},"nodes":[%s]}}}}`, next, cursor, strings.Join(nodes, ","))
}

func TestParseRepo(t *testing.T) {
	for in, want := range map[string]Repo{
		"o/r":                          oR,
		"https://github.com/o/r.git":   oR,
		"http://GitHub.com/o/r/":       {Scheme: "https", Host: "github.com", Owner: "o", Name: "r"},
		"https://ghe.example:8443/o/r": {Scheme: "https", Host: "ghe.example:8443", Owner: "o", Name: "r"},
	} {
		if got, err := ParseRepo(in); err != nil || got != want {
			t.Errorf("ParseRepo(%q) = %+v, %v", in, got, err)
		}
	}
	for _, in := range []string{"o", "o/r/x", "ftp://h/o/r", "https://h/o", "/r"} {
		if _, err := ParseRepo(in); err == nil || err.Error() != fmt.Sprintf("invalid github repository %q", in) {
			t.Errorf("ParseRepo(%q) err = %v", in, err)
		}
	}
	c := New(http.DefaultClient, nil, nil)
	ghe := Repo{Scheme: "https", Host: "ghe.example:8443", Owner: "o", Name: "r"}
	if c.rest(ghe) != "https://ghe.example:8443/api/v3" || c.graphqlURL(ghe) != "https://ghe.example:8443/api/graphql" ||
		c.rest(oR) != "https://api.github.com" || c.graphqlURL(oR) != "https://api.github.com/graphql" || ghe.hostname() != "ghe.example" {
		t.Error("endpoints")
	}
}

// Pages follow the cursor only while the caller needs more, the list stays shared, and paging
// stops at the last page or MaxPages.
func TestReleasesGraphQLPaging(t *testing.T) {
	c, s := newServer(t, tokenAuth(), func(w http.ResponseWriter, q gql) {
		switch q.Variables["name"] {
		case "short":
			_, _ = io.WriteString(w, releasesPage(0, 3, false, ""))
		default:
			page := 0
			if cur, ok := q.Variables["cursor"].(string); ok {
				_, _ = fmt.Sscanf(cur, "c%d", &page)
			}
			_, _ = io.WriteString(w, releasesPage(page*100, 100, true, fmt.Sprintf("c%d", page+1)))
		}
	})
	ctx := t.Context()
	find := func(tag string) func(*List) bool { return func(l *List) bool { _, ok := l.Find(tag); return ok } }
	var n int
	for _, step := range []struct {
		tag   string
		pages int
	}{{"v50", 1}, {"v150", 2}, {"v10", 2}, {"v9999", 5}, {"v450", 5}} {
		var listed, complete bool
		if err := c.Releases(ctx, oR, find(step.tag), func(l *List) { _, listed = l.Find(step.tag); complete = l.Complete(); n = len(l.Stable()) }); err != nil {
			t.Fatal(err)
		}
		if len(s.reqs) != step.pages || listed != (step.tag != "v9999") || complete {
			t.Errorf("%s: %d pages, listed %v, complete %v", step.tag, len(s.reqs), listed, complete)
		}
	}
	if n != 5*98 { // one draft and one prerelease per page
		t.Errorf("stable = %d", n)
	}
	for i, q := range s.reqs {
		want := any(nil)
		if i > 0 {
			want = fmt.Sprintf("c%d", i)
		}
		if q.Variables["owner"] != "o" || q.Variables["name"] != "r" || q.Variables["first"] != float64(PageSize) || q.Variables["cursor"] != want ||
			!strings.Contains(q.Query, "orderBy:{field:CREATED_AT,direction:DESC}") || strings.Contains(q.Query, "description") {
			t.Errorf("request %d = %+v", i, q)
		}
	}
	var rel Release
	var ok, complete bool
	short := Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: "short"}
	if err := c.Releases(ctx, short, nil, func(l *List) { rel, ok = l.Find("v2"); complete = l.Complete() }); err != nil || !ok || !rel.Prerelease || !complete {
		t.Errorf("short list: %+v %v %v %v", rel, ok, complete, err)
	}
}

func TestGraphQLErrors(t *testing.T) {
	c, _ := newServer(t, tokenAuth(), func(w http.ResponseWriter, q gql) {
		switch q.Variables["name"] {
		case "missing":
			_, _ = io.WriteString(w, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","path":["repository"],"message":"Could not resolve to a Repository with the name 'o/missing'."}]}`)
		case "null":
			_, _ = io.WriteString(w, `{"data":{"repository":null}}`)
		case "limited":
			w.Header().Set("X-RateLimit-Reset", "1793707200")
			_, _ = io.WriteString(w, `{"errors":[{"type":"RATE_LIMITED","message":"API rate limit exceeded for user ID 1."}]}`)
		case "other":
			_, _ = io.WriteString(w, `{"errors":[{"message":"Field 'x' doesn't exist"},{"type":"INTERNAL","message":"boom"}]}`)
		case "unauthorized":
			w.WriteHeader(http.StatusUnauthorized)
		case "exhausted":
			w.Header().Set("X-RateLimit-Remaining", "0")
			w.Header().Set("X-RateLimit-Reset", "1793707200")
			w.WriteHeader(http.StatusForbidden)
		case "html":
			_, _ = io.WriteString(w, `<html>`)
		}
	})
	for name, want := range map[string]string{
		"missing":      "releases o/missing: Could not resolve to a Repository with the name 'o/missing'.: not found",
		"null":         "releases o/null: repository: not found",
		"limited":      "releases o/limited: rate limited by github.com until 2026-11-03T12:00:00Z: API rate limit exceeded for user ID 1.",
		"other":        "releases o/other: graphql: Field 'x' doesn't exist; boom",
		"unauthorized": "releases o/unauthorized: HTTP 401",
		"exhausted":    "releases o/exhausted: HTTP 403: rate limit exceeded (resets 2026-11-03T12:00:00Z)",
		"html":         "releases o/html: malformed response: jsontext: invalid character '<' at start of value",
	} {
		r := Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: name}
		for range 2 { // a failed list stays failed for the run
			err := c.Releases(t.Context(), r, nil, func(*List) { t.Error("fn called on error") })
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Errorf("%s: err = %v, want %q", name, err, want)
			}
			if errors.Is(err, ErrNotFound) != (name == "missing" || name == "null") || errors.Is(err, httpx.ErrRateLimited) != (name == "limited") {
				t.Errorf("%s: error kind of %v", name, err)
			}
		}
	}
	if err := graphql(t.Context(), New(http.DefaultClient, nil, nil), oR, "{}", nil, &struct{}{}); err == nil || err.Error() != "graphql requires a token" {
		t.Errorf("anonymous graphql: %v", err)
	}
	if err := graphql(t.Context(), c, oR, "{}", map[string]any{"x": func() {}}, &struct{}{}); err == nil {
		t.Error("unencodable variables: no error")
	}
}

// GraphQL queries only read, so the shared client retries them like GET requests.
func TestGraphQLRetried(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if !strings.Contains(string(body), `"owner":"o"`) {
			t.Errorf("replayed body = %s", body)
		}
		_, _ = io.WriteString(w, releasesPage(0, 1, false, ""))
	}))
	defer srv.Close()
	c := New(httpx.NewClient(), tokenAuth(), nil, WithAPI(srv.URL, srv.URL))
	if err := c.Releases(t.Context(), oR, nil, func(*List) {}); err != nil || calls.Load() != 2 {
		t.Errorf("err = %v after %d calls", err, calls.Load())
	}
}

func TestReleasesREST(t *testing.T) {
	c, s := newServer(t, nil, func(w http.ResponseWriter, q gql) {
		switch {
		case strings.Contains(q.Query, "/repos/o/r/releases?"):
			_, _ = io.WriteString(w, `[{"tag_name":"v2","published_at":"2026-10-30T12:00:00Z","assets":[{"name":"a","digest":"sha256:ab"}]},
				{"tag_name":"v1","draft":true},{"tag_name":""},{"tag_name":"v1-rc","prerelease":true}]`)
		case strings.Contains(q.Query, "/releases/tags/v3"):
			_, _ = io.WriteString(w, `{"published_at":"2026-10-01T00:00:00Z"}`)
		case strings.Contains(q.Query, "/releases/tags/draft"):
			_, _ = io.WriteString(w, `{"draft":true,"published_at":null}`)
		case strings.Contains(q.Query, "/releases/tags/bad"):
			_, _ = io.WriteString(w, `[`)
		case strings.Contains(q.Query, "/releases/tags/sub/x%201"):
			w.WriteHeader(http.StatusInternalServerError)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	})
	ctx := t.Context()
	var rel Release
	var ok, complete bool
	var stable []string
	err := c.Releases(ctx, oR, nil, func(l *List) {
		rel, ok = l.Find("v2")
		complete = l.Complete()
		for _, s := range l.Stable() {
			stable = append(stable, s.Version)
		}
	})
	if err != nil || !ok || rel.Published.Format(time.DateOnly) != "2026-10-30" || !complete || !slices.Equal(stable, []string{"v2"}) {
		t.Errorf("list: %+v %v %v %v %v", rel, ok, complete, stable, err)
	}
	for _, c2 := range []struct{ asset, want string }{{"a", "sha256:ab"}, {"x", ""}} {
		if got, err := c.AssetDigest(ctx, oR, "v2", c2.asset); err != nil || got != c2.want {
			t.Errorf("AssetDigest(%s) = %q, %v", c2.asset, got, err)
		}
	}
	for tag, want := range map[string]string{"v3": "2026-10-01", "draft": "", "none": "", "bad": "error", "sub/x 1": "error"} {
		got, err := c.ReleaseTime(ctx, oR, tag)
		switch {
		case want == "error":
			if err == nil || !strings.HasPrefix(err.Error(), "release "+tag+" of o/r: ") {
				t.Errorf("ReleaseTime(%s) err = %v", tag, err)
			}
		case err != nil || (want == "") != got.IsZero() || want != "" && got.Format(time.DateOnly) != want:
			t.Errorf("ReleaseTime(%s) = %v, %v", tag, got, err)
		}
	}
	if len(s.reqs) != 0 || !slices.Contains(s.rest, "/repos/o/r/releases/tags/sub/x%201") {
		t.Errorf("graphql %d, rest %q", len(s.reqs), s.rest)
	}
	if err := c.Releases(ctx, Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: "gone"}, nil, func(*List) {}); !errors.Is(err, ErrNotFound) {
		t.Errorf("REST 404: %v", err)
	}
}

func TestReleaseTimeAndAssetsGraphQL(t *testing.T) {
	c, s := newServer(t, tokenAuth(), func(w http.ResponseWriter, q gql) {
		v := q.Variables
		switch {
		case v["name"] == "gone":
			_, _ = io.WriteString(w, `{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"gone"}]}`)
		case v["name"] == "err":
			_, _ = io.WriteString(w, `{"errors":[{"message":"boom"}]}`)
		case strings.Contains(q.Query, "releaseAssets"):
			if v["tag"] == "none" {
				_, _ = io.WriteString(w, `{"data":{"repository":{"release":null}}}`)
				return
			}
			_, _ = fmt.Fprintf(w, `{"data":{"repository":{"release":{"releaseAssets":{"nodes":[{"name":%q,"digest":"sha256:ff"}]}}}}}`, v["asset"])
		case v["tag"] == "v1":
			_, _ = io.WriteString(w, `{"data":{"repository":{"release":{"isDraft":false,"publishedAt":"2026-10-30T12:00:00Z"}}}}`)
		case v["tag"] == "draft":
			_, _ = io.WriteString(w, `{"data":{"repository":{"release":{"isDraft":true,"publishedAt":"2026-10-30T12:00:00Z"}}}}`)
		default:
			_, _ = io.WriteString(w, `{"data":{"repository":{"release":null}}}`)
		}
	})
	ctx := t.Context()
	for tag, want := range map[string]string{"v1": "2026-10-30", "draft": "", "v2": ""} {
		if got, err := c.ReleaseTime(ctx, oR, tag); err != nil || (want == "") != got.IsZero() || want != "" && got.Format(time.DateOnly) != want {
			t.Errorf("ReleaseTime(%s) = %v, %v", tag, got, err)
		}
	}
	gone := Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: "gone"}
	if got, err := c.ReleaseTime(ctx, gone, "v1"); err != nil || !got.IsZero() {
		t.Errorf("missing repo = %v, %v", got, err)
	}
	bad := Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: "err"}
	if _, err := c.ReleaseTime(ctx, bad, "v1"); err == nil || err.Error() != "release v1 of o/err: graphql: boom" {
		t.Errorf("error = %v", err)
	}
	n := len(s.reqs)
	for range 2 {
		if got, err := c.AssetDigest(ctx, oR, "v1", "a.tgz"); err != nil || got != "sha256:ff" {
			t.Errorf("AssetDigest = %q, %v", got, err)
		}
	}
	if got, err := c.AssetDigest(ctx, oR, "none", "a.tgz"); err != nil || got != "" {
		t.Errorf("AssetDigest without release = %q, %v", got, err)
	}
	if _, err := c.AssetDigest(ctx, bad, "v1", "a"); err == nil || err.Error() != "release v1 of o/err: asset a: graphql: boom" {
		t.Errorf("asset error = %v", err)
	}
	if got := len(s.reqs) - n; got != 3 {
		t.Errorf("asset requests = %d, want 3 (deduped)", got)
	}
	q := s.reqs[n]
	if q.Variables["tag"] != "v1" || q.Variables["asset"] != "a.tgz" || !strings.Contains(q.Query, "releaseAssets(first:1,name:$asset){nodes{name digest}}") {
		t.Errorf("asset query = %+v", q)
	}
}

func TestNotes(t *testing.T) {
	handler := func(w http.ResponseWriter, q gql) {
		switch {
		case q.Variables["name"] == "gone":
			_, _ = io.WriteString(w, `{"data":{"repository":null}}`)
		case q.Variables["name"] == "r":
			_, _ = io.WriteString(w, `{"data":{"repository":{"releases":{"nodes":[{"tagName":"v2","url":"u2","description":"two","isPrerelease":true},{"tagName":"v1","isDraft":true}]}}}}`)
		case strings.Contains(q.Query, "/repos/o/r/releases?per_page=7"):
			_, _ = io.WriteString(w, `[{"tag_name":"v2","html_url":"u2","body":"two","prerelease":true},{"tag_name":"v1","draft":true}]`)
		case strings.Contains(q.Query, "/repos/o/obj/"):
			_, _ = io.WriteString(w, `{}`)
		case strings.Contains(q.Query, "/repos/o/bad/"):
			_, _ = io.WriteString(w, `[{"tag_name":1}]`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
	}
	want := []Note{{Tag: "v2", URL: "u2", Body: "two", Prerelease: true}, {Tag: "v1", Draft: true}}
	for _, auth := range []*httpx.Auth{tokenAuth(), nil} {
		c, s := newServer(t, auth, handler)
		var got []Note
		if err := c.Notes(t.Context(), oR, 7, func(n Note) { got = append(got, n) }); err != nil || !slices.Equal(got, want) {
			t.Errorf("authed %v: Notes = %+v, %v", auth != nil, got, err)
		}
		if auth != nil && (len(s.reqs) != 1 || s.reqs[0].Variables["first"] != float64(7) || !strings.Contains(s.reqs[0].Query, "description")) {
			t.Errorf("notes query = %+v", s.reqs)
		}
		errs := map[string]string{"fail": "HTTP 500", "obj": "malformed response: not a release list", "bad": "malformed response"}
		if auth != nil {
			errs = map[string]string{"fail": "HTTP 500", "gone": "repository: not found"}
		}
		for name, wantErr := range errs {
			r := Repo{Scheme: "https", Host: "github.com", Owner: "o", Name: name}
			if err := c.Notes(t.Context(), r, 7, func(Note) {}); err == nil || !strings.HasPrefix(err.Error(), "release notes of o/"+name+": "+wantErr) {
				t.Errorf("authed %v %s: err = %v", auth != nil, name, err)
			}
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := New(http.DefaultClient, nil, make(chan struct{})).Notes(ctx, oR, 1, func(Note) {}); !errors.Is(err, context.Canceled) {
		t.Errorf("canceled: %v", err)
	}
}
