package releasenotes

import (
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// fakeSource is a datasource with optional release times, source repositories and tags.
type fakeSource struct {
	times map[string]time.Time // version -> time
	err   error
	repo  string
	tags  []string
}

func (f *fakeSource) Releases(context.Context, dep.Dep, string) ([]datasource.Release, error) {
	rels := make([]datasource.Release, len(f.tags))
	for i, t := range f.tags {
		rels[i].Version = t
	}
	return rels, f.err
}

func (*fakeSource) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func (f *fakeSource) ReleaseTime(_ context.Context, d dep.Dep, v string) (time.Time, error) {
	if t, ok := f.times[d.Name+"@"+v]; ok {
		return t, f.err
	}
	return f.times[v], f.err
}

func (f *fakeSource) SourceRepo(context.Context, dep.Dep, string) (string, error) {
	return f.repo, f.err
}

// plainSource implements only datasource.Datasource.
type plainSource struct{}

func (plainSource) Releases(context.Context, dep.Dep, string) ([]datasource.Release, error) {
	return nil, nil
}

func (plainSource) Digest(context.Context, dep.Dep, string) (string, error) { return "", nil }

func TestAge(t *testing.T) {
	now := time.Date(2026, 11, 3, 9, 0, 0, 0, time.UTC)
	src := &fakeSource{times: map[string]time.Time{
		"v4": now.Add(-4*24*time.Hour - time.Hour),
		"v1": now.Add(-25 * time.Hour),
		"v0": now.Add(-time.Hour),
		"vf": now.Add(time.Hour), // clock skew
	}}
	n := New(nil, nil, nil, nil, map[string]datasource.Datasource{
		"t": src, "plain": plainSource{}, "err": &fakeSource{err: errors.New("boom")},
	})
	n.now = func() time.Time { return now }
	tests := []struct{ ds, version, want string }{
		{"t", "v4", "released 2026-10-30 (4 days ago)"},
		{"t", "v1", "released 2026-11-02 (1 day ago)"},
		{"t", "v0", "released 2026-11-03 (today)"},
		{"t", "vf", "released 2026-11-03 (today)"},
		{"t", "unknown", ""},
		{"plain", "v1", ""},
		{"err", "v1", ""},
		{"missing", "v1", ""},
	}
	for _, tt := range tests {
		if got := n.Age(t.Context(), dep.Update{Dep: dep.Dep{Datasource: tt.ds}, NewVersion: tt.version}); got != tt.want {
			t.Errorf("Age(%s, %s) = %q, want %q", tt.ds, tt.version, got, tt.want)
		}
	}
	u := dep.Update{Dep: dep.Dep{Datasource: "t", Name: "example.com/m/v2"}, NewVersion: "v3.0.0", NewName: "example.com/m/v3"}
	src.times["example.com/m/v3@v3.0.0"] = now.Add(-25 * time.Hour)
	if got := n.Age(t.Context(), u); got != "released 2026-11-02 (1 day ago)" {
		t.Errorf("Age(new module path) = %q", got)
	}
}

func TestResolve(t *testing.T) {
	auth := httpx.NewAuth(platform.Info{Host: "git.example"}, "tok", func(string) string { return "" })
	sources := map[string]datasource.Datasource{
		dsNpm:    &fakeSource{repo: "git+ssh://git@github.com/eslint/eslint.git"},
		dsDocker: &fakeSource{err: errors.New("no config")},
	}
	n := New(nil, nil, auth, nil, sources)
	gh := func(o, r, prefix string) repoRef {
		return repoRef{kind: github, host: "github.com", owner: o, name: r, tagPrefix: prefix}
	}
	tests := []struct {
		name string
		d    dep.Dep
		want repoRef
		ok   bool
	}{
		{"action", dep.Dep{Datasource: dsGitTags, Source: "https://github.com/actions/checkout"}, gh("actions", "checkout", ""), true},
		{"gitea action", dep.Dep{Datasource: dsGitTags, Source: "https://gitea.com/actions/checkout"}, repoRef{kind: gitea, host: "gitea.com", owner: "actions", name: "checkout"}, true},
		{"git-tags without source", dep.Dep{Datasource: dsGitTags}, repoRef{}, false},
		{"github-releases name", dep.Dep{Datasource: dsGitHubReleases, Name: "cli/cli"}, gh("cli", "cli", ""), true},
		{"github-releases GHES", dep.Dep{Datasource: dsGitHubReleases, Source: "https://ghe.example/o/r"}, repoRef{kind: github, host: "ghe.example", owner: "o", name: "r"}, true},
		{"go root", dep.Dep{Datasource: dsGo, Name: "github.com/spf13/cobra"}, gh("spf13", "cobra", ""), true},
		{"go major", dep.Dep{Datasource: dsGo, Name: "github.com/Masterminds/semver/v3"}, gh("Masterminds", "semver", ""), true},
		{"go subdir major", dep.Dep{Datasource: dsGo, Name: "github.com/o/r/sdk/go/v2"}, gh("o", "r", "sdk/go/"), true},
		{"go short", dep.Dep{Datasource: dsGo, Name: "github.com/o"}, repoRef{}, false},
		{"golang.org/x", dep.Dep{Datasource: dsGo, Name: "golang.org/x/mod"}, gh("golang", "mod", ""), true},
		{"k8s.io", dep.Dep{Datasource: dsGo, Name: "k8s.io/client-go"}, gh("kubernetes", "client-go", ""), true},
		{"sigs.k8s.io subdir", dep.Dep{Datasource: dsGo, Name: "sigs.k8s.io/controller-runtime/tools/setup-envtest"}, gh("kubernetes-sigs", "controller-runtime", "tools/setup-envtest/"), true},
		{"grpc", dep.Dep{Datasource: dsGo, Name: "google.golang.org/grpc"}, gh("grpc", "grpc-go", ""), true},
		{"grpc submodule", dep.Dep{Datasource: dsGo, Name: "google.golang.org/grpc/cmd/protoc-gen-go-grpc"}, gh("grpc", "grpc-go", "cmd/protoc-gen-go-grpc/"), true},
		{"vanity lookalike", dep.Dep{Datasource: dsGo, Name: "google.golang.org/grpcx"}, repoRef{}, false},
		{"vanity prefix without repo", dep.Dep{Datasource: dsGo, Name: "golang.org/x/"}, repoRef{}, false},
		{"go.yaml.in", dep.Dep{Datasource: dsGo, Name: "go.yaml.in/yaml/v3"}, gh("yaml", "go-yaml", ""), true},
		{"unknown vanity", dep.Dep{Datasource: dsGo, Name: "go.opentelemetry.io/otel"}, repoRef{}, false},
		{"gitea module with token", dep.Dep{Datasource: dsGo, Name: "git.example/o/r/v2"}, repoRef{kind: gitea, host: "git.example", owner: "o", name: "r"}, true},
		{"npm", dep.Dep{Datasource: dsNpm, Name: "eslint"}, gh("eslint", "eslint", ""), true},
		{"docker error", dep.Dep{Datasource: dsDocker, Name: "alpine"}, repoRef{}, false},
		{"toolchain", dep.Dep{Datasource: "go-toolchain", Name: "go"}, repoRef{}, false},
	}
	for _, tt := range tests {
		got, ok := n.resolve(t.Context(), tt.d, "1.0.0")
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("%s: resolve = %+v, %v; want %+v, %v", tt.name, got, ok, tt.want, tt.ok)
		}
	}
	// A datasource without SourceRepo yields nothing.
	n.sources[dsDocker] = plainSource{}
	if _, ok := n.resolve(t.Context(), dep.Dep{Datasource: dsDocker}, "1"); ok {
		t.Error("docker without SourceRepo resolved")
	}
}

func TestParseRepo(t *testing.T) {
	tests := []struct {
		raw       string
		shorthand bool
		want      string // host/owner/name, "" = not ok
		kind      forgeKind
	}{
		{"https://github.com/o/r", false, "github.com/o/r", github},
		{"https://GitHub.com/o/r.git/", false, "github.com/o/r", github},
		{"git+https://github.com/o/r.git", true, "github.com/o/r", github},
		{"git://github.com/o/r.git", true, "github.com/o/r", github},
		{"git+ssh://git@github.com/o/r.git", true, "github.com/o/r", github},
		{"ssh://git@git.example:2222/o/r.git", true, "git.example/o/r", gitea},
		{"https://git.example:8443/o/r/tree/main/packages/x", true, "git.example:8443/o/r", gitea},
		{"git@github.com:o/r.git", true, "github.com/o/r", github},
		{"git@github.com", true, "", 0},
		{"github:o/r", true, "github.com/o/r", github},
		{"github:o/r", false, "", 0},
		{"o/r", true, "github.com/o/r", github},
		{"o/r", false, "", 0},
		{"gitlab:o/r", true, "", 0},
		{"ftp://example.com/o/r", true, "", 0},
		{"https://github.com/o", true, "", 0},
		{"https://%zz", true, "", 0},
		{"", true, "", 0},
	}
	for _, tt := range tests {
		r, ok := parseRepo(tt.raw, tt.shorthand)
		got := ""
		if ok {
			got = r.host + "/" + r.owner + "/" + r.name
		}
		if got != tt.want || (ok && r.kind != tt.kind) {
			t.Errorf("parseRepo(%q, %v) = %q kind %d, want %q kind %d", tt.raw, tt.shorthand, got, r.kind, tt.want, tt.kind)
		}
	}
}

func TestVersions(t *testing.T) {
	mustParse := func(s string) version {
		v, ok := parseVersion(s)
		if !ok {
			t.Fatalf("parseVersion(%q) failed", s)
		}
		return v
	}
	for _, bad := range []string{"", "v", "1..2", "1.x", "+1", "1.-2", "main", "v1.2a"} {
		if _, ok := parseVersion(bad); ok {
			t.Errorf("parseVersion(%q) succeeded", bad)
		}
	}
	tests := []struct {
		x, b string
		want int
	}{
		{"7.0.1", "v7", 0},
		{"6.9.0", "v7", -1},
		{"8.0.0", "v7", 1},
		{"6.0.1", "v6", 0},
		{"1.2.3", "1.2.3", 0},
		{"1.2.3-rc.1", "1.2.3", -1},
		{"1.2.3", "1.2.3-rc.1", 1},
		{"1.2.3-rc.1", "1.2.3-rc.2", -1},
		{"1.2", "1.2.1", -1},
		{"1.2.3+build", "1.2.3", 0},
	}
	for _, tt := range tests {
		if got := compareAt(mustParse(tt.x), mustParse(tt.b)); got != tt.want {
			t.Errorf("compareAt(%s, %s) = %d, want %d", tt.x, tt.b, got, tt.want)
		}
	}
	if compare(mustParse("1.2"), mustParse("1.3.0")) >= 0 || compare(mustParse("1.3.0"), mustParse("1.2")) <= 0 {
		t.Error("compare does not order across precisions")
	}
	for _, c := range []struct {
		tag  string
		pfx  []string
		want string
	}{
		{"v1.2.0", []string{""}, "1.2.0"},
		{"eslint@1.2.0", []string{"", "eslint@"}, "1.2.0"},
		{"sub/v1.2.0", []string{"sub/"}, "1.2.0"},
		{"sub/v1.2.0", []string{""}, ""},
		{"other@1.2.0", []string{"", "eslint@"}, ""},
	} {
		v, ok := tagVersion(c.tag, c.pfx)
		got := ""
		if ok {
			got = fmt.Sprint(v.parts[0], ".", v.parts[1], ".", v.parts[2])
		}
		if got != c.want {
			t.Errorf("tagVersion(%q, %q) = %q, want %q", c.tag, c.pfx, got, c.want)
		}
	}
}

func TestCollect(t *testing.T) {
	d := dep.Dep{Datasource: dsGo, Name: "m"}
	up := func(from, to string) dep.Update {
		u := dep.Update{Dep: d, NewVersion: to}
		u.Dep.Version = from
		return u
	}
	pin := up("v1", "v1")
	other := up("v2.0.0", "v2.1.0")
	other.Dep.Name = "n"
	got := collect([]dep.Update{up("v1.2.0", "v1.3.0"), pin, up("v1.1.0", "v1.4.0"), other, up("v1.3.0", "v1.3.5")})
	want := []item{{d: up("v1.2.0", "").Dep, from: "v1.1.0", to: "v1.4.0"}, {d: other.Dep, from: "v2.0.0", to: "v2.1.0"}}
	if len(got) != 2 || got[0].from != want[0].from || got[0].to != want[0].to || got[1].d.Name != "n" || got[1].from != "v2.0.0" {
		t.Errorf("collect = %+v", got)
	}
	if collect([]dep.Update{pin}) != nil {
		t.Error("pin-only updates collected")
	}
}

func TestTruncate(t *testing.T) {
	long := strings.Repeat("a", maxBody+10)
	multi := strings.Repeat("ä", maxBody) // 2 bytes per rune
	fenced := "intro\n```\n" + strings.Repeat("code\n", maxBody)
	tests := []struct {
		name      string
		body      string
		cut       bool
		wantLen   int // 0 = len(body)
		wantEnd   string
		wantFence bool
	}{
		{name: "short", body: "hello"},
		{name: "ascii", body: long, wantLen: maxBody, wantEnd: "a…"},
		{name: "runes", body: multi, wantEnd: "ä…"},
		{name: "open fence closed within cap", body: fenced, wantEnd: "…\n```", wantFence: true},
		{name: "stored body already cut", body: "abc…", cut: true, wantEnd: "abc…"},
	}
	for _, tt := range tests {
		got := cutBody(tt.body, tt.cut)
		if len(got) > maxBody || !utf8.ValidString(got) {
			t.Errorf("%s: %d bytes, valid %v", tt.name, len(got), utf8.ValidString(got))
		}
		if tt.wantLen > 0 && len(got) != tt.wantLen {
			t.Errorf("%s: len = %d, want %d", tt.name, len(got), tt.wantLen)
		}
		if tt.wantEnd == "" && got != tt.body {
			t.Errorf("%s: changed to %q", tt.name, got)
		}
		if !strings.HasSuffix(got, tt.wantEnd) {
			t.Errorf("%s: ends %q, want %q", tt.name, got[max(0, len(got)-10):], tt.wantEnd)
		}
		if tt.wantFence && strings.Count(got, "```")%2 != 0 {
			t.Errorf("%s: unbalanced fences", tt.name)
		}
	}
	if s, cut := truncate("héllo", 4); s != "h…" || !cut {
		t.Errorf("truncate = %q, %v", s, cut)
	}
}

func TestSanitize(t *testing.T) {
	gh := repoRef{kind: github, host: "github.com", owner: "o", name: "r"}
	gt := repoRef{kind: gitea, host: "git.example", owner: "o", name: "r"}
	tests := []struct {
		name, in, want string
		r              repoRef
	}{
		{"mention", "thanks @alice and (@bob-2)", "thanks @\u200balice and (@\u200bbob-2)", gh},
		{"email and scoped package untouched", "mail a@b.c, use x/@y", "mail a@b.c, use x/@y", gh},
		{"issue", "fixes #12 and #3.", "fixes [#12](https://redirect.github.com/o/r/issues/12) and [#3](https://redirect.github.com/o/r/issues/3).", gh},
		{"gitea issue", "#7", "[#7](https://git.example/o/r/issues/7)", gt},
		{"cross repo", "see other/repo#5", "see [other/repo#5](https://redirect.github.com/other/repo/issues/5)", gh},
		{"gitea cross repo", "see a/b#5", "see [a/b#5](https://git.example/a/b/issues/5)", gt},
		{"not references", "## 1.2 #x [#9](u) a#1 #12abc a/b/c#1 /a#1 a/#1 a/b#", "## 1.2 #x [#9](u) a#1 #12abc a/b/c#1 /a#1 a/#1 a/b#", gt},
		{"links", "in https://github.com/o/r/pull/1", "in https://redirect.github.com/o/r/pull/1", gh},
		{"code untouched", "`@x #1`\n```\n@y #2\n```\n@z", "`@x #1`\n```\n@y #2\n```\n@\u200bz", gh},
		{"trailing newline", "a\n", "a\n", gh},
	}
	for _, tt := range tests {
		if got := sanitize(tt.in, tt.r); got != tt.want {
			t.Errorf("%s: sanitize(%q) =\n%q\nwant\n%q", tt.name, tt.in, got, tt.want)
		}
	}
}

// forge serves GitHub (REST `/repos/...` on the API base and GraphQL at `/graphql`) and Gitea
// (`/api/v1/repos/...`) release lists over TLS and records "<Authorization> <path>?<query>"
// per request (GraphQL: "<Authorization> /graphql <owner>/<name> first=<n>").
type forge struct {
	srv  *httptest.Server
	mu   sync.Mutex
	reqs []string
	rels map[string][]map[string]any // "/repos/o/r" -> releases; missing = 404
	fail map[string]int              // path -> status
}

func newForge(t *testing.T) *forge {
	t.Helper()
	f := &forge{rels: map[string][]map[string]any{}, fail: map[string]int{}}
	f.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.URL.Path == "/graphql" {
			f.graphql(w, r)
			return
		}
		f.reqs = append(f.reqs, strings.TrimSpace(r.Header.Get("Authorization")+" "+r.URL.Path+"?"+r.URL.RawQuery))
		p := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1"), "/releases")
		if code := f.fail[p]; code != 0 {
			w.WriteHeader(code)
			_, _ = w.Write([]byte("not json"))
			return
		}
		rels, ok := f.rels[p]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_ = json.MarshalWrite(w, rels)
	}))
	f.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // handshakes aborted by canceled requests
	f.srv.StartTLS()
	t.Cleanup(f.srv.Close)
	return f
}

// graphql answers the release notes query from the same releases as REST.
func (f *forge) graphql(w http.ResponseWriter, r *http.Request) {
	var q struct {
		Variables struct {
			Owner string `json:"owner"`
			Name  string `json:"name"`
			First int    `json:"first"`
		} `json:"variables"`
	}
	_ = json.UnmarshalRead(r.Body, &q)
	v := q.Variables
	f.reqs = append(f.reqs, fmt.Sprintf("%s /graphql %s/%s first=%d", r.Header.Get("Authorization"), v.Owner, v.Name, v.First))
	p := "/repos/" + v.Owner + "/" + v.Name
	if code := f.fail[p]; code != 0 {
		w.WriteHeader(code)
		_, _ = w.Write([]byte("not json"))
		return
	}
	rels, ok := f.rels[p]
	if !ok {
		_, _ = w.Write([]byte(`{"data":{"repository":null},"errors":[{"type":"NOT_FOUND","message":"Could not resolve"}]}`))
		return
	}
	nodes := make([]map[string]any, len(rels))
	for i, rel := range rels {
		nodes[i] = map[string]any{"tagName": rel["tag_name"], "url": rel["html_url"], "description": rel["body"], "isDraft": rel["draft"] == true, "isPrerelease": rel["prerelease"] == true}
	}
	_ = json.MarshalWrite(w, map[string]any{"data": map[string]any{"repository": map[string]any{"releases": map[string]any{"nodes": nodes}}}})
}

// newNotes returns Notes whose github.com API is served by f.
func (f *forge) newNotes(sem chan struct{}, auth *httpx.Auth, sources map[string]datasource.Datasource) *Notes {
	c := f.srv.Client()
	return New(c, sem, auth, githubapi.New(c, auth, sem, githubapi.WithAPI(f.srv.URL, f.srv.URL+"/graphql")), sources)
}

func (f *forge) host() string { u, _ := url.Parse(f.srv.URL); return u.Host }

func rel(tag, body string, extra ...any) map[string]any {
	m := map[string]any{"tag_name": tag, "body": body, "html_url": "https://github.com/o/r/releases/tag/" + tag}
	for i := 0; i+1 < len(extra); i += 2 {
		m[extra[i].(string)] = extra[i+1]
	}
	return m
}

func TestSection(t *testing.T) {
	f := newForge(t)
	f.rels["/repos/actions/checkout"] = []map[string]any{
		rel("v7.1.0", "too new"),
		rel("v7.0.1", "fix #1 by @alice"),
		rel("v8.0.0-rc.1", "prerelease", "prerelease", true),
		rel("v7.0.0", "major\r\n"),
		rel("v6.1.0", "backport"),
		rel("v6.0.0", "current"),
		rel("draft", "x", "draft", true),
		rel("", "untagged"),
		rel("v6.5.0", "draft in range", "draft", true),
	}
	f.rels["/repos/spf13/cobra"] = []map[string]any{rel("v1.10.2", "cobra notes"), rel("v1.9.1", "old")}
	f.rels["/repos/kubernetes/api"] = []map[string]any{}
	f.rels["/repos/o/gitea-mod"] = []map[string]any{rel("sub/v2.1.0", "gitea sub"), rel("v2.1.0", "root")}
	f.rels["/repos/eslint/eslint"] = []map[string]any{rel("v10.14.0", "fourteen"), rel("v10.13.0", ""), rel("v10.12.0", "twelve")}
	f.fail["/repos/broken/repo"] = http.StatusInternalServerError
	f.fail["/repos/bad/json"] = http.StatusOK

	gitHost := f.host()
	auth := httpx.NewAuth(platform.Info{Host: strings.Split(gitHost, ":")[0]}, "gitea-tok", func(k string) string {
		return map[string]string{"DEPS_GITHUB_COM_TOKEN": "gh-tok"}[k]
	})
	sources := map[string]datasource.Datasource{
		dsGitTags: &fakeSource{tags: []string{"v6.0.0", "v7.0.1"}},
		dsNpm:     &fakeSource{repo: "git+https://github.com/eslint/eslint.git"},
		dsGo:      plainSource{},
	}
	n := f.newNotes(make(chan struct{}, 2), auth, sources)

	action := dep.Update{Dep: dep.Dep{Datasource: dsGitTags, Name: "actions/checkout", Source: "https://github.com/actions/checkout", Version: "v6.0.0"}, NewVersion: "v7.0.1"}
	cobra := dep.Update{Dep: dep.Dep{Datasource: dsGo, Name: "github.com/spf13/cobra", Version: "v1.9.1"}, NewVersion: "v1.10.2"}
	k8s := dep.Update{Dep: dep.Dep{Datasource: dsGo, Name: "k8s.io/api", Version: "v0.34.0"}, NewVersion: "v0.35.0"}
	giteaMod := dep.Update{Dep: dep.Dep{Datasource: dsGo, Name: gitHost + "/o/gitea-mod/sub/v2", Version: "v2.0.0"}, NewVersion: "v2.1.0"}
	eslint := dep.Update{Dep: dep.Dep{Datasource: dsNpm, Name: "eslint", Version: "10.12.0"}, NewVersion: "10.14.0"}
	broken := dep.Update{Dep: dep.Dep{Datasource: dsGitTags, Name: "broken/repo", Source: "https://github.com/broken/repo", Version: "v1"}, NewVersion: "v2"}
	badJSON := dep.Update{Dep: dep.Dep{Datasource: dsGitTags, Name: "bad/json", Source: "https://github.com/bad/json", Version: "v1"}, NewVersion: "v2"}
	unknown := dep.Update{Dep: dep.Dep{Datasource: dsGo, Name: "go.opentelemetry.io/otel", Version: "v1.0.0"}, NewVersion: "v1.1.0"}

	got := n.Section(t.Context(), []dep.Update{action, broken, cobra, k8s, giteaMod, eslint, badJSON, unknown})
	for _, want := range []string{
		"### Release notes\n",
		"<summary>Release notes: actions/checkout v6.0.0 → v7.0.1</summary>\n\n" +
			"#### [v7.0.1](https://redirect.github.com/o/r/releases/tag/v7.0.1)\n\n" +
			"fix [#1](https://redirect.github.com/actions/checkout/issues/1) by @\u200balice\n\n" +
			"#### [v7.0.0](https://redirect.github.com/o/r/releases/tag/v7.0.0)\n\nmajor\n\n" +
			"#### [v6.1.0](https://redirect.github.com/o/r/releases/tag/v6.1.0)\n\nbackport\n\n" +
			"[Compare changes](https://github.com/actions/checkout/compare/v6.0.0...v7.0.1)\n\n</details>\n",
		"<summary>Release notes: github.com/spf13/cobra v1.9.1 → v1.10.2</summary>\n\n#### [v1.10.2](https://redirect.github.com/o/r/releases/tag/v1.10.2)\n\ncobra notes\n\n" +
			"[Compare changes](https://github.com/spf13/cobra/compare/v1.9.1...v1.10.2)\n\n</details>\n",
		// No releases, but Go module versions are tags: the compare link alone.
		"<summary>Release notes: k8s.io/api v0.34.0 → v0.35.0</summary>\n\n[Compare changes](https://github.com/kubernetes/api/compare/v0.34.0...v0.35.0)\n\n</details>\n",
		"#### [sub/v2.1.0](https://redirect.github.com/o/r/releases/tag/sub/v2.1.0)\n\ngitea sub\n\n[Compare changes](https://" + gitHost + "/o/gitea-mod/compare/sub/v2.0.0...sub/v2.1.0)",
		// An empty body renders only the heading; both tags have releases, so they are linked.
		"#### [v10.14.0](https://redirect.github.com/o/r/releases/tag/v10.14.0)\n\nfourteen\n\n#### [v10.13.0](https://redirect.github.com/o/r/releases/tag/v10.13.0)\n\n" +
			"[Compare changes](https://github.com/eslint/eslint/compare/v10.12.0...v10.14.0)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("section lacks\n%s\n--- got:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"too new", "prerelease", "current", "draft", "untagged", "broken/repo", "bad/json", "otel", "root", "twelve"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("section contains %q:\n%s", unwanted, got)
		}
	}

	// Lists are fetched once per repository; tokens go to github.com (GraphQL) and the platform
	// host (Gitea REST).
	_ = n.Section(t.Context(), []dep.Update{action, giteaMod})
	slices.Sort(f.reqs)
	want := []string{
		"Bearer gh-tok /graphql actions/checkout first=100",
		"Bearer gh-tok /graphql bad/json first=100",
		"Bearer gh-tok /graphql broken/repo first=100",
		"Bearer gh-tok /graphql eslint/eslint first=100",
		"Bearer gh-tok /graphql kubernetes/api first=100",
		"Bearer gh-tok /graphql spf13/cobra first=100",
		"token gitea-tok /api/v1/repos/o/gitea-mod/releases?limit=50",
	}
	if !slices.Equal(f.reqs, want) {
		t.Errorf("requests =\n%q\nwant\n%q", f.reqs, want)
	}

	if got := n.Section(t.Context(), []dep.Update{unknown, broken}); got != "" {
		t.Errorf("section without notes = %q, want empty", got)
	}
	if got := n.Section(t.Context(), nil); got != "" {
		t.Errorf("empty section = %q", got)
	}
}

func TestSectionNoCompareWithoutTags(t *testing.T) {
	f := newForge(t)
	f.rels["/repos/o/r"] = []map[string]any{rel("v2.0.0", "two")}
	n := f.newNotes(nil, nil, map[string]datasource.Datasource{dsGitTags: &fakeSource{tags: []string{"v2.0.0"}}})
	u := dep.Update{Dep: dep.Dep{Datasource: dsGitTags, Name: "o/r", Source: "https://github.com/o/r", Version: "v1.0.0"}, NewVersion: "v2.0.0"}
	got := n.Section(t.Context(), []dep.Update{u})
	if !strings.Contains(got, "two") || strings.Contains(got, "compare") {
		t.Errorf("section = %q, want notes without a compare link (v1.0.0 is not a tag)", got)
	}
	// Anonymous requests use REST and carry no Authorization header.
	if len(f.reqs) != 1 || f.reqs[0] != "/repos/o/r/releases?per_page=100" {
		t.Errorf("requests = %q", f.reqs)
	}
	// Without a git-tags datasource there is no tag list to check either.
	n.sources = nil
	n.lists = map[string]*listEntry{}
	if got := n.Section(t.Context(), []dep.Update{u}); strings.Contains(got, "compare") {
		t.Errorf("compare link without tags: %q", got)
	}
}

func TestSectionCaps(t *testing.T) {
	f := newForge(t)
	var ups []dep.Update
	for i := range 30 {
		repo := fmt.Sprintf("/repos/o/r%d", i)
		for v := 1; v <= 5; v++ {
			f.rels[repo] = append(f.rels[repo], rel(fmt.Sprintf("v1.%d.0", v), strings.Repeat("x", 5000)))
		}
		ups = append(ups, dep.Update{Dep: dep.Dep{Datasource: dsGo, Name: fmt.Sprintf("github.com/o/r%d", i), Version: "v1.0.0"}, NewVersion: "v1.5.0"})
	}
	n := f.newNotes(make(chan struct{}, 4), nil, nil)
	got := n.Section(t.Context(), ups)
	if len(got) > maxSection {
		t.Errorf("section is %d bytes, cap %d", len(got), maxSection)
	}
	if !strings.HasSuffix(got, omitted) {
		t.Errorf("section does not end with the omission note: %q", got[len(got)-80:])
	}
	if o, c := strings.Count(got, "<details>"), strings.Count(got, "</details>"); o != c || o == 0 {
		t.Errorf("details open %d, closed %d", o, c)
	}
	if c := strings.Count(got, "x…\n\n"); c == 0 {
		t.Error("no body was cut")
	}
	// Only the heading and the first block opener fit: the second check of render stops.
	huge := strings.Repeat("y", maxSection)
	blocks := []block{{title: huge, compare: "u"}}
	if got := render(blocks); got != "" {
		t.Errorf("render of an oversized block = %d bytes, want empty", len(got))
	}
}

func TestSectionCanceled(t *testing.T) {
	f := newForge(t)
	f.rels["/repos/o/r"] = []map[string]any{rel("v2.0.0", "two")}
	sem := make(chan struct{}, 1)
	sem <- struct{}{} // never available
	n := f.newNotes(sem, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	u := dep.Update{Dep: dep.Dep{Datasource: dsGo, Name: "github.com/o/r", Version: "v1.0.0"}, NewVersion: "v2.0.0"}
	got := n.Section(ctx, []dep.Update{u})
	if strings.Contains(got, "two") || len(f.reqs) != 0 {
		t.Errorf("canceled section = %q, requests %q", got, f.reqs)
	}
}

func TestFetchErrors(t *testing.T) {
	c := &http.Client{Transport: roundTrip(func(*http.Request) (*http.Response, error) { return nil, errors.New("boom") })}
	n := New(c, nil, nil, githubapi.New(c, nil, nil), nil)
	if _, err := n.releases(t.Context(), repoRef{kind: github, host: "github.com", owner: "o", name: "r"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("err = %v", err)
	}
	if _, err := n.releases(t.Context(), repoRef{kind: gitea, host: "git.example", owner: "o", name: "r"}); err == nil || !strings.Contains(err.Error(), "boom") {
		t.Errorf("gitea err = %v", err)
	}
	n.gh = githubapi.New(c, nil, nil, githubapi.WithAPI("http://[::1", ""))
	if _, err := n.fetchGitHub(t.Context(), repoRef{kind: github, host: "github.com", owner: "o", name: "r"}); err == nil {
		t.Error("bad API URL: no error")
	}
}

type roundTrip func(*http.Request) (*http.Response, error)

func (f roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
