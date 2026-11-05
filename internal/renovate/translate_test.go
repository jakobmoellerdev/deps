package renovate

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

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

const preCommitExclusion = `
  - # pre-commit is disabled by default in Renovate
    manager: pre-commit
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
rebaseWhen: conflicted
minimumReleaseAgeBehaviour: timestamp-required
ignorePaths: ['**/node_modules/**', '**/bower_components/**']
rules:` + preCommitExclusion + "\n"
	if got := string(res.YAML); got != want {
		t.Errorf("YAML:\n%s\nwant:\n%s", got, want)
	}
	if len(res.Crons) != 0 || len(res.Notes) != 2 {
		t.Errorf("crons %v, notes %v", res.Crons, noteLines(res))
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
    manager: github-actions
    group: GitHub Actions
  - # packageRules[0]
    manager: docker
    depTypes: [workflow, action]
    group: GitHub Actions` + preCommitExclusion,
		},
		{
			name: "dockerfile is docker with depType dockerfile",
			src:  `{packageRules: [{matchManagers: ['dockerfile'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    manager: docker
    depTypes: [dockerfile]
    ignore: true` + preCommitExclusion,
		},
		{
			name:  "unknown depTypes only drop the rule",
			src:   `{packageRules: [{matchManagers: ['dockerfile'], matchDepTypes: ['final', 'stage'], enabled: false}]}`,
			rules: strings.TrimPrefix(preCommitExclusion, "\n"),
			notes: []string{"warn packageRules[0]: depType final has no deps equivalent; dropped", "warn packageRules[0]: matches no deps depType; rule dropped"},
		},
		{
			name: "intersection of depTypes and the manager restriction, in matchManagers order",
			src:  `{packageRules: [{matchManagers: ['dockerfile', 'github-actions'], matchDepTypes: ['action'], pinDigests: true}]}`,
			rules: `  - # packageRules[0]
    manager: docker
    depTypes: [action]
    pin: digest
  - # packageRules[0]
    manager: github-actions
    depTypes: [action]
    pin: digest` + preCommitExclusion,
		},
		{
			name: "golang depType is go, categories select managers",
			src:  `{packageRules: [{matchCategories: ['golang'], matchDepTypes: ['golang'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    manager: gomod
    depTypes: [go]
    ignore: true` + preCommitExclusion,
		},
		{
			name: "pinDigest is pin; unknown update types are dropped",
			src:  `{packageRules: [{matchUpdateTypes: ['pinDigest', 'bump'], groupName: 'pins'}]}`,
			rules: `  - # packageRules[0]
    updateTypes: [pin]
    group: pins` + preCommitExclusion,
			notes: []string{"info packageRules[0]: update type bump has no deps equivalent; dropped"},
		},
		{
			name: "rangeStrategy with matchUpdateTypes is dropped",
			src:  `{packageRules: [{matchUpdateTypes: ['major'], rangeStrategy: 'bump', automerge: false}]}`,
			rules: `  - # packageRules[0]
    updateTypes: [major]
    automerge: false` + preCommitExclusion,
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
    assignees: [alice]` + preCommitExclusion,
		},
		{
			name:  "matchCurrentValue drops the rule",
			src:   `{packageRules: [{matchCurrentValue: '/^1/', matchPackageNames: ['a'], enabled: false}]}`,
			rules: strings.TrimPrefix(preCommitExclusion, "\n"),
			notes: []string{"warn packageRules[0]: matchCurrentValue has no deps equivalent; rule dropped"},
		},
		{
			name: "matchCurrentVersion and matchCurrentAge",
			src:  `{packageRules: [{matchCurrentAge: '> 1 year', matchCurrentVersion: '< 2', matchPackageNames: ['a'], matchUpdateTypes: ['major'], enabled: false}]}`,
			rules: `  - # packageRules[0]
    names: [a]
    currentVersion: < 2
    currentAge: '> 1 year'
    updateTypes: [major]
    ignore: true` + preCommitExclusion,
		},
		{
			name: "deprecated patterns, prefixes and exclusions",
			src: `{packageRules: [{matchPackagePatterns: ['^k8s\\.io/'], matchPackagePrefixes: ['sigs.k8s.io/'],
				excludePackageNames: ['k8s.io/kube-openapi'], matchPaths: ['charts/**'], stabilityDays: 3}]}`,
			rules: `  - # packageRules[0]
    names: ['!k8s.io/kube-openapi', /^k8s\.io//, /^sigs\.k8s\.io//]
    paths: [charts/**]
    minimumReleaseAge: 3 days` + preCommitExclusion,
		},
		{
			name:  "package and dep names together drop the rule",
			src:   `{packageRules: [{matchPackageNames: ['a'], matchDepNames: ['b'], enabled: false}]}`,
			rules: strings.TrimPrefix(preCommitExclusion, "\n"),
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
    manager: gomod
    ignore: true`,
		},
		{
			name: "enabledManagers excludes every other manager",
			src:  `{enabledManagers: ['npm', 'dockerfile', 'helmv3']}`,
			rules: `  - # enabledManagers
    manager: github-actions
    ignore: true
  - # enabledManagers
    manager: gomod
    ignore: true
  - # enabledManagers
    manager: docker
    depTypes: [compose, workflow, action, kubernetes, helm]
    ignore: true
  - # enabledManagers
    manager: helm
    depTypes: [helmfile, k0s, flux, argocd]
    ignore: true
` + strings.Join(func() []string {
				var out []string
				for _, m := range config.Managers {
					if !slices.Contains([]string{"github-actions", "gomod", "docker", "npm", "helm", "custom"}, m) {
						out = append(out, "  - # enabledManagers\n    manager: "+m+"\n    ignore: true")
					}
				}
				return out
			}(), "\n"),
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
		pinDigests: true, rangeStrategy: 'pin', 'git-submodules': {enabled: true},
	}`)
	if err != nil {
		t.Fatal(err)
	}
	y := string(res.YAML)
	_, body, _ := strings.Cut(y, "\n\nbranchPrefix:")
	const want = ` bot/
prConcurrentLimit: 3
pin: digest
security: false
monorepos: false
rebaseWhen: behind-base-branch
minimumReleaseAge: 7 days
minimumReleaseAgeBehaviour: timestamp-required
schedule: [0 0 * * 1]
ignorePaths: [/foo/, '**/bar/**', /^x/]
automerge: true
automergeStrategy: merge
labels: [deps]
lockFileMaintenance:
  enabled: true
  schedule: [0 0 * * 1]
rules:` + preCommitExclusion + "\n"
	if body != want {
		t.Errorf("YAML:\n%s\nwant body:\n%s", y, want)
	}
	if !slices.Equal(res.Crons, []string{"0 0 * * 1"}) {
		t.Errorf("crons = %v", res.Crons)
	}
	notes := noteLines(res)
	for _, n := range []string{
		"warn prHourlyLimit: must be a non-negative integer, got 1.5; dropped",
		"warn ignorePaths[2]: negation \"!keep\" is not supported in ignorePaths; dropped",
		"warn automergeType: deps automerges through the PR; dropped",
		"warn schedule: unsupported schedule \"every 2 weeks\"; entry dropped",
		"warn timezone: timezone \"Europe/Berlin\" ignored: deps compares schedules with workflow crons, which are UTC",
		"info lockFileMaintenance.automerge: no deps equivalent; ignored",
		"warn rangeStrategy: rangeStrategy pin is not supported; dropped",
		"warn git-submodules: not supported; ignored",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
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
			{customType: 'jsonata', managerFilePatterns: ['x']},
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
		"warn customManagers[2]: datasourceTemplate \"{{{datasource}}}\" is a template; dropped",
		"warn customManagers[3]: customType jsonata is not supported; dropped",
		"warn packageRules[0]: datasource custom.envtest has no deps equivalent; dropped",
		"warn packageRules[0]: matches no deps datasource; rule dropped",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
	var dropped bool
	for _, n := range res.Notes {
		dropped = dropped || (n.Path == "customManagers[0]" && strings.HasPrefix(n.Msg, "dropped: datasource:") && strings.Contains(n.Msg, `got "custom.envtest"`))
	}
	if !dropped {
		t.Errorf("customManagers[0] (envtest) not dropped:\n%s", strings.Join(notes, "\n"))
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
	res, err := load(t, `{extends: [':pinDevDependencies', ':ignoreModulesAndTests'], packageRules: [{matchDepTypes: ['dev'], enabled: false}], 'git-submodules': {enabled: true}}`)
	if err != nil {
		t.Fatal(err)
	}
	notes := noteLines(res)
	for _, n := range []string{
		"info :pinDevDependencies packageRules[0]: depType dev has no deps equivalent; dropped (from a built-in preset)",
		"info nuget: not supported; ignored (from a built-in preset)",
		"warn packageRules[0]: depType dev has no deps equivalent; dropped",
		"warn git-submodules: not supported; ignored",
	} {
		if !slices.Contains(notes, n) {
			t.Errorf("missing note %q in\n%s", n, strings.Join(notes, "\n"))
		}
	}
}
