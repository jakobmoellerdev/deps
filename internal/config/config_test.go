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
		{"custom: [{files: [x], datasource: npm}]\n", "custom[0].datasource: must be one of git-tags"},
		{"custom: [{files: [x], versioning: calver}]\n", "custom[0].versioning: must be one of tag"},
		{"custom: [{files: [x], match: '('}]\n", "custom[0].match: invalid RE2"},
		{"custom: [{files: [x], match: '(?=x)'}]\n", "custom[0].match: invalid RE2"},
		{"custom: [{files: [x], match: 'v(\\d+)', datasource: go, name: x}]\n", "custom[0].match: must have a (?P<version>...) group"},
		{"custom: [{files: [x], match: '(?P<version>\\S+)', name: x}]\n", "custom[0].datasource: required"},
		{"custom: [{files: [x], match: '(?P<version>\\S+)', datasource: go}]\n", "custom[0].name: required"},
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
