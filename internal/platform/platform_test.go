package platform

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func envFunc(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnv(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want Info
		ok   bool
	}{
		{"none", map[string]string{"GITHUB_SERVER_URL": "https://github.com"}, Info{}, false},
		{"github", map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://github.com",
			"GITHUB_API_URL": "https://api.github.com", "GITHUB_REPOSITORY": "o/r",
		}, Info{GitHub, "https://github.com", "https://api.github.com", "github.com", "o/r"}, true},
		{"gitea", map[string]string{
			"GITHUB_ACTIONS": "true", "GITEA_ACTIONS": "true", "GITHUB_SERVER_URL": "https://g.example:3000",
			"GITHUB_API_URL": "https://g.example:3000/api/v1", "GITHUB_REPOSITORY": "o/r",
		}, Info{Gitea, "https://g.example:3000", "https://g.example:3000/api/v1", "g.example", "o/r"}, true},
		{"forgejo native", map[string]string{
			"GITEA_ACTIONS": "true", "FORGEJO_ACTIONS": "true", "FORGEJO_SERVER_URL": "https://f.example",
			"FORGEJO_API_URL": "https://f.example/api/v1", "FORGEJO_REPOSITORY": "o/r", "GITHUB_REPOSITORY": "x/y",
		}, Info{Forgejo, "https://f.example", "https://f.example/api/v1", "f.example", "o/r"}, true},
		{"forgejo fallback", map[string]string{
			"FORGEJO_ACTIONS": "true", "GITHUB_SERVER_URL": "https://f.example",
			"GITHUB_API_URL": "https://f.example/api/v1", "GITHUB_REPOSITORY": "o/r",
		}, Info{Forgejo, "https://f.example", "https://f.example/api/v1", "f.example", "o/r"}, true},
		{"ghes without api url", map[string]string{
			"GITHUB_ACTIONS": "true", "GITHUB_SERVER_URL": "https://ghe.example/", "GITHUB_REPOSITORY": "o/r",
		}, Info{GitHub, "https://ghe.example", "https://ghe.example/api/v3", "ghe.example", "o/r"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FromEnv(envFunc(tt.env))
			if ok != tt.ok || got != tt.want {
				t.Fatalf("got %+v,%v want %+v,%v", got, ok, tt.want, tt.ok)
			}
		})
	}
}

func TestParseRemote(t *testing.T) {
	tests := []struct{ in, hostport, host, repo string }{
		{"https://git.example.com/o/r.git", "git.example.com", "git.example.com", "o/r"},
		{"https://git.example.com:8443/o/r", "git.example.com:8443", "git.example.com", "o/r"},
		{"git@github.com:o/r.git", "github.com", "github.com", "o/r"},
		{"ssh://git@git.example.com:2222/o/r.git", "git.example.com", "git.example.com", "o/r"},
	}
	for _, tt := range tests {
		hp, h, r, err := parseRemote(tt.in)
		if err != nil || hp != tt.hostport || h != tt.host || r != tt.repo {
			t.Errorf("%s: got %q %q %q %v", tt.in, hp, h, r, err)
		}
	}
	for _, bad := range []string{"", "/local/path", "https://h/onlyowner", "file:///x/o/r", "https://h/%zz/r"} {
		if _, _, _, err := parseRemote(bad); err == nil {
			t.Errorf("%q: want error", bad)
		}
	}
	if _, _, _, err := parseRemote("https://h/%zz/r"); err == nil || !strings.HasPrefix(err.Error(), `parse remote "https://h/%zz/r": `) {
		t.Errorf("parse error = %v", err)
	}
}

func TestDetectFailures(t *testing.T) {
	down := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("connection refused")
	})}
	tests := []struct {
		name, remote, want string
	}{
		{"unsupported remote", "/local/path", `unsupported remote "/local/path"`},
		{"probes unreachable", "https://h.example/o/r", "cannot detect platform for h.example; pass --platform and --api-url"},
		{"probe URL invalid", "git@ho st:o/r", "cannot detect platform for ho st; pass --platform and --api-url"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info, err := Detect(t.Context(), envFunc(nil), tt.remote, Overrides{}, down)
			if err == nil || err.Error() != tt.want || info != (Info{}) {
				t.Fatalf("Detect = %+v, %v; want error %s", info, err, tt.want)
			}
		})
	}
}

func TestDetectProbes(t *testing.T) {
	tests := []struct {
		name   string
		routes map[string]string // path -> body (200)
		want   Kind
		api    string // suffix after server
	}{
		{"forgejo", map[string]string{"/api/forgejo/v1/version": `{"version":"9"}`, "/api/v1/version": `{"version":"9"}`}, Forgejo, "/api/v1"},
		{"gitea", map[string]string{"/api/v1/version": `{"version":"1.25"}`, "/api/v3/meta": `{}`}, Gitea, "/api/v1"},
		{"gitea without version field", map[string]string{"/api/v1/version": `{}`, "/api/v3/meta": `{}`}, GitHub, "/api/v3"},
		{"gitea sign-in required", map[string]string{"/api/v1/version": `403:{"message":"Only signed in user is allowed to call APIs."}`, "/api/v3/meta": `{}`}, Gitea, "/api/v1"},
		{"forgejo sign-in required", map[string]string{"/api/forgejo/v1/version": `403:{"message":"x"}`}, Forgejo, "/api/v1"},
		{"403 without json is not gitea", map[string]string{"/api/v1/version": `403:forbidden`, "/api/v3/meta": `{}`}, GitHub, "/api/v3"},
		{"ghes", map[string]string{"/api/v3/meta": `{}`}, GitHub, "/api/v3"},
		{"none", map[string]string{}, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var seen []string
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				seen = append(seen, r.URL.Path)
				mu.Unlock()
				if b, ok := tt.routes[r.URL.Path]; ok {
					if rest, ok := strings.CutPrefix(b, "403:"); ok {
						w.WriteHeader(http.StatusForbidden)
						b = rest
					}
					io.WriteString(w, b)
					return
				}
				http.NotFound(w, r)
			}))
			defer srv.Close()
			host := strings.TrimPrefix(srv.URL, "https://")
			info, err := Detect(context.Background(), envFunc(nil), "https://"+host+"/o/r.git", Overrides{}, srv.Client())
			if tt.want == "" {
				if err == nil || !strings.Contains(err.Error(), "cannot detect platform") {
					t.Fatalf("err = %v", err)
				}
				want := []string{"/api/forgejo/v1/version", "/api/v1/version", "/api/v3/meta"}
				if fmt.Sprint(seen) != fmt.Sprint(want) {
					t.Fatalf("probe order %v", seen)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if info.Kind != tt.want || info.APIURL != "https://"+host+tt.api || info.Repo != "o/r" || info.Host != "127.0.0.1" {
				t.Fatalf("got %+v", info)
			}
		})
	}
}

func TestDetectNoProbe(t *testing.T) {
	c := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected request %s", r.URL)
		return nil, nil
	})}
	info, err := Detect(context.Background(), envFunc(nil), "git@github.com:o/r.git", Overrides{}, c)
	if err != nil || info.Kind != GitHub || info.APIURL != "https://api.github.com" || info.Repo != "o/r" {
		t.Fatalf("github: %+v %v", info, err)
	}
	info, err = Detect(context.Background(), envFunc(nil), "ssh://git@h.example:22/o/r", Overrides{Kind: "forgejo", Repo: "x/y"}, c)
	if err != nil || info.Kind != Forgejo || info.APIURL != "https://h.example/api/v1" || info.Repo != "x/y" {
		t.Fatalf("override kind: %+v %v", info, err)
	}
	env := envFunc(map[string]string{"GITEA_ACTIONS": "true", "GITHUB_SERVER_URL": "https://g.example", "GITHUB_API_URL": "https://g.example/api/v1", "GITHUB_REPOSITORY": "o/r"})
	info, err = Detect(context.Background(), env, "", Overrides{APIURL: "https://other/api/"}, c)
	if err != nil || info.Kind != Gitea || info.APIURL != "https://other/api" {
		t.Fatalf("env+api override: %+v %v", info, err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDefaultActionsHost(t *testing.T) {
	tests := []struct {
		kind      Kind
		env       string
		host, src string
	}{
		{Forgejo, "", "data.forgejo.org", "platform default"},
		{Gitea, "", "github.com", "platform default"},
		{"", "", "github.com", "platform default"},
		{Gitea, "https://gitea.com/", "gitea.com", "env"},
		{Gitea, "code.example", "code.example", "env"},
	}
	for _, tt := range tests {
		h, s := DefaultActionsHost(Info{Kind: tt.kind}, envFunc(map[string]string{"DEPS_DEFAULT_ACTIONS_URL": tt.env}))
		if h != tt.host || s != tt.src {
			t.Errorf("%v %q: got %q %q", tt.kind, tt.env, h, s)
		}
	}
}

func TestGitLabURL(t *testing.T) {
	tests := []struct {
		configured, env string
		url, src        string
	}{
		{"", "", "https://gitlab.com", "default"},
		{"", "https://gitlab.example.com/", "https://gitlab.example.com", "env"},
		{"https://example.com/gitlab", "https://gitlab.example.com", "https://example.com/gitlab", "config"},
		{"http://git.example:8080/", "", "http://git.example:8080", "config"},
	}
	for _, tt := range tests {
		u, s := GitLabURL(tt.configured, envFunc(map[string]string{"CI_SERVER_URL": tt.env}))
		if u != tt.url || s != tt.src {
			t.Errorf("%q %q: got %q %q", tt.configured, tt.env, u, s)
		}
	}
}

type recorded struct {
	method, uri, auth, accept, apiVer string
	body                              map[string]any
}

// fakeAPI serves a fixed number of PRs per list page and records requests.
func fakeAPI(t *testing.T, kind Kind, totalPRs int) (*httptest.Server, *[]recorded) {
	var mu sync.Mutex
	var reqs []recorded
	per := 50
	if kind == GitHub {
		per = 100
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec := recorded{method: r.Method, uri: r.URL.RequestURI(), auth: r.Header.Get("Authorization"), accept: r.Header.Get("Accept"), apiVer: r.Header.Get("X-GitHub-Api-Version")}
		if r.Body != nil {
			_ = json.UnmarshalRead(r.Body, &rec.body)
		}
		mu.Lock()
		reqs = append(reqs, rec)
		mu.Unlock()
		p := r.URL.Path
		switch {
		case p == "/repos/o/r" && r.Method == http.MethodGet:
			io.WriteString(w, `{"default_branch":"main"}`)
		case p == "/repos/o/empty":
			io.WriteString(w, `{}`)
		case p == "/repos/o/badjson":
			io.WriteString(w, `not json`)
		case p == "/repos/o/r/pulls" && r.Method == http.MethodGet:
			var page int
			fmt.Sscan(r.URL.Query().Get("page"), &page)
			n := min(per, totalPRs-(page-1)*per)
			items := make([]string, 0, max(n, 0))
			for i := range n {
				items = append(items, fmt.Sprintf(`{"number":%d,"head":{"ref":"deps/x"}}`, (page-1)*per+i+1))
			}
			io.WriteString(w, "["+strings.Join(items, ",")+"]")
		case p == "/repos/o/r/pulls/1":
			io.WriteString(w, `{"number":1,"title":"t","body":"b","state":"closed","html_url":"u","merged":true,"merged_at":"2026-01-01T00:00:00Z","mergeable":null,"created_at":"2026-01-01T00:00:00Z","head":{"ref":"deps/a"},"base":{"ref":"main"}}`)
		case p == "/repos/o/r/pulls/2":
			io.WriteString(w, `{"number":2,"state":"open","merged":false,"merged_at":null,"mergeable":true,"head":{"ref":"deps/b"}}`)
		case p == "/repos/o/r/pulls/3":
			io.WriteString(w, `{"number":3,"state":"open","merged":false,"merged_at":null,"mergeable":false,"head":{"ref":"deps/c"}}`)
		case p == "/repos/o/r/pulls" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			io.WriteString(w, `{"number":7,"title":"t","html_url":"https://x/pr/7","head":{"ref":"deps/a"}}`)
		case p == "/repos/o/r/pulls/9":
			w.WriteHeader(http.StatusUnprocessableEntity)
			io.WriteString(w, strings.Repeat("e", 300))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &reqs
}

func TestClients(t *testing.T) {
	for _, tc := range []struct {
		kind               Kind
		auth, accept       string
		listQuery          string
		merged1            bool
		mergeable1, merge3 *bool
	}{
		{GitHub, "Bearer tok", "application/vnd.github+json", "state=open&sort=updated&direction=desc&per_page=100&page=", true, nil, new(false)},
		{Gitea, "token tok", "application/json", "state=open&sort=recentupdate&limit=50&page=", true, new(false), new(false)},
		{Forgejo, "token tok", "application/json", "state=open&sort=recentupdate&limit=50&page=", true, new(false), new(false)},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			per := 50
			if tc.kind == GitHub {
				per = 100
			}
			srv, reqs := fakeAPI(t, tc.kind, per*2+3)
			c := NewClient(Info{Kind: tc.kind, APIURL: srv.URL, Repo: "o/r"}, "tok", srv.Client())
			ctx := context.Background()

			if b, err := c.DefaultBranch(ctx); err != nil || b != "main" {
				t.Fatalf("DefaultBranch %q %v", b, err)
			}
			// three pages: two full, one short.
			prs, err := c.ListPRs(ctx, "open", 20)
			if err != nil || len(prs) != per*2+3 || prs[0].Head != "deps/x" {
				t.Fatalf("ListPRs len %d %v", len(prs), err)
			}
			// maxPages caps pagination.
			prs, err = c.ListPRs(ctx, "open", 1)
			if err != nil || len(prs) != per {
				t.Fatalf("ListPRs maxPages len %d %v", len(prs), err)
			}
			p1, err := c.GetPR(ctx, 1)
			if err != nil || !p1.Merged || p1.Head != "deps/a" || p1.Base != "main" || p1.URL != "u" || p1.Title != "t" || !p1.CreatedAt.Equal(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)) || !eqPtr(p1.Mergeable, tc.mergeable1) {
				t.Fatalf("GetPR 1 %+v %v", p1, err)
			}
			p2, _ := c.GetPR(ctx, 2)
			if p2.Merged || p2.Mergeable == nil || !*p2.Mergeable {
				t.Fatalf("GetPR 2 %+v", p2)
			}
			p3, _ := c.GetPR(ctx, 3)
			if !eqPtr(p3.Mergeable, tc.merge3) {
				t.Fatalf("GetPR 3 %+v", p3)
			}
			np, err := c.CreatePR(ctx, "deps/a", "main", "t", "b")
			if err != nil || np.Number != 7 || np.URL != "https://x/pr/7" {
				t.Fatalf("CreatePR %+v %v", np, err)
			}
			if err := c.UpdatePR(ctx, 2, "t2", "b2"); err != nil {
				t.Fatal(err)
			}
			if err := c.ClosePR(ctx, 2); err != nil {
				t.Fatal(err)
			}
			err = c.UpdatePR(ctx, 9, "t", "b")
			want := "PATCH /repos/o/r/pulls/9: 422 Unprocessable Entity: " + strings.Repeat("e", 200)
			if err == nil || err.Error() != want {
				t.Fatalf("error format: %v", err)
			}

			rs := *reqs
			for _, r := range rs {
				if r.auth != tc.auth || r.accept != tc.accept {
					t.Fatalf("%s %s headers auth=%q accept=%q", r.method, r.uri, r.auth, r.accept)
				}
				if tc.kind == GitHub && r.apiVer != "2022-11-28" {
					t.Fatalf("missing api version header")
				}
			}
			wantURIs := []string{
				"GET /repos/o/r",
				"GET /repos/o/r/pulls?" + tc.listQuery + "1",
				"GET /repos/o/r/pulls?" + tc.listQuery + "2",
				"GET /repos/o/r/pulls?" + tc.listQuery + "3",
				"GET /repos/o/r/pulls?" + tc.listQuery + "1",
				"GET /repos/o/r/pulls/1", "GET /repos/o/r/pulls/2", "GET /repos/o/r/pulls/3",
				"POST /repos/o/r/pulls", "PATCH /repos/o/r/pulls/2", "PATCH /repos/o/r/pulls/2", "PATCH /repos/o/r/pulls/9",
			}
			if len(rs) != len(wantURIs) {
				t.Fatalf("got %d requests", len(rs))
			}
			for i, w := range wantURIs {
				if got := rs[i].method + " " + rs[i].uri; got != w {
					t.Fatalf("req %d = %q, want %q", i, got, w)
				}
			}
			if b := rs[8].body; b["title"] != "t" || b["head"] != "deps/a" || b["base"] != "main" || b["body"] != "b" {
				t.Fatalf("create body %v", b)
			}
			if b := rs[9].body; b["title"] != "t2" || b["body"] != "b2" || b["state"] != nil {
				t.Fatalf("update body %v", b)
			}
			if b := rs[10].body; b["state"] != "closed" || len(b) != 1 {
				t.Fatalf("close body %v", b)
			}
		})
	}
}

func TestGitHubNoToken(t *testing.T) {
	srv, reqs := fakeAPI(t, GitHub, 0)
	c := NewClient(Info{Kind: GitHub, APIURL: srv.URL, Repo: "o/r"}, "", srv.Client())
	if _, err := c.DefaultBranch(context.Background()); err != nil {
		t.Fatal(err)
	}
	if (*reqs)[0].auth != "" {
		t.Fatalf("auth header sent without token")
	}
}

func eqPtr(a, b *bool) bool { return (a == nil) == (b == nil) && (a == nil || *a == *b) }

func TestClientErrors(t *testing.T) {
	for _, kind := range []Kind{GitHub, Gitea} {
		t.Run(string(kind), func(t *testing.T) {
			srv, _ := fakeAPI(t, kind, 0)
			ctx := t.Context()
			client := func(repo string) Client {
				return NewClient(Info{Kind: kind, APIURL: srv.URL, Repo: repo}, "tok", srv.Client())
			}
			missing := client("o/missing")
			_, err := missing.DefaultBranch(ctx)
			assertErrPrefix(t, "DefaultBranch", err, "GET /repos/o/missing: 404 Not Found: ")
			_, err = missing.ListPRs(ctx, "open", 3)
			assertErrPrefix(t, "ListPRs", err, "GET /repos/o/missing/pulls?state=open&")
			_, err = missing.GetPR(ctx, 1)
			assertErrPrefix(t, "GetPR", err, "GET /repos/o/missing/pulls/1: 404 Not Found: ")
			_, err = missing.CreatePR(ctx, "deps/a", "main", "t", "b")
			assertErrPrefix(t, "CreatePR", err, "POST /repos/o/missing/pulls: 404 Not Found: ")

			_, err = client("o/empty").DefaultBranch(ctx)
			assertErrPrefix(t, "empty default branch", err, "GET /repos/o/empty: empty default_branch")
			_, err = client("o/badjson").DefaultBranch(ctx)
			assertErrPrefix(t, "bad json", err, "GET /repos/o/badjson: decode: ")

			boom := errors.New("boom")
			down := NewClient(Info{Kind: kind, APIURL: "https://api.test", Repo: "o/r"}, "tok", &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, boom
			})})
			_, err = down.DefaultBranch(ctx)
			assertErrPrefix(t, "transport", err, `GET /repos/o/r: Get "https://api.test/repos/o/r": boom`)
			if !errors.Is(err, boom) {
				t.Errorf("transport error %v does not wrap the cause", err)
			}

			badURL := NewClient(Info{Kind: kind, APIURL: "https://%zz", Repo: "o/r"}, "tok", srv.Client())
			_, err = badURL.DefaultBranch(ctx)
			assertErrPrefix(t, "invalid api url", err, `parse "https://%zz/repos/o/r": `)
		})
	}
}

func assertErrPrefix(t *testing.T, op string, err error, prefix string) {
	t.Helper()
	if err == nil || !strings.HasPrefix(err.Error(), prefix) {
		t.Errorf("%s: err = %v, want prefix %q", op, err, prefix)
	}
}
