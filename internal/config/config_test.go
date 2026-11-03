package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/Masterminds/semver/v3"
	"github.com/bmatcuk/doublestar/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

func GoTestVer(s string) *semver.Version { return semver.MustParse(s) }

func load(t *testing.T, name, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Load(dir, "")
}

func TestLoadDefaults(t *testing.T) {
	for _, tc := range []struct{ name, body string }{{"", ""}, {".deps.yaml", ""}, {".deps.yml", "rules: []\n"}} {
		c, err := load(t, tc.name, tc.body)
		if err != nil || c.Version != 1 || c.Pin != "keep" {
			t.Errorf("%q: %+v %v", tc.name, c, err)
		}
	}
	_, err := Load(t.TempDir(), "missing.yaml")
	var ce *Error
	if !errors.As(err, &ce) || !errors.Is(err, os.ErrNotExist) {
		t.Errorf("explicit missing path: err = %v, want *Error wrapping ErrNotExist", err)
	}
}

func TestLoadUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root can read mode 000 files")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".deps.yaml"), []byte("pin: digest\n"), 0); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, "")
	var ce *Error
	if !errors.As(err, &ce) || !errors.Is(err, os.ErrPermission) {
		t.Fatalf("err = %v, want *Error wrapping ErrPermission (no silent fallback to defaults)", err)
	}
}

func TestValidateWithoutPath(t *testing.T) {
	err := (&Config{Version: 1, Pin: "always"}).Validate()
	if err == nil || err.Error() != ".deps.yaml: pin: must be keep or digest, got \"always\"" {
		t.Fatalf("err = %v", err)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, tc := range []struct{ body, want string }{
		{"pinn: digest\n", "line 1"},
		{"version: 2\n", "version: must be 1"},
		{"pin: always\n", "pin: must be keep or digest"},
		{"rules: [{manager: pip}]\n", "rules[0].manager"},
		{"rules: [{names: ['[']}]\n", "rules[0].names[0]"},
		{"rules: [{pin: x}]\n", "rules[0].pin"},
		{"rules: [{allow: '>>1'}]\n", "rules[0].allow"},
		{"rules: [{updates: [huge]}]\n", "rules[0].updates[0]"},
		{"ignorePaths: ['[']\n", "ignorePaths[0]"},
		{"timezone: Mars/Olympus\n", `timezone: unknown time zone "Mars/Olympus"`},
		{"timezone: Mars/Olympus\nschedule: ['0 * * * *']\n", "schedule[0]: \"0 * * * *\": minute field must be *"},
		{"schedule: ['* 25 * * *']\n", "schedule[0]"},
		{"rules: [{schedule: ['* * * *']}]\n", "rules[0].schedule[0]"},
		{"rules: [{depTypes: [weird]}]\n", "rules[0].depTypes[0]: must be one of direct, indirect"},
		{"security: maybe\n", "line 1"},
		{"custom: [{}]\n", "custom[0].files: must not be empty"},
		{"custom: [{files: ['[']}]\n", "custom[0].files[0]: invalid glob"},
		{"custom: [{files: [x], datasource: pip}]\n", "custom[0].datasource: must be one of git-tags"},
		{"custom: [{files: [x], versioning: calver}]\n", "custom[0].versioning: must be one of tag"},
		{"custom: [{files: [x], match: '('}]\n", "custom[0].match: invalid RE2"},
		{"custom: [{files: [x], match: '(?=x)'}]\n", "custom[0].match: invalid RE2"},
		{"custom: [{files: [x], match: 'v(\\d+)', datasource: go, name: x}]\n", "custom[0].match: must have a (?P<version>...) or (?P<currentValue>...) group"},
		{"custom: [{files: [x], match: '(?P<version>\\S+)', name: x}]\n", "custom[0].datasource: required"},
		{"custom: [{files: [x], match: '(?P<version>\\S+)', datasource: go}]\n", "custom[0].name: required"},
		{"branchPrefix: deps\n", `branchPrefix: must end with /, got "deps"`},
		{"branchPrefix: 'a b/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: 'a..b/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: 'a@{/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: 'x~/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: '/x/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: 'a//'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: '.x/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"branchPrefix: 'x.lock/'\n", "branchPrefix: must be a valid git ref prefix"},
		{"monorepos: maybe\n", "line 1"},
		{"rules: [{commitType: feature}]\n", "rules[0].commitType: must be one of fix, chore"},
		{"rules: [{commitScope: 'a b'}]\n", "rules[0].commitScope: must match"},
		{"rules: [{names: ['{a']}]\n", "rules[0].names[0]"},
		{"rules: [{paths: ['[']}]\n", "rules[0].paths[0]: invalid glob"},
		{"rules: [{datasources: [pypi]}]\n", "rules[0].datasources[0]: must be one of git-tags, github-releases"},
		{"rules: [{updateTypes: [huge]}]\n", "rules[0].updateTypes[0]: must be one of major"},
		{"rules: [{updateTypes: []}]\n", "rules[0].updateTypes: must not be empty"},
		{"rules: [{updateTypes: [major], pin: digest}]\n", "rules[0].pin: cannot be combined with updateTypes"},
		{"rules: [{patterns: [x]}]\n", "line 1"},
		{"custom: [{files: [x], checksum: {url: u}}]\n", "custom[0].checksum: requires match (regex mode)"},
		{"custom: [{files: [x], match: '(?P<version>\\S+)', datasource: go, name: x, checksum: {url: u}}]\n", "custom[0].checksum: requires a (?P<checksum>...) group in match"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: go, name: x}]\n", "custom[0].match: (?P<checksum>...) group requires checksum"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: go, name: x, checksum: {}}]\n", "custom[0].checksum: one of asset and url is required"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: github-releases, name: o/r, checksum: {asset: a, url: u}}]\n", "custom[0].checksum: asset and url are mutually exclusive"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: go, name: x, checksum: {asset: a}}]\n", "custom[0].checksum.asset: requires datasource github-releases"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: github-releases, name: o/r, checksum: {asset: a, file: f}}]\n", "custom[0].checksum.file: only allowed with url"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: github-releases, name: o/r, checksum: {asset: 'a_{{ver}}'}}]\n", "custom[0].checksum.asset: unknown template variable {{ver}}"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: go, name: x, checksum: {url: 'u/{{version}', file: f}}]\n", "custom[0].checksum.url: unbalanced template braces"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: go, name: x, checksum: {url: u, file: '{{name}}'}}]\n", "custom[0].checksum.file: unknown template variable {{name}}"},
		{"custom: [{files: [x], match: '(?P<version>\\S+) (?P<checksum>\\S+)', datasource: go, name: x, checksum: {url: u, pathh: f}}]\n", "line 1"},
	} {
		_, err := load(t, ".deps.yaml", tc.body)
		var ce *Error
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), ".deps.yaml") {
			t.Errorf("%q: err = %v", tc.body, err)
		}
	}
	_, err := load(t, ".deps.yaml", "pinn: digest\n")
	if !strings.Contains(err.Error(), "pinn") {
		t.Errorf("unknown key not named: %v", err)
	}
}

func TestPolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `pin: digest
rules:
  - names: ["actions/*"]
    allow: "< 7"
    updates: [major, minor]
  - manager: github-actions
    names: ["actions/cache"]
    updates: [patch]
    pin: keep
  - manager: gomod
    ignore: true
`)
	if err != nil {
		t.Fatal(err)
	}
	co := c.Policy(dep.Dep{Manager: "github-actions", Name: "actions/checkout"})
	if co.Pin != "digest" || co.Allow == nil || !co.Updates[dep.Minor] || co.Updates[dep.Patch] || co.Ignore {
		t.Errorf("checkout: %+v", co)
	}
	if !co.Accepts(dep.Major, GoTestVer("6.1.0")) || co.Accepts(dep.Major, GoTestVer("7.0.0")) || co.Accepts(dep.Major, nil) || co.Accepts(dep.Patch, GoTestVer("6.0.1")) {
		t.Error("checkout Accepts")
	}
	ca := c.Policy(dep.Dep{Manager: "github-actions", Name: "actions/cache"})
	if ca.Pin != "keep" || ca.Updates[dep.Minor] || !ca.Updates[dep.Patch] || ca.Allow == nil {
		t.Errorf("cache: %+v", ca)
	}
	other := c.Policy(dep.Dep{Manager: "github-actions", Name: "foo/bar"})
	if other.Pin != "digest" || len(other.Updates) != 5 || other.Allow != nil || !other.Accepts(dep.Pin, nil) {
		t.Errorf("other: %+v", other)
	}
	if !c.Policy(dep.Dep{Manager: "gomod", Name: "x"}).Ignore {
		t.Error("gomod ignore")
	}
}

func TestNewKeys(t *testing.T) {
	c, err := load(t, ".deps.yaml", `timezone: Europe/Berlin
schedule: ["* 0-5 * * *"]
security: false
rules:
  - depTypes: [indirect]
    ignore: false
    group: go-indirect
  - names: ["x/*"]
    schedule: []
  - names: ["x/y"]
    group: xy
    schedule: ["* * * * 6"]
custom:
  - files: ["**/*.txt"]
  - files: [Makefile]
    match: 'TOOL_(?P<name>\w+) = (?P<version>\S+)'
    datasource: go
    versioning: go
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.SecurityEnabled() || !Default().SecurityEnabled() {
		t.Error("SecurityEnabled")
	}
	if len(c.Custom) != 2 || c.Custom[0].Versioning != "semver" || c.Custom[1].Versioning != "go" || c.Custom[1].Datasource != "go" {
		t.Errorf("custom = %+v", c.Custom)
	}
	monday3 := time.Date(2026, 11, 2, 3, 0, 0, 0, time.UTC) // 04:00 in Berlin
	monday12 := monday3.Add(9 * time.Hour)

	top := c.Policy(dep.Dep{Manager: "gomod", Name: "a/b", DepType: "direct"})
	if top.Ignore || top.Group != "" || top.Schedule == nil || !top.Schedule.Contains(monday3) || top.Schedule.Contains(monday12) || len(top.Rules) != 0 {
		t.Errorf("top: %+v", top)
	}
	ind := c.Policy(dep.Dep{Manager: "gomod", Name: "a/b", DepType: "indirect"})
	if ind.Ignore || ind.Group != "go-indirect" || !slices.Equal(ind.Rules, []int{0}) {
		t.Errorf("indirect opted in: %+v", ind)
	}
	always := c.Policy(dep.Dep{Manager: "custom", Name: "x/z"})
	if always.Schedule != nil || !slices.Equal(always.Rules, []int{1}) {
		t.Errorf("schedule [] overrides to always: %+v", always)
	}
	xy := c.Policy(dep.Dep{Manager: "custom", Name: "x/y"})
	if xy.Group != "xy" || xy.Schedule == nil || xy.Schedule.Contains(monday3) || !slices.Equal(xy.Rules, []int{1, 2}) {
		t.Errorf("x/y: %+v", xy)
	}
}

func TestDefaultIgnore(t *testing.T) {
	c := Default()
	for typ, want := range map[string]bool{"indirect": true, "go": true, "peerDependencies": true, "direct": false, "toolchain": false, "dependencies": false, "packageManager": false, "": false} {
		if got := c.Policy(dep.Dep{Manager: "gomod", DepType: typ}).Ignore; got != want {
			t.Errorf("depType %q: Ignore = %v, want %v", typ, got, want)
		}
	}
}

func TestPolicyCheck(t *testing.T) {
	c, err := load(t, ".deps.yaml", "rules: [{allow: '< 2', updates: [minor]}]\n")
	if err != nil {
		t.Fatal(err)
	}
	p := c.Policy(dep.Dep{})
	for _, tc := range []struct {
		kind dep.UpdateType
		v    *semver.Version
		ok   bool
		why  string
	}{
		{dep.Minor, GoTestVer("1.1.0"), true, ""},
		{dep.Major, GoTestVer("1.1.0"), false, "update type major not allowed"},
		{dep.Minor, GoTestVer("2.1.0"), false, "outside allow <2"},
		{dep.Minor, nil, false, "outside allow <2"},
	} {
		if ok, why := p.Check(tc.kind, tc.v); ok != tc.ok || why != tc.why {
			t.Errorf("Check(%s, %v) = %v %q, want %v %q", tc.kind, tc.v, ok, why, tc.ok, tc.why)
		}
	}
}

func TestCustomChecksum(t *testing.T) {
	c, err := load(t, ".deps.yaml", `custom:
  - files: [Dockerfile]
    match: 'ARG GH_VERSION=(?P<version>\S+)\nARG GH_SHA256=(?P<checksum>[0-9a-f]{64})'
    datasource: github-releases
    name: cli/cli
    checksum: {asset: 'gh_{{version}}_linux_amd64.tar.gz'}
  - files: [Dockerfile]
    match: 'X=(?P<version>\S+) (?P<checksum>\S+)'
    datasource: git-tags
    name: example.com/x
    checksum:
      url: 'https://example.com/v{{version}}/sums.txt'
      file: 'x_{{version}}.tgz'
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := *c.Custom[0].Checksum; got != (Checksum{Asset: "gh_{{version}}_linux_amd64.tar.gz"}) {
		t.Errorf("custom[0].checksum = %+v", got)
	}
	if got := *c.Custom[1].Checksum; got != (Checksum{URL: "https://example.com/v{{version}}/sums.txt", File: "x_{{version}}.tgz"}) {
		t.Errorf("custom[1].checksum = %+v", got)
	}
}

func TestCustomRegexGroupAliases(t *testing.T) {
	c, err := load(t, ".deps.yaml", `custom:
  - files: [a]
    match: 'datasource=(?P<datasource>\S+) depName=(?P<depName>\S+)\s+version: (?P<currentValue>\S+)'
  - files: [b]
    match: 'pkg=(?P<packageName>\S+) (?P<version>\S+)'
    datasource: npm
  - files: [c]
    match: '(?P<currentValue>\S+)'
    datasource: github-release-attachments
    name: o/r
`)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Custom) != 3 {
		t.Fatalf("custom = %+v", c.Custom)
	}
	for _, ds := range DatasourceNames {
		if _, err := load(t, ".deps.yaml", "custom: [{files: [x], datasource: "+ds+"}]\n"); err != nil {
			t.Errorf("datasource %s: %v", ds, err)
		}
	}
}

func TestBranchPrefix(t *testing.T) {
	if p := Default().Prefix(); p != "deps/" {
		t.Errorf("default prefix = %q", p)
	}
	for _, ok := range []string{"renovate/", "bots/deps/", "a.b-c_d/"} {
		c, err := load(t, ".deps.yaml", "branchPrefix: '"+ok+"'\n")
		if err != nil || c.Prefix() != ok {
			t.Errorf("%q: prefix %v err %v", ok, c, err)
		}
	}
}

func TestCommitTypeAndScope(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - manager: github-actions
    commitType: ci
  - names: ["example.com/**"]
    commitType: build
    commitScope: ""
  - names: ["example.com/keep"]
    commitScope: tools
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		d          dep.Dep
		typ, scope string
	}{
		{dep.Dep{Manager: "gomod", DepType: "direct", Name: "a/b"}, "fix", "deps"},
		{dep.Dep{Manager: "gomod", DepType: "toolchain", Name: "go"}, "chore", "deps"},
		{dep.Dep{Manager: "npm", DepType: "dependencies", Name: "x"}, "fix", "deps"},
		{dep.Dep{Manager: "npm", DepType: "devDependencies", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "docker", DepType: "dockerfile", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "github-actions", DepType: "action", Name: "actions/checkout"}, "ci", "deps"},
		{dep.Dep{Manager: "gomod", DepType: "direct", Name: "example.com/x/y"}, "build", ""},
		{dep.Dep{Manager: "gomod", DepType: "direct", Name: "example.com/keep"}, "build", "tools"},
	} {
		if p := c.Policy(tc.d); p.CommitType != tc.typ || p.CommitScope != tc.scope {
			t.Errorf("%s %s: %q(%q), want %q(%q)", tc.d.Manager, tc.d.Name, p.CommitType, p.CommitScope, tc.typ, tc.scope)
		}
	}
}

func TestMonorepos(t *testing.T) {
	c, err := load(t, ".deps.yaml", "rules: [{names: ['k8s.io/client-go'], group: k8s-client}]\n")
	if err != nil {
		t.Fatal(err)
	}
	off, err := load(t, ".deps.yaml", "monorepos: false\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cfg           *Config
		d             dep.Dep
		group, mono   string
		wantUserRules []int
	}{
		{c, dep.Dep{Manager: "gomod", Name: "k8s.io/api"}, "kubernetes monorepo", "kubernetes", nil},
		{c, dep.Dep{Manager: "gomod", Name: "k8s.io/client-go"}, "k8s-client", "kubernetes", []int{0}},
		{c, dep.Dep{Manager: "gomod", Name: "go.opentelemetry.io/otel"}, "opentelemetry-go monorepo", "opentelemetry-go", nil},
		{c, dep.Dep{Manager: "gomod", Name: "go.opentelemetry.io/otel/sdk/metric"}, "opentelemetry-go monorepo", "opentelemetry-go", nil},
		{c, dep.Dep{Manager: "gomod", Name: "go.opentelemetry.io/otelx"}, "", "", nil},
		{c, dep.Dep{Manager: "gomod", Name: "sigs.k8s.io/controller-runtime"}, "", "", nil},
		{c, dep.Dep{Manager: "npm", Name: "@babel/core"}, "babel monorepo", "babel", nil},
		{c, dep.Dep{Manager: "npm", Name: "@typescript-eslint/parser"}, "typescript-eslint monorepo", "typescript-eslint", nil},
		{c, dep.Dep{Manager: "npm", Name: "@types/react"}, "", "", nil},
		{c, dep.Dep{Manager: "npm", Name: "@tanstack/react-query"}, "tanstack-query monorepo", "tanstack-query", nil},
		{c, dep.Dep{Manager: "gomod", Name: "react"}, "", "", nil},
		{c, dep.Dep{Manager: "github-actions", Name: "github/codeql-action/init"}, "codeql-action monorepo", "codeql-action", nil},
		{off, dep.Dep{Manager: "gomod", Name: "k8s.io/api"}, "", "", nil},
	} {
		p := tc.cfg.Policy(tc.d)
		if p.Group != tc.group || p.Monorepo != tc.mono || !slices.Equal(p.Rules, tc.wantUserRules) {
			t.Errorf("%s %s: group %q monorepo %q rules %v, want %q %q %v", tc.d.Manager, tc.d.Name, p.Group, p.Monorepo, p.Rules, tc.group, tc.mono, tc.wantUserRules)
		}
	}
}

func TestMonorepoList(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range Monorepos {
		if m.Name == "" || seen[m.Name] || len(m.Names) == 0 || len(m.Managers) == 0 {
			t.Errorf("bad entry %+v", m)
		}
		seen[m.Name] = true
		for _, mgr := range m.Managers {
			if !slices.Contains(Managers, mgr) {
				t.Errorf("%s: unknown manager %q", m.Name, mgr)
			}
		}
		for _, g := range m.Names {
			if !doublestar.ValidatePattern(g) {
				t.Errorf("%s: invalid glob %q", m.Name, g)
			}
		}
	}
}

func TestDefaultIgnorePaths(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"", "", DefaultIgnorePaths},
		{".deps.yaml", "", DefaultIgnorePaths},
		{".deps.yaml", "rules: []\n", DefaultIgnorePaths},
		{".deps.yaml", "ignorePaths: []\n", []string{}},
		{".deps.yaml", "ignorePaths:\n", nil},
		{".deps.yaml", "ignorePaths: ['a/**']\n", []string{"a/**"}},
	} {
		c, err := load(t, tc.name, tc.body)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(c.IgnorePaths, tc.want) {
			t.Errorf("%q %q: ignorePaths = %q, want %q", tc.name, tc.body, c.IgnorePaths, tc.want)
		}
	}
	c := Default()
	c.IgnorePaths[0] = "changed"
	if DefaultIgnorePaths[0] != "**/node_modules/**" {
		t.Error("Default aliases DefaultIgnorePaths")
	}
}

func TestRuleNamesDoublestar(t *testing.T) {
	c, err := load(t, ".deps.yaml", "rules: [{names: ['go.opentelemetry.io/**', 'actions/*', '@scope/{a,b}'], ignore: true}]\n")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]bool{
		"go.opentelemetry.io/otel/sdk": true, "actions/checkout": true, "actions/cache/save": false,
		"@scope/a": true, "@scope/c": false,
	} {
		if got := c.Policy(dep.Dep{Manager: "npm", Name: name}).Ignore; got != want {
			t.Errorf("%s: ignore = %v, want %v", name, got, want)
		}
	}
}

func TestRulePathsAndDatasources(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - paths: ["chart/**"]
    group: chart
  - datasources: [golang-version]
    group: golang
  - datasources: [docker, npm]
    paths: ["web/**", "site/*"]
    group: web
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		file, datasource, group string
		rules                   []int
	}{
		{"chart/values.yaml", "docker", "chart", []int{0}},
		{"chart/sub/Dockerfile", "docker", "chart", []int{0}},
		{"charts/values.yaml", "docker", "", nil},
		{"go.mod", "go-toolchain", "golang", []int{1}},
		{"go.mod", "go", "", nil},
		{"web/a/Dockerfile", "docker", "web", []int{2}},
		{"site/package.json", "npm", "web", []int{2}},
		{"site/a/package.json", "npm", "", nil},
		{"web/go.mod", "go", "", nil},
	} {
		p := c.Policy(dep.Dep{File: tc.file, Datasource: tc.datasource})
		if p.Group != tc.group || !slices.Equal(p.Rules, tc.rules) {
			t.Errorf("%s (%s): group %q rules %v, want %q %v", tc.file, tc.datasource, p.Group, p.Rules, tc.group, tc.rules)
		}
	}
}

func TestCanonicalDatasource(t *testing.T) {
	if got := len(Datasources) + len(DatasourceAliases); got != len(DatasourceNames) {
		t.Fatalf("DatasourceNames has %d entries, want %d", len(DatasourceNames), got)
	}
	for i, n := range DatasourceNames {
		c := CanonicalDatasource(n)
		if !slices.Contains(Datasources, c) || (i < len(Datasources)) != (c == n) {
			t.Errorf("%s -> %s", n, c)
		}
	}
}

func TestPolicyFor(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - manager: github-actions
    schedule: ["* * * * 0"]
  - manager: github-actions
    updateTypes: [minor, patch]
    group: GitHub Actions
  - manager: github-actions
    updateTypes: [digest, pin]
    group: Digest pins
  - manager: gomod
    ignore: false
  - manager: gomod
    depTypes: [indirect]
    updateTypes: [major]
    ignore: true
  - names: ["actions/cache"]
    updateTypes: [minor]
    group: cache
    commitType: ci
    allow: "< 5"
    updates: [minor]
  - manager: npm
    updateTypes: [patch]
    ignore: false
`)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasUpdateTypeRules() || Default().HasUpdateTypeRules() {
		t.Error("HasUpdateTypeRules")
	}
	checkout := dep.Dep{Manager: "github-actions", Name: "actions/checkout"}
	cache := dep.Dep{Manager: "github-actions", Name: "actions/cache"}
	indirect := dep.Dep{Manager: "gomod", Name: "a/b", DepType: "indirect"}
	peer := dep.Dep{Manager: "npm", Name: "p", DepType: "peerDependencies"}
	all := len(dep.UpdateTypes)
	for _, tc := range []struct {
		name    string
		d       dep.Dep
		kind    dep.UpdateType // "" = Policy
		group   string
		rules   []int
		ignore  string // "" = not ignored, else IgnoreReason
		updates int
	}{
		{"pre-policy skips updateTypes rules", checkout, "", "", []int{0}, "", all},
		{"minor joins the group", checkout, dep.Minor, "GitHub Actions", []int{0, 1}, "", all},
		{"patch joins the group", checkout, dep.Patch, "GitHub Actions", []int{0, 1}, "", all},
		{"major keeps the pre-policy", checkout, dep.Major, "", []int{0}, "", all},
		{"digest pins", checkout, dep.Digest, "Digest pins", []int{0, 2}, "", all},
		{"pin pins", checkout, dep.Pin, "Digest pins", []int{0, 2}, "", all},
		{"later kind rule overrides", cache, dep.Minor, "cache", []int{0, 1, 5}, "", 1},
		{"other kind unaffected", cache, dep.Patch, "GitHub Actions", []int{0, 1}, "", all},
		{"indirect opted in", indirect, "", "", []int{3}, "", all},
		{"indirect majors ignored", indirect, dep.Major, "", []int{3, 4}, "ignored by rule #4 for major", all},
		{"indirect minors kept", indirect, dep.Minor, "", []int{3}, "", all},
		{"default-ignored dep", peer, "", "", nil, "ignored by default (peerDependencies; enable with a rule setting ignore: false)", all},
		{"kind rule opts in", peer, dep.Patch, "", []int{6}, "", all},
		{"other kinds stay ignored", peer, dep.Minor, "", nil, "ignored by default (peerDependencies; enable with a rule setting ignore: false)", all},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Policy
			if tc.kind == "" {
				p = c.Policy(tc.d)
			} else {
				p = c.PolicyFor(tc.d, tc.kind)
			}
			ignore := ""
			if p.Ignore {
				ignore = p.IgnoreReason
			}
			if p.Kind != tc.kind || p.Group != tc.group || !slices.Equal(p.Rules, tc.rules) || ignore != tc.ignore || len(p.Updates) != tc.updates {
				t.Errorf("got kind %q group %q rules %v ignore %q updates %d, want %q %q %v %q %d", p.Kind, p.Group, p.Rules, ignore, len(p.Updates), tc.kind, tc.group, tc.rules, tc.ignore, tc.updates)
			}
		})
	}
	ca := c.PolicyFor(cache, dep.Minor)
	if ca.CommitType != "ci" || ca.Allow == nil || ca.Schedule == nil || !ca.Accepts(dep.Minor, GoTestVer("4.1.0")) || ca.Accepts(dep.Minor, GoTestVer("5.0.0")) {
		t.Errorf("cache minor: %+v", ca)
	}
}
