package renovate

import (
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

func TestParsePreset(t *testing.T) {
	tests := []struct {
		in   string
		want presetRef
		err  string
	}{
		{in: ":dependencyDashboard", want: presetRef{source: "internal", repo: "default", name: "dependencyDashboard"}},
		{in: "config:recommended", want: presetRef{source: "internal", repo: "config", name: "recommended"}},
		{in: ":assignee(alice)", want: presetRef{source: "internal", repo: "default", name: "assignee", params: []string{"alice"}}},
		{in: "schedule:x(a, b)", want: presetRef{source: "internal", repo: "schedule", name: "x", params: []string{"a", "b"}}},
		{in: "github>o/r", want: presetRef{source: "github", repo: "o/r", name: "default"}},
		{in: "github>o/r:sub#v1.2", want: presetRef{source: "github", repo: "o/r", name: "sub", tag: "v1.2"}},
		{in: "github>o/r#v1.2", want: presetRef{source: "github", repo: "o/r", name: "default", tag: "v1.2"}},
		{in: "github>o/r:file/preset/sub", want: presetRef{source: "github", repo: "o/r", name: "file/preset/sub"}},
		{in: "gitea>o/r//dir/x/preset#main", want: presetRef{source: "gitea", repo: "o/r", path: "dir/x", name: "preset", tag: "main"}},
		{in: "forgejo>o/r//preset", want: presetRef{source: "forgejo", repo: "o/r", name: "preset"}},
		{in: "local>o/r:sub", want: presetRef{source: "local", repo: "o/r", name: "sub"}},
		{in: "o/r", want: presetRef{source: "local", repo: "o/r", name: "default"}},
		{in: "o/r(x)", want: presetRef{source: "local", repo: "o/r", name: "default", params: []string{"x"}}},
		{in: "gitlab>group/sub/r:p#v1", want: presetRef{source: "gitlab", repo: "group/sub/r", name: "p", tag: "v1"}},
		{in: "npm>x", err: "unsupported preset source, skipped"},
		{in: "@scope/x", err: "unsupported preset source, skipped"},
		{in: "renovate-config-x", err: "unsupported preset source, skipped"},
		{in: "https://example.com/p.json", err: "unsupported preset source, skipped"},
		{in: "./p.json", err: "unsupported preset source, skipped"},
		{in: "github>o", err: `invalid preset "github>o"`},
	}
	for _, tt := range tests {
		got, err := parsePreset(tt.in)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("parsePreset(%q) err = %v, want %s", tt.in, err, tt.err)
			}
			continue
		}
		if err != nil || !reflect.DeepEqual(got, tt.want) {
			t.Errorf("parsePreset(%q) = %+v, %v, want %+v", tt.in, got, err, tt.want)
		}
	}
}

func mustJSON5(t *testing.T, s string) map[string]any {
	t.Helper()
	v, err := parseJSON5([]byte(s))
	if err != nil {
		t.Fatal(err)
	}
	return v.(map[string]any)
}

func TestMerge(t *testing.T) {
	parent := mustJSON5(t, `{packageRules: [{a: 1}], ignorePaths: ['x'], lockFileMaintenance: {enabled: true, schedule: ['s']}, matchFoo: ['p'], labels: ['l']}`)
	child := mustJSON5(t, `{packageRules: [{b: 2}], ignorePaths: ['y'], lockFileMaintenance: {enabled: false}, matchFoo: ['c'], labels: ['m']}`)
	got := merge(parent, child)
	want := mustJSON5(t, `{packageRules: [{a: 1}, {b: 2}], ignorePaths: ['y'], lockFileMaintenance: {enabled: false, schedule: ['s']}, matchFoo: ['p', 'c'], labels: ['m']}`)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merge = %v, want %v", got, want)
	}
	if len(parent["packageRules"].([]any)) != 1 {
		t.Error("merge modified the parent")
	}
}

// resolveWith resolves cfg with extra built-in presets.
func resolveWith(t *testing.T, presets map[string]string, cfg string) (map[string]any, *notes) {
	t.Helper()
	for k, v := range presets {
		old, had := builtin[k]
		builtin[k] = v
		t.Cleanup(func() {
			if had {
				builtin[k] = old
			} else {
				delete(builtin, k)
			}
		})
	}
	n := &notes{}
	r := &resolver{ctx: t.Context(), notes: n}
	m := mustJSON5(t, cfg)
	setOrigins(m, "")
	got, err := r.resolve(m, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return got, n
}

func TestResolve(t *testing.T) {
	got, n := resolveWith(t, map[string]string{
		"default:testA":      `{extends: [':testB'], packageRules: [{matchPackageNames: ['a'], groupName: '{{arg0}}'}], schedule: ['before 1am']}`,
		"default:testB":      `{extends: [':testA'], packageRules: [{matchPackageNames: ['b']}], prHourlyLimit: 4}`,
		"default:testWeekly": `{schedule: ['on monday']}`,
	}, `{extends: [':testA(grp)', 'group:monorepos', 'npm>x'], schedule: ['on sunday'],
		packageRules: [{matchPackageNames: ['c'], extends: [':testWeekly']}], lockFileMaintenance: {extends: [':testWeekly']}}`)
	rules := got["packageRules"].([]any)
	var origins []string
	for _, r := range rules {
		origins = append(origins, r.(map[string]any)[originKey].(string))
	}
	if want := []string{":testB packageRules[0]", ":testA(grp) packageRules[0]", "packageRules[0]"}; !slices.Equal(origins, want) {
		t.Errorf("origins = %q, want %q", origins, want)
	}
	if g := rules[1].(map[string]any)["groupName"]; g != "grp" {
		t.Errorf("{{arg0}} = %v", g)
	}
	if s := rules[2].(map[string]any)["schedule"]; !reflect.DeepEqual(s, []any{"on monday"}) {
		t.Errorf("rule extends: schedule = %v", s)
	}
	if s := got["lockFileMaintenance"].(map[string]any)["schedule"]; !reflect.DeepEqual(s, []any{"on monday"}) {
		t.Errorf("nested extends: schedule = %v", s)
	}
	if s := got["schedule"]; !reflect.DeepEqual(s, []any{"on sunday"}) || got["prHourlyLimit"] != 4.0 {
		t.Errorf("own keys win: schedule = %v, prHourlyLimit = %v", s, got["prHourlyLimit"])
	}
	if _, ok := got["extends"]; ok {
		t.Error("extends kept")
	}
	want := []string{
		"warn :testA: preset cycle, skipped",
		"info group:monorepos: deps' built-in monorepo groups (monorepos: true)",
		"warn npm>x: unsupported preset source, skipped",
	}
	var gotNotes []string
	for _, note := range n.list {
		gotNotes = append(gotNotes, note.Level.String()+" "+note.Path+": "+note.Msg)
	}
	if !slices.Equal(gotNotes, want) {
		t.Errorf("notes = %q, want %q", gotNotes, want)
	}
}

// The built-in preset table is valid JSON5 and every preset resolves.
func TestBuiltinPresets(t *testing.T) {
	for name := range builtin {
		r := &resolver{ctx: t.Context(), notes: &notes{}}
		if _, err := r.resolve(map[string]any{"extends": []any{name}}, nil, nil); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// presetServer serves GitHub contents API and Gitea raw API requests from files, keyed by
// "<api>/<repo>/<file>@<ref>".
func presetServer(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ref := r.URL.Query().Get("ref")
		var key string
		switch {
		case strings.HasPrefix(r.URL.Path, "/repos/") && strings.Contains(r.URL.Path, "/contents/"):
			repo, file, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/repos/"), "/contents/")
			key = "github/" + repo + "/" + file + "@" + ref
			if body, ok := files[key]; ok {
				_ = json.MarshalWrite(w, map[string]string{"type": "file", "encoding": "base64", "content": base64.StdEncoding.EncodeToString([]byte(body))})
				return
			}
		case strings.HasPrefix(r.URL.Path, "/api/v1/repos/"):
			if r.Header.Get("Authorization") != "token gtok" {
				t.Errorf("gitea Authorization = %q", r.Header.Get("Authorization"))
			}
			repo, file, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/api/v1/repos/"), "/raw/")
			key = "gitea/" + repo + "/" + file + "@" + ref
			if body, ok := files[key]; ok {
				_, _ = io.WriteString(w, body)
				return
			}
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestFetcher(t *testing.T) {
	srv := presetServer(t, map[string]string{
		"github/org/presets/renovate.json@":  `{packageRules: [{matchPackageNames: ['gh'], enabled: false}]}`,
		"github/org/presets/go.json@v1":      `{packageRules: [{matchPackageNames: ['gh-go'], enabled: false}]}`,
		"gitea/org/presets/default.json@":    `{extends: ['local>org/presets:sub/inner']}`,
		"gitea/org/presets/sub.json@":        `{inner: {branchPrefix: 'gitea/'}}`,
		"gitea/org/presets/dir/p.json5@main": `{prHourlyLimit: 7}`,
	})
	auth := httpx.NewAuth(platform.Info{Host: "127.0.0.1"}, "gtok", func(k string) string {
		return map[string]string{"DEPS_GITHUB_COM_TOKEN": "tok"}[k]
	}, httpx.WithHelper(func(context.Context, string) (string, string, bool) { return "", "", false }))
	gitea := platform.Info{Kind: platform.Gitea, ServerURL: srv.URL, APIURL: srv.URL + "/api/v1", Host: "127.0.0.1", Repo: "org/repo"}
	f := &Fetcher{
		Platform: func(context.Context) (platform.Info, error) { return gitea, nil },
		GitHub:   githubapi.New(srv.Client(), auth, nil, githubapi.WithAPI(srv.URL, srv.URL+"/graphql")),
		Client:   srv.Client(),
		Auth:     auth,
	}
	dir := t.TempDir()
	write := func(src string) {
		if err := os.WriteFile(filepath.Join(dir, "renovate.json"), []byte(src), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(`{extends: ['github>org/presets', 'github>org/presets:go#v1', 'local>org/presets', 'gitea>org/presets//dir/p.json5#main']}`)
	res, err := Load(t.Context(), dir, "", f)
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.YAML)
	for _, s := range []string{"\nbranchPrefix: gitea/\n", "\nprHourlyLimit: 7\n", "names: [gh]", "names: [gh-go]", "# github>org/presets packageRules[0]"} {
		if !strings.Contains(y, s) {
			t.Errorf("missing %q in\n%s", s, y)
		}
	}
	if !slices.Contains(noteLines(res), `info extends[0] "github>org/presets": renovate.json as preset file is deprecated in Renovate`) {
		t.Errorf("notes = %q", noteLines(res))
	}

	write(`{extends: ['github>org/presets:missing']}`)
	if _, err := Load(t.Context(), dir, "", f); err == nil || err.Error() != `extends[0] "github>org/presets:missing": missing.json not found in org/presets` {
		t.Errorf("missing file: %v", err)
	}
	write(`{extends: ['local>org/presets:sub/nope']}`)
	if _, err := Load(t.Context(), dir, "", f); err == nil || err.Error() != `extends[0] "local>org/presets:sub/nope": preset sub/nope not found in sub.json` {
		t.Errorf("missing key: %v", err)
	}
	f = &Fetcher{Platform: func(context.Context) (platform.Info, error) { return platform.Info{}, errors.New("no origin") }}
	write(`{extends: ['local>org/presets']}`)
	_, err = Load(t.Context(), dir, "", f)
	if err == nil || err.Error() != `extends[0] "local>org/presets": cannot detect the repository's forge for local> presets: no origin` {
		t.Errorf("no forge: %v", err)
	}
}

// local> presets of a GitLab repository and gitlab> presets are read through the GitLab
// repository files API, at HEAD unless a tag is given.
func TestFetcherGitLab(t *testing.T) {
	var got []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "Bearer gltok" {
			t.Errorf("Authorization = %q", a)
		}
		got = append(got, r.URL.EscapedPath()+"?"+r.URL.RawQuery)
		switch r.URL.EscapedPath() {
		case "/api/v4/projects/group%2Fsub%2Fpresets/repository/files/default.json/raw":
			_, _ = io.WriteString(w, `{extends: ['gitlab>org/shared//ci/base#v1']}`)
		case "/api/v4/projects/org%2Fshared/repository/files/ci%2Fbase.json/raw":
			_, _ = io.WriteString(w, `{prHourlyLimit: 5}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	gl := platform.Info{Kind: platform.GitLab, ServerURL: srv.URL, APIURL: srv.URL + "/api/v4", Host: "127.0.0.1", Repo: "group/sub/app"}
	old := gitLabCom
	gitLabCom = gl // gitlab> presets are on gitlab.com
	t.Cleanup(func() { gitLabCom = old })
	auth := httpx.NewAuth(platform.Info{Host: "127.0.0.1"}, "gltok", func(string) string { return "" },
		httpx.WithHelper(func(context.Context, string) (string, string, bool) { return "", "", false }))
	f := &Fetcher{Platform: func(context.Context) (platform.Info, error) { return gl, nil }, Client: srv.Client(), Auth: auth}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renovate.json"), []byte(`{extends: ['local>group/sub/presets']}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(t.Context(), dir, "", f)
	if err != nil {
		t.Fatal(err)
	}
	if y := string(res.YAML); !strings.Contains(y, "\nprHourlyLimit: 5\n") {
		t.Errorf("YAML:\n%s", y)
	}
	want := []string{
		"/api/v4/projects/group%2Fsub%2Fpresets/repository/files/default.json/raw?ref=HEAD",
		"/api/v4/projects/org%2Fshared/repository/files/ci%2Fbase.json/raw?ref=v1",
	}
	if !slices.Equal(got, want) {
		t.Errorf("requests = %q, want %q", got, want)
	}
}

// ignorePresets drops matching extends entries, in the config's own list and in every
// preset it extends.
func TestResolveIgnorePresets(t *testing.T) {
	builtin["default:testIgnore"] = `{extends: ['group:monorepos', ':testIgnoreRule'], prHourlyLimit: 4}`
	builtin["default:testIgnoreRule"] = `{packageRules: [{matchPackageNames: ['a']}]}`
	t.Cleanup(func() { delete(builtin, "default:testIgnore"); delete(builtin, "default:testIgnoreRule") })
	for _, cfg := range []string{
		`{extends: ['config:recommended'], ignorePresets: ['group:monorepos', 'group:recommended']}`,
		`{extends: [':testIgnore'], ignorePresets: ['group:monorepos', ':testIgnoreRule']}`,
	} {
		r := &resolver{ctx: t.Context(), notes: &notes{}}
		got, err := r.resolve(mustJSON5(t, cfg), nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		if r.monorepos {
			t.Errorf("%s: monorepos set", cfg)
		}
		if _, ok := got["packageRules"]; ok && strings.Contains(cfg, "testIgnoreRule") {
			t.Errorf("%s: ignored preset's rules kept: %v", cfg, got["packageRules"])
		}
		if _, ok := got["ignorePresets"]; ok {
			t.Errorf("%s: ignorePresets kept", cfg)
		}
	}
}
