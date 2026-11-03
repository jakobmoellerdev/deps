package main

import (
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

func TestExplain(t *testing.T) {
	files := map[string]string{
		".github/workflows/ci.yml": workflow,
		"go.mod":                   gomodSrc,
	}
	withRules := map[string]string{
		".github/workflows/ci.yml": workflow,
		"go.mod":                   gomodSrc + "require example.com/i v1.0.0 // indirect\n",
		".deps.yaml": `rules:
  - names: ["actions/*"]
    allow: "< 7"
  - manager: gomod
    updates: [patch]
  - manager: github-actions
    pin: digest
    group: ci
`,
	}
	tests := []struct {
		name   string
		files  map[string]string
		args   []string
		code   int
		stdout string
		stderr string
	}{
		{
			name:  "actions occurrences, skip excluded by glob",
			files: files,
			args:  []string{"actions/*"},
			stdout: `actions/checkout  .github/workflows/ci.yml:6  (github-actions, action, git-tags, tag)
  current   v6.0.0
  rules     none
  policy    pin=keep allow=- updates=major,minor,patch,digest,pin group=- schedule=always
  candidates 2
  rejected  not newer: 1 (v6.0.0)
  result    update v6.0.0 -> v7.0.1 (major)

actions/checkout  .github/workflows/ci.yml:7  (github-actions, action, git-tags, tag)
  current   v6.0.0@1af3b93
  rules     none
  policy    pin=keep allow=- updates=major,minor,patch,digest,pin group=- schedule=always
  candidates 2
  rejected  not newer: 1 (v6.0.0)
  result    update v6.0.0@1af3b93 -> v7.0.1@3d3c42e (major)
`,
		},
		{
			name:  "rules, policy and policy rejections",
			files: withRules,
			args:  []string{"actions/checkout", "example.com/*"},
			stdout: `actions/checkout  .github/workflows/ci.yml:6  (github-actions, action, git-tags, tag)
  current   v6.0.0
  rules     #0 names=[actions/*] allow=<7
            #2 manager=github-actions pin=digest group=ci
  policy    pin=digest allow=<7 updates=major,minor,patch,digest,pin group=ci schedule=always
  candidates 2
  rejected  not newer: 1 (v6.0.0)  outside allow <7: 1 (v7.0.1)
  result    update v6.0.0 -> v6.0.0@1af3b93 (pin)

actions/checkout  .github/workflows/ci.yml:7  (github-actions, action, git-tags, tag)
  current   v6.0.0@1af3b93
  rules     #0 names=[actions/*] allow=<7
            #2 manager=github-actions pin=digest group=ci
  policy    pin=digest allow=<7 updates=major,minor,patch,digest,pin group=ci schedule=always
  candidates 2
  rejected  not newer: 1 (v6.0.0)  outside allow <7: 1 (v7.0.1)
  result    up to date

example.com/a  go.mod:5  (gomod, direct, go, go)
  current   v1.0.0
  rules     #1 manager=gomod updates=[patch]
  policy    pin=keep allow=- updates=patch group=- schedule=always
  candidates 3
  rejected  major: 1 (v2.0.0)  not newer: 1 (v1.0.0)  update type minor not allowed: 1 (v1.1.0)
  result    up to date

example.com/i  go.mod:6  (gomod, indirect, go, go)
  current   v1.0.0
  rules     #1 manager=gomod updates=[patch]
  policy    pin=keep allow=- updates=patch group=- schedule=always
  result    skipped: ignored by default (indirect; enable with a rule setting ignore: false)
`,
		},
		{
			name:  "skipped by manager",
			files: files,
			args:  []string{"./local"},
			stdout: `./local  .github/workflows/ci.yml:8  (github-actions, action, git-tags, tag)
  current   -
  result    skipped: local action
`,
		},
		{
			name:   "no match",
			files:  files,
			args:   []string{"nope/*", "other"},
			code:   1,
			stderr: "deps: no dependency matches nope/*, other\n",
		},
		{
			name:  "upstream error",
			files: map[string]string{".github/workflows/ci.yml": "jobs:\n  a:\n    steps:\n      - uses: actions/missing@v1\n"},
			args:  []string{"actions/missing"},
			code:  1,
			stdout: `actions/missing  .github/workflows/ci.yml:4  (github-actions, action, git-tags, tag)
  current   v1
  rules     none
  policy    pin=keep allow=- updates=major,minor,patch,digest,pin group=- schedule=always
  candidates 0
  rejected  none
  result    error: .github/workflows/ci.yml:4: actions/missing: ls-refs https://github.com/actions/missing: HTTP 404
`,
			stderr: "error   .github/workflows/ci.yml:4: actions/missing: ls-refs https://github.com/actions/missing: HTTP 404\n",
		},
		{
			name:   "no globs",
			files:  files,
			code:   2,
			stderr: "deps: requires at least one name glob\n",
		},
		{
			name:   "invalid glob",
			files:  files,
			args:   []string{"[a"},
			code:   2,
			stderr: "deps: invalid glob \"[a\": syntax error in pattern\n",
		},
		{
			name:   "config error",
			files:  map[string]string{".deps.yaml": "pinn: digest\n"},
			args:   []string{"x"},
			code:   2,
			stderr: "line 1: field pinn not found",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleanEnv(t)
			newFakeNet(t)
			dir := t.TempDir()
			writeFiles(t, dir, tt.files)
			stdout, stderr, code := execute(t, append([]string{"explain", "--dir", dir}, tt.args...)...)
			if code != tt.code {
				t.Errorf("exit %d, want %d\n%s", code, tt.code, stderr)
			}
			if stdout != tt.stdout {
				t.Errorf("stdout:\n%s\nwant:\n%s", stdout, tt.stdout)
			}
			if !strings.Contains(stderr, tt.stderr) {
				t.Errorf("stderr lacks %q:\n%s", tt.stderr, stderr)
			}
		})
	}
}

func TestWriteTrace(t *testing.T) {
	yes := true
	cfg := &config.Config{Rules: []config.Rule{
		{},
		{DepTypes: []string{"direct"}, Ignore: &yes, Schedule: []string{"* 0-5 * * *", "* * * * 0"}},
	}}
	d := dep.Dep{Manager: "custom", DepType: "regex", Datasource: "docker", Versioning: "docker", File: "Dockerfile", Line: 1, Name: "golang", Version: "1.24-alpine"}
	tests := []struct {
		name string
		t    engine.Trace
		want string
	}{
		{
			name: "security update, rules without fields, empty updates",
			t: engine.Trace{
				Dep:        d,
				Rules:      []int{0, 1},
				Policy:     config.Policy{Pin: "keep", Updates: map[dep.UpdateType]bool{}},
				Candidates: 7,
				Rejected: map[string][]string{
					"suffix":    {"1.25", "latest-alpine", "1.27", "1.26"},
					"invalid":   {"latest"},
					"precision": {"1.25.1-alpine"},
				},
				Update: &dep.Update{Dep: d, Type: dep.Minor, NewVersion: "1.25-alpine", Advisories: []dep.Advisory{
					{ID: "GO-2026-1", Summary: "bad", URL: "https://osv.dev/GO-2026-1"},
					{ID: "GO-2026-2"},
				}},
			},
			want: `golang  Dockerfile:1  (custom, regex, docker, docker)
  current   1.24-alpine
  rules     #0 (matches all)
            #1 depTypes=[direct] ignore=true schedule=[* 0-5 * * *; * * * * 0]
  policy    pin=keep allow=- updates=- group=- schedule=always
  candidates 7
  rejected  suffix: 4 (1.27, 1.26, 1.25, ...)  invalid: 1 (latest)  precision: 1 (1.25.1-alpine)
  result    update 1.24-alpine -> 1.25-alpine (minor)
  security  GO-2026-1  bad  https://osv.dev/GO-2026-1
  security  GO-2026-2
`,
		},
		{
			name: "unknown versioning keeps order",
			t: engine.Trace{
				Dep:      dep.Dep{Name: "x", File: "f", Line: 2, Version: "1"},
				Policy:   config.Policy{Pin: "keep", Updates: map[dep.UpdateType]bool{dep.Major: true}},
				Rejected: map[string][]string{"invalid": {"b", "a", "c", "d"}},
			},
			want: `x  f:2  (-, -, -, -)
  current   1
  rules     none
  policy    pin=keep allow=- updates=major group=- schedule=always
  candidates 0
  rejected  invalid: 4 (b, a, c, ...)
  result    up to date
`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var b strings.Builder
			writeTrace(&b, cfg, tt.t)
			if b.String() != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", b.String(), tt.want)
			}
		})
	}
}
