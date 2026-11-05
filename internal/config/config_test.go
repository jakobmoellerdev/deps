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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// sv converts like a scheme's Semver: nil when v is not a version.
func sv(v string) *semver.Version {
	s, err := semver.NewVersion(v)
	if err != nil {
		return nil
	}
	return s
}

func load(t *testing.T, name, body string) (*Config, error) {
	t.Helper()
	dir := t.TempDir()
	if name != "" {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return Load(t.Context(), dir, "", nil)
}

func TestLoadDefaults(t *testing.T) {
	for _, tc := range []struct{ name, body string }{{"", ""}, {".deps.yaml", ""}, {".deps.yml", "rules: []\n"}} {
		c, err := load(t, tc.name, tc.body)
		if err != nil || c.Version != 1 || c.Pin != "keep" {
			t.Errorf("%q: %+v %v", tc.name, c, err)
		}
	}
	_, err := Load(t.Context(), t.TempDir(), "missing.yaml", nil)
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
	_, err := Load(t.Context(), dir, "", nil)
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
		{"gitAuthor: bot@example.com\n", "gitAuthor: must be 'Name <email>'"},
		{"gitAuthor: 'Bot <nope>'\n", "gitAuthor: must be 'Name <email>'"},
		{"gitIgnoredAuthors: ['125909804+ocmbot[bot]@users.noreply.github.com', 'a b@c']\n", "gitIgnoredAuthors[1]: must be an email address"},
		{"gitIgnoredAuthors: ['Bot <b@example.com>']\n", "gitIgnoredAuthors[0]: must be an email address"},
		{"gitIgnoredAuthors: [nope]\n", "gitIgnoredAuthors[0]: must be an email address"},
		{"prConcurrentLimit: -1\n", "prConcurrentLimit: must be >= 0"},
		{"prHourlyLimit: -2\n", "prHourlyLimit: must be >= 0"},
		{"dashboardApproval: true\n", "dashboardApproval: requires dependencyDashboard: true"},
		{"rules: [{manager: gomod, dashboardApproval: false}, {updateTypes: [major], dashboardApproval: true}]\n", "rules[1].dashboardApproval: requires dependencyDashboard: true"},
		{"pin: always\n", "pin: must be keep or digest"},
		{"rules: [{manager: pip}]\n", "rules[0].manager"},
		{"rules: [{names: ['[']}]\n", "rules[0].names[0]"},
		{"rules: [{pin: x}]\n", "rules[0].pin"},
		{"rules: [{allow: '>>1'}]\n", "rules[0].allow"},
		{"rules: [{updateTypes: [major], replacementName: x}]\n", "rules[0].replacementName: cannot be combined with updateTypes"},
		{"rules: [{replacementVersion: '1.0.0'}]\n", "rules[0].replacementVersion: requires replacementName"},
		{"rules: [{replacementPrefix: a/}]\n", "rules[0].replacementPrefix: requires replacementName"},
		{"rules: [{currentAge: '> 1 year', replacementName: x}]\n", "rules[0].replacementName: cannot be combined with currentAge"},
		{"rules: [{updates: [huge]}]\n", "rules[0].updates[0]"},
		{"ignorePaths: ['[']\n", "ignorePaths[0]"},
		{"timezone: Europe/Berln\n", `timezone: unknown IANA time zone "Europe/Berln"`},
		{"timezone: Local\n", `timezone: must be an IANA time zone like Europe/Berlin, got "Local"`},
		{"timezone: [x]\n", "line 1"},
		{"schedule: ['befor 6am on monday']\n", `schedule[0]: "befor 6am on monday": unknown word "befor"`},
		{"rules: [{schedule: ['before 25:00']}]\n", `rules[0].schedule[0]: "before 25:00": before: want a time like 6am or 18:30, got "25:00"`},
		{"schedule: ['60 5 * * *']\n", `schedule[0]: "60 5 * * *": minute: value 60 out of range 0-59`},
		{"schedule: ['* 25 * * *']\n", "schedule[0]"},
		{"rules: [{schedule: ['* * * *']}]\n", "rules[0].schedule[0]"},
		{"lockFileMaintenance: {enabled: true, schedule: ['60 5 * * *']}\n", `lockFileMaintenance.schedule[0]: "60 5 * * *": minute`},
		{"lockFileMaintenance: {enable: true}\n", "field enable not found"},
		{"rules: [{depTypes: [weird]}]\n", "rules[0].depTypes[0]: must be one of direct, indirect"},
		{"security: maybe\n", "line 1"},
		{"custom: [{}]\n", "custom[0].files: must not be empty"},
		{"custom: [{files: ['[']}]\n", "custom[0].files[0]: invalid glob"},
		{"custom: [{files: ['/(/']}]\n", `custom[0].files[0]: invalid regex "/(/"`},
		{"ignorePaths: ['!x']\n", `ignorePaths[0]: negation is not supported in ignorePaths, got "!x"`},
		{"rules: [{names: ['/a(/']}]\n", `rules[0].names[0]: invalid regex "/a(/"`},
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
		{"rules: [{datasources: [cpan]}]\n", "rules[0].datasources[0]: must be one of git-tags, git-refs, github-releases"},
		{"rules: [{updateTypes: [huge]}]\n", "rules[0].updateTypes[0]: must be one of major"},
		{"rules: [{updateTypes: []}]\n", "rules[0].updateTypes: must not be empty"},
		{"rules: [{updateTypes: [major], pin: digest}]\n", "rules[0].pin: cannot be combined with updateTypes"},
		{"rangeStrategy: pinned\n", `rangeStrategy: must be one of auto, bump, replace, update-lockfile, widen, pin, in-range-only, got "pinned"`},
		{"rules: [{rangeStrategy: wide}]\n", `rules[0].rangeStrategy: must be one of auto, bump, replace, update-lockfile, widen, pin, in-range-only, got "wide"`},
		{"rules: [{updateTypes: [major], rangeStrategy: bump}]\n", "rules[0].rangeStrategy: cannot be combined with updateTypes"},
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
		{"rebaseWhen: always\n", `rebaseWhen: must be one of conflicted, behind-base-branch, never, got "always"`},
		{"platformCommit: always\n", `platformCommit: must be one of auto, enabled, disabled, got "always"`},
		{"minimumReleaseAge: soon\n", `minimumReleaseAge: invalid age "soon"`},
		{"minimumReleaseAge: -1h\n", `minimumReleaseAge: invalid age "-1h"`},
		{"minimumReleaseAge: 99999999999999999 weeks\n", "minimumReleaseAge: age \"99999999999999999 weeks\" out of range"},
		{"rules: [{versioning: calver}]\n", `rules[0].versioning: must be one of tag, go, docker, semver, loose, cargo, pep440, hashicorp, maven, ruby, conda, composer, julia, bazel-module, nuget or regex:<RE2>, got "calver"`},
		{"rules: [{versioning: 'regex:('}]\n", "rules[0].versioning: regex: invalid RE2 regexp"},
		{"rules: [{versioning: 'regex:^(?<minor>\\d+)$'}]\n", "rules[0].versioning: regex: must have a (?<major>...) group"},
		{"rules: [{versioning: 'regex:^(?<major>\\d+)(?P<mjor>x)$'}]\n", `rules[0].versioning: regex: unknown group "mjor" (allowed: major, minor, patch, build, revision, prerelease, compatibility)`},
		{"rules: [{updateTypes: [major], versioning: loose}]\n", "rules[0].versioning: cannot be combined with updateTypes"},
		{"rules: [{allow: '/(/'}]\n", `rules[0].allow: invalid regex "/(/"`},
		{"custom: [{files: [x], versioning: 'regex:('}]\n", "custom[0].versioning: regex: invalid RE2 regexp"},
		{"rules: [{extractVersion: '^jq-(.+)$'}]\n", "rules[0].extractVersion: needs a (?<version>…) group"},
		{"rules: [{extractVersion: '('}]\n", "rules[0].extractVersion: invalid RE2 regexp"},
		{"rules: [{updateTypes: [major], extractVersion: '^v(?<version>.+)$'}]\n", "rules[0].extractVersion: cannot be combined with updateTypes"},
		{"separateMinorPatch: maybe\n", "line 1"},
		{"rules: [{separateMultipleMajor: 1}]\n", "line 1"},
		{"updateNotScheduled: sometimes\n", "line 1"},
		{"rules: [{minimumReleaseAge: 3 fortnights}]\n", `rules[0].minimumReleaseAge: invalid age "3 fortnights"`},
		{"abandonmentThreshold: ages\n", `abandonmentThreshold: invalid age "ages"`},
		{"abandonmentThreshold: 99999999999 years\n", `abandonmentThreshold: age "99999999999 years" out of range`},
		{"rules: [{abandonmentThreshold: -1d}]\n", `rules[0].abandonmentThreshold: invalid age "-1d"`},
		{"minimumReleaseAgeBehaviour: timestamp-strict\n", `minimumReleaseAgeBehaviour: must be one of timestamp-optional, timestamp-required, got "timestamp-strict"`},
		{"rules: [{minimumReleaseAgeBehaviour: required}]\n", `rules[0].minimumReleaseAgeBehaviour: must be one of timestamp-optional, timestamp-required, got "required"`},
		{"rules: [{datasources: [custom.envtest]}]\n", `rules[0].datasources[0]: must be one of git-tags, git-refs, github-releases, go, go-toolchain, docker, npm, node-version, helm, crate, pypi, conda, terraform-provider, terraform-module, maven, gradle-version, rubygems, ruby-version, rust-version, orb, azure-pipelines-tasks, vcpkg, elm-package, packagist, julia-registry, bazel, nuget, dotnet-version, golang-version, github-tags, github-release-attachments, node, bitbucket-tags or custom.<name> of customDatasources, got "custom.envtest"`},
		{"custom: [{files: [x], datasource: custom.envtest}]\n", `custom[0].datasource: must be one of git-tags`},
		{"customDatasources: {envtest: {}}\n", "customDatasources.envtest.url: required"},
		{"customDatasources: {envtest: {url: 'ftp://x/y'}}\n", `customDatasources.envtest.url: must be an http(s) URL, got "ftp://x/y"`},
		{"customDatasources: {envtest: {url: 'https://'}}\n", `customDatasources.envtest.url: must be an http(s) URL`},
		{"customDatasources: {envtest: {url: '%zz'}}\n", `customDatasources.envtest.url: must be an http(s) URL`},
		{"customDatasources: {'-x': {url: 'https://x/y'}}\n", "customDatasources.-x: name must match"},
		{"customDatasources: {e: {url: 'https://x/y', format: toml}}\n", `customDatasources.e.format: must be one of json, yaml, plain, got "toml"`},
		{"customDatasources: {e: {url: 'https://x/y', format: plain, versions: .a}}\n", "customDatasources.e.versions: not allowed with format plain"},
		{"customDatasources: {e: {url: 'https://x/y', versions: 'a'}}\n", `customDatasources.e.versions: selector "a"`},
		{"customDatasources: {e: {url: 'https://x/y', versionRegex: '('}}\n", "customDatasources.e.versionRegex: invalid RE2 regexp"},
		{"customDatasources: {e: {url: 'https://x/y', transformTemplates: []}}\n", "line 1"},
		{"automergeStrategy: fast-forward\n", `automergeStrategy: must be one of auto, merge, squash, rebase, got "fast-forward"`},
		{"rules: [{automergeStrategy: merge-commit}]\n", `rules[0].automergeStrategy: must be one of auto, merge, squash, rebase, got "merge-commit"`},
		{"automergeType: pr-comment\n", `automergeType: must be one of pr, branch, got "pr-comment"`},
		{"rules: [{automergeType: fast-forward}]\n", `rules[0].automergeType: must be one of pr, branch, got "fast-forward"`},
		{"prCreation: approval\n", `prCreation: must be one of immediate, not-pending, status-success, got "approval"`},
		{"rules: [{prCreation: later}]\n", `rules[0].prCreation: must be one of immediate, not-pending, status-success, got "later"`},
		{"draftPR: maybe\n", "line 1"},
		{"automerge: sometimes\n", "line 1"},
		{"dependencyDashboard: maybe\n", "line 1"},
		{"dependencyDashboardTitle: '  '\n", "dependencyDashboardTitle: must be a non-blank single line"},
		{"dependencyDashboardTitle: \"a\\nb\"\n", "dependencyDashboardTitle: must be a non-blank single line"},
		{"dependencyDashboardLabels: [deps, '']\n", "dependencyDashboardLabels[1]: must be a non-blank label name"},
		{"dependencyDashboardLabels: ['a,b']\n", "dependencyDashboardLabels[0]: must be a non-blank label name without ','"},
		{"gitlabci: {url: 'gitlab.example.com'}\n", `gitlabci.url: must be an absolute http(s) URL without query or fragment, got "gitlab.example.com"`},
		{"gitlabci: {url: 'ssh://gitlab.example.com'}\n", "gitlabci.url: must be an absolute http(s) URL"},
		{"gitlabci: {url: 'https://'}\n", "gitlabci.url: must be an absolute http(s) URL"},
		{"gitlabci: {url: 'https://gitlab.example.com?x=1'}\n", "gitlabci.url: must be an absolute http(s) URL"},
		{"gitlabci: {url: 'https://gitlab.example.com/#x'}\n", "gitlabci.url: must be an absolute http(s) URL"},
		{"gitlabci: {url: '%zz'}\n", "gitlabci.url: must be an absolute http(s) URL"},
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
	for _, tc := range []struct {
		kind     dep.UpdateType
		v        string
		semverOf func(string) *semver.Version
		want     bool
	}{
		{dep.Major, "6.1.0", sv, true},
		{dep.Major, "7.0.0", sv, false},
		{dep.Major, "main", sv, false},
		{dep.Major, "6.1.0", nil, false},
		{dep.Patch, "6.0.1", sv, false},
	} {
		if ok, _ := co.Check(tc.kind, tc.v, tc.semverOf); ok != tc.want {
			t.Errorf("checkout Check(%s, %q) = %v, want %v", tc.kind, tc.v, ok, tc.want)
		}
	}
	ca := c.Policy(dep.Dep{Manager: "github-actions", Name: "actions/cache"})
	if ca.Pin != "keep" || ca.Updates[dep.Minor] || !ca.Updates[dep.Patch] || ca.Allow == nil {
		t.Errorf("cache: %+v", ca)
	}
	other := c.Policy(dep.Dep{Manager: "github-actions", Name: "foo/bar"})
	if ok, _ := other.Check(dep.Pin, "", nil); other.Pin != "digest" || len(other.Updates) != len(dep.UpdateTypes) || other.Allow != nil || !ok {
		t.Errorf("other: %+v", other)
	}
	if !c.Policy(dep.Dep{Manager: "gomod", Name: "x"}).Ignore {
		t.Error("gomod ignore")
	}
}

func TestPolicyRenovatePatterns(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - names: ['/^k8s\.io\//', '!k8s.io/kube-openapi']
    group: k8s
  - paths: ['!charts/**']
    pin: digest
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		d          dep.Dep
		group, pin string
	}{
		{dep.Dep{Manager: "gomod", Name: "k8s.io/api", File: "go.mod"}, "k8s", "digest"},
		{dep.Dep{Manager: "gomod", Name: "k8s.io/kube-openapi", File: "go.mod"}, "", "digest"},
		{dep.Dep{Manager: "helm", Name: "k8s.io/api", File: "charts/a/Chart.yaml"}, "k8s", "keep"},
	} {
		p := c.Policy(tt.d)
		if p.Group != tt.group || p.Pin != tt.pin {
			t.Errorf("%s %s: group %q pin %q, want %q %q", tt.d.Name, tt.d.File, p.Group, p.Pin, tt.group, tt.pin)
		}
	}
}

func TestRangeStrategyPolicy(t *testing.T) {
	d := dep.Dep{Manager: "npm", Name: "a", DepType: "dependencies"}
	c, err := load(t, "", "")
	if err != nil || c.Policy(d).RangeStrategy != RangeAuto {
		t.Fatalf("default: %v %v", c.Policy(d).RangeStrategy, err)
	}
	c, err = load(t, ".deps.yaml", "rangeStrategy: bump\nrules:\n  - names: [a]\n    rangeStrategy: update-lockfile\n")
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Policy(d).RangeStrategy; got != RangeUpdateLockfile {
		t.Errorf("rule: %s", got)
	}
	if got := c.Policy(dep.Dep{Manager: "npm", Name: "b"}).RangeStrategy; got != RangeBump {
		t.Errorf("top level: %s", got)
	}
}

func TestNewKeys(t *testing.T) {
	c, err := load(t, ".deps.yaml", `schedule: ["0 5 * * *", "0 5 * * 0"]
security: false
rules:
  - depTypes: [indirect]
    ignore: false
    group: go-indirect
  - names: ["x/*"]
    schedule: []
  - names: ["x/y"]
    group: xy
    schedule: ["0  5 * * 0"]
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
	daily := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 5 * * *"}
	weekly := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 5 * * 0"}
	other := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 6 * * *"}

	top := c.Policy(dep.Dep{Manager: "gomod", Name: "a/b", DepType: "direct"})
	if top.Ignore || top.Group != "" || top.Schedule == nil || !top.Schedule.Matches(daily) || !top.Schedule.Matches(weekly) || top.Schedule.Matches(other) || len(top.Rules) != 0 {
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
	if xy.Group != "xy" || xy.Schedule == nil || xy.Schedule.Matches(daily) || !xy.Schedule.Matches(weekly) || !slices.Equal(xy.Rules, []int{1, 2}) {
		t.Errorf("x/y: %+v", xy)
	}
}

// timezone applies to the time windows of every schedule (top level, rules, lock file
// maintenance); cron triggers still match the triggering cron as text.
func TestTimezone(t *testing.T) {
	c, err := load(t, ".deps.yaml", `timezone: America/New_York
schedule: ["before 6am on monday", "0 5 * * 3"]
rules:
  - names: ["x/*"]
    schedule: ["every weekend"]
lockFileMaintenance:
  enabled: true
  schedule: ["on the first day of the month"]
`)
	if err != nil {
		t.Fatal(err)
	}
	// Monday 2026-11-09 10:00 UTC is 05:00 in New York (UTC-5).
	monday := schedule.Trigger{Event: "push", Time: time.Date(2026, 11, 9, 10, 0, 0, 0, time.UTC)}
	late := schedule.Trigger{Event: "push", Time: time.Date(2026, 11, 9, 11, 0, 0, 0, time.UTC)}
	wed := schedule.Trigger{Event: schedule.EventSchedule, Cron: "0 5 * * 3", Time: late.Time}
	top := c.Policy(dep.Dep{Manager: "gomod", Name: "a/b"}).Schedule
	if !top.Matches(monday) || top.Matches(late) || !top.Matches(wed) {
		t.Errorf("top schedule %s", top)
	}
	if got := top.String(); got != "before 6am on monday; 0 5 * * 3 (America/New_York)" {
		t.Errorf("String() = %q", got)
	}
	weekend := c.Policy(dep.Dep{Manager: "gomod", Name: "x/y"}).Schedule
	if weekend.Matches(schedule.Trigger{Time: time.Date(2026, 11, 10, 3, 0, 0, 0, time.UTC)}) {
		t.Error("Tuesday 03:00 UTC is Monday in New York, not the weekend")
	}
	if !weekend.Matches(schedule.Trigger{Time: time.Date(2026, 11, 9, 3, 0, 0, 0, time.UTC)}) {
		t.Error("Monday 03:00 UTC is Sunday in New York")
	}
	lfm := c.LockFileMaintenanceSchedule()
	if !lfm.Matches(schedule.Trigger{Time: time.Date(2026, 12, 1, 12, 0, 0, 0, time.UTC)}) || lfm.Matches(schedule.Trigger{Time: time.Date(2026, 12, 1, 3, 0, 0, 0, time.UTC)}) {
		t.Errorf("lock file maintenance schedule %s", lfm)
	}
}

func TestDefaultIgnore(t *testing.T) {
	c := Default()
	for typ, want := range map[string]bool{"indirect": true, "go": true, "peerDependencies": true, "engines": true, "volta": false, "setup": false, "direct": false, "toolchain": false, "dependencies": false, "packageManager": false, "": false} {
		if got := c.Policy(dep.Dep{Manager: "gomod", DepType: typ}).Ignore; got != want {
			t.Errorf("depType %q: Ignore = %v, want %v", typ, got, want)
		}
	}
}

func TestPolicyCheck(t *testing.T) {
	c, err := load(t, ".deps.yaml", `rules:
  - allow: '< 2'
    updates: [minor]
  - names: [gl]
    allow: '/^\d+\.\d+$/'
  - names: [neg]
    allow: '!/^v1\./'
  - names: [ci]
    allow: '/^V\d+$/i'
`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		kind dep.UpdateType
		v    string
		ok   bool
		why  string
	}{
		{"", dep.Minor, "1.1.0", true, ""},
		{"", dep.Major, "1.1.0", false, "update type major not allowed"},
		{"", dep.Minor, "2.1.0", false, "outside allow <2"},
		{"", dep.Minor, "main", false, "outside allow <2"},
		{"gl", dep.Minor, "1877.28", true, ""},
		{"gl", dep.Minor, "1877.28.1", false, `outside allow /^\d+\.\d+$/`},
		{"gl", dep.Minor, "amd64-1877.28", false, `outside allow /^\d+\.\d+$/`},
		{"neg", dep.Minor, "v2.0.0", true, ""},
		{"neg", dep.Minor, "v1.9.0", false, `outside allow !/^v1\./`},
		{"ci", dep.Minor, "v7", true, ""},
	} {
		p := c.Policy(dep.Dep{Name: tc.name})
		if ok, why := p.Check(tc.kind, tc.v, sv); ok != tc.ok || why != tc.why {
			t.Errorf("%s: Check(%s, %s) = %v %q, want %v %q", tc.name, tc.kind, tc.v, ok, why, tc.ok, tc.why)
		}
	}
	// The regex form never converts to semver.
	p := c.Policy(dep.Dep{Name: "gl"})
	if ok, _ := p.Check(dep.Minor, "1877.28", func(string) *semver.Version { t.Fatal("semverOf called"); return nil }); !ok {
		t.Error("regex allow")
	}
}

func TestParseAllowedErrors(t *testing.T) {
	for in, want := range map[string]string{
		"/(/":     `invalid regex "/(/": error parsing regexp`,
		"/x/g":    `invalid regex "/x/g": unknown flags "g" (only i)`,
		"!/x":     `invalid regex "!/x": missing closing /`,
		"!< 2":    `invalid constraint "!< 2"`,
		">>1":     `invalid constraint ">>1"`,
		"/":       `invalid constraint "/"`,
		"!/(?=)/": `invalid regex "!/(?=)/"`,
	} {
		if _, err := ParseAllowed(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseAllowed(%q) = %v, want %q", in, err, want)
		}
	}
	for in, want := range map[string]string{"< 7": "<7", "/^v/": "/^v/", "!/x/i": "!/x/i"} {
		if a, err := ParseAllowed(in); err != nil || a.String() != want {
			t.Errorf("ParseAllowed(%q) = %v, %v; want %s", in, a, err, want)
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

func TestGitAuthorAndLimits(t *testing.T) {
	c, err := load(t, ".deps.yaml", "gitAuthor: 'OCM Bot <bot@ocm.example>'\ngitIgnoredAuthors: [a@example.com]\nprConcurrentLimit: 3\nprHourlyLimit: 1\n")
	if err != nil {
		t.Fatal(err)
	}
	a, err := ParseAuthor(c.GitAuthor)
	if err != nil || a.Name != "OCM Bot" || a.Email != "bot@ocm.example" {
		t.Errorf("ParseAuthor = %v, %v", a, err)
	}
	const ocm = "ocmbot[bot] <125909804+ocmbot[bot]@users.noreply.github.com>" // GitHub app identity
	if a, err := ParseAuthor(ocm); err != nil || a.Name != "ocmbot[bot]" || a.Email != "125909804+ocmbot[bot]@users.noreply.github.com" {
		t.Errorf("ParseAuthor(%q) = %v, %v", ocm, a, err)
	}
	for _, bad := range []string{"", "x@y", "<x@y>", "A <b>", "A <b@c> d", "A <b c@d>", "A < <b@c>"} {
		if _, err := ParseAuthor(bad); err == nil {
			t.Errorf("ParseAuthor(%q) accepted", bad)
		}
	}
	if len(c.GitIgnoredAuthors) != 1 || c.PRConcurrentLimit != 3 || c.PRHourlyLimit != 1 {
		t.Errorf("config = %+v", c)
	}
}

func TestGitLabCIURL(t *testing.T) {
	for body, want := range map[string]string{
		"": "",
		"gitlabci: {url: 'https://gitlab.example.com'}\n":      "https://gitlab.example.com",
		"gitlabci: {url: 'http://example.com:8080/gitlab/'}\n": "http://example.com:8080/gitlab",
	} {
		c, err := load(t, ".deps.yaml", body)
		if err != nil {
			t.Fatalf("%q: %v", body, err)
		}
		if c.GitLabCI.URL != want {
			t.Errorf("%q: url = %q, want %q", body, c.GitLabCI.URL, want)
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
  - manager: pipenv
    depTypes: [category]
    commitType: docs
  - depTypes: [tool.pdm.dev-dependencies]
    commitScope: dev
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
		{dep.Dep{Manager: "cargo", DepType: "dependencies", Name: "x"}, "fix", "deps"},
		{dep.Dep{Manager: "cargo", DepType: "dev-dependencies", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "poetry", DepType: "dependencies", Name: "x"}, "fix", "deps"},
		{dep.Dep{Manager: "poetry", DepType: "group", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "pep621", DepType: "project.dependencies", Name: "x"}, "fix", "deps"},
		{dep.Dep{Manager: "pep621", DepType: "dependency-groups", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "pipenv", DepType: "packages", Name: "x"}, "fix", "deps"},
		{dep.Dep{Manager: "pipenv", DepType: "dev-packages", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "pipenv", DepType: "category", Name: "x"}, "docs", "deps"},
		{dep.Dep{Manager: "pep621", DepType: "tool.pdm.dev-dependencies", Name: "x"}, "chore", "dev"},
		{dep.Dep{Manager: "pip_requirements", DepType: "requirements", Name: "x"}, "chore", "deps"},
		{dep.Dep{Manager: "maven", DepType: "compile", Name: "g:a"}, "fix", "deps"},
		{dep.Dep{Manager: "maven", DepType: "parent", Name: "g:a"}, "fix", "deps"},
		{dep.Dep{Manager: "maven", DepType: "test", Name: "g:a"}, "chore", "deps"},
		{dep.Dep{Manager: "maven", DepType: "build", Name: "g:a"}, "chore", "deps"},
		{dep.Dep{Manager: "gradle", DepType: "dependencies", Name: "g:a"}, "chore", "deps"},
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
		{c, dep.Dep{Manager: "cargo", Name: "tokio-util"}, "tokio monorepo", "tokio", nil},
		{c, dep.Dep{Manager: "cargo", Name: "tokio-postgres"}, "", "", nil}, // a separate repository
		{c, dep.Dep{Manager: "cargo", Name: "serde_derive"}, "serde monorepo", "serde", nil},
		{c, dep.Dep{Manager: "cargo", Name: "serde_json"}, "", "", nil},
		{c, dep.Dep{Manager: "cargo", Name: "aws-sdk-s3"}, "aws-sdk-rust monorepo", "aws-sdk-rust", nil},
		{c, dep.Dep{Manager: "npm", Name: "tokio"}, "", "", nil},
		{off, dep.Dep{Manager: "gomod", Name: "k8s.io/api"}, "", "", nil},
	} {
		p := tc.cfg.Policy(tc.d)
		if p.Group != tc.group || p.Monorepo != tc.mono || !slices.Equal(p.Rules, tc.wantUserRules) {
			t.Errorf("%s %s: group %q monorepo %q rules %v, want %q %q %v", tc.d.Manager, tc.d.Name, p.Group, p.Monorepo, p.Rules, tc.group, tc.mono, tc.wantUserRules)
		}
	}
}

func TestRubyGroup(t *testing.T) {
	c, err := load(t, ".deps.yaml", "rules: [{manager: asdf, names: [ruby], group: tools}]\n")
	if err != nil {
		t.Fatal(err)
	}
	off, err := load(t, ".deps.yaml", "monorepos: false\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cfg              *Config
		d                dep.Dep
		group, toolchain string
	}{
		{c, dep.Dep{Manager: "bundler", Datasource: "ruby-version", DepType: "ruby", Name: "ruby"}, RubyGroup, RubyGroup},
		{c, dep.Dep{Manager: "ruby-version", Datasource: "ruby-version", Name: "ruby"}, RubyGroup, RubyGroup},
		{c, dep.Dep{Manager: "mise", Datasource: "ruby-version", Name: "ruby"}, RubyGroup, RubyGroup},
		{c, dep.Dep{Manager: "asdf", Datasource: "ruby-version", Name: "ruby"}, "tools", RubyGroup},
		{c, dep.Dep{Manager: "bundler", Datasource: "rubygems", Name: "ruby"}, "", ""}, // a gem named ruby
		{c, dep.Dep{Manager: "docker", Datasource: "docker", Name: "ruby"}, "", ""},
		{off, dep.Dep{Manager: "ruby-version", Datasource: "ruby-version", Name: "ruby"}, "", ""},
	} {
		if p := tc.cfg.Policy(tc.d); p.Group != tc.group || p.Toolchain != tc.toolchain {
			t.Errorf("%s %s: group %q toolchain %q, want %q %q", tc.d.Manager, tc.d.Datasource, p.Group, p.Toolchain, tc.group, tc.toolchain)
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

// TestReplacementPolicy: the built-in replacements (prefix moves, currentVersion-gated
// entries) set the pre-policy's replacement, a rule's replacementName replaces the built-in
// one with its own version, and replacements: false turns the built-in list off only.
func TestReplacementPolicy(t *testing.T) {
	const rules = "rules: [{names: [request], replacementName: got, replacementVersion: 12.0.0}, {names: [jade], replacementName: '@x/pug'}, {manager: docker, replacementPrefix: quay.io/old/, replacementName: ghcr.io/new/}]\n"
	c, err := load(t, ".deps.yaml", rules)
	if err != nil {
		t.Fatal(err)
	}
	off, err := load(t, ".deps.yaml", "replacements: false\n"+rules)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		cfg                    *Config
		d                      dep.Dep
		name, version, builtin string
	}{
		{c, dep.Dep{Manager: "docker", Name: "k8s.gcr.io/ingress-nginx/controller", Version: "v1.9.0", Versioning: "docker"}, "registry.k8s.io/ingress-nginx/controller", "", "k8s-registry-move"},
		{c, dep.Dep{Manager: "gitlabci", Name: "k8s.gcr.io/pause", Version: "3.9", Versioning: "docker"}, "registry.k8s.io/pause", "", "k8s-registry-move"},
		{c, dep.Dep{Manager: "helm", Name: "k8s.gcr.io/pause", Version: "3.9"}, "", "", ""},
		{c, dep.Dep{Manager: "helm", Name: "cilium", Source: "oci://ghcr.io/home-operations/charts-mirror", Version: "1.18.6", Versioning: "semver"}, "quay.io/cilium/charts/cilium", "1.18.6", "home-operations-to-official"},
		{c, dep.Dep{Manager: "docker", Name: "owasp/zap2docker-stable", Version: "2.13.0", Versioning: "docker"}, "zaproxy/zap-stable", "2.14.0", "zap"},
		{c, dep.Dep{Manager: "docker", Name: "owasp/zap2docker-stable", Version: "2.14.0", Versioning: "docker"}, "", "", ""},
		{c, dep.Dep{Manager: "npm", Name: "eslint-plugin-node", Version: "11.0.0", Locked: "11.1.0", Versioning: "semver"}, "eslint-plugin-n", "14.0.0", "eslint-plugin-node-to-maintained-fork"},
		{c, dep.Dep{Manager: "npm", Name: "eslint-plugin-node", Version: "10.0.0", Versioning: "semver"}, "", "", ""},
		{c, dep.Dep{Manager: "npm", Name: "jade", Version: "1.11.0", Versioning: "semver"}, "@x/pug", "", ""},
		{c, dep.Dep{Manager: "npm", Name: "request", Version: "2.88.0", Versioning: "semver"}, "got", "12.0.0", ""},
		{off, dep.Dep{Manager: "npm", Name: "cucumber", Version: "6.0.0", Versioning: "semver"}, "", "", ""},
		{c, dep.Dep{Manager: "docker", Name: "quay.io/old/app", Version: "1.0", Versioning: "docker"}, "ghcr.io/new/app", "", ""},
		{c, dep.Dep{Manager: "docker", Name: "quay.io/other/app", Version: "1.0", Versioning: "docker"}, "", "", ""},
		{off, dep.Dep{Manager: "npm", Name: "request", Version: "2.88.0", Versioning: "semver"}, "got", "12.0.0", ""},
	} {
		p := tc.cfg.Policy(tc.d)
		if p.ReplacementName != tc.name || p.ReplacementVersion != tc.version || p.Replacement != tc.builtin {
			t.Errorf("%s %s@%s: replacement %q %q (built-in %q), want %q %q (%q)", tc.d.Manager, tc.d.Name, tc.d.Version, p.ReplacementName, p.ReplacementVersion, p.Replacement, tc.name, tc.version, tc.builtin)
		}
	}
}

func TestReplacementList(t *testing.T) {
	for _, r := range Replacements {
		if r.Name == "" || r.To == "" || len(r.Names)+len(r.Patterns) == 0 || len(r.Managers) == 0 {
			t.Errorf("bad entry %+v", r)
		}
		for _, mgr := range r.Managers {
			if !slices.Contains(Managers, mgr) {
				t.Errorf("%s: unknown manager %q", r.Name, mgr)
			}
		}
		for _, g := range r.Names {
			if !doublestar.ValidatePattern(g) {
				t.Errorf("%s: invalid glob %q", r.Name, g)
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

func TestCustomDatasources(t *testing.T) {
	c, err := load(t, ".deps.yaml", `customDatasources:
  envtest:
    url: https://raw.githubusercontent.com/kubernetes-sigs/controller-tools/master/envtest-releases.yaml
    format: yaml
    versions: .releases | keys
    versionRegex: '^v?(?P<version>\d+\.\d+\.\d+)$'
custom:
  - files: [.env]
    match: 'ENVTEST_K8S_VERSION=(?P<version>\S+)'
    datasource: custom.envtest
    name: envtest
rules:
  - datasources: [custom.envtest, node]
    group: test tools
`)
	if err != nil {
		t.Fatal(err)
	}
	cd := c.CustomDatasources["envtest"]
	if sp := cd.Spec(); sp.Format != "yaml" || sp.Versions != ".releases | keys" || sp.URL != cd.URL || sp.VersionRegex != cd.VersionRegex {
		t.Errorf("spec = %+v", sp)
	}
	for name, want := range map[string]bool{"custom.envtest": true, "custom.other": false, "custom.": false, "node": true, "node-version": true, "envtest": false} {
		if got := c.KnownDatasource(name); got != want {
			t.Errorf("KnownDatasource(%q) = %v", name, got)
		}
	}
	for ds, group := range map[string]string{"custom.envtest": "test tools", "node-version": "test tools", "npm": ""} {
		if got := c.Policy(dep.Dep{Datasource: ds}).Group; got != group {
			t.Errorf("%s: group %q, want %q", ds, got, group)
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
	okIn, _ := ca.Check(dep.Minor, "4.1.0", sv)
	okOut, _ := ca.Check(dep.Minor, "5.0.0", sv)
	if ca.CommitType != "ci" || ca.Allow == nil || ca.Schedule == nil || !okIn || okOut {
		t.Errorf("cache minor: %+v", ca)
	}
}

func TestAutomergePolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `automergeType: pr
rules:
  - updateTypes: [minor, patch]
    automerge: true
  - manager: gomod
    automergeStrategy: squash
    automergeType: branch
  - names: ["actions/cache"]
    updateTypes: [patch]
    automerge: false
`)
	if err != nil {
		t.Fatal(err)
	}
	checkout := dep.Dep{Manager: "github-actions", Name: "actions/checkout"}
	cache := dep.Dep{Manager: "github-actions", Name: "actions/cache"}
	mod := dep.Dep{Manager: "gomod", Name: "a/b", DepType: "direct"}
	for _, tc := range []struct {
		name     string
		d        dep.Dep
		kind     dep.UpdateType
		want     bool
		strategy string
		typ      string
	}{
		{"pre-policy", checkout, "", false, StrategyAuto, AutomergeTypePR},
		{"minor automerges", checkout, dep.Minor, true, StrategyAuto, AutomergeTypePR},
		{"patch automerges", checkout, dep.Patch, true, StrategyAuto, AutomergeTypePR},
		{"major does not", checkout, dep.Major, false, StrategyAuto, AutomergeTypePR},
		{"digest does not", checkout, dep.Digest, false, StrategyAuto, AutomergeTypePR},
		{"later kind rule turns it off", cache, dep.Patch, false, StrategyAuto, AutomergeTypePR},
		{"strategy and type from a rule", mod, dep.Minor, true, StrategySquash, AutomergeTypeBranch},
	} {
		p := c.PolicyFor(tc.d, tc.kind)
		if tc.kind == "" {
			p = c.Policy(tc.d)
		}
		if p.Automerge != tc.want || p.AutomergeStrategy != tc.strategy || p.AutomergeType != tc.typ {
			t.Errorf("%s: automerge %v %q %q, want %v %q %q", tc.name, p.Automerge, p.AutomergeStrategy, p.AutomergeType, tc.want, tc.strategy, tc.typ)
		}
	}

	top, err := load(t, ".deps.yaml", "automerge: true\nautomergeStrategy: rebase\nautomergeType: branch\nrules: [{manager: gomod, automerge: false}, {names: [actions/cache], automergeType: pr}]\n")
	if err != nil {
		t.Fatal(err)
	}
	if p := top.PolicyFor(checkout, dep.Major); !p.Automerge || p.AutomergeStrategy != StrategyRebase || p.AutomergeType != AutomergeTypeBranch {
		t.Errorf("top-level automerge: %v %q %q", p.Automerge, p.AutomergeStrategy, p.AutomergeType)
	}
	if p := top.PolicyFor(cache, dep.Major); p.AutomergeType != AutomergeTypePR {
		t.Errorf("rule did not override automergeType: %q", p.AutomergeType)
	}
	if p := top.PolicyFor(mod, dep.Patch); p.Automerge {
		t.Error("rule did not turn automerge off")
	}
}

func TestDraftPRAndPRCreationPolicy(t *testing.T) {
	checkout := dep.Dep{Manager: "github-actions", Name: "actions/checkout"}
	mod := dep.Dep{Manager: "gomod", Name: "a/b", DepType: "direct"}
	def, err := load(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if p := def.PolicyFor(checkout, dep.Major); p.DraftPR || p.PRCreation != PRCreationImmediate {
		t.Errorf("defaults: %v %q", p.DraftPR, p.PRCreation)
	}
	c, err := load(t, ".deps.yaml", "draftPR: true\nprCreation: not-pending\nrules:\n  - updateTypes: [major]\n    prCreation: status-success\n  - manager: gomod\n    draftPR: false\n    prCreation: immediate\n")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		d     dep.Dep
		kind  dep.UpdateType
		draft bool
		mode  string
	}{
		{checkout, dep.Minor, true, PRCreationNotPending},
		{checkout, dep.Major, true, PRCreationStatusSuccess},
		{mod, dep.Major, false, PRCreationImmediate},
	} {
		if p := c.PolicyFor(tc.d, tc.kind); p.DraftPR != tc.draft || p.PRCreation != tc.mode {
			t.Errorf("%s %s: draftPR %v prCreation %q, want %v %q", tc.d.Name, tc.kind, p.DraftPR, p.PRCreation, tc.draft, tc.mode)
		}
	}
}

func TestDashboardConfig(t *testing.T) {
	c, err := load(t, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if c.DependencyDashboard || c.DashboardTitle() != "Dependency Dashboard" || c.DependencyDashboardLabels != nil {
		t.Errorf("defaults: %v %q %v", c.DependencyDashboard, c.DashboardTitle(), c.DependencyDashboardLabels)
	}
	c, err = load(t, ".deps.yaml", "dependencyDashboard: true\ndependencyDashboardTitle: Deps\ndependencyDashboardLabels: [deps, bot]\n")
	if err != nil {
		t.Fatal(err)
	}
	if !c.DependencyDashboard || c.DashboardTitle() != "Deps" || !slices.Equal(c.DependencyDashboardLabels, []string{"deps", "bot"}) {
		t.Errorf("set: %v %q %v", c.DependencyDashboard, c.DashboardTitle(), c.DependencyDashboardLabels)
	}

	// dashboardApproval: the top-level default, overridden by rules (per update type too).
	c, err = load(t, ".deps.yaml", "dependencyDashboard: true\ndashboardApproval: true\nrules: [{manager: gomod, dashboardApproval: false}, {manager: gomod, updateTypes: [major], dashboardApproval: true}]\n")
	if err != nil {
		t.Fatal(err)
	}
	checkout := dep.Dep{Manager: "github-actions", Name: "actions/checkout"}
	mod := dep.Dep{Manager: "gomod", Name: "a/b", DepType: "direct"}
	for _, tc := range []struct {
		d    dep.Dep
		kind dep.UpdateType
		want bool
	}{{checkout, dep.Minor, true}, {mod, dep.Minor, false}, {mod, dep.Major, true}} {
		if p := c.PolicyFor(tc.d, tc.kind); p.DashboardApproval != tc.want {
			t.Errorf("%s %s: dashboardApproval %v, want %v", tc.d.Name, tc.kind, p.DashboardApproval, tc.want)
		}
	}
}

func TestParseAge(t *testing.T) {
	day := 24 * time.Hour
	for in, want := range map[string]time.Duration{
		"28 days": 28 * day, "1 day": day, "3d": 3 * day, "12h": 12 * time.Hour, "90m": 90 * time.Minute,
		"2 weeks": 14 * day, "1w": 7 * day, "5 minutes": 5 * time.Minute, "3 hours": 3 * time.Hour, "1h30m": 90 * time.Minute,
		"0": 0, "0 days": 0, "0s": 0,
	} {
		if got, err := ParseAge(in); err != nil || got != want {
			t.Errorf("ParseAge(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "soon", "-3h", "3 months", "days", "1.5 days"} {
		if _, err := ParseAge(in); err == nil {
			t.Errorf("ParseAge(%q): no error", in)
		}
	}
	for d, want := range map[time.Duration]string{0: "0", day: "1 day", 28 * day: "28 days", 12 * time.Hour: "12h", 90 * time.Minute: "90m", 90 * time.Second: "1m30s"} {
		if got := FormatAge(d); got != want {
			t.Errorf("FormatAge(%v) = %q, want %q", d, got, want)
		}
	}
}

func TestAbandonmentThresholdPolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `abandonmentThreshold: 1 year
rules:
  - manager: npm
    abandonmentThreshold: 18 months
  - names: [left-pad]
    abandonmentThreshold: "0"
`)
	if err != nil {
		t.Fatal(err)
	}
	day := 24 * time.Hour
	for _, tc := range []struct {
		d    dep.Dep
		want time.Duration
	}{
		{dep.Dep{Manager: "docker", Name: "alpine"}, 365 * day},
		{dep.Dep{Manager: "npm", Name: "react"}, 540 * day},
		{dep.Dep{Manager: "npm", Name: "left-pad"}, 0},
	} {
		if p := c.Policy(tc.d); p.AbandonAfter != tc.want {
			t.Errorf("%s: AbandonAfter = %v, want %v", tc.d.Name, p.AbandonAfter, tc.want)
		}
	}
}

func TestMinimumReleaseAgePolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `minimumReleaseAge: 28 days
minimumReleaseAgeBehaviour: timestamp-required
rebaseWhen: behind-base-branch
rules:
  - manager: npm
    minimumReleaseAge: 3d
  - names: ["cosign"]
    minimumReleaseAge: 0 days
  - datasources: [custom.envtest]
    minimumReleaseAgeBehaviour: timestamp-optional
  - manager: gomod
    updateTypes: [patch]
    minimumReleaseAge: 12h
    minimumReleaseAgeBehaviour: timestamp-optional
customDatasources:
  envtest: {url: 'https://example.invalid/envtest.yaml'}
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.RebaseWhen != RebaseBehindBase {
		t.Errorf("rebaseWhen = %q", c.RebaseWhen)
	}
	day := 24 * time.Hour
	for _, tc := range []struct {
		d        dep.Dep
		kind     dep.UpdateType
		want     time.Duration
		required bool
	}{
		{dep.Dep{Manager: "docker", Name: "alpine"}, "", 28 * day, true},
		{dep.Dep{Manager: "npm", Name: "react"}, "", 3 * day, true},
		{dep.Dep{Manager: "npm", Name: "cosign"}, "", 0, true},
		{dep.Dep{Manager: "custom", Name: "envtest", Datasource: "custom.envtest"}, "", 28 * day, false},
		{dep.Dep{Manager: "gomod", Name: "x"}, dep.Minor, 28 * day, true},
		{dep.Dep{Manager: "gomod", Name: "x"}, dep.Patch, 12 * time.Hour, false},
	} {
		if p := c.PolicyFor(tc.d, tc.kind); p.MinAge != tc.want || p.TimestampRequired != tc.required {
			t.Errorf("%s %s: MinAge = %v, TimestampRequired = %v; want %v, %v", tc.d.Name, tc.kind, p.MinAge, p.TimestampRequired, tc.want, tc.required)
		}
	}
	if p := Default().Policy(dep.Dep{}); p.MinAge != 0 || p.TimestampRequired {
		t.Errorf("default MinAge = %v, TimestampRequired = %v", p.MinAge, p.TimestampRequired)
	}
}

func TestVersioningAndBucketPolicy(t *testing.T) {
	c, err := load(t, ".deps.yaml", `separateMultipleMajor: true
separateMinorPatch: true
updateNotScheduled: false
rules:
  - manager: github-actions
    separateMinorPatch: false
  - names: [fips]
    versioning: loose
  - names: [cosign]
    versioning: 'regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$'
  - manager: npm
    updateTypes: [major]
    separateMajorMinor: false
`)
	if err != nil {
		t.Fatal(err)
	}
	if c.UpdateNotScheduledEnabled() || !Default().UpdateNotScheduledEnabled() {
		t.Error("UpdateNotScheduledEnabled")
	}
	type flags struct{ majorMinor, multipleMajor, minorPatch bool }
	for _, tc := range []struct {
		d          dep.Dep
		kind       dep.UpdateType
		versioning string
		want       flags
	}{
		{dep.Dep{Manager: "gomod", Name: "x"}, "", "", flags{true, true, true}},
		{dep.Dep{Manager: "github-actions", Name: "x"}, "", "", flags{true, true, false}},
		{dep.Dep{Manager: "docker", Name: "fips"}, "", "loose", flags{true, true, true}},
		{dep.Dep{Manager: "docker", Name: "cosign"}, "", `regex:^v(?<major>\d+)\.(?<minor>\d+)\.(?<patch>\d+)[-+]ocm\.(?<build>\d+)$`, flags{true, true, true}},
		{dep.Dep{Manager: "npm", Name: "x"}, "", "", flags{true, true, true}},         // the updateTypes rule is not in the pre-policy
		{dep.Dep{Manager: "npm", Name: "x"}, dep.Major, "", flags{false, true, true}}, // ... but in the major policy
		{dep.Dep{Manager: "npm", Name: "x"}, dep.Minor, "", flags{true, true, true}},
	} {
		p := c.PolicyFor(tc.d, tc.kind)
		if got := (flags{p.SeparateMajorMinor, p.SeparateMultipleMajor, p.SeparateMinorPatch}); got != tc.want || p.Versioning != tc.versioning {
			t.Errorf("%s/%s %s: flags %+v versioning %q, want %+v %q", tc.d.Manager, tc.d.Name, tc.kind, got, p.Versioning, tc.want, tc.versioning)
		}
	}
	if p := Default().Policy(dep.Dep{}); !p.SeparateMajorMinor || p.SeparateMultipleMajor || p.SeparateMinorPatch || p.Versioning != "" {
		t.Errorf("defaults: %+v", p)
	}
}
