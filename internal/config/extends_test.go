package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// fakeFetcher serves extended files by Extend.String(), counting fetches.
type fakeFetcher struct {
	files map[string]string
	calls []string
}

func (f *fakeFetcher) Fetch(_ context.Context, e Extend) ([]byte, error) {
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

func TestParseExtend(t *testing.T) {
	for _, tt := range []struct {
		in   string
		want Extend
		err  string
	}{
		{in: "org/deps-config", want: Extend{Repo: "org/deps-config", Path: ".deps.yaml"}},
		{in: "org/cfg//go.yaml#v1", want: Extend{Repo: "org/cfg", Path: "go.yaml", Ref: "v1"}},
		{in: "group/sub/cfg//presets/base.yaml#release/1.x", want: Extend{Repo: "group/sub/cfg", Path: "presets/base.yaml", Ref: "release/1.x"}},
		{in: "https://github.com/org/cfg.git//a.yaml#main", want: Extend{Repo: "https://github.com/org/cfg.git", Path: "a.yaml", Ref: "main"}},
		{in: "file:///srv/git/cfg.git", want: Extend{Repo: "file:///srv/git/cfg.git", Path: ".deps.yaml"}},
		{in: "git@github.com:org/cfg.git//x.yaml", want: Extend{Repo: "git@github.com:org/cfg.git", Path: "x.yaml"}},
		{in: "ssh://git@host:2222/org/cfg.git", want: Extend{Repo: "ssh://git@host:2222/org/cfg.git", Path: ".deps.yaml"}},
		{in: "cfg", err: "must be owner/name"},
		{in: "org/cfg#", err: "invalid ref"},
		{in: "org/cfg#-upload-pack=x", err: "invalid ref"},
		{in: "org/cfg#a..b", err: "invalid ref"},
		{in: "org/cfg//", err: "invalid path"},
		{in: "org/cfg//../x.yaml", err: "invalid path"},
		{in: "org/cfg///etc/passwd", err: "invalid path"},
		{in: "org/cfg//a/./b.yaml", err: "invalid path"},
		{in: "ftp://host/org/cfg", err: "scheme must be"},
		{in: "https:///org/cfg", err: "no host"},
		{in: "https://host", err: "no path"},
		{in: "https://u:secret@host/org/cfg", err: "must not contain a password"},
		{in: "-oops/x", err: "must be owner/name"},
	} {
		got, err := ParseExtend(tt.in)
		if tt.err != "" {
			if err == nil || !strings.Contains(err.Error(), tt.err) {
				t.Errorf("ParseExtend(%q) = %+v, %v; want error containing %q", tt.in, got, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("ParseExtend(%q) = %+v, %v; want %+v", tt.in, got, err, tt.want)
		}
	}
	if s := (Extend{Repo: "org/cfg", Path: ".deps.yaml", Ref: "v1"}).String(); s != "org/cfg//.deps.yaml#v1" {
		t.Errorf("String = %q", s)
	}
}

func TestLoadExtendsMerge(t *testing.T) {
	base := `pin: digest
schedule: ["0 5 * * 1"]
labels: [dependencies]
rules:
  - manager: gomod
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
extends: [org/presets//go.yaml]
`
	presets := `rules:
  - manager: gomod
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
		got = append(got, fmt.Sprintf("%s:%d %s %s", file, line, r.Manager, strings.Join(r.Names, ",")))
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
	for _, tt := range []struct {
		name  string
		local string
		files map[string]string
		msg   string // in the error
		prob  Problem
	}{
		{
			name:  "cycle",
			local: "extends: [org/a]\n",
			files: map[string]string{"org/a//.deps.yaml": "extends: [org/b]\n", "org/b//.deps.yaml": "extends: [org/a]\n"},
			msg:   "org/b//.deps.yaml: extends[0]: cycle: org/a//.deps.yaml -> org/b//.deps.yaml -> org/a//.deps.yaml",
			prob:  Problem{Line: 1, Msg: "org/b//.deps.yaml: extends[0]: cycle: org/a//.deps.yaml -> org/b//.deps.yaml -> org/a//.deps.yaml"},
		},
		{
			name:  "unknown key in an extended file",
			local: "pin: digest\nextends:\n  - org/a\n",
			files: map[string]string{"org/a//.deps.yaml": "rules:\n  - managr: gomod\n"},
			msg:   "org/a//.deps.yaml: yaml:",
			prob:  Problem{Line: 3, Msg: "org/a//.deps.yaml:2: field managr not found in type config.Rule"},
		},
		{
			name:  "invalid rule in an extended file",
			local: "extends:\n  - org/a\nrules:\n  - allow: '<<1'\n",
			files: map[string]string{"org/a//.deps.yaml": "rules:\n  - group: x\n  - allow: '<<7'\n"},
			msg:   "org/a//.deps.yaml: rules[1].allow:",
		},
		{
			name:  "fetch failure",
			local: "extends:\n  - org/missing#v1\n",
			msg:   ".deps.yaml: extends[0]: org/missing//.deps.yaml#v1: not found",
			prob:  Problem{Line: 2, Msg: "extends[0]: org/missing//.deps.yaml#v1: not found"},
		},
		{
			name:  "bad entry",
			local: "extends:\n  - nope\n",
			msg:   "extends[0]: repository \"nope\" must be owner/name",
			prob:  Problem{Line: 2, Msg: "extends[0]: repository \"nope\" must be owner/name or a git URL"},
		},
		{
			name:  "bad entry in an extended file",
			local: "extends:\n  - org/a\n",
			files: map[string]string{"org/a//.deps.yaml": "extends: ['x#']\n"},
			prob:  Problem{Line: 2, Msg: "org/a//.deps.yaml: extends[0]: invalid ref \"\" after '#'"},
		},
		{
			name:  "not a list",
			local: "extends: org/a\n",
			msg:   "cannot construct",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, _, err := loadExtends(t, tt.local, tt.files)
			var ce *Error
			if !errors.As(err, &ce) {
				t.Fatalf("err = %v, want *Error", err)
			}
			if !strings.Contains(err.Error(), tt.msg) {
				t.Errorf("err = %v, want it to contain %q", err, tt.msg)
			}
			if !strings.HasSuffix(ce.File, ".deps.yaml") {
				t.Errorf("File = %q, want the local file", ce.File)
			}
			if tt.prob != (Problem{}) && !slices.Contains(ce.Problems, tt.prob) {
				t.Errorf("problems = %+v, want %+v", ce.Problems, tt.prob)
			}
		})
	}
	// Problems of both files: the local one at its line (its own index), the extended one
	// at the extends entry.
	_, _, err := loadExtends(t, "extends:\n  - org/a\nrules:\n  - allow: '<<1'\n", map[string]string{"org/a//.deps.yaml": "rules:\n  - group: x\n  - allow: '<<7'\n"})
	var ce *Error
	if !errors.As(err, &ce) || len(ce.Problems) != 2 || ce.Problems[0].Line != 2 || ce.Problems[1].Line != 4 ||
		!strings.HasPrefix(ce.Problems[0].Msg, "org/a//.deps.yaml: rules[1].allow:") || !strings.HasPrefix(ce.Problems[1].Msg, "rules[0].allow:") {
		t.Errorf("problems = %+v", ce)
	}
	// Without a fetcher extends is an error.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte("extends: [org/a]\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(t.Context(), dir, "", nil); err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Errorf("nil fetcher: %v", err)
	}
}

// gitRepo creates a bare repository with files committed on main and tagged v1, and returns
// its file:// URL.
func gitRepo(t *testing.T, files map[string]string) string {
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
	u := gitRepo(t, map[string]string{".deps.yaml": "pin: digest\n", "presets/go.yaml": "rules: [{manager: gomod, group: go}]\n"})
	g := &GitFetcher{}
	for _, tt := range []struct {
		e    Extend
		want string
		err  string
	}{
		{e: Extend{Repo: u, Path: ".deps.yaml"}, want: "pin: digest\n"},
		{e: Extend{Repo: u, Path: "presets/go.yaml", Ref: "v1"}, want: "rules: [{manager: gomod, group: go}]\n"},
		{e: Extend{Repo: u, Path: "presets/go.yaml", Ref: "main"}, want: "rules: [{manager: gomod, group: go}]\n"},
		{e: Extend{Repo: u, Path: "missing.yaml"}, err: "no file missing.yaml at HEAD of " + u},
		{e: Extend{Repo: u, Path: ".deps.yaml", Ref: "nope"}, err: "fetch " + u + " nope"},
		{e: Extend{Repo: "org/cfg", Path: ".deps.yaml"}, err: "no forge to resolve owner/name against"},
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

func TestGitFetcherServer(t *testing.T) {
	u := gitRepo(t, map[string]string{".deps.yaml": "pin: digest\n"})
	server := strings.TrimSuffix(u, "/cfg.git")
	parent := filepath.Base(filepath.Dir(strings.TrimPrefix(u, "file://")))
	server = strings.TrimSuffix(server, "/"+parent)
	calls := 0
	g := &GitFetcher{Server: func(context.Context) (string, error) { calls++; return server + "/", nil }}
	for range 2 {
		got, err := g.Fetch(t.Context(), Extend{Repo: parent + "/cfg", Path: ".deps.yaml"})
		if err != nil || string(got) != "pin: digest\n" {
			t.Fatalf("Fetch = %q, %v", got, err)
		}
	}
	if calls != 1 {
		t.Errorf("Server called %d times", calls)
	}
	g = &GitFetcher{Server: func(context.Context) (string, error) { return "", errors.New("no origin remote") }}
	if _, err := g.Fetch(t.Context(), Extend{Repo: "org/cfg", Path: ".deps.yaml"}); err == nil || !strings.Contains(err.Error(), "no origin remote; write the repository as a git URL") {
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
