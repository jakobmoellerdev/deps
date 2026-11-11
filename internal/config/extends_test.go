package config

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config/extendref"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// fakeFetcher serves extended files by Extend.String(), counting fetches.
type fakeFetcher struct {
	files map[string]string
	calls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, e extendref.Extend) ([]byte, error) {
	f.calls = append(f.calls, e.String())
	s, ok := f.files[e.String()]
	if !ok {
		return nil, errors.New("not found")
	}
	return []byte(s), nil
}

func loadExtends(t *testing.T, body string, files map[string]string) (*Config, *fakeFetcher, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	f := &fakeFetcher{files: files}
	c, err := Load(t.Context(), dir, "", f)
	return c, f, err
}

func TestLoadExtendsMerge(t *testing.T) {
	base := `pin: digest
schedule: ["0 5 * * 1"]
labels: [dependencies]
rules:
  - managers: [gomod]
    group: go
  - names: ["k8s.io/**"]
    group: k8s
custom:
  - files: [Makefile]
customDatasources:
  shared:
    url: https://example.com/shared.json
  over:
    url: https://example.com/base.json
registryAliases: {$CI_REGISTRY: registry.example.com, $HUB: mirror.example.com}
extends: [org/presets//go.yaml]
`
	presets := `rules:
  - managers: [gomod]
    updateTypes: [major]
    ignore: true
ignorePaths: [vendor/**]
`
	local := `extends:
  - org/cfg
labels: [deps]
rules:
  - names: ["k8s.io/**"]
    ignore: true
customDatasources:
  over:
    url: https://example.com/local.json
registryAliases:
  $HUB: ""
`
	c, f, err := loadExtends(t, local, map[string]string{"org/cfg//.deps.yaml": base, "org/presets//go.yaml": presets})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(f.calls, []string{"org/cfg//.deps.yaml", "org/presets//go.yaml"}) {
		t.Errorf("fetches = %v", f.calls)
	}
	if c.Pin != PinDigest || !slices.Equal(c.Schedule, []string{"0 5 * * 1"}) || c.Schedule == nil {
		t.Errorf("pin/schedule not inherited: %q %v", c.Pin, c.Schedule)
	}
	if !slices.Equal(c.Labels, []string{"deps"}) {
		t.Errorf("labels = %v, want the local file's", c.Labels)
	}
	if !slices.Equal(c.IgnorePaths, []string{"vendor/**"}) {
		t.Errorf("ignorePaths = %v, want the extended file's", c.IgnorePaths)
	}
	if !slices.Equal(c.Extends, []string{"org/cfg"}) {
		t.Errorf("extends = %v", c.Extends)
	}
	var got []string
	for _, r := range c.Rules {
		file, line := r.Source()
		got = append(got, fmt.Sprintf("%s:%d %s %s", file, line, strings.Join(r.Managers, ","), strings.Join(r.Names, ",")))
	}
	want := []string{
		"org/presets//go.yaml:2 gomod ",
		"org/cfg//.deps.yaml:5 gomod ",
		"org/cfg//.deps.yaml:7  k8s.io/**",
		".deps.yaml:5  k8s.io/**",
	}
	if !slices.Equal(got, want) {
		t.Errorf("rules =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if len(c.Custom) != 1 || c.Custom[0].Files[0] != "Makefile" {
		t.Errorf("custom = %+v", c.Custom)
	}
	if c.CustomDatasources["shared"].URL != "https://example.com/shared.json" || c.CustomDatasources["over"].URL != "https://example.com/local.json" {
		t.Errorf("customDatasources = %+v", c.CustomDatasources)
	}
	if !maps.Equal(c.RegistryAliases, map[string]string{"$CI_REGISTRY": "registry.example.com", "$HUB": ""}) {
		t.Errorf("registryAliases = %v", c.RegistryAliases)
	}
}

// TestLoadExtendsNullLists checks that a layer whose rules, custom, customDatasources,
// registryAliases or groups are null (every entry commented out) keeps what it extends
// instead of replacing it with nothing.
func TestLoadExtendsNullLists(t *testing.T) {
	base := `rules:
  - managers: [gomod]
    group: go
custom:
  - files: [Makefile]
    match: 'x (?P<version>\S+)'
    datasource: docker
    name: alpine
customDatasources:
  shared: {url: https://example.com/shared.json}
registryAliases: {$HUB: mirror.example.com}
groups:
  go: {automerge: true}
`
	local := `extends: [org/cfg]
rules:
custom:
customDatasources:
registryAliases:
groups:
`
	c, _, err := loadExtends(t, local, map[string]string{"org/cfg//.deps.yaml": base})
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Rules) != 1 || c.Rules[0].Group != "go" {
		t.Errorf("rules = %+v, want the extended file's", c.Rules)
	}
	if len(c.Custom) != 1 || len(c.Custom[0].Files) != 1 || c.Custom[0].Files[0] != "Makefile" {
		t.Errorf("custom = %+v, want the extended file's", c.Custom)
	}
	if c.CustomDatasources["shared"].URL != "https://example.com/shared.json" {
		t.Errorf("customDatasources = %+v", c.CustomDatasources)
	}
	if !maps.Equal(c.RegistryAliases, map[string]string{"$HUB": "mirror.example.com"}) {
		t.Errorf("registryAliases = %v", c.RegistryAliases)
	}
	if g := c.Groups["go"]; g == nil || g.Automerge == nil || !*g.Automerge {
		t.Errorf("groups = %+v", c.Groups)
	}
}

func TestLoadExtendsDefaultsAndDiamond(t *testing.T) {
	// Without ignorePaths anywhere the default list applies; a file two entries reach is
	// read once.
	c, f, err := loadExtends(t, "extends: [org/a, org/b]\n", map[string]string{
		"org/a//.deps.yaml":      "extends: [org/common]\nrules: [{group: a}]\n",
		"org/b//.deps.yaml":      "extends: [org/common]\nrules: [{group: b}]\n",
		"org/common//.deps.yaml": "rules: [{group: common}]\n",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c.IgnorePaths, DefaultIgnorePaths) {
		t.Errorf("ignorePaths = %v", c.IgnorePaths)
	}
	var groups []string
	for _, r := range c.Rules {
		groups = append(groups, r.Group)
	}
	if !slices.Equal(groups, []string{"common", "a", "b"}) {
		t.Errorf("groups = %v", groups)
	}
	if len(f.calls) != 3 {
		t.Errorf("fetches = %v", f.calls)
	}
	// The local file's own rules come last and use its line numbers.
	if c2, _, err := loadExtends(t, "rules: [{group: x}]\n", nil); err != nil || c2.Rules[0].source != ".deps.yaml" || c2.Rules[0].line != 1 {
		t.Errorf("local rule source = %+v, %v", c2, err)
	}
}

func TestLoadExtendsErrors(t *testing.T) {
	// Problems of an extended file are located at the local extends entry they came through;
	// their message is the problem in the extended file.
	for _, tt := range []struct {
		name  string
		local string
		files map[string]string
		want  []Problem // Path is the local file's base name
	}{
		{
			name:  "cycle",
			local: "extends: [org/a]\n",
			files: map[string]string{"org/a//.deps.yaml": "extends: [org/b]\n", "org/b//.deps.yaml": "extends: [org/a]\n"},
			want:  []Problem{{Line: 1, Col: 11, Msg: "org/b//.deps.yaml:1:11: extends[0]: cycle: org/a//.deps.yaml -> org/b//.deps.yaml -> org/a//.deps.yaml"}},
		},
		{
			name:  "unknown key in an extended file",
			local: "pin: digest\nextends:\n  - org/a\n",
			files: map[string]string{"org/a//.deps.yaml": "rules:\n  - managr: gomod\n"},
			want:  []Problem{{Line: 3, Col: 5, Msg: "org/a//.deps.yaml:2:5: rules[0].managr: unknown key (did you mean managers?)"}},
		},
		{
			name:  "invalid rules in both files",
			local: "extends:\n  - org/a\nrules:\n  - allow: '<<1'\n",
			files: map[string]string{"org/a//.deps.yaml": "rules:\n  - group: x\n  - allow: '<<7'\n"},
			want: []Problem{
				{Line: 2, Col: 5, Msg: `org/a//.deps.yaml:3:12: rules[1].allow: invalid constraint "<<7": improper constraint: "<<7"`},
				{Line: 4, Col: 12, Field: "rules[0].allow", Msg: `invalid constraint "<<1": improper constraint: "<<1"`},
			},
		},
		{
			name:  "fetch failure",
			local: "extends:\n  - org/missing#v1\n",
			want:  []Problem{{Line: 2, Col: 5, Field: "extends[0]", Msg: "org/missing//.deps.yaml#v1: not found"}},
		},
		{
			name:  "bad entry",
			local: "extends:\n  - nope\n",
			want:  []Problem{{Line: 2, Col: 5, Field: "extends[0]", Msg: `repository "nope" must be owner/name or a git URL`}},
		},
		{
			name:  "bad entry in an extended file",
			local: "extends:\n  - org/a\n",
			files: map[string]string{"org/a//.deps.yaml": "extends: ['x#']\n"},
			want:  []Problem{{Line: 2, Col: 5, Msg: `org/a//.deps.yaml:1:11: extends[0]: invalid ref "" after '#'`}},
		},
		{
			name:  "syntax error in an extended file",
			local: "pinn: digest\nextends:\n  - org/a\n",
			files: map[string]string{"org/a//.deps.yaml": "rules: [\n"},
			want: []Problem{
				{Line: 1, Col: 1, Field: "pinn", Msg: "unknown key (did you mean pin?)"},
				{Line: 3, Col: 5, Msg: "org/a//.deps.yaml:2:1: did not find expected node content"},
			},
		},
		{
			name:  "Renovate preset",
			local: "extends: [config:recommended, org/a]\n",
			files: map[string]string{"org/a//.deps.yaml": "rules: [{managers: [gomod], allow: '<<1'}]\n"},
			want: []Problem{{Line: 1, Col: 11, Field: "extends[0]", Msg: `repository "config:recommended" must be owner/name or a git URL` +
				" (Renovate presets are not supported; the common ones are built in, see docs/migrating-from-renovate.md#presets)"}},
		},
		{
			name:  "not a list",
			local: "extends: org/a\n",
			want:  []Problem{{Line: 1, Col: 10, Field: "extends", Msg: `want a list, got "org/a"`}},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := loadExtends(t, tt.local, tt.files)
			ce, ok := errors.AsType[*Error](err)
			if !ok {
				t.Fatalf("err = %v, want *Error", err)
			}
			var got []Problem
			for _, p := range ce.Problems() {
				q := *p
				q.Path = filepath.Base(q.Path)
				got = append(got, q)
			}
			for i := range tt.want {
				tt.want[i].Path = ".deps.yaml"
			}
			if !slices.Equal(got, tt.want) {
				t.Errorf("problems =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
	// Without a fetcher extends is an error.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte("extends: [org/a]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), dir, "", nil); err == nil || !strings.Contains(err.Error(), "extends[0]: extends is not supported by this command") {
		t.Errorf("nil fetcher: %v", err)
	}
}

// gitRepo creates a bare repository with files committed on main and tagged v1, and returns
// its file:// URL.
func gitRepo(t testing.TB, files map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	root := t.TempDir()
	work, bare := filepath.Join(root, "work"), filepath.Join(root, "cfg.git")
	for name, content := range files {
		p := filepath.Join(work, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, args := range [][]string{
		{"init", "-q", "-b", "main", work},
		{"-C", work, "add", "."},
		{"-C", work, "-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "config"},
		{"-C", work, "tag", "v1"},
		{"clone", "-q", "--bare", work, bare},
	} {
		if out, err := exec.Command("git", args...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	return "file://" + filepath.ToSlash(bare)
}

func TestGitFetcher(t *testing.T) {
	u := gitRepo(t, map[string]string{".deps.yaml": "pin: digest\n", "presets/go.yaml": "rules: [{managers: [gomod], group: go}]\n"})
	g := &GitFetcher{}
	for _, tt := range []struct {
		e    extendref.Extend
		want string
		err  string
	}{
		{e: extendref.Extend{Repo: u, Path: ".deps.yaml"}, want: "pin: digest\n"},
		{e: extendref.Extend{Repo: u, Path: "presets/go.yaml", Ref: "v1"}, want: "rules: [{managers: [gomod], group: go}]\n"},
		{e: extendref.Extend{Repo: u, Path: "presets/go.yaml", Ref: "main"}, want: "rules: [{managers: [gomod], group: go}]\n"},
		{e: extendref.Extend{Repo: u, Path: "missing.yaml"}, err: "no file missing.yaml at HEAD of " + u},
		{e: extendref.Extend{Repo: u, Path: ".deps.yaml", Ref: "nope"}, err: "fetch " + u + " nope"},
		{e: extendref.Extend{Repo: "org/cfg", Path: ".deps.yaml"}, err: "no forge to resolve owner/name against"},
	} {
		got, err := g.Fetch(t.Context(), tt.e)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("Fetch(%v) = %q, %v; want error containing %q", tt.e, got, err, tt.err)
			}
			continue
		}
		if err != nil || string(got) != tt.want {
			t.Errorf("Fetch(%v) = %q, %v; want %q", tt.e, got, err, tt.want)
		}
	}
	// Load end to end through git, with a ref.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte("extends: ['"+u+"//presets/go.yaml#v1']\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(t.Context(), dir, "", &GitFetcher{})
	if err != nil || len(c.Rules) != 1 || c.Rules[0].Group != "go" || c.Rules[0].source != u+"//presets/go.yaml#v1" {
		t.Fatalf("Load = %+v, %v", c, err)
	}
}

// TestGitFetcherSharesFetch reads several files of one repository and ref from one fetch:
// once the repository is gone, its other files still read, until Close.
func TestGitFetcherSharesFetch(t *testing.T) {
	u := gitRepo(t, map[string]string{".deps.yaml": "pin: digest\n", "presets/go.yaml": "rules: [{managers: [gomod], group: go}]\n"})
	g := &GitFetcher{}
	defer g.Close()
	for _, ref := range []string{"", "v1"} {
		if _, err := g.Fetch(t.Context(), extendref.Extend{Repo: u, Path: ".deps.yaml", Ref: ref}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.RemoveAll(strings.TrimPrefix(u, "file://")); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"", "v1"} {
		e := extendref.Extend{Repo: u, Path: "presets/go.yaml", Ref: ref}
		if got, err := g.Fetch(t.Context(), e); err != nil || string(got) != "rules: [{managers: [gomod], group: go}]\n" {
			t.Errorf("Fetch(%v) = %q, %v", e, got, err)
		}
	}
	if err := g.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Fetch(t.Context(), extendref.Extend{Repo: u, Path: "missing.yaml"}); err == nil || !strings.Contains(err.Error(), "fetch "+u+" HEAD") {
		t.Errorf("Fetch after Close = %v; want a new fetch", err)
	}
	// The content cache outlives Close.
	if got, err := g.Fetch(t.Context(), extendref.Extend{Repo: u, Path: ".deps.yaml"}); err != nil || string(got) != "pin: digest\n" {
		t.Errorf("cached Fetch after Close = %q, %v", got, err)
	}
}

// BenchmarkGitFetcherSameRepo reads two files of one repository and ref, as an extends chain
// of presets from one config repository does.
func BenchmarkGitFetcherSameRepo(b *testing.B) {
	u := gitRepo(b, map[string]string{".deps.yaml": "pin: digest\n", "presets/go.yaml": "rules: [{managers: [gomod], group: go}]\n"})
	for b.Loop() {
		g := &GitFetcher{}
		for _, p := range []string{".deps.yaml", "presets/go.yaml"} {
			if _, err := g.Fetch(b.Context(), extendref.Extend{Repo: u, Path: p, Ref: "v1"}); err != nil {
				b.Fatal(err)
			}
		}
		if err := g.Close(); err != nil {
			b.Fatal(err)
		}
	}
}

func TestGitFetcherServer(t *testing.T) {
	u := gitRepo(t, map[string]string{".deps.yaml": "pin: digest\n"})
	server := strings.TrimSuffix(u, "/cfg.git")
	parent := filepath.Base(filepath.Dir(strings.TrimPrefix(u, "file://")))
	server = strings.TrimSuffix(server, "/"+parent)
	calls := 0
	g := &GitFetcher{Server: func(context.Context) (string, error) { calls++; return server + "/", nil }}
	for range 2 {
		got, err := g.Fetch(t.Context(), extendref.Extend{Repo: parent + "/cfg", Path: ".deps.yaml"})
		if err != nil || string(got) != "pin: digest\n" {
			t.Fatalf("Fetch = %q, %v", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("Server called %d times", calls)
	}
	g = &GitFetcher{Server: func(context.Context) (string, error) { return "", errors.New("no origin remote") }}
	if _, err := g.Fetch(t.Context(), extendref.Extend{Repo: "org/cfg", Path: ".deps.yaml"}); err == nil || !strings.Contains(err.Error(), "no origin remote; write the repository as a git URL") {
		t.Errorf("Fetch without server: %v", err)
	}
}

func TestGitFetcherCredentialEnv(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "2")
	auth := httpx.NewAuth(platform.Info{Host: "git.example.com"}, "tok", func(string) string { return "" })
	g := &GitFetcher{Auth: auth}
	env := g.credentialEnv("https://git.example.com/org/cfg.git")
	want := []string{
		"GIT_CONFIG_COUNT=3",
		"GIT_CONFIG_KEY_2=credential.https://git.example.com.helper",
		"DEPS_EXTENDS_USER=x-access-token",
		"DEPS_EXTENDS_TOKEN=tok",
	}
	for _, w := range want {
		if !slices.Contains(env, w) {
			t.Errorf("env %v lacks %q", env, w)
		}
	}
	if strings.Contains(strings.Join(env[:3], "\n"), "tok\n") {
		t.Errorf("token in the git config: %v", env)
	}
	for _, u := range []string{"https://other.example.com/org/cfg.git", "ssh://git@git.example.com/org/cfg.git", "git@git.example.com:org/cfg.git", "::"} {
		if env := g.credentialEnv(u); env != nil {
			t.Errorf("credentialEnv(%q) = %v, want none", u, env)
		}
	}
	if env := (&GitFetcher{}).credentialEnv("https://git.example.com/org/cfg.git"); env != nil {
		t.Errorf("nil Auth: %v", env)
	}
	if got := redact("https://user@host/x.git"); got != "https://host/x.git" {
		t.Errorf("redact = %q", got)
	}
}

// TestGitFetcherHelperAnswers runs the credential helper through git itself.
func TestGitFetcherHelperAnswers(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not in PATH")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_COUNT", "0")
	auth := httpx.NewAuth(platform.Info{Host: "git.example.com"}, "tok", func(string) string { return "" })
	env := (&GitFetcher{Auth: auth}).credentialEnv("https://git.example.com/org/cfg.git")
	cmd := exec.Command("git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=git.example.com\n\n")
	cmd.Env = append(append(os.Environ(), "GIT_TERMINAL_PROMPT=0"), env...)
	out, err := cmd.Output()
	if err != nil || !strings.Contains(string(out), "username=x-access-token\n") || !strings.Contains(string(out), "password=tok\n") {
		t.Errorf("git credential fill = %q, %v", out, err)
	}
}
