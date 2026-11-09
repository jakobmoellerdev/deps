package renovate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/recolabs/gnata"
	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
)

// load translates renovate.json with content src in a temporary directory.
func load(t *testing.T, src string) (*Result, error) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "renovate.json"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(t.Context(), dir, "", nil)
}

// section returns the lines of top-level key k of the translated YAML, without the key line.
func section(t *testing.T, res *Result, k string) string {
	t.Helper()
	y := string(res.YAML)
	_, rest, ok := strings.Cut(y, "\n"+k+":\n")
	if !ok {
		t.Fatalf("no %s in\n%s", k, y)
	}
	var out []string
	for l := range strings.SplitSeq(rest, "\n") {
		if l != "" && !strings.HasPrefix(l, " ") {
			break
		}
		out = append(out, l)
	}
	return strings.TrimRight(strings.Join(out, "\n"), "\n")
}

func noteLines(res *Result) []string {
	out := make([]string, len(res.Notes))
	for i, n := range res.Notes {
		out[i] = n.Level.String() + " " + n.Path + ": " + n.Msg
	}
	return out
}

// defaultExclusions are the rules of every translation: the managers Renovate disables by
// default and the deps managers Renovate does not have.
const defaultExclusions = `
  - # pre-commit is disabled by default in Renovate
    managers: [pre-commit]
    ignore: true` + otherExclusions

// otherExclusions are defaultExclusions without pre-commit.
const otherExclusions = `
  - # Renovate has no conda manager
    managers: [conda]
    ignore: true
  - # azure-pipelines is disabled by default in Renovate
    managers: [azure-pipelines]
    ignore: true
  - # Renovate has no vcpkg manager
    managers: [vcpkg]
    ignore: true
  - # Renovate has no elm manager
    managers: [elm]
    ignore: true` + nixExclusion

// nixExclusion is the rule for nix, an opt-in beta in Renovate.
const nixExclusion = `
  - # nix is disabled by default in Renovate
    managers: [nix]
    ignore: true`

func TestLoadDefaults(t *testing.T) {
	res, err := load(t, `{}`)
	if err != nil {
		t.Fatal(err)
	}
	const want = `# Translated from renovate.json by deps (Renovate compatibility).
# branchPrefix renovate/: deps run closes open PRs under it that it does not manage; disable Renovate before the first deps run.

# warn branchPrefix: deps run closes open PRs under renovate/ that it does not manage; disable Renovate before the first deps run
# info vulnerabilityAlerts: security updates come from OSV

branchPrefix: renovate/
prConcurrentLimit: 10
prHourlyLimit: 2
security: true
monorepos: false
replacements: false
rebaseWhen: conflicted
minimumReleaseAgeBehaviour: timestamp-required
ignorePaths: ['**/node_modules/**', '**/bower_components/**']
rules:` + defaultExclusions + "\n"
	if got := string(res.YAML); got != want {
		t.Errorf("YAML:\n%s\nwant:\n%s", got, want)
	}
	if len(res.Windows) != 0 || len(res.Notes) != 2 {
		t.Errorf("windows %v, notes %v", res.Windows, noteLines(res))
	}
}

func TestLoadRules(t *testing.T) {
	tests := []struct {
		name, src, rules string
		notes            []string // each must be among the notes
	}{
		{
			name: "github-actions expands to two rules",
			src:  `{packageRules: [{matchManagers: ['github-actions'], groupName: 'GitHub Actions'}]}`,
			rules: `  - # packageRules[0]
    managers: [github-actions]
    group: GitHub Actions
  - # packageRules[0]
    managers: [docker]
    depTypes: [workflow, action]
    group: GitHub Actions` + defaultExclusions,
		},
		{
			name: "dockerfile is docker with depType dockerfile",
			src:  `{packageRules: [{matchManagers: ['dockerfile'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    managers: [docker]
    depTypes: [dockerfile]
    ignore: true` + defaultExclusions,
		},
		{
			name:  "unknown depTypes only drop the rule",
			src:   `{packageRules: [{matchManagers: ['dockerfile'], matchDepTypes: ['final', 'stage'], enabled: false}]}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
			notes: []string{"warn packageRules[0]: depType final has no deps equivalent; dropped", "warn packageRules[0]: matches no deps depType; rule dropped"},
		},
		{
			name: "github-runner depType and github-runners datasource",
			src:  `{packageRules: [{matchManagers: ['github-actions'], matchDepTypes: ['github-runner'], matchDatasources: ['github-runners'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    managers: [github-actions]
    depTypes: [github-runner]
    datasources: [github-runners]
    ignore: true` + defaultExclusions,
		},
		{
			name: "intersection of depTypes and the manager restriction, in matchManagers order",
			src:  `{packageRules: [{matchManagers: ['dockerfile', 'github-actions'], matchDepTypes: ['action'], pinDigests: true}]}`,
			rules: `  - # packageRules[0]
    managers: [docker, github-actions]
    depTypes: [action]
    pin: digest` + defaultExclusions,
		},
		{
			name: "golang depType is go, categories select managers",
			src:  `{packageRules: [{matchCategories: ['golang'], matchDepTypes: ['golang'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    managers: [gomod]
    depTypes: [go]
    ignore: true` + defaultExclusions,
		},
		{
			name: "pinDigest is pin; unknown update types are dropped",
			src:  `{packageRules: [{matchUpdateTypes: ['pinDigest', 'bump'], groupName: 'pins'}]}`,
			rules: `  - # packageRules[0]
    updateTypes: [pin]
    group: pins` + defaultExclusions,
			notes: []string{"info packageRules[0]: update type bump has no deps equivalent; dropped"},
		},
		{
			name: "replacementName and replacementVersion",
			src:  `{packageRules: [{matchPackageNames: ['foo'], replacementName: 'bar', replacementVersion: '2.0.0', replacementVersionTemplate: 'x'}]}`,
			rules: `  - # packageRules[0]
    names: [foo]
    replacementName: bar
    replacementVersion: 2.0.0` + defaultExclusions,
			notes: []string{"warn packageRules[0].replacementVersionTemplate: not supported; dropped"},
		},
		{
			name: "a prefix replacementNameTemplate is replacementPrefix",
			src:  `{packageRules: [{matchDatasources: ['docker'], matchPackageNames: ['k8s.gcr.io/**'], replacementNameTemplate: "{{{replace 'k8s\\.gcr\\.io/' 'registry.k8s.io/' packageName}}}"}]}`,
			rules: `  - # packageRules[0]
    names: [k8s.gcr.io/**]
    datasources: [docker]
    replacementName: registry.k8s.io/
    replacementPrefix: k8s.gcr.io/` + defaultExclusions,
		},
		{
			name:  "other replacementNameTemplates and a lone replacementVersion are dropped",
			src:   `{packageRules: [{matchPackageNames: ['a'], replacementNameTemplate: '{{{packageName}}}-x'}, {matchPackageNames: ['b'], replacementVersion: '1'}]}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
			notes: []string{
				"warn packageRules[0].replacementNameTemplate: only the prefix form {{{replace '<prefix>' '<new prefix>' packageName}}} is supported; dropped",
				"warn packageRules[1].replacementVersion: dropped: replacementVersion: requires replacementName",
			},
		},
		{
			name: "matchUpdateTypes replacement and the replacement block",
			src:  `{replacement: {automerge: true}, packageRules: [{matchUpdateTypes: ['replacement'], matchPackageNames: ['x'], enabled: false}]}`,
			rules: `  - # replacement
    updateTypes: [replacement]
    automerge: true
  - # packageRules[0]
    names: [x]
    updateTypes: [replacement]
    ignore: true` + defaultExclusions,
		},
		{
			name: "rangeStrategy with matchUpdateTypes is dropped",
			src:  `{packageRules: [{matchUpdateTypes: ['major'], rangeStrategy: 'bump', automerge: false}]}`,
			rules: `  - # packageRules[0]
    updateTypes: [major]
    automerge: false` + defaultExclusions,
			notes: []string{"warn packageRules[0].rangeStrategy: deps decides rangeStrategy before the update type is known; dropped"},
		},
		{
			name: "release names, release age setting and PR metadata",
			src: `{packageRules: [{matchPackageNames: ['jqlang/jq'], extractVersion: '^jq-(?<version>.+)$', minimumReleaseAgeBehaviour: 'timestamp-optional',
				addLabels: ['jq'], reviewers: ['team:core'], assignees: 'alice', labels: ['deps']}]}`,
			rules: `  - # packageRules[0]
    names: [jqlang/jq]
    extractVersion: ^jq-(?<version>.+)$
    minimumReleaseAgeBehaviour: timestamp-optional
    labels: [deps]
    addLabels: [jq]
    reviewers: ['team:core']
    assignees: [alice]` + defaultExclusions,
		},
		{
			name: "dependencyDashboardApproval per rule, rust-toolchain manager",
			src:  `{dependencyDashboard: true, packageRules: [{matchManagers: ['rust-toolchain'], matchUpdateTypes: ['major'], dependencyDashboardApproval: true}]}`,
			rules: `  - # packageRules[0]
    managers: [rust-toolchain]
    updateTypes: [major]
    dashboardApproval: true` + defaultExclusions,
		},
		{
			name:  "dart category; pub datasources",
			src:   `{packageRules: [{matchCategories: ['dart'], matchDatasources: ['dart', 'flutter-version'], groupName: 'dart'}, {matchManagers: ['fvm'], enabled: false}]}`,
			rules: "  - # packageRules[0]\n    managers: [pub, fvm]\n    datasources: [dart, flutter-version]\n    group: dart\n  - # packageRules[1]\n    managers: [fvm]\n    ignore: true" + defaultExclusions,
		},
		{
			name:  "devcontainer manager",
			src:   `{packageRules: [{matchManagers: ['devcontainer'], enabled: false}]}`,
			rules: "  - # packageRules[0]\n    managers: [devcontainer]\n    ignore: true" + defaultExclusions,
		},
		{
			name:  "sbt and scalafmt managers, java category, sbt datasources",
			src:   `{packageRules: [{matchManagers: ['sbt', 'scalafmt'], matchDatasources: ['sbt-package', 'sbt-plugin'], groupName: 'scala'}, {matchCategories: ['java'], enabled: false}]}`,
			rules: "  - # packageRules[0]\n    managers: [sbt, scalafmt]\n    datasources: [sbt-package, sbt-plugin]\n    group: scala\n  - # packageRules[1]\n    managers: [maven, gradle, gradle-wrapper, maven-wrapper, sbt, scalafmt]\n    ignore: true" + defaultExclusions,
		},
		{
			name: "php, bazel and ci categories; julia datasource; draftPR, prCreation and abandonmentThreshold per rule; azure-pipelines opt-in",
			src: `{'azure-pipelines': {enabled: true}, packageRules: [{matchCategories: ['php', 'bazel'], groupName: 'g'},
				{matchManagers: ['nuget', 'julia'], matchDatasources: ['julia-general-metadata'], draftPR: true, prCreation: 'status-success', abandonmentThreshold: null},
				{matchCategories: ['ci'], matchPackageNames: ['x'], prCreation: 'approval', abandonmentThreshold: '2 years'}]}`,
			rules: `  - # azure-pipelines
    managers: [azure-pipelines]
    ignore: false
  - # packageRules[0]
    managers: [composer, bazel-module, bazelisk]
    group: g
  - # packageRules[1]
    managers: [nuget, julia]
    datasources: [julia-registry]
    abandonmentThreshold: "0"
    draftPR: true
    prCreation: status-success
  - # packageRules[2]
    managers: [github-actions, gitlabci, bitbucket-pipelines, woodpecker, droneci, circleci, azure-pipelines]
    names: [x]
    abandonmentThreshold: 2 years
  - # packageRules[2]
    managers: [docker]
    names: [x]
    depTypes: [workflow, action]
    abandonmentThreshold: 2 years
  - # pre-commit is disabled by default in Renovate
    managers: [pre-commit]
    ignore: true
  - # Renovate has no conda manager
    managers: [conda]
    ignore: true
  - # Renovate has no vcpkg manager
    managers: [vcpkg]
    ignore: true
  - # Renovate has no elm manager
    managers: [elm]
    ignore: true
  - # nix is disabled by default in Renovate
    managers: [nix]
    ignore: true`,
			notes: []string{"warn packageRules[2].prCreation: approval is not supported (deps' dashboardApproval also holds back the branch); dropped"},
		},
		{
			name:  "ansible category; galaxy datasources",
			src:   `{packageRules: [{matchCategories: ['ansible'], matchDatasources: ['galaxy', 'galaxy-collection'], groupName: 'ansible'}]}`,
			rules: "  - # packageRules[0]\n    managers: [ansible, ansible-galaxy]\n    datasources: [galaxy, galaxy-collection]\n    group: ansible" + defaultExclusions,
		},
		{
			name: "postUpgradeTasks top level and per rule, Ruby and Swift managers, rangeStrategy widen",
			src: `{postUpgradeTasks: {commands: ['make generate'], fileFilters: ['**/*.go'], executionMode: 'branch'},
				packageRules: [{matchCategories: ['ruby'], rangeStrategy: 'widen', postUpgradeTasks: {commands: ['bundle exec rake']}},
					{matchManagers: ['swift'], postUpgradeTasks: {commands: ['swift build {{{depName}}}']}, enabled: false}]}`,
			rules: `  - # postUpgradeTasks
    postUpgradeTasks:
      commands: [make generate]
      executionMode: branch
      files: ['**/*.go']
  - # packageRules[0]
    managers: [bundler, ruby-version]
    rangeStrategy: widen
    postUpgradeTasks:
      commands: [bundle exec rake]
  - # packageRules[1]
    managers: [swift]
    ignore: true` + defaultExclusions,
			notes: []string{
				"info postUpgradeTasks.postUpgradeTasks: deps runs postUpgradeTasks only with --allow-tasks (or DEPS_ALLOW_TASKS) listing the commands, like Renovate's allowedCommands",
				"warn packageRules[1].postUpgradeTasks: command \"swift build {{{depName}}}\" is a template; deps passes the update as DEPS_* environment variables (deps help environment); dropped",
			},
		},
		{
			name: "Python category, pyenv and runtime-version managers, python-version datasource",
			src: `{packageRules: [{matchCategories: ['python'], automerge: true},
				{matchManagers: ['pyenv', 'runtime-version'], matchDatasources: ['python-version'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    managers: [pip_requirements, pep621, pep723, setup-cfg, pip_setup, poetry, pipenv, pyenv, runtime-version, homeassistant-manifest]
    automerge: true
  - # packageRules[1]
    managers: [pyenv, runtime-version]
    datasources: [python-version]
    ignore: true` + defaultExclusions,
		},
		{
			name: "sveltos, bun and pip-compile managers",
			src: `{packageRules: [{matchManagers: ['sveltos'], automerge: true},
				{matchManagers: ['bun'], groupName: 'js'},
				{matchManagers: ['pip-compile'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    managers: [helm]
    depTypes: [sveltos]
    automerge: true
  - # packageRules[1]
    managers: [npm]
    group: js
  - # packageRules[2]
    managers: [pip_requirements]
    ignore: true` + defaultExclusions,
		},
		{
			name: "matchCurrentValue regexes and exact strings are currentVersion",
			src: `{packageRules: [{matchCurrentValue: '/^1/', matchPackageNames: ['a'], enabled: false},
				{matchCurrentValue: '!/-rc/i', matchPackageNames: ['b'], enabled: false},
				{matchCurrentValue: 'v1.2.3', matchPackageNames: ['c'], enabled: false},
				{matchCurrentValue: '!latest', matchPackageNames: ['d'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    names: [a]
    currentVersion: /^1/
    ignore: true
  - # packageRules[1]
    names: [b]
    currentVersion: '!/-rc/i'
    ignore: true
  - # packageRules[2]
    names: [c]
    currentVersion: /^v1\.2\.3$/i
    ignore: true
  - # packageRules[3]
    names: [d]
    currentVersion: '!/^latest$/i'
    ignore: true` + defaultExclusions,
		},
		{
			name: "matchCurrentValue globs, matchCurrentValue with matchCurrentVersion and invalid regexes drop the rule",
			src: `{packageRules: [{matchCurrentValue: '1.*', matchPackageNames: ['a'], enabled: false},
				{matchCurrentValue: '/^1/', matchCurrentVersion: '< 2', matchPackageNames: ['b'], enabled: false},
				{matchCurrentValue: '/(?=x)/', matchPackageNames: ['c'], enabled: false},
				{matchCurrentValue: 1, matchPackageNames: ['d'], enabled: false}]}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
			notes: []string{
				`warn packageRules[0]: matchCurrentValue "1.*": globs have no deps equivalent; rule dropped`,
				"warn packageRules[1]: matchCurrentVersion and matchCurrentValue together cannot be expressed (one currentVersion); rule dropped",
				"warn packageRules[3]: matchCurrentValue must be a string; rule dropped",
			},
		},
		{
			name: "versioning is dropped next to currentVersion and currentAge",
			src: `{packageRules: [{matchCurrentValue: '/^v1/', matchPackageNames: ['a'], versioning: 'loose', groupName: 'a'},
				{matchCurrentAge: '> 1 year', matchPackageNames: ['b'], extractVersion: '^v(?<version>.+)$', enabled: false},
				{matchCurrentVersion: '< 2', matchPackageNames: ['c'], versioning: 'loose'}]}`,
			rules: `  - # packageRules[0]
    names: [a]
    currentVersion: /^v1/
    group: a
  - # packageRules[1]
    names: [b]
    currentAge: '> 1 year'
    ignore: true` + defaultExclusions,
			notes: []string{
				"warn packageRules[0].versioning: deps cannot combine versioning with currentVersion; dropped",
				"warn packageRules[1].extractVersion: deps cannot combine extractVersion with currentAge; dropped",
				"warn packageRules[2].versioning: deps cannot combine versioning with currentVersion; dropped",
				"info packageRules[2]: no deps effect left; rule dropped",
			},
		},
		{
			name: "prHeader and prFooter per rule",
			src: `{packageRules: [{matchPackageNames: ['a'], prHeader: 'Bumps {{depName}} from {{{currentValue}}} to {{ newVersion }}{{! note }}', prFooter: null},
				{matchPackageNames: ['b'], prHeader: '{{#if isMajor}}major{{/if}}', prFooter: 'Group \\{{groupName}} {{groupName}}'}]}`,
			rules: `  - # packageRules[0]
    names: [a]
    prHeader: Bumps {{.Name}} from {{.From}} to {{.To}}
    prFooter: ""
  - # packageRules[1]
    names: [b]
    prFooter: Group {{"{{"}}groupName}} {{.Group}}` + defaultExclusions,
			notes: []string{"warn packageRules[1].prHeader: unsupported Handlebars expression {{#if isMajor}}; dropped"},
		},
		{
			name: "matchCurrentVersion and matchCurrentAge",
			src:  `{packageRules: [{matchCurrentAge: '> 1 year', matchCurrentVersion: '< 2', matchPackageNames: ['a'], matchUpdateTypes: ['major'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    names: [a]
    currentVersion: < 2
    currentAge: '> 1 year'
    updateTypes: [major]
    ignore: true` + defaultExclusions,
		},
		{
			name: "deprecated patterns, prefixes and exclusions",
			src: `{packageRules: [{matchPackagePatterns: ['^k8s\\.io/'], matchPackagePrefixes: ['sigs.k8s.io/'],
				excludePackageNames: ['k8s.io/kube-openapi'], matchPaths: ['charts/**'], stabilityDays: 3}]}`,
			rules: `  - # packageRules[0]
    names: ['!k8s.io/kube-openapi', /^k8s\.io//, /^sigs\.k8s\.io//]
    paths: [charts/**]
    minimumReleaseAge: 3 days` + defaultExclusions,
		},
		{
			name:  "package and dep names together drop the rule",
			src:   `{packageRules: [{matchPackageNames: ['a'], matchDepNames: ['b'], enabled: false}]}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
			notes: []string{"warn packageRules[0]: matchPackageNames and matchDepNames together cannot be expressed; rule dropped"},
		},
		{
			name: "blocks, ignoreDeps and enabledManagers in order",
			src: `{semanticCommitType: 'build', gomod: {enabled: false}, major: {automerge: false},
				'pre-commit': {enabled: true}, packageRules: [{matchPackageNames: ['*'], groupName: 'all'}], ignoreDeps: ['x']}`,
			rules: `  - # semanticCommitType
    commitType: build
  - # major
    updateTypes: [major]
    automerge: false
  - # packageRules[0]
    names: ['**']
    group: all
  - # ignoreDeps
    names: [x]
    ignore: true
  - # gomod.enabled: false
    managers: [gomod]
    ignore: true` + otherExclusions,
		},
		{
			name: "ignoreDeps entries are exact names",
			src:  `{ignoreDeps: ['[a]', '/x/y', 'a*', '!b', 'github.com/c/d']}`,
			rules: `  - # ignoreDeps
    names: ['/^\[a\]$/', /^/x/y$/, /^a\*$/, /^!b$/, github.com/c/d]
    ignore: true` + defaultExclusions,
		},
		{
			name: "enabledManagers excludes every other manager",
			src:  `{enabledManagers: ['npm', 'dockerfile', 'helmv3']}`,
			rules: `  - # enabledManagers
    managers: [github-actions]
    ignore: true
  - # enabledManagers
    managers: [gomod]
    ignore: true
  - # enabledManagers
    managers: [docker]
    depTypes: [compose, workflow, action, kubernetes, helm]
    ignore: true
  - # enabledManagers
    managers: [helm]
    depTypes: [requirements, helmfile, k0s, flux, argocd, sveltos]
    ignore: true
` + strings.Join(func() []string {
				var out []string
				for _, m := range config.Managers {
					why := "Renovate has no " + m + " manager"
					for _, xs := range managerMap {
						if slices.ContainsFunc(xs, func(x expansion) bool { return x.manager == m }) {
							why = "enabledManagers"
						}
					}
					if !slices.Contains([]string{"github-actions", "gomod", "docker", "npm", "helm", "custom"}, m) {
						out = append(out, "  - # "+why+"\n    managers: ["+m+"]\n    ignore: true")
					}
				}
				return out
			}(), "\n"),
		},
		{
			name: "vulnerabilityAlerts.groupName is a securityGroup rule after the packageRules",
			src: `{vulnerabilityAlerts: {groupName: 'security', labels: ['sec'], automerge: true, schedule: ['at any time'],
				minimumReleaseAge: null, vulnerabilityFixStrategy: 'highest', commitMessageSuffix: '[SECURITY]'},
				packageRules: [{matchManagers: ['gomod'], groupName: 'go'}], ignoreDeps: ['x']}`,
			rules: `  - # packageRules[0]
    managers: [gomod]
    group: go
  - # ignoreDeps
    names: [x]
    ignore: true
  - # vulnerabilityAlerts
    securityGroup: security` + defaultExclusions,
			notes: []string{
				"info vulnerabilityAlerts.labels: no deps equivalent for security updates only; ignored",
				"info vulnerabilityAlerts.commitMessageSuffix: no deps equivalent for security updates only; ignored",
				"warn vulnerabilityAlerts.automerge: no deps equivalent for security updates only; ignored",
				"info vulnerabilityAlerts.schedule: Renovate and deps both ignore schedules for security updates",
				"warn vulnerabilityAlerts.vulnerabilityFixStrategy: not supported; deps picks the lowest update at or above the minimum fixed version",
			},
		},
		{
			name:  "disabled vulnerabilityAlerts have no securityGroup",
			src:   `{vulnerabilityAlerts: {enabled: false, groupName: 'security'}}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
		},
		{
			name: "osvVulnerabilityAlerts keeps the securityGroup of disabled vulnerabilityAlerts",
			src:  `{osvVulnerabilityAlerts: true, vulnerabilityAlerts: {enabled: false, groupName: 'security'}}`,
			rules: `  - # vulnerabilityAlerts
    securityGroup: security` + defaultExclusions,
		},
		{
			name:  "a null groupName is no securityGroup",
			src:   `{vulnerabilityAlerts: {groupName: null}}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
		},
		{
			name:  "a non-string groupName is ignored",
			src:   `{vulnerabilityAlerts: {groupName: 1}}`,
			rules: strings.TrimPrefix(defaultExclusions, "\n"),
			notes: []string{"warn vulnerabilityAlerts.groupName: must be a string; ignored"},
		},
		{
			name: "git-submodules opt-in un-ignores the deps manager",
			src:  `{'git-submodules': {enabled: true}}`,
			rules: `  - # git-submodules
    managers: [git-submodules]
    ignore: false` + defaultExclusions,
		},
		{
			name:  "nix opt-in drops its exclusion",
			src:   `{nix: {enabled: true}}`,
			rules: strings.TrimPrefix(strings.TrimSuffix(defaultExclusions, nixExclusion), "\n"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res, err := load(t, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			if got := section(t, res, "rules"); got != tt.rules {
				t.Errorf("rules:\n%s\nwant:\n%s", got, tt.rules)
			}
			notes := noteLines(res)
			for _, n := range tt.notes {
				if !slices.Contains(notes, n) {
					t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
				}
			}
		})
	}
}

func TestLoadTopLevel(t *testing.T) {
	res, err := load(t, `{
		branchPrefix: 'bot/', prConcurrentLimit: 3, prHourlyLimit: 1.5, automerge: true, rebaseWhen: 'auto',
		ignorePaths: ['foo', '**/bar/**', '!keep', '/^x/'], automergeStrategy: 'merge-commit', automergeType: 'branch',
		minimumReleaseAge: '7 days', vulnerabilityAlerts: {enabled: false}, schedule: ['before 6am on monday', 'every 2 weeks'],
		timezone: 'Europe/Berlin', lockFileMaintenance: {enabled: true, automerge: true}, labels: ['deps'],
		pinDigests: true, rangeStrategy: 'pin', cocoapods: {enabled: true},
		registryAliases: {'${HARBOR:-}/proxy': 'harbor.example.com/hub', '$CI_REGISTRY': 'registry.example.com'},
	}`)
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.YAML)
	_, body, _ := strings.Cut(y, "\n\nbranchPrefix:")
	const want = ` bot/
prConcurrentLimit: 3
pin: digest
rangeStrategy: pin
security: false
monorepos: false
replacements: false
rebaseWhen: behind-base-branch
minimumReleaseAge: 7 days
minimumReleaseAgeBehaviour: timestamp-required
schedule: [before 6am on monday]
timezone: Europe/Berlin
registryAliases:
  $CI_REGISTRY: registry.example.com
  ${HARBOR:-}/proxy: harbor.example.com/hub
ignorePaths: [/foo/, '**/bar/**', /^x/]
automerge: true
automergeStrategy: merge
labels: [deps]
lockFileMaintenance:
  enabled: true
  schedule: [before 4am on monday]
rules:` + defaultExclusions + "\n"
	if body != want {
		t.Errorf("YAML:\n%s\nwant body:\n%s", y, want)
	}
	if !slices.Equal(res.Windows, []string{"before 4am on monday", "before 6am on monday"}) {
		t.Errorf("windows = %v", res.Windows)
	}
	notes := noteLines(res)
	for _, n := range []string{
		"warn prHourlyLimit: must be a non-negative integer, got 1.5; dropped",
		"warn ignorePaths[2]: negation \"!keep\" is not supported in ignorePaths; dropped",
		"warn automergeType: deps automerges through the PR; dropped",
		`warn schedule: schedule "every 2 weeks": "every" needs weekday, weekend, a day name or <n> months, got "weeks"; entry dropped`,
		"info lockFileMaintenance.automerge: no deps equivalent; ignored",
		"warn cocoapods: not supported; ignored",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
}

func TestLoadLabelsAndPRTexts(t *testing.T) {
	for _, tt := range []struct {
		name, src, want string
		notes           []string
	}{
		{
			name: "translated",
			src: `{prHeader: 'Update of {{{depName}}}', prFooter: '{{!-- x --}}Thanks {', rebaseLabel: 'deps-rebase',
				stopUpdatingLabel: 'hands off'}`,
			want: "prHeader: Update of {{.Name}}\nprFooter: Thanks {\nrebaseLabel: deps-rebase\nstopUpdatingLabel: hands off\n",
		},
		{
			name: "dropped",
			src:  `{prHeader: 'x {{lookup a b}}', prFooter: '{{depName', rebaseLabel: 'Stop', stopUpdatingLabel: 'stop'}`,
			want: "rebaseLabel: Stop\n",
			notes: []string{
				"warn prHeader: unsupported Handlebars expression {{lookup a b}}; dropped",
				"warn prFooter: unclosed {{; dropped",
				`warn stopUpdatingLabel: dropped: stopUpdatingLabel: must differ from rebaseLabel, got "stop"`,
			},
		},
		{
			name: "an empty prHeader is none",
			src:  `{prHeader: '', prFooter: null}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := load(t, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for l := range strings.SplitSeq(string(res.YAML), "\n") {
				for _, k := range []string{"prTitle:", "prHeader:", "prFooter:", "rebaseLabel:", "stopUpdatingLabel:"} {
					if strings.HasPrefix(l, k) {
						got = append(got, l+"\n")
					}
				}
			}
			if strings.Join(got, "") != tt.want {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, ""), tt.want)
			}
			notes := noteLines(res)
			for _, n := range tt.notes {
				if !slices.Contains(notes, n) {
					t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
				}
			}
		})
	}
}

// TestLoadTitles checks the prTitle composed of Renovate's title settings: every prTitle line
// of the translation (top level, then rules) and the notes.
func TestLoadTitles(t *testing.T) {
	const (
		pin     = `{{if eq .UpdateType "pin"}}pin{{else}}update{{end}}`
		topic   = `{{if and .Group (gt .Count 1)}}{{.Group}}{{else}}{{if eq .UpdateType "digest"}}{{.Name}} digest{{else}}dependency {{.Name}}{{end}}{{end}}`
		extra   = `{{if .To}}to {{.To}}{{end}}`
		semPref = `{{.Type}}{{with .Scope}}({{.}}){{end}}:`
	)
	upper := strings.NewReplacer("pin{{else}}update", "Pin{{else}}Update")
	for _, tt := range []struct {
		name, src string
		titles    []string
		notes     []string
	}{
		{name: "unset", src: `{semanticCommitType: 'build', semanticCommitScope: null}`},
		{name: "semanticCommits enabled alone", src: `{semanticCommits: 'enabled'}`,
			notes: []string{"info semanticCommits: deps always writes conventional commit messages"}},
		{name: "semanticCommits disabled drops the prefix and keeps the case", src: `{extends: [':semanticCommitsDisabled']}`,
			titles: []string{`prTitle: '` + upper.Replace(pin) + ` ` + topic + ` ` + extra + `'`}},
		{name: "topic and suffix, rules inherit them", src: `{commitMessageTopic: '{{depName}}', commitMessageSuffix: '[Skip CI]',
			packageRules: [{matchManagers: ['gomod'], commitMessageAction: 'Bump'}, {matchPackageNames: ['x'], prTitle: '{{{commitMessagePrefix}}} {{depName}}!'}]}`,
			titles: []string{
				`prTitle: '` + semPref + ` ` + pin + ` ` + strings.Replace(topic, "dependency {{.Name}}", "{{.Name}}", 1) + ` ` + extra + ` [skip ci]'`,
				`    prTitle: '` + semPref + ` bump ` + strings.Replace(topic, "dependency {{.Name}}", "{{.Name}}", 1) + ` ` + extra + ` [skip ci]'`,
				`    prTitle: '` + semPref + ` {{.Name}}!'`,
			}},
		{name: "a prefix replaces the semantic one and keeps the case", src: `{commitMessagePrefix: 'deps:', commitMessageExtra: null}`,
			titles: []string{`prTitle: 'deps: ` + upper.Replace(pin) + ` ` + topic + `'`}},
		{name: "commitMessage is the title's first line", src: `{commitMessage: '{{{commitMessagePrefix}}} Bump {{depName}}\n\nbody'}`,
			titles: []string{`prTitle: '` + semPref + ` bump {{.Name}}'`}},
		{name: "prTitle over commitMessage", src: `{prTitle: '{{commitMessage}} ({{currentVersion}})', commitMessage: 'Bump {{depName}}'}`,
			titles: []string{`prTitle: bump {{.Name}} ({{.From}})`}},
		{name: "commitMessageLowerCase never and uppercase types keep the case", src: `{commitMessageLowerCase: 'never', commitMessageAction: 'Bump',
			packageRules: [{matchPackageNames: ['a'], semanticCommitType: 'CI', commitMessageTopic: 'A'}]}`,
			titles: []string{
				`prTitle: '` + semPref + ` {{if eq .UpdateType "pin"}}Pin{{else}}Bump{{end}} ` + topic + ` ` + extra + `'`,
				`    prTitle: '` + semPref + ` {{if eq .UpdateType "pin"}}Pin{{else}}Bump{{end}} {{if and .Group (gt .Count 1)}}{{.Group}}{{else}}A{{end}} ` + extra + `'`,
			}},
		{name: "rule semanticCommits disabled", src: `{packageRules: [{matchPackageNames: ['b'], semanticCommits: 'disabled', commitMessageSuffix: '(b)'}]}`,
			titles: []string{`    prTitle: '` + upper.Replace(pin) + ` ` + topic + ` ` + extra + ` (b)'`}},
		{name: "an unconvertible part keeps the built-in titles", src: `{commitMessageExtra: '{{#if isMajor}}v{{newMajor}}{{/if}}', semanticCommits: 'disabled',
			packageRules: [{matchPackageNames: ['a'], commitMessageAction: 'Bump', groupName: 'a'}]}`,
			notes: []string{
				"warn commitMessageExtra: unsupported Handlebars expression {{#if isMajor}}; deps keeps its built-in PR titles",
				"warn semanticCommits: deps always writes conventional commit messages",
			}},
		{name: "an unconvertible rule part", src: `{packageRules: [{matchPackageNames: ['a'], commitMessageTopic: '{{depNameSanitized}}', groupName: 'g'}]}`,
			notes: []string{"warn packageRules[0].commitMessageTopic: unsupported Handlebars expression {{depNameSanitized}}; deps keeps its built-in PR titles"}},
		{name: "a non-string part is ignored", src: `{commitMessageTopic: 1, commitMessageAction: 'Bump'}`,
			titles: []string{`prTitle: '` + semPref + ` {{if eq .UpdateType "pin"}}pin{{else}}bump{{end}} ` + topic + ` ` + extra + `'`},
			notes:  []string{"warn commitMessageTopic: must be a string; ignored"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			res, err := load(t, tt.src)
			if err != nil {
				t.Fatal(err)
			}
			var titles []string
			for l := range strings.SplitSeq(string(res.YAML), "\n") {
				if strings.HasPrefix(strings.TrimSpace(l), "prTitle:") {
					titles = append(titles, l)
				}
			}
			if !slices.Equal(titles, tt.titles) {
				t.Errorf("titles:\n%s\nwant:\n%s", strings.Join(titles, "\n"), strings.Join(tt.titles, "\n"))
			}
			notes := noteLines(res)
			for _, n := range tt.notes {
				if !slices.Contains(notes, n) {
					t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
				}
			}
		})
	}
}

func TestHandlebars(t *testing.T) {
	vars := map[string]string{"commitMessageTopic": "T"}
	for _, tt := range []struct {
		src, want, err string
		lower          bool
	}{
		{src: "plain", want: "plain"},
		{src: "{{depName}} {{{packageName}}} {{newVersion}} {{newValue}} {{currentVersion}} {{currentValue}} {{groupName}} {{updateType}}",
			want: "{{.Name}} {{.Name}} {{.To}} {{.To}} {{.From}} {{.From}} {{.Group}} {{.UpdateType}}"},
		{src: "Up {{{commitMessageTopic}}}", want: "up T", lower: true},
		{src: `a\{{b}} \\{{depName}}`, want: `a{{"{{"}}b}} \{{.Name}}`},
		{src: "{{depName}}}}{", want: "{{.Name}}}}{"},
		{src: `x{\{{ {{{depName}}}`, want: `x{{"{{"}}{ {{.Name}}`},
		{src: `{\{{`, want: `{{"{{"}}{`},
		{src: `x{{{depName}}}`, want: `x{{.Name}}`},
		{src: `x{ {{depName}}{`, want: `x{ {{.Name}}{`},
		{src: `x{\\{{depName}}`, want: `x{\{{.Name}}`},
		{src: `x{{{"{"}}`, err: `unclosed {{{`},
		{src: "{{!-- {{depName}} --}}x", want: "x"},
		{src: "{{#if isMajor}}x{{/if}}", err: "unsupported Handlebars expression {{#if isMajor}}"},
		{src: "{{depNameSanitized}}", err: "unsupported Handlebars expression {{depNameSanitized}}"},
		{src: "{{{depName}}", err: "unclosed {{{"},
	} {
		got, err := handlebars(tt.src, vars, tt.lower)
		if tt.err != "" {
			if err == nil || err.Error() != tt.err {
				t.Errorf("%q: err = %v, want %s", tt.src, err, tt.err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("%q = %q, %v; want %q", tt.src, got, err, tt.want)
		}
	}
}

func TestLoadCustom(t *testing.T) {
	res, err := load(t, `{
		customDatasources: {
			envtest: {defaultRegistryUrlTemplate: 'https://x.example/e.yaml', format: 'yaml', transformTemplates: ['$x']},
			list: {defaultRegistryUrlTemplate: 'https://x.example/l.txt', format: 'plain'},
		},
		customManagers: [
			{customType: 'regex', fileMatch: ['(^|/)\\.env$'], matchStrings: ['E=(?<currentValue>\\S+)'], datasourceTemplate: 'custom.envtest', depNameTemplate: 'envtest'},
			{customType: 'regex', managerFilePatterns: ['**/*.mk'], matchStrings: ['L=(?<currentValue>\\S+)', 'M=(?<currentValue>\\S+)'], datasourceTemplate: 'custom.list', depNameTemplate: 'l', versioningTemplate: 'loose'},
			{customType: 'regex', managerFilePatterns: ['x'], matchStrings: ['(?<currentValue>.)'], datasourceTemplate: '{{{datasource}}}'},
			{customType: 'xpath', managerFilePatterns: ['x']},
		],
		packageRules: [{matchDatasources: ['custom.envtest'], enabled: false}],
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := section(t, res, "customDatasources"), `  list:
    url: https://x.example/l.txt
    format: plain`; got != want {
		t.Errorf("customDatasources:\n%s\nwant:\n%s", got, want)
	}
	if got, want := section(t, res, "custom"), `  - # customManagers[1].matchStrings[0]
    files: ['**/*.mk']
    match: L=(?<currentValue>\S+)
    datasource: custom.list
    name: l
    versioning: loose
  - # customManagers[1].matchStrings[1]
    files: ['**/*.mk']
    match: M=(?<currentValue>\S+)
    datasource: custom.list
    name: l
    versioning: loose`; got != want {
		t.Errorf("custom:\n%s\nwant:\n%s", got, want)
	}
	notes := noteLines(res)
	for _, n := range []string{
		"warn customDatasources.envtest: JSONata transformTemplates \"$x\" has no deps equivalent; write versions/versionRegex by hand; datasource dropped",
		"warn customManagers[2]: dropped: datasource: required unless match has a (?P<datasource>...) group; name: required unless match has a (?P<name>...), (?P<depName>...) or (?P<packageName>...) group",
		"warn customManagers[3]: customType xpath is not supported; dropped",
		"warn packageRules[0]: datasource custom.envtest has no deps equivalent; dropped",
		"warn packageRules[0]: matches no deps datasource; rule dropped",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
	var dropped bool
	for _, n := range res.Notes {
		dropped = dropped || (n.Path == "customManagers[0]" && strings.HasPrefix(n.Msg, "dropped: datasource:") && strings.Contains(n.Msg, `customDatasources has no entry "envtest"`))
	}
	if !dropped {
		t.Errorf("customManagers[0] (envtest) not dropped:\n%s", strings.Join(notes, "\n"))
	}
}

// Renovate's documented package.json JSONata manager: one entry per query; the literal
// templates are merged into the query's results, as they override the query's fields.
func TestLoadCustomJSONata(t *testing.T) {
	res, err := load(t, `{
		customManagers: [
			{
				customType: 'jsonata', fileFormat: 'json', managerFilePatterns: ['/package.json/'],
				matchStrings: [
					'$each(dependencies, function($v, $k) { {"depName":$k, "currentValue": $v, "depType": "dependencies"}})',
					'{ "depName": "pnpm", "currentValue": $substring(packageManager, 5),  "depType": "packageManager"}',
				],
				datasourceTemplate: 'npm',
			},
			{
				customType: 'jsonata', fileFormat: 'yaml', managerFilePatterns: ['tools.yaml'],
				matchStrings: ['tools.{"depName": repo, "currentValue": version}'],
				datasourceTemplate: 'github-tags', extractVersionTemplate: '^v(?<version>.+)$', registryUrlTemplate: 'https://github.com', depTypeTemplate: 'tool',
			},
			{customType: 'jsonata', fileFormat: 'toml', managerFilePatterns: ['x'], matchStrings: ['a'], depNameTemplate: '{{{packageName}}}'},
			{customType: 'jsonata', fileFormat: 'toml', managerFilePatterns: ['x'], matchStrings: ['a'], extractVersionTemplate: '{{{x}}}'},
			{customType: 'jsonata', fileFormat: 'xml', managerFilePatterns: ['x'], matchStrings: ['a']},
			{customType: 'jsonata', fileFormat: 'json', managerFilePatterns: ['x'], matchStrings: ['a('], currentValueTemplate: '1'},
			{customType: 'regex', fileFormat: 'json', managerFilePatterns: ['x'], matchStrings: ['(?<currentValue>.)'], datasourceTemplate: 'npm', depNameTemplate: 'x'},
		],
		packageRules: [
			{matchManagers: ['custom.jsonata'], groupName: 'tools'},
			{matchCategories: ['custom'], groupName: 'all custom'},
		],
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := section(t, res, "custom"), `  - # customManagers[0].matchStrings[0]
    files: [/package.json/]
    fileFormat: json
    jsonata: '($each(dependencies, function($v, $k) { {"depName":$k, "currentValue": $v, "depType": "dependencies"}})) ~> $map(function($d) { $merge([$d, {"datasource": "npm"}]) })'
  - # customManagers[0].matchStrings[1]
    files: [/package.json/]
    fileFormat: json
    jsonata: '({ "depName": "pnpm", "currentValue": $substring(packageManager, 5),  "depType": "packageManager"}) ~> $map(function($d) { $merge([$d, {"datasource": "npm"}]) })'
  - # customManagers[1]
    files: [tools.yaml]
    fileFormat: yaml
    jsonata: '(tools.{"depName": repo, "currentValue": version}) ~> $map(function($d) { $merge([$d, {"datasource": "github-tags", "registryUrl": "https://github.com", "extractVersion": "^v(?<version>.+)$"}]) })'
  - # customManagers[6]
    files: [x]
    match: (?<currentValue>.)
    datasource: npm
    name: x`; got != want {
		t.Errorf("custom:\n%s\nwant:\n%s", got, want)
	}
	notes := noteLines(res)
	for _, n := range []string{
		"info customManagers[1].depTypeTemplate: no deps equivalent; ignored",
		`warn customManagers[2]: depNameTemplate "{{{packageName}}}" is a template; dropped`,
		"warn customManagers[3]: extractVersionTemplate has no deps equivalent; dropped",
		"warn customManagers[5]: currentValueTemplate has no deps equivalent; dropped",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
	var badFormat, regexFormat bool
	for _, n := range res.Notes {
		badFormat = badFormat || n.Path == "customManagers[4]" && strings.Contains(n.Msg, `fileFormat: must be one of json, yaml, toml, got "xml"`)
		regexFormat = regexFormat || strings.HasPrefix(n.Path, "customManagers[6].fileFormat")
	}
	if !badFormat || !regexFormat {
		t.Errorf("fileFormat notes missing:\n%s", strings.Join(notes, "\n"))
	}
	if got := section(t, res, "rules"); strings.Count(got, "managers: [custom]") != 2 {
		t.Errorf("rules:\n%s", got)
	}
}

// Renovate's literal templates override the query's fields (createDependency compiles the
// template instead of reading the field), while deps' entry fields are defaults.
func TestLoadCustomJSONataTemplatesOverride(t *testing.T) {
	res, err := load(t, `{customManagers: [{
		customType: 'jsonata', fileFormat: 'json', managerFilePatterns: ['x.json'],
		matchStrings: ['{"depName": n, "packageName": n, "currentValue": v, "datasource": "docker", "versioning": "loose", "registryUrl": "https://a.example.com"}'],
		datasourceTemplate: 'helm', packageNameTemplate: 'chart', versioningTemplate: 'semver', registryUrlTemplate: 'https://charts.example.com',
	}]}`)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Custom []config.Custom `yaml:"custom"`
	}
	if err := yaml.Unmarshal(res.YAML, &doc); err != nil || len(doc.Custom) != 1 {
		t.Fatalf("custom = %+v, %v:\n%s", doc.Custom, err, res.YAML)
	}
	q, err := gnata.Compile(doc.Custom[0].JSONata)
	if err != nil {
		t.Fatal(err)
	}
	r, err := q.EvalBytes(t.Context(), []byte(`{"n": "x", "v": "1.0.0"}`))
	if err != nil {
		t.Fatal(err)
	}
	got, _ := gnata.NormalizeValue(r).(map[string]any)
	for k, want := range map[string]string{"depName": "x", "packageName": "chart", "datasource": "helm", "versioning": "semver", "registryUrl": "https://charts.example.com"} {
		if got[k] != want {
			t.Errorf("%s = %v, want %q (query %s)", k, got[k], want, doc.Custom[0].JSONata)
		}
	}
}

// A managerFilePatterns entry with slashes but no regex end is a glob that matches nothing.
func TestLoadRootedGlob(t *testing.T) {
	res, err := load(t, `{customManagers: [{customType: 'regex', managerFilePatterns: ['/.github/workflows/**/*.yml'], matchStrings: ['V=(?<currentValue>\\S+)'], datasourceTemplate: 'docker', depNameTemplate: 'x'}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := section(t, res, "custom"); !strings.Contains(got, "files: [/.github/workflows/**/*.yml]") {
		t.Errorf("custom:\n%s", got)
	}
	want := `warn customManagers[0]: managerFilePatterns "/.github/workflows/**/*.yml" is a glob starting with / (not /regex/): it matches no repository path, in Renovate neither`
	if !slices.Contains(noteLines(res), want) {
		t.Errorf("notes = %q", noteLines(res))
	}
}

// fileMatch entries are regexes; the translation keeps them as /re/ patterns.
func TestLoadFileMatch(t *testing.T) {
	res, err := load(t, `{regexManagers: [{fileMatch: ['^Dockerfile$'], matchStrings: ['V=(?<currentValue>\\S+)'], datasourceTemplate: 'docker', depNameTemplate: 'x'}]}`)
	if err != nil {
		t.Fatal(err)
	}
	if got := section(t, res, "custom"); !strings.Contains(got, "files: [/^Dockerfile$/]") {
		t.Errorf("custom:\n%s", got)
	}
}

func TestLoadErrors(t *testing.T) {
	for _, tt := range []struct{ src, want string }{
		{`{enabled: false}`, "renovate.json: enabled: false disables Renovate for this repository"},
		{`[]`, "renovate.json: Renovate config must be a JSON object"},
		{`{a: }`, "renovate.json: 1:5: "},
		{`{extends: ['github>o/presets']}`, `extends[0] "github>o/presets": remote presets are not available here`},
	} {
		_, err := load(t, tt.src)
		var ce *config.Error
		if !errors.As(err, &ce) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want *config.Error containing %q", tt.src, err, tt.want)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name": "x"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(t.Context(), dir, "", nil)
	if err == nil || !strings.HasPrefix(err.Error(), "no Renovate config in "+dir+" (looked for renovate.json, renovate.jsonc") {
		t.Errorf("err = %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"renovate": {"branchPrefix": "pkg/"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := Load(t.Context(), dir, "", nil)
	if err != nil || !strings.Contains(string(res.YAML), "\nbranchPrefix: pkg/\n") {
		t.Errorf("package.json renovate key: %v", err)
	}
}

func TestIsConfigName(t *testing.T) {
	for name, want := range map[string]bool{"renovate.json5": true, ".renovaterc": true, "renovate.jsonc": true, "package.json": false, ".deps.yaml": false} {
		if got := IsConfigName(name); got != want {
			t.Errorf("IsConfigName(%q) = %v", name, got)
		}
	}
}

// Settings only Renovate's built-in presets set are reported as info, the repository's own as
// warnings.
func TestLoadBuiltinPresetNotes(t *testing.T) {
	res, err := load(t, `{extends: [':pinDevDependencies', ':ignoreModulesAndTests'], packageRules: [{matchDepTypes: ['devonly'], enabled: false}], cocoapods: {enabled: true}}`)
	if err != nil {
		t.Fatal(err)
	}
	notes := noteLines(res)
	for _, n := range []string{
		"info nuget.ignorePaths: not supported; ignored (from a built-in preset)",
		"warn packageRules[0]: depType devonly has no deps equivalent; dropped",
		"warn cocoapods: not supported; ignored",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
}

// Renovate's dashboard approval presets become deps' dashboardApproval.
func TestLoadDashboardApproval(t *testing.T) {
	res, err := load(t, `{extends: [':dependencyDashboardApproval'], dependencyDashboard: true}`)
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.YAML)
	if !strings.Contains(y, "\ndependencyDashboard: true\n") || !strings.Contains(y, "\ndashboardApproval: true\n") {
		t.Errorf("YAML:\n%s", y)
	}
	for _, n := range noteLines(res) {
		if strings.Contains(n, "dependencyDashboardApproval") {
			t.Errorf("unexpected note %q", n)
		}
	}
}

// baseBranchPatterns becomes baseBranches and matchBaseBranches rules[].baseBranches; globs,
// which deps does not support, are dropped with a note.
func TestLoadBaseBranches(t *testing.T) {
	res, err := load(t, `{baseBranchPatterns: ['main', '/^release\\//', 'dev-*'], packageRules: [
		{matchBaseBranches: ['main'], automerge: true},
		{matchBaseBranches: ['rel-*'], enabled: false}]}`)
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.YAML)
	for _, want := range []string{
		"\nbaseBranches: [main, /^release\\//]\n",
		"\n  - # packageRules[0]\n    baseBranches: [main]\n    automerge: true\n",
	} {
		if !strings.Contains(y, want) {
			t.Errorf("YAML lacks %q:\n%s", want, y)
		}
	}
	if _, body, _ := strings.Cut(y, "\nbranchPrefix:"); strings.Contains(body, "rel-*") || strings.Contains(body, "dev-*") {
		t.Errorf("glob rule kept:\n%s", y)
	}
	notes := noteLines(res)
	for _, want := range []string{
		`warn baseBranchPatterns: glob "dev-*" has no deps equivalent (names and /regex/ only); dropped`,
		"warn packageRules[1]: matches no base branch deps supports; rule dropped",
	} {
		if !slices.Contains(notes, want) {
			t.Errorf("notes %q lack %q", notes, want)
		}
	}
}

// draftPR, prCreation and abandonmentThreshold keep their meaning; prCreation approval and a
// null abandonmentThreshold (Renovate's default) have no deps setting.
func TestLoadPRCreation(t *testing.T) {
	for _, tt := range []struct {
		src, want string
		notes     []string
	}{
		{
			src:   `{draftPR: true, prCreation: 'not-pending', abandonmentThreshold: '2 years'}`,
			want:  "draftPR: true\nprCreation: not-pending\nabandonmentThreshold: 2 years\n",
			notes: []string{"warn prCreation: deps has no prNotPendingHours fallback: a branch without checks gets its PR only when ticked on the dashboard"},
		},
		{
			src:   `{prCreation: 'approval', abandonmentThreshold: null, prNotPendingHours: 4}`,
			notes: []string{"warn prCreation: approval is not supported (deps' dashboardApproval also holds back the branch); dropped", "info prNotPendingHours: no deps equivalent; ignored"},
		},
	} {
		res, err := load(t, tt.src)
		if err != nil {
			t.Fatal(err)
		}
		_, body, _ := strings.Cut(string(res.YAML), "\nignorePaths: ['**/node_modules/**', '**/bower_components/**']\n")
		body, _, _ = strings.Cut(body, "rules:\n")
		if body != tt.want {
			t.Errorf("%s: top level after ignorePaths:\n%s\nwant:\n%s", tt.src, body, tt.want)
		}
		if notes := noteLines(res); !slices.Equal(notes[2:], tt.notes) {
			t.Errorf("%s: notes = %q, want %q", tt.src, notes[2:], tt.notes)
		}
	}
}

// abandonments:recommended (in config:best-practices) becomes abandonmentThreshold rules.
func TestLoadAbandonments(t *testing.T) {
	res, err := load(t, `{extends: ['abandonments:recommended']}`)
	if err != nil {
		t.Fatal(err)
	}
	want := `  - # abandonments:recommended packageRules[0]
    names: ['**']
    abandonmentThreshold: 1 year
  - # abandonments:recommended packageRules[1]
    names: ['@types/*']
    datasources: [npm]
    abandonmentThreshold: "0"
  - # abandonments:recommended packageRules[2]
    names: [eslint-plugin-no-only-tests]
    datasources: [npm]
    abandonmentThreshold: 5 years
  - # abandonments:recommended packageRules[3]
    names: [lodash]
    datasources: [npm]
    abandonmentThreshold: 6 years` + defaultExclusions
	if got := section(t, res, "rules"); got != want {
		t.Errorf("rules:\n%s\nwant:\n%s", got, want)
	}
	for _, n := range noteLines(res) {
		if strings.Contains(n, "abandonments") {
			t.Errorf("unexpected note %q", n)
		}
	}
}

// Renovate's schedules are time windows, which deps evaluates itself: they are kept as written.
func TestLoadSchedules(t *testing.T) {
	res, err := load(t, `{schedule: ['* 0-3 * * 1', 'after 10pm   every weekday', '0 5 * * *'],
		packageRules: [{matchPackageNames: ['a'], schedule: ['at any time']}, {matchPackageNames: ['b'], schedule: ['0 4 * * *']}]}`)
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.YAML)
	if !strings.Contains(y, "\nschedule: ['* 0-3 * * 1', after 10pm every weekday]\n") || !strings.Contains(y, "schedule: [at any time]") {
		t.Errorf("YAML:\n%s", y)
	}
	if !slices.Equal(res.Windows, []string{"* 0-3 * * 1", "after 10pm every weekday"}) {
		t.Errorf("windows = %v", res.Windows)
	}
	notes := noteLines(res)
	for _, n := range []string{
		`warn schedule: schedule "0 5 * * *": a cron needs * as minutes in Renovate (it rejects this one); entry dropped`,
		`warn packageRules[1].schedule: no schedule entry could be translated; the schedule is omitted (a rule inherits the top-level schedule)`,
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
}

// A literal registryUrlTemplate becomes the entry's registryUrl; a templated one is ignored.
func TestLoadRegistryURLTemplate(t *testing.T) {
	res, err := load(t, `{customManagers: [
		{customType: 'regex', managerFilePatterns: ['/^versions\\.env$/'], matchStrings: ['GLAB=(?<currentValue>\\S+)'], datasourceTemplate: 'gitlab-packages', packageNameTemplate: 'gitlab-org/cli:glab', registryUrlTemplate: 'https://gitlab.example.com'},
		{customType: 'regex', managerFilePatterns: ['/^b$/'], matchStrings: ['V=(?<currentValue>\\S+)'], datasourceTemplate: 'gitlab-packages', packageNameTemplate: 'g/p:x', registryUrlTemplate: '{{{registry}}}'}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got := section(t, res, "custom")
	if !strings.Contains(got, "registryUrl: https://gitlab.example.com") || strings.Count(got, "registryUrl") != 1 || !strings.Contains(got, "datasource: gitlab-packages") {
		t.Errorf("custom:\n%s", got)
	}
	if !slices.ContainsFunc(noteLines(res), func(s string) bool { return strings.Contains(s, "customManagers[1].registryUrlTemplate") }) {
		t.Errorf("notes = %q", noteLines(res))
	}
}

// Templates that only render the match's own group ({{{datasource}}}, {{registryUrl}}) are
// what deps' regex mode does without them: the manager is kept.
func TestLoadGroupTemplates(t *testing.T) {
	res, err := load(t, `{customManagers: [{customType: 'regex', managerFilePatterns: ['/Dockerfile$/'],
		matchStrings: ['# renovate: datasource=(?<datasource>\\S+) depName=(?<depName>\\S+) registryUrl=(?<registryUrl>\\S+)\\s+ARG \\w+_VERSION=(?<currentValue>\\S+)'],
		datasourceTemplate: '{{{datasource}}}', depNameTemplate: '{{ depName }}', registryUrlTemplate: '{{{registryUrl}}}'}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got := section(t, res, "custom")
	if !strings.Contains(got, "(?<registryUrl>") || strings.Contains(got, "datasource:") || strings.Contains(got, "registryUrl:") || strings.Contains(got, "name:") {
		t.Errorf("custom:\n%s", got)
	}
	for _, s := range noteLines(res) {
		if strings.Contains(s, "customManagers[0]") {
			t.Errorf("note %q", s)
		}
	}
	for k, want := range map[string]bool{"{{{datasource}}}": true, "{{datasource}}": true, "{{ datasource }}": true, "{{{depName}}}": false, "{{datasource}": false, "apk": false} {
		if got := groupTemplate("datasourceTemplate", k); got != want {
			t.Errorf("groupTemplate(%q) = %v", k, got)
		}
	}
}

// The forge datasources keep their Renovate names; a literal registryUrlTemplate is their
// instance.
func TestLoadForgeDatasources(t *testing.T) {
	res, err := load(t, `{customManagers: [
		{customType: 'regex', managerFilePatterns: ['/^versions\\.env$/'], matchStrings: ['GLAB=(?<currentValue>\\S+)'], datasourceTemplate: 'gitlab-releases', packageNameTemplate: 'gitlab-org/cli', registryUrlTemplate: 'https://gitlab.example.com'},
		{customType: 'regex', managerFilePatterns: ['/^b$/'], matchStrings: ['V=(?<currentValue>\\S+)'], datasourceTemplate: 'forgejo-tags', packageNameTemplate: 'forgejo/runner'}]}`)
	if err != nil {
		t.Fatal(err)
	}
	got := section(t, res, "custom")
	if !strings.Contains(got, "registryUrl: https://gitlab.example.com") || !strings.Contains(got, "datasource: gitlab-releases") || !strings.Contains(got, "datasource: forgejo-tags") {
		t.Errorf("custom:\n%s", got)
	}
}
