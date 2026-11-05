// Package config loads and validates .deps.yaml.
package config

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bmatcuk/doublestar/v4"
	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Pin modes.
const (
	PinKeep   = "keep"
	PinDigest = "digest"
)

// Range strategies of rangeStrategy (Renovate's names), for npm, cargo and Python dependency ranges,
// terraform constraints and Gradle dynamic versions. Widen and pin need a manager that rewrites
// whole ranges (npm, cargo, Python); the others resolve them to replace.
const (
	RangeAuto           = "auto"
	RangeBump           = "bump"
	RangeReplace        = "replace"
	RangeUpdateLockfile = "update-lockfile"
	RangeWiden          = dep.RangeWiden
	RangePin            = dep.RangePin
	RangeInRangeOnly    = "in-range-only"
)

// RangeStrategies are the accepted rangeStrategy values.
var RangeStrategies = []string{RangeAuto, RangeBump, RangeReplace, RangeUpdateLockfile, RangeWiden, RangePin, RangeInRangeOnly}

// Rebase modes of rebaseWhen.
const (
	RebaseConflicted = "conflicted"
	RebaseBehindBase = "behind-base-branch"
	RebaseNever      = "never"
)

// RebaseWhens are the accepted rebaseWhen values.
var RebaseWhens = []string{RebaseConflicted, RebaseBehindBase, RebaseNever}

// Values of platformCommit.
const (
	PlatformCommitAuto     = "auto"
	PlatformCommitEnabled  = "enabled"
	PlatformCommitDisabled = "disabled"
)

// PlatformCommits are the accepted platformCommit values.
var PlatformCommits = []string{PlatformCommitAuto, PlatformCommitEnabled, PlatformCommitDisabled}

// Automerge strategies of automergeStrategy; auto uses the repository's default merge style.
const (
	StrategyAuto   = "auto"
	StrategyMerge  = "merge"
	StrategySquash = "squash"
	StrategyRebase = "rebase"
)

// AutomergeStrategies are the accepted automergeStrategy values.
var AutomergeStrategies = []string{StrategyAuto, StrategyMerge, StrategySquash, StrategyRebase}

// minimumReleaseAgeBehaviour values (Renovate's names): whether a version with an unknown
// release time passes minimumReleaseAge.
const (
	TimestampOptional = "timestamp-optional"
	TimestampRequired = "timestamp-required"
)

// MinimumReleaseAgeBehaviours are the accepted minimumReleaseAgeBehaviour values.
var MinimumReleaseAgeBehaviours = []string{TimestampOptional, TimestampRequired}

// Automerge types of automergeType: pr requests the platform's auto-merge of the PR; branch
// pushes the update branch without a PR and fast-forwards the base branch to it once its
// checks pass.
const (
	AutomergeTypePR     = "pr"
	AutomergeTypeBranch = "branch"
)

// AutomergeTypes are the accepted automergeType values.
var AutomergeTypes = []string{AutomergeTypePR, AutomergeTypeBranch}

// PR creation modes of prCreation: immediate opens the PR right after the branch is pushed;
// not-pending once the branch's checks finished (passed or failed); status-success once they
// all passed. Until then the branch waits without a PR.
const (
	PRCreationImmediate     = "immediate"
	PRCreationNotPending    = "not-pending"
	PRCreationStatusSuccess = "status-success"
)

// PRCreations are the accepted prCreation values, from the least to the most waiting.
var PRCreations = []string{PRCreationImmediate, PRCreationNotPending, PRCreationStatusSuccess}

// DefaultDashboardTitle is the dependency dashboard issue title when none is configured.
const DefaultDashboardTitle = "Dependency Dashboard"

// Default PR labels of rebaseLabel and stopUpdatingLabel (Renovate's defaults).
const (
	DefaultRebaseLabel       = "rebase"
	DefaultStopUpdatingLabel = "stop-updating"
)

// Known values for validation.
var (
	Managers = []string{"github-actions", "gomod", "docker", "npm", "helm", "kustomize", "pre-commit", "gitlabci", "nvm", "nodenv", "asdf", "mise", "cargo", "pip_requirements", "pep621", "poetry", "pipenv", "conda", "terraform", "terraform-version", "tflint-plugin", "terragrunt", "maven", "gradle", "gradle-wrapper", "maven-wrapper", "swift", "bundler", "ruby-version", "git-submodules", "rust-toolchain", "devcontainer", "bitbucket-pipelines", "woodpecker", "droneci", "circleci", "azure-pipelines", "vcpkg", "elm", "composer", "julia", "bazel-module", "bazelisk", "custom"}
	DepTypes = []string{"direct", "indirect", "go", "toolchain", "action", "dockerfile", "compose", "workflow", "kubernetes", "helm", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "packageManager", "setup", "engines", "volta", "chart", "helmfile", "k0s", "flux", "argocd", "sveltos", "image", "helmChart", "resource", "repository", "service", "include", "component", "node", "tool", "annotation", "regex", "dev-dependencies", "build-dependencies", "workspace.dependencies", "patch", "replace", "requirements", "project.dependencies", "project.optional-dependencies", "dependency-groups", "tool.uv.dev-dependencies", "tool.pdm.dev-dependencies", "group", "packages", "dev-packages", "category", "pip", "required_provider", "required_version", "module", "provider", "helm_release", "tfe_workspace", "docker_image", "docker_container", "docker_service", "docker_registry_image", "kubernetes_pod", "kubernetes_pod_v1", "kubernetes_deployment", "kubernetes_deployment_v1", "kubernetes_daemonset", "kubernetes_daemon_set_v1", "kubernetes_stateful_set", "kubernetes_stateful_set_v1", "kubernetes_job", "kubernetes_job_v1", "kubernetes_cron_job", "kubernetes_cron_job_v1", "kubernetes_replication_controller", "kubernetes_replication_controller_v1", "terraform-version", "opentofu-version", "plugin", "github", "gitTags", "terragrunt", "compile", "provided", "runtime", "test", "system", "import", "parent", "build", "wrapper", "maven", "ruby", "development", "submodule", "builtin-baseline", "default-registry", "registry", "overrides", "feature", "feature-version", "pipe", "orb", "container", "task", "test-direct", "test-indirect", "test-dependencies", "elm-version", "require", "require-dev", "deps", "weakdeps", "extras", "julia", "bazel_dep", "single_version_override", "multiple_version_override", "git_override", "archive_override", "local_path_override", "git_repository", "new_git_repository", "oci_pull", "rules_img_pull", "maven_install", "crate_spec", "bazel"}
	// Datasources are the deps datasource names (dep.Dep.Datasource); custom datasources add
	// custom.<name> (CustomDatasourcePrefix).
	Datasources = []string{"git-tags", "git-refs", "github-releases", "go", "go-toolchain", "docker", "npm", "node-version", "helm", "crate", "pypi", "conda", "terraform-provider", "terraform-module", "maven", "gradle-version", "rubygems", "ruby-version", "rust-version", "orb", "azure-pipelines-tasks", "vcpkg", "elm-package", "packagist", "julia-registry", "bazel"}
	// DatasourceAliases maps Renovate datasource names to deps datasource names.
	DatasourceAliases = map[string]string{
		"golang-version":             "go-toolchain",
		"github-tags":                "git-tags",
		"github-release-attachments": "github-releases",
		"node":                       "node-version",
		"bitbucket-tags":             "git-tags",
	}
	// DatasourceNames are the accepted datasource names of custom[].datasource and
	// rules[].datasources: Datasources followed by the DatasourceAliases keys (plus
	// custom.<name> of customDatasources, see Config.KnownDatasource).
	DatasourceNames = []string{"git-tags", "git-refs", "github-releases", "go", "go-toolchain", "docker", "npm", "node-version", "helm", "crate", "pypi", "conda", "terraform-provider", "terraform-module", "maven", "gradle-version", "rubygems", "ruby-version", "rust-version", "orb", "azure-pipelines-tasks", "vcpkg", "elm-package", "packagist", "julia-registry", "bazel", "golang-version", "github-tags", "github-release-attachments", "node", "bitbucket-tags"}
	// Versionings are the versioning scheme names; rules[].versioning and custom[].versioning
	// also accept regex:<RE2> (versioning.RegexPrefix).
	Versionings = versioning.Names
	// CommitTypes are the accepted conventional-commit types of rules[].commitType.
	CommitTypes = []string{"fix", "chore", "build", "ci", "docs", "feat", "perf", "refactor", "revert", "style", "test"}
)

// UpdateTypeSelectors are the accepted updateTypes values: dep.UpdateTypes and replacement.
var UpdateTypeSelectors = append(slices.Clone(dep.UpdateTypes), dep.Replacement)

// updateTypeList renders dep.UpdateTypes for validation messages.
var updateTypeList = func() string {
	s := make([]string, len(dep.UpdateTypes))
	for i, t := range dep.UpdateTypes {
		s[i] = string(t)
	}
	return strings.Join(s, ", ")
}()

// CanonicalDatasource maps a Renovate alias to its deps datasource name; other names are
// returned unchanged.
func CanonicalDatasource(name string) string {
	if a, ok := DatasourceAliases[name]; ok {
		return a
	}
	return name
}

// DefaultBranchPrefix is the branch prefix used when branchPrefix is not set.
const DefaultBranchPrefix = "deps/"

// DefaultIgnorePaths are used when the config file has no ignorePaths key: dependency
// trees, examples and tests are not scanned.
var DefaultIgnorePaths = []string{
	"**/node_modules/**", "**/bower_components/**", "**/vendor/**", "**/examples/**",
	"**/__tests__/**", "**/test/**", "**/tests/**", "**/__fixtures__/**",
}

// Config is the parsed .deps.yaml.
type Config struct {
	Version       int      `yaml:"version" jsonschema:"enum=1,default=1" jsonschema_description:"Config format version; must be 1 if set."`
	Pin           string   `yaml:"pin" jsonschema:"enum=keep,enum=digest,default=keep" jsonschema_description:"Default pin mode for actions and docker: keep the current style, or pin to the digest with the version in a comment."`
	RangeStrategy string   `yaml:"rangeStrategy" jsonschema:"enum=auto,enum=bump,enum=replace,enum=update-lockfile,enum=widen,enum=pin,enum=in-range-only,default=auto" jsonschema_description:"How npm, cargo, Python, bundler and composer dependency ranges, Swift package requirements, Elm package constraints, Julia compat entries, terraform version constraints and Gradle dynamic versions (1.+, [1.0,2.0)) are updated: update-lockfile moves only the lockfile (package-lock.json, Cargo.lock, uv.lock, poetry.lock, pdm.lock, Pipfile.lock, Gemfile.lock, composer.lock, Manifest.toml, Package.resolved, .terraform.lock.hcl, gradle.lockfile) while the range admits the new version (rewriting the range otherwise, 1.+ -> 2.+), replace leaves admitted versions alone and rewrites the range otherwise (~> 5.0 -> ~> 6.2), bump always raises the range (^1.2.3 -> ^1.4.0, >=1.2,<2 -> >=1.5.0,<2, ~> 5.0 -> ~> 5.46; Gradle dynamic versions and ranges without a floor keep update-lockfile), in-range-only raises the range like bump but only to versions it admits, widen leaves admitted versions alone and widens the range to admit others (^1.2.3 -> ^1.2.3 || ^2.0.0, >=1.2,<2 -> >=1.2,<3, cargo 1.2 -> >=1.2, <3, Ruby \"~> 7.1\" -> \">= 7.1\", \"< 9\"), pin writes the exact version (^1.2.3 -> 1.4.0, cargo =1.4.0, Python ==1.4.0, Swift exact: 1.4.0 or .exact(1.4.0), Ruby \"= 1.4.0\"): a pin update to the locked or newest admitted version, then each newer one (widen and pin are replace for terraform, terragrunt and Gradle; Swift from: is replaced under widen); auto is Renovate's default per manager: update-lockfile, widen for npm peerDependencies, || ranges, Elm package constraints and julia projects without a Manifest.toml, replace for terraform, terragrunt and gemspec dependencies."`
	Schedule      []string `yaml:"schedule" jsonschema_description:"When new update PRs are created: cron triggers of the workflow running deps (minute fixed, e.g. '0 5 * * 1'), matched against the cron that started the run, and time windows evaluated against the run time in timezone: Renovate's text ('before 6am on monday', 'every weekend') or crons with minute * ('* 0-5 * * 1'). Empty means every run."`
	Timezone      string   `yaml:"timezone" jsonschema_description:"IANA time zone (e.g. Europe/Berlin) of the schedule time windows; default UTC. Cron triggers are unaffected."`
	Security      *bool    `yaml:"security" jsonschema:"default=true" jsonschema_description:"Look up OSV advisories and propose security updates that bypass allow, updates and schedule."`
	BranchPrefix  string   `yaml:"branchPrefix" jsonschema:"default=deps/" jsonschema_description:"Prefix of update branches (<prefix><manager>/<name>, <prefix>group/<group>); must end with '/'. Only branches and PRs under it are managed."`
	BaseBranches  []string `yaml:"baseBranches" jsonschema_description:"Base branches deps run proposes updates against, each resolved from its own tip: branch names, $default (the repository's default branch) and RE2 regexes '/<RE2>/' (flag i allowed) matching branches on origin. With more than one entry, or a regex, update branches carry their base (<prefix><base>/<manager>/<name>) and PRs, cleanup and the dashboard are kept per base. Empty means the default branch; run --base overrides it."`
	RebaseWhen    string   `yaml:"rebaseWhen" jsonschema:"enum=conflicted,enum=behind-base-branch,enum=never,default=conflicted" jsonschema_description:"When to rebuild an open update PR whose content is unchanged: conflicted (only when not mergeable), behind-base-branch (also when the base branch moved), never (only when its content changes)."`
	Monorepos     *bool    `yaml:"monorepos" jsonschema:"default=true" jsonschema_description:"Group deps released from one source repository (built-in list, e.g. kubernetes, opentelemetry-go, babel) into one PR, and the Ruby version (the Gemfile ruby directive, .ruby-version, asdf and mise ruby) into the group ruby; rules can override the group."`
	Replacements  *bool    `yaml:"replacements" jsonschema:"default=true" jsonschema_description:"Replace packages that moved or were renamed upstream with a replacement update: the built-in list generated from Renovate's replacements presets (lib/data/replacements.json, the moves deps can write: e.g. k8s.gcr.io/* images to registry.k8s.io/*, npm jade to pug 2.0.0, google-github-actions/release-please-action to googleapis/release-please-action). Rules' replacementName overrides an entry; updateTypes: [replacement] with ignore: true switches replacements off per dep."`
	GitAuthor     string   `yaml:"gitAuthor" jsonschema:"default=deps[bot] <deps-bot@noreply.invalid>" jsonschema_description:"Author and committer of deps commits as 'Name <email>' (git identity). Overridden by $DEPS_GIT_AUTHOR and the --author-name/--author-email flags. With a signing key (run --signing-key), use a verified email of the key's owner on the forge, or the signature does not count as verified. Ignored for platform commits (platformCommit), whose author is the token's account; branches whose tip it authored still count as deps' own."`
	// PlatformCommit creates commits through the GitHub API (Renovate's platformCommit).
	PlatformCommit string `yaml:"platformCommit" jsonschema:"enum=auto,enum=enabled,enum=disabled,default=auto" jsonschema_description:"Create commits and move update branches through the GitHub API instead of git, so that GitHub signs them and shows them as verified, authored by the GitHub App (e.g. ocmbot[bot]). auto enables it on GitHub when the token is a GitHub App installation token (ghs_..., also GITHUB_TOKEN in Actions) and no signing key is set; enabled requires GitHub and no signing key; disabled always uses git. GitHub signs API commits only for App tokens; with a personal access token they stay unsigned. Overridden by $DEPS_PLATFORM_COMMIT."`
	// GitIgnoredAuthors are emails whose commits on an update branch count as the bot's own.
	GitIgnoredAuthors []string  `yaml:"gitIgnoredAuthors" jsonschema_description:"Commit author emails (case-insensitive) besides the bot's own whose commits on an update branch do not count as modifications: such branches are still updated and rebased."`
	PRConcurrentLimit int       `yaml:"prConcurrentLimit" jsonschema:"minimum=0,default=0" jsonschema_description:"Maximum number of open managed PRs; new PRs are only created below it. 0 means unlimited."`
	PRHourlyLimit     int       `yaml:"prHourlyLimit" jsonschema:"minimum=0,default=0" jsonschema_description:"Maximum number of managed PRs created per hour, counting existing PRs by creation time. 0 means unlimited."`
	Actions           Actions   `yaml:"actions" jsonschema_description:"github-actions manager settings."`
	Terraform         Terraform `yaml:"terraform" jsonschema_description:"terraform manager settings."`
	// GomodUpdateImportPaths enables major module path updates (Renovate's
	// postUpdateOptions: [gomodUpdateImportPaths]).
	GomodUpdateImportPaths *bool    `yaml:"gomodUpdateImportPaths" jsonschema:"default=true" jsonschema_description:"Propose major updates of Go modules to their next major module path (github.com/x/y/v2 → /v3, also /v2 for v0/v1 modules), rewriting the require and the import paths in the module's .go files before go mod tidy. The new major's API may differ, so the build can break. false proposes no module path changes (go mod tidy would undo a path change without rewritten imports)."`
	GitLabCI               GitLabCI `yaml:"gitlabci" jsonschema_description:"gitlabci manager settings."`
	// MinimumReleaseAge is a string, not a duration, so "28 days" style values are accepted.
	MinimumReleaseAge string `yaml:"minimumReleaseAge" jsonschema_description:"Minimum age of a new version before it is proposed, e.g. '3 days', '12h', '90m' or a Go duration; '0' disables. Versions with an unknown release time are allowed unless minimumReleaseAgeBehaviour is timestamp-required; digest refresh, pin and security updates are exempt, except Go pseudo-version digest updates, which have a commit time."`
	// MinimumReleaseAgeBehaviour decides whether an unknown release time passes
	// minimumReleaseAge.
	MinimumReleaseAgeBehaviour string `yaml:"minimumReleaseAgeBehaviour" jsonschema:"enum=timestamp-optional,enum=timestamp-required,default=timestamp-optional" jsonschema_description:"How minimumReleaseAge treats a version whose release time is unknown (the datasource has none, or the lookup failed): timestamp-optional proposes it, timestamp-required holds it back like a too-new version (reported as 'release time unknown'). Renovate's default is timestamp-required."`
	// AbandonmentThreshold flags deps whose newest release is older than it (Renovate's
	// abandonmentThreshold); a string like MinimumReleaseAge.
	AbandonmentThreshold string   `yaml:"abandonmentThreshold" jsonschema_description:"Age of a dependency's newest release after which the dependency dashboard and its PRs flag it as abandoned, e.g. '1 year' or '18 months' (units of minimumReleaseAge, plus months of 30 days and years of 365 days); empty or '0' turns the check off. It costs at most one release time lookup per dep (npm: none when its modified time is old enough)."`
	IgnorePaths          []string `yaml:"ignorePaths" jsonschema_description:"Doublestar globs on slash paths relative to --dir; matching files and directories are not scanned. When the key is absent, the default list (dependency trees, examples and tests) applies; any list (also []) replaces it."`
	// Separate* split the candidates of one dep into PR buckets (Renovate's options of the
	// same names); rules override them.
	SeparateMajorMinor    *bool  `yaml:"separateMajorMinor" jsonschema:"default=true" jsonschema_description:"Propose the newest major update in its own PR (branch suffix -major), apart from the newest minor/patch update; false proposes only the newest update."`
	SeparateMultipleMajor *bool  `yaml:"separateMultipleMajor" jsonschema:"default=false" jsonschema_description:"With separateMajorMinor: one PR per newer major version (branch suffix -v<N>, newest release of each major) instead of one for the newest major."`
	SeparateMinorPatch    *bool  `yaml:"separateMinorPatch" jsonschema:"default=false" jsonschema_description:"With separateMajorMinor: separate PRs for the newest minor (branch suffix -minor) and the newest patch of the current minor (-patch)."`
	UpdateNotScheduled    *bool  `yaml:"updateNotScheduled" jsonschema:"default=true" jsonschema_description:"Keep updating and rebasing open update PRs outside their schedule; only new PRs wait for the schedule window. false leaves out-of-schedule branches untouched."`
	Automerge             bool   `yaml:"automerge" jsonschema:"default=false" jsonschema_description:"Request platform auto-merge for update PRs: the forge merges them once required checks pass. Rules override it, e.g. for minor and patch updates only."`
	AutomergeStrategy     string `yaml:"automergeStrategy" jsonschema:"enum=auto,enum=merge,enum=squash,enum=rebase,default=auto" jsonschema_description:"Merge method of automerged PRs: auto (the repository's default merge style), merge, squash or rebase."`
	AutomergeType         string `yaml:"automergeType" jsonschema:"enum=pr,enum=branch,default=pr" jsonschema_description:"How updates are automerged: pr (platform auto-merge of the PR) or branch (push the branch without a PR and fast-forward the base branch to it once its checks pass; a PR is raised only when they fail)."`
	DraftPR               bool   `yaml:"draftPR" jsonschema:"default=false" jsonschema_description:"Open new update PRs as drafts (GitHub draft PRs; on Gitea and Forgejo the title starts with WIP:, on GitLab with Draft:). PRs that automerge are never drafts. Rules override it."`
	PRCreation            string `yaml:"prCreation" jsonschema:"enum=immediate,enum=not-pending,enum=status-success,default=immediate" jsonschema_description:"When the PR of a new update branch is opened: immediate (right after the push), not-pending (on a later run, once the branch's checks finished, passed or failed) or status-success (once they all passed). Until then the branch waits without a PR, listed on the dependency dashboard. Rules override it."`
	// Labels, Reviewers and Assignees are set on new update PRs; rules override them.
	Labels    []string `yaml:"labels" jsonschema_description:"Labels added to new update PRs (by name; on Gitea and Forgejo they must exist in the repository or its organization). Rules replace them (labels) or add to them (addLabels)."`
	Reviewers []string `yaml:"reviewers" jsonschema_description:"Reviewers requested on new update PRs: user names, or team:<slug> for a team of the repository's organization (not on GitLab). Rules replace them."`
	Assignees []string `yaml:"assignees" jsonschema_description:"Users assigned to new update PRs. Rules replace them."`
	// PRTitle, PRHeader and PRFooter are Go templates over prtemplate.Data; rules override them.
	PRTitle  string `yaml:"prTitle" jsonschema_description:"Go text/template of PR titles and commit subjects over the fields .Type, .Scope, .Name, .Group, .From, .To, .UpdateType, .Count and .IsSecurity, e.g. '{{.Type}}({{.Scope}}): bump {{.Name}} to {{.To}}'; empty means the built-in titles. Security PRs keep the ' [security]' suffix. Rules override it."`
	PRHeader string `yaml:"prHeader" jsonschema_description:"Go text/template (the fields of prTitle) rendered at the top of update PR bodies; empty means none. Rules override it."`
	PRFooter string `yaml:"prFooter" jsonschema_description:"Go text/template (the fields of prTitle) rendered at the end of update PR bodies, above the rebase checkbox; empty means none. Rules override it."`
	// RebaseLabel and StopUpdatingLabel are PR labels people set (Renovate's options of the
	// same names).
	RebaseLabel       string `yaml:"rebaseLabel" jsonschema:"default=rebase" jsonschema_description:"Label that makes the next run rebuild an update PR's branch on the current base, like ticking its rebase checkbox (bypassing the schedule and discarding commits by others); deps removes the label afterwards."`
	StopUpdatingLabel string `yaml:"stopUpdatingLabel" jsonschema:"default=stop-updating" jsonschema_description:"Label that makes deps leave an update PR alone: its branch is no longer pushed or rebased, and the PR is not closed as obsolete. Remove the label to hand the PR back to deps."`
	// DependencyDashboard* configure the dashboard issue (Renovate's :dependencyDashboard).
	DependencyDashboard       bool     `yaml:"dependencyDashboard" jsonschema:"default=false" jsonschema_description:"Maintain a dependency dashboard issue listing pending, blocked and open updates, errors and detected dependencies; its checkboxes make the next run create, recreate or rebase PRs. false leaves an existing dashboard untouched."`
	DependencyDashboardTitle  string   `yaml:"dependencyDashboardTitle" jsonschema:"default=Dependency Dashboard" jsonschema_description:"Title of the dependency dashboard issue; the issue is found by this exact title."`
	DependencyDashboardLabels []string `yaml:"dependencyDashboardLabels" jsonschema_description:"Labels set when the dashboard issue is created (by name; on Gitea and Forgejo they must exist in the repository or its organization)."`
	DashboardApproval         bool     `yaml:"dashboardApproval" jsonschema:"default=false" jsonschema_description:"Create no new PR (or automerge branch) for an update until it is approved on the dependency dashboard: it is listed under Pending approval, and ticking its checkbox makes the next run create it. Security updates need no approval. Rules override it; requires dependencyDashboard."`
	// Extends names configs of other repositories this file builds on.
	Extends []string `yaml:"extends" jsonschema_description:"Configs this file builds on, each '<repo>[//<path>][#<ref>]': owner/name of a repository on the same forge (or a git URL: https://, ssh://, file://, user@host:path), the file in it (default .deps.yaml) and a branch, tag or commit (default: its default branch). Their rules come first, so this file's rules override them; this file's other keys replace theirs, custom managers are appended and customDatasources merged by name. Later entries override earlier ones; extended files may extend others, cycles are errors."`
	Rules   []Rule   `yaml:"rules" jsonschema_description:"Policy rules, evaluated top to bottom; every set field of a matching rule overrides earlier ones."`
	Custom  []Custom `yaml:"custom" jsonschema_description:"Custom managers: '# deps: <datasource> <name>' annotations, or RE2 regex managers when match is set."`
	// CustomDatasources are referenced as datasource custom.<name>.
	CustomDatasources map[string]CustomDatasource `yaml:"customDatasources" jsonschema_description:"User-defined datasources by name; deps (custom[] entries, annotations, Renovate markers) refer to them as datasource custom.<name>. The version list is fetched once per run."`
	// LockFileMaintenance configures Renovate's lockFileMaintenance: one PR refreshing every
	// lockfile.
	LockFileMaintenance LockFileMaintenance `yaml:"lockFileMaintenance" jsonschema_description:"Lock file maintenance: one PR (branch <branchPrefix>lock-file-maintenance) that refreshes every lockfile deps maintains (Cargo.lock, package-lock.json, pnpm-lock.yaml, yarn.lock, uv.lock, poetry.lock, pdm.lock, Pipfile.lock, Gemfile.lock, composer.lock, MODULE.bazel.lock, Gradle lockfiles, .terraform.lock.hcl, pip-compile and uv pip compile output, and the indirect requirements of go.mod with go.sum) to the newest versions the requirements admit, without changing manifests."`

	path      string         // source path for error messages
	layers    []layer        // the files of the extends chain, the local one last; nil without extends
	schedule  *schedule.Set  // parsed Schedule
	loc       *time.Location // parsed Timezone; UTC when unset
	templates prtemplate.Set // parsed PRTitle, PRHeader and PRFooter
	// lfmSchedule is the parsed LockFileMaintenance.Schedule, or schedule when it is nil.
	lfmSchedule *schedule.Set
	minAge      time.Duration // parsed MinimumReleaseAge
	abandon     time.Duration // parsed AbandonmentThreshold
	// baseBranches are the parsed BaseBranches; baseBranch is the base branch rules with
	// baseBranches match against (ForBaseBranch), "" = none.
	baseBranches []branchPattern
	baseBranch   string
}

// SecurityEnabled reports whether OSV lookups are on (default true).
func (c *Config) SecurityEnabled() bool { return c.Security == nil || *c.Security }

// MonoreposEnabled reports whether the built-in monorepo groups apply (default true).
func (c *Config) MonoreposEnabled() bool { return c.Monorepos == nil || *c.Monorepos }

// ReplacementsEnabled reports whether the built-in replacements apply (default true).
func (c *Config) ReplacementsEnabled() bool { return c.Replacements == nil || *c.Replacements }

// GomodUpdateImportPathsEnabled reports whether major module path updates are proposed
// (default true).
func (c *Config) GomodUpdateImportPathsEnabled() bool {
	return c.GomodUpdateImportPaths == nil || *c.GomodUpdateImportPaths
}

// UpdateNotScheduledEnabled reports whether open PRs are updated outside their schedule
// (default true).
func (c *Config) UpdateNotScheduledEnabled() bool {
	return c.UpdateNotScheduled == nil || *c.UpdateNotScheduled
}

// DashboardTitle returns DependencyDashboardTitle, or DefaultDashboardTitle when unset.
func (c *Config) DashboardTitle() string {
	return cmp.Or(c.DependencyDashboardTitle, DefaultDashboardTitle)
}

// RebaseLabelName returns RebaseLabel, or DefaultRebaseLabel when unset.
func (c *Config) RebaseLabelName() string { return cmp.Or(c.RebaseLabel, DefaultRebaseLabel) }

// StopUpdatingLabelName returns StopUpdatingLabel, or DefaultStopUpdatingLabel when unset.
func (c *Config) StopUpdatingLabelName() string {
	return cmp.Or(c.StopUpdatingLabel, DefaultStopUpdatingLabel)
}

// validateAutomerge checks automergeStrategy and automergeType of the top level (f "") or
// a rule (f "rules[i].").
func validateAutomerge(f, strategy, typ string, add addFunc) {
	if strategy != "" && !slices.Contains(AutomergeStrategies, strategy) {
		add(f+"automergeStrategy", "must be one of %s, got %q", list(AutomergeStrategies), strategy)
	}
	if typ != "" && !slices.Contains(AutomergeTypes, typ) {
		add(f+"automergeType", "must be one of %s, got %q", list(AutomergeTypes), typ)
	}
}

// validatePRCreation checks prCreation of the top level (f "") or a rule (f "rules[i].").
func validatePRCreation(f, v string, add addFunc) {
	if v != "" && !slices.Contains(PRCreations, v) {
		add(f+"prCreation", "must be one of %s, got %q", list(PRCreations), v)
	}
}

// TeamPrefix marks a team reviewer: team:<slug>.
const TeamPrefix = "team:"

// userRe matches forge user and team names.
var userRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// validatePRMeta checks labels, addLabels, reviewers and assignees of the top level (f "")
// or a rule (f "rules[i].").
func validatePRMeta(f string, labels, addLabels, reviewers, assignees []string, add addFunc) {
	for _, key := range []struct {
		name string
		ls   []string
	}{{"labels", labels}, {"addLabels", addLabels}} {
		for i, l := range key.ls {
			if strings.TrimSpace(l) == "" || strings.Contains(l, ",") {
				add(fmt.Sprintf("%s%s[%d]", f, key.name, i), "must be a non-blank label name without ',', got %q", l)
			}
		}
	}
	for i, r := range reviewers {
		if !userRe.MatchString(strings.TrimPrefix(r, TeamPrefix)) {
			add(fmt.Sprintf("%sreviewers[%d]", f, i), "must be a user name or team:<slug>, got %q", r)
		}
	}
	for i, a := range assignees {
		if !userRe.MatchString(a) {
			add(fmt.Sprintf("%sassignees[%d]", f, i), "must be a user name, got %q", a)
		}
	}
}

// Author is a parsed gitAuthor.
type Author struct{ Name, Email string }

// authorRe and emailRe accept git identities, including GitHub bot noreply addresses
// such as 'ocmbot[bot] <1+ocmbot[bot]@users.noreply.github.com>' that RFC 5322 parsers
// (net/mail) reject; git itself only forbids '<', '>' and newlines.
var (
	authorRe = regexp.MustCompile(`^([^<>\n]*[^<>\s])\s*<([^<>\s@]+@[^<>\s@]+)>$`)
	emailRe  = regexp.MustCompile(`^[^<>\s@]+@[^<>\s@]+$`)
)

// ParseAuthor parses a 'Name <email>' git author (Renovate's gitAuthor format); both parts
// are required.
func ParseAuthor(s string) (Author, error) {
	m := authorRe.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return Author{}, fmt.Errorf("must be 'Name <email>', got %q", s)
	}
	return Author{Name: m[1], Email: m[2]}, nil
}

// Prefix returns BranchPrefix, or DefaultBranchPrefix when unset.
func (c *Config) Prefix() string { return cmp.Or(c.BranchPrefix, DefaultBranchPrefix) }

// LockFileMaintenance holds the lockFileMaintenance settings.
type LockFileMaintenance struct {
	Enabled bool `yaml:"enabled" jsonschema:"default=false" jsonschema_description:"Propose the lock file maintenance PR."`
	// Schedule overrides the top-level schedule when non-nil; [] = every run.
	Schedule []string `yaml:"schedule" jsonschema_description:"Schedule (cron triggers and time windows) of the lock file maintenance PR, overriding the top-level schedule; [] means every run."`
}

// LockFileMaintenanceSchedule is the schedule of the lock file maintenance PR:
// lockFileMaintenance.schedule, else the top-level schedule; nil = every run.
func (c *Config) LockFileMaintenanceSchedule() *schedule.Set { return c.lfmSchedule }

// Actions holds github-actions manager settings.
type Actions struct {
	DefaultHost string `yaml:"defaultHost" jsonschema_description:"Host for owner/repo@ref actions; empty means auto-detected from the platform."`
}

// GitLabCI holds gitlabci manager settings.
type GitLabCI struct {
	URL string `yaml:"url" jsonschema_description:"Base URL of the GitLab instance project includes and $CI_SERVER_FQDN components resolve against, e.g. https://gitlab.example.com or https://example.com/gitlab; empty means auto-detected (CI_SERVER_URL, else the GitLab platform run detected from the remote, else https://gitlab.com)."`
}

// validate checks URL and strips a trailing slash.
func (g *GitLabCI) validate(add addFunc) {
	if g.URL == "" {
		return
	}
	u, err := url.Parse(g.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.User != nil {
		add("gitlabci.url", "must be an absolute http(s) URL without query or fragment, got %q", g.URL)
		return
	}
	g.URL = strings.TrimRight(g.URL, "/")
}

// Terraform holds terraform manager settings.
type Terraform struct {
	DefaultRegistry string `yaml:"defaultRegistry" jsonschema_description:"Registry host of provider and module addresses without one in *.tf files, e.g. registry.opentofu.org; empty means registry.terraform.io. *.tofu files default to registry.opentofu.org unless this is set."`
}

// Rule overrides the policy for matching deps.
type Rule struct {
	Manager     string   `yaml:"manager" jsonschema:"enum=github-actions,enum=gomod,enum=docker,enum=npm,enum=helm,enum=kustomize,enum=pre-commit,enum=gitlabci,enum=nvm,enum=nodenv,enum=asdf,enum=mise,enum=cargo,enum=pip_requirements,enum=pep621,enum=poetry,enum=pipenv,enum=conda,enum=terraform,enum=terraform-version,enum=tflint-plugin,enum=terragrunt,enum=maven,enum=gradle,enum=gradle-wrapper,enum=maven-wrapper,enum=swift,enum=bundler,enum=ruby-version,enum=git-submodules,enum=rust-toolchain,enum=devcontainer,enum=bitbucket-pipelines,enum=woodpecker,enum=droneci,enum=circleci,enum=azure-pipelines,enum=vcpkg,enum=elm,enum=composer,enum=julia,enum=bazel-module,enum=bazelisk,enum=custom" jsonschema_description:"Only match deps of this manager."`
	Names       []string `yaml:"names" jsonschema_description:"Only match deps whose name matches any of these doublestar globs ('*' stays within a path segment, '**' crosses segments, '{a,b}' alternates). An entry starting with '!' excludes the names its glob matches (Renovate's '!pattern'); a list of only exclusions matches every other name."`
	DepTypes    []string `yaml:"depTypes" jsonschema:"enum=direct,enum=indirect,enum=go,enum=toolchain,enum=action,enum=dockerfile,enum=compose,enum=workflow,enum=kubernetes,enum=helm,enum=dependencies,enum=devDependencies,enum=optionalDependencies,enum=peerDependencies,enum=packageManager,enum=setup,enum=engines,enum=volta,enum=chart,enum=helmfile,enum=k0s,enum=flux,enum=argocd,enum=sveltos,enum=image,enum=helmChart,enum=resource,enum=repository,enum=service,enum=include,enum=component,enum=node,enum=tool,enum=annotation,enum=regex,enum=dev-dependencies,enum=build-dependencies,enum=workspace.dependencies,enum=patch,enum=replace,enum=requirements,enum=project.dependencies,enum=project.optional-dependencies,enum=dependency-groups,enum=tool.uv.dev-dependencies,enum=tool.pdm.dev-dependencies,enum=group,enum=packages,enum=dev-packages,enum=category,enum=pip,enum=required_provider,enum=required_version,enum=module,enum=provider,enum=helm_release,enum=tfe_workspace,enum=docker_image,enum=docker_container,enum=docker_service,enum=docker_registry_image,enum=kubernetes_pod,enum=kubernetes_pod_v1,enum=kubernetes_deployment,enum=kubernetes_deployment_v1,enum=kubernetes_daemonset,enum=kubernetes_daemon_set_v1,enum=kubernetes_stateful_set,enum=kubernetes_stateful_set_v1,enum=kubernetes_job,enum=kubernetes_job_v1,enum=kubernetes_cron_job,enum=kubernetes_cron_job_v1,enum=kubernetes_replication_controller,enum=kubernetes_replication_controller_v1,enum=terraform-version,enum=opentofu-version,enum=plugin,enum=github,enum=gitTags,enum=terragrunt,enum=compile,enum=provided,enum=runtime,enum=test,enum=system,enum=import,enum=parent,enum=build,enum=wrapper,enum=maven,enum=ruby,enum=development,enum=submodule,enum=builtin-baseline,enum=default-registry,enum=registry,enum=overrides,enum=feature,enum=feature-version,enum=pipe,enum=orb,enum=container,enum=task,enum=test-direct,enum=test-indirect,enum=test-dependencies,enum=elm-version,enum=require,enum=require-dev,enum=deps,enum=weakdeps,enum=extras,enum=julia,enum=bazel_dep,enum=single_version_override,enum=multiple_version_override,enum=git_override,enum=archive_override,enum=local_path_override,enum=git_repository,enum=new_git_repository,enum=oci_pull,enum=rules_img_pull,enum=maven_install,enum=crate_spec,enum=bazel" jsonschema_description:"Only match deps of these types."`
	Paths       []string `yaml:"paths" jsonschema_description:"Only match deps whose file (slash path relative to --dir) matches any of these doublestar globs, e.g. 'charts/**'. An entry starting with '!' excludes the files its glob matches; a list of only exclusions matches every other file."`
	Datasources []string `yaml:"datasources" jsonschema:"enum=git-tags,enum=git-refs,enum=github-releases,enum=go,enum=go-toolchain,enum=docker,enum=npm,enum=node-version,enum=helm,enum=crate,enum=pypi,enum=conda,enum=terraform-provider,enum=terraform-module,enum=maven,enum=gradle-version,enum=rubygems,enum=ruby-version,enum=rust-version,enum=orb,enum=azure-pipelines-tasks,enum=vcpkg,enum=elm-package,enum=packagist,enum=julia-registry,enum=bazel,enum=golang-version,enum=github-tags,enum=github-release-attachments,enum=node,enum=bitbucket-tags" jsonschema_description:"Only match deps looked up with these datasources, or custom.<name> of customDatasources; Renovate names (golang-version, github-tags, github-release-attachments, node, bitbucket-tags) match their deps datasource."`
	// BaseBranches matches the base branch of the run (Renovate's matchBaseBranches).
	BaseBranches []string `yaml:"baseBranches" jsonschema_description:"Only match deps on these base branches of run (baseBranches, --base, else the default branch): names or RE2 regexes '/<RE2>/' (flag i allowed); an entry starting with '!' excludes the branches it matches, and a list of only exclusions matches every other branch. plan, apply, explain and preview have no base branch, so no rule with baseBranches matches there."`
	// CurrentVersion and CurrentAge match the dep's current version (Renovate's
	// matchCurrentVersion and matchCurrentAge).
	CurrentVersion string   `yaml:"currentVersion" jsonschema_description:"Only match deps whose current version (the locked one when a lockfile has it) satisfies this Masterminds semver constraint (e.g. '< 2', converted with the dep's versioning scheme), or matches the regex form '/<RE2>/' as written ('!/<RE2>/' excludes matches; flag i allowed). Cannot be combined with versioning."`
	CurrentAge     string   `yaml:"currentAge" jsonschema_description:"Only match deps whose current version was released this long ago: an operator <, <=, > or >= and an age, e.g. '> 1 year' or '< 30 days' (units of minimumReleaseAge, plus months of 30 days and years of 365 days). The release time is looked up only for deps the other selectors of the rule match, from datasources with release times; an unknown time does not match. Cannot be combined with versioning or extractVersion."`
	UpdateTypes    []string `yaml:"updateTypes" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin,enum=replacement" jsonschema_description:"Only match candidate updates of these types: the rule applies once the type of an update is known (e.g. group minor and patch updates, ignore majors). Cannot set pin."`
	Ignore         *bool    `yaml:"ignore" jsonschema_description:"Do not update matching deps; false opts indirect Go requirements, pip-compile pins and Elm indirect dependencies, go directives, npm peerDependencies and engines, actions pinned to a bare SHA no tag points at (digest updates to the default branch), git submodules and Azure Pipelines tasks in. With updateTypes: reject updates of these types."`
	Pin            string   `yaml:"pin" jsonschema:"enum=keep,enum=digest" jsonschema_description:"Pin mode for matching deps."`
	Allow          string   `yaml:"allow" jsonschema_description:"New versions must satisfy this Masterminds semver constraint (e.g. '< 7'), or match Renovate's regex form '/<RE2>/' (e.g. '/^\\d+\\.\\d+$/'; '!/<RE2>/' rejects matches; flag i allowed) against the version as written."`
	RangeStrategy  string   `yaml:"rangeStrategy" jsonschema:"enum=auto,enum=bump,enum=replace,enum=update-lockfile,enum=widen,enum=pin,enum=in-range-only" jsonschema_description:"Range strategy for matching npm, cargo, Python, Swift, bundler, Elm, composer, julia and terraform deps and Gradle dynamic versions (see the top-level rangeStrategy). Cannot be combined with updateTypes."`
	Updates        []string `yaml:"updates" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin" jsonschema_description:"Allowed update types; replaces the earlier list. Replacements are not listed here: they are switched off with ignore (updateTypes: [replacement]) or replacements: false."`
	// Versioning overrides the dep's scheme before resolution, so it is a pre-policy field.
	Versioning string `yaml:"versioning" jsonschema:"pattern=^(tag|go|docker|semver|loose|cargo|pep440|hashicorp|maven|ruby|conda|composer|julia|bazel-module|regex:.+)$" jsonschema_description:"Versioning scheme overriding the manager's: tag, go, docker, semver, loose, cargo, pep440, hashicorp, maven, ruby, conda, composer, julia, bazel-module, or regex:<RE2> with named groups major (required), minor, patch, build, revision, prerelease and compatibility; (?<name>) and (?P<name>) syntax both work. Cannot be combined with updateTypes."`
	// ExtractVersion replaces the dep's release-name mapping before resolution (pre-policy).
	ExtractVersion string `yaml:"extractVersion" jsonschema_description:"RE2 regexp with a (?<version>) group extracting the version from the datasource's release names, e.g. '^jq-(?<version>.+)$' for jq-1.7.1 tags or '^kustomize/(?<version>.+)$'; names that do not match are ignored and the extracted version is written to the file. Cannot be combined with updateTypes."`
	// ReplacementName and ReplacementVersion move matching deps to another package (Renovate's
	// options of the same names); pre-policy fields.
	ReplacementName    string `yaml:"replacementName" jsonschema_description:"Replace matching deps with this package (image, npm package, owner/repo[/path] action, Go module path, group:artifact, chart or registry address; with replacementPrefix, the new prefix) in one replacement update instead of their usual updates, which run proposes once the replacement's PR is closed. The version is kept unless replacementVersion is set, and the new package must have it (else the dep is skipped with the reason). Supported by the docker, gitlabci, kustomize, helm, terraform (providers, registry modules, images), terragrunt, tflint, maven, gradle, npm, github-actions and gomod managers where the name is written literally (see the managers reference). Overrides earlier rules and the built-in replacements. updates lists do not apply to replacements; select them with updateTypes: [replacement]. Cannot be combined with updateTypes or currentAge."`
	ReplacementVersion string `yaml:"replacementVersion" jsonschema_description:"Version written with replacementName (the new package's version; an npm range keeps its operator); empty keeps the current version. Requires replacementName."`
	ReplacementPrefix  string `yaml:"replacementPrefix" jsonschema_description:"Prefix of the dep name that replacementName replaces, for moves of many names at once (Renovate's replacementNameTemplate with replace): with replacementPrefix: k8s.gcr.io/ and replacementName: registry.k8s.io/, k8s.gcr.io/pause becomes registry.k8s.io/pause. Deps whose name does not start with it get no replacement from the rule. Requires replacementName."`
	// Separate* override the top-level PR bucket options for matching deps.
	SeparateMajorMinor    *bool    `yaml:"separateMajorMinor" jsonschema_description:"Overrides the top-level separateMajorMinor for matching deps (with updateTypes: for candidates of those types)."`
	SeparateMultipleMajor *bool    `yaml:"separateMultipleMajor" jsonschema_description:"Overrides the top-level separateMultipleMajor for matching deps (with updateTypes: for candidates of those types)."`
	SeparateMinorPatch    *bool    `yaml:"separateMinorPatch" jsonschema_description:"Overrides the top-level separateMinorPatch for matching deps (with updateTypes: for candidates of those types)."`
	Group                 string   `yaml:"group" jsonschema_description:"Group name: all deps with the same group share one PR."`
	SecurityGroup         string   `yaml:"securityGroup" jsonschema_description:"Group name for the security updates of matching deps: those with the same securityGroup share one PR on <prefix>group/<slug>-security, apart from non-security updates (also of a group with the same name). Without it every security update gets its own PR."`
	CommitType            string   `yaml:"commitType" jsonschema:"enum=fix,enum=chore,enum=build,enum=ci,enum=docs,enum=feat,enum=perf,enum=refactor,enum=revert,enum=style,enum=test" jsonschema_description:"Conventional commit type of commit and PR titles; default fix for production deps (gomod direct, npm, cargo, poetry and bundler dependencies, composer require, pep621 project.dependencies, pipenv packages, elm direct and dependencies, julia deps), chore otherwise. Security updates always use fix."`
	CommitScope           *string  `yaml:"commitScope" jsonschema_description:"Conventional commit scope; default deps, empty string means no scope ('chore: ...')."`
	Schedule              []string `yaml:"schedule" jsonschema_description:"Schedule (cron triggers and time windows, like the top-level schedule) overriding the top-level schedule; [] means every run."` // non-nil overrides the top-level schedule; [] = always
	// MinimumReleaseAge overrides the top-level minimumReleaseAge when set; "0" disables it.
	MinimumReleaseAge string `yaml:"minimumReleaseAge" jsonschema_description:"Minimum release age overriding the top-level minimumReleaseAge, e.g. '7 days'; '0' or '0 days' disables it for matching deps."`
	// MinimumReleaseAgeBehaviour overrides the top-level minimumReleaseAgeBehaviour when set.
	MinimumReleaseAgeBehaviour string `yaml:"minimumReleaseAgeBehaviour" jsonschema:"enum=timestamp-optional,enum=timestamp-required" jsonschema_description:"Overrides the top-level minimumReleaseAgeBehaviour for matching deps (with updateTypes: for candidates of those types)."`
	// AbandonmentThreshold overrides the top-level abandonmentThreshold when set; "0" disables it.
	AbandonmentThreshold string   `yaml:"abandonmentThreshold" jsonschema_description:"Overrides the top-level abandonmentThreshold for matching deps, e.g. '2 years'; '0' turns the check off for them."`
	Automerge            *bool    `yaml:"automerge" jsonschema_description:"Overrides the top-level automerge for matching deps (with updateTypes: for updates of those types). A PR is automerged only when all of its updates are."`
	AutomergeStrategy    string   `yaml:"automergeStrategy" jsonschema:"enum=auto,enum=merge,enum=squash,enum=rebase" jsonschema_description:"Overrides the top-level automergeStrategy for matching deps; a PR uses the strategy of its first update."`
	AutomergeType        string   `yaml:"automergeType" jsonschema:"enum=pr,enum=branch" jsonschema_description:"Overrides the top-level automergeType for matching deps; a group merges its branch only when all of its updates use branch."`
	DashboardApproval    *bool    `yaml:"dashboardApproval" jsonschema_description:"Overrides the top-level dashboardApproval for matching deps (with updateTypes: for updates of those types). A PR needs approval when any of its updates does."`
	DraftPR              *bool    `yaml:"draftPR" jsonschema_description:"Overrides the top-level draftPR for matching deps (with updateTypes: for updates of those types). A group's PR is a draft when any of its updates asks for one."`
	PRCreation           string   `yaml:"prCreation" jsonschema:"enum=immediate,enum=not-pending,enum=status-success" jsonschema_description:"Overrides the top-level prCreation for matching deps; a group waits as long as the most waiting of its updates asks (status-success over not-pending over immediate)."`
	Labels               []string `yaml:"labels" jsonschema_description:"Replaces the labels of new PRs of matching deps; [] removes them. A group's PR gets the labels of all its updates."`
	AddLabels            []string `yaml:"addLabels" jsonschema_description:"Labels added to those of earlier rules and the top level for new PRs of matching deps."`
	Reviewers            []string `yaml:"reviewers" jsonschema_description:"Replaces the reviewers of new PRs of matching deps (user names or team:<slug>); [] removes them. A group's PR requests the reviewers of all its updates."`
	Assignees            []string `yaml:"assignees" jsonschema_description:"Replaces the assignees of new PRs of matching deps; [] removes them. A group's PR gets the assignees of all its updates."`
	// PRTitle, PRHeader and PRFooter override the top-level templates when set; "" resets them.
	PRTitle  *string `yaml:"prTitle" jsonschema_description:"Overrides the top-level prTitle for matching deps (with updateTypes: for updates of those types); empty string means the built-in titles. A group's PR uses its first update's templates."`
	PRHeader *string `yaml:"prHeader" jsonschema_description:"Overrides the top-level prHeader for matching deps; empty string means none."`
	PRFooter *string `yaml:"prFooter" jsonschema_description:"Overrides the top-level prFooter for matching deps; empty string means none."`
	// PostUpgradeTasks of the last matching rule setting it run after the update is built.
	PostUpgradeTasks *PostUpgradeTasks `yaml:"postUpgradeTasks" jsonschema_description:"Shell commands run in the repository after the update is built (apply, run with --allow-tasks), e.g. go generate ./...: a list (once per branch) or {commands, executionMode}. The files they change, apart from .gitignored ones, join the commit. The last matching rule setting it wins; [] clears."`

	baseBranches   []branchPattern // parsed BaseBranches
	source         string          // the file the rule is written in (Source)
	line           int             // its line there
	allow          *Allowed
	updates        map[dep.UpdateType]bool // set of Updates; nil = not set
	currentVersion *Allowed                // parsed CurrentVersion; nil = not set
	currentAge     *ageRange               // parsed CurrentAge; nil = not set
	schedule       *schedule.Set
	templates      prtemplate.Set // parsed PRTitle, PRHeader and PRFooter; members of unset fields are nil
	minAge         *time.Duration // parsed MinimumReleaseAge; nil = not set
	abandon        *time.Duration // parsed AbandonmentThreshold; nil = not set
}

// Source returns the file a loaded rule is written in (the config file's path relative to
// --dir, or the extends entry of another repository's file) and its line there; "" when the
// rule was not loaded from a file.
func (r *Rule) Source() (file string, line int) { return r.source, r.line }

// Custom is a custom manager: annotation mode when Match is empty, regex mode otherwise.
type Custom struct {
	Files      []string  `yaml:"files" jsonschema:"required,minItems=1" jsonschema_description:"Doublestar globs of the files this manager scans."`
	Match      string    `yaml:"match" jsonschema_description:"RE2 regexp with a (?P<version>) or (?P<currentValue>) group and optional name (or depName/packageName), datasource, digest and checksum groups; empty means annotation mode."`
	Datasource string    `yaml:"datasource" jsonschema:"enum=git-tags,enum=git-refs,enum=github-releases,enum=go,enum=go-toolchain,enum=docker,enum=npm,enum=node-version,enum=helm,enum=crate,enum=pypi,enum=conda,enum=terraform-provider,enum=terraform-module,enum=maven,enum=gradle-version,enum=rubygems,enum=ruby-version,enum=rust-version,enum=orb,enum=azure-pipelines-tasks,enum=vcpkg,enum=elm-package,enum=packagist,enum=julia-registry,enum=bazel,enum=golang-version,enum=github-tags,enum=github-release-attachments,enum=node,enum=bitbucket-tags" jsonschema_description:"Datasource of the matched deps, or custom.<name> of customDatasources; required in regex mode unless match has a (?P<datasource>) group. Renovate names (golang-version, github-tags, github-release-attachments, node, bitbucket-tags) are aliases."`
	Name       string    `yaml:"name" jsonschema_description:"Dependency name; required in regex mode unless match has a (?P<name>), (?P<depName>) or (?P<packageName>) group."`
	Versioning string    `yaml:"versioning" jsonschema:"pattern=^(tag|go|docker|semver|loose|cargo|pep440|hashicorp|maven|ruby|conda|composer|julia|bazel-module|regex:.+)$,default=semver" jsonschema_description:"Versioning scheme of the matched versions: tag, go, docker, semver, loose, cargo, pep440, hashicorp, maven, ruby, conda, composer, julia, bazel-module or regex:<RE2> (see rules[].versioning); annotation and regex-group versioning fields accept the scheme names."` // default semver
	Checksum   *Checksum `yaml:"checksum" jsonschema_description:"Where the checksum of a new version comes from; regex mode only, requires a (?P<checksum>) group in match."`
}

// Checksum locates the sha256/sha512 checksum written next to a version. Its fields are
// templates; {{version}} is the new version exactly as written into the file.
// executionMode values of postUpgradeTasks (Renovate's names).
const (
	ExecutionBranch = "branch" // once per branch, the default
	ExecutionUpdate = "update" // once per update of the branch, with DEPS_* variables
)

// ExecutionModes are the accepted postUpgradeTasks.executionMode values.
var ExecutionModes = []string{ExecutionBranch, ExecutionUpdate}

// PostUpgradeTasks is rules[].postUpgradeTasks: a list of commands (executionMode branch) or
// the object form.
type PostUpgradeTasks struct {
	Commands      []string `yaml:"commands" jsonschema_description:"Shell commands, run in order with sh -c."`
	ExecutionMode string   `yaml:"executionMode" jsonschema:"enum=branch,enum=update,default=branch" jsonschema_description:"branch runs the commands once per branch, update once per update of the branch, in the branch's order, with DEPS_* variables describing the update."`
	Files         []string `yaml:"files" jsonschema_description:"Doublestar globs on slash paths relative to --dir (an entry starting with '!' excludes, as in rules[].paths): only the files the commands change that match join the commit (run). Default: every changed file that .gitignore does not ignore."`

	list bool // written as a list
}

// UnmarshalYAML accepts a list of commands or a mapping with the PostUpgradeTasks keys.
func (t *PostUpgradeTasks) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		t.list = true
		return n.Decode(&t.Commands)
	}
	if n.Kind == yaml.MappingNode {
		for i := 0; i+1 < len(n.Content); i += 2 { // node.Decode does not reject unknown keys
			if k := n.Content[i]; k.Value != "commands" && k.Value != "executionMode" && k.Value != "files" {
				return fmt.Errorf("line %d: field %s not found in type config.PostUpgradeTasks", k.Line, k.Value)
			}
		}
	}
	type plain PostUpgradeTasks
	return n.Decode((*plain)(t))
}

type Checksum struct {
	Asset string `yaml:"asset" jsonschema_description:"github-releases only: release asset whose API digest is the checksum, e.g. 'gh_{{version}}_linux_amd64.tar.gz'. Exclusive with url."`
	URL   string `yaml:"url" jsonschema_description:"URL of a checksum document (sha256sum style or a bare hex digest). Exclusive with asset."`
	File  string `yaml:"file" jsonschema_description:"Entry name inside the url document ('<hex>  <name>' or '<hex> *<name>'); empty means the first hex token of the body."`
}

// CustomDatasourcePrefix starts the datasource name of a custom datasource: custom.<name>.
const CustomDatasourcePrefix = customds.Prefix

// CustomDatasourceNamePattern is the accepted syntax of customDatasources keys.
const CustomDatasourceNamePattern = `^[A-Za-z0-9][A-Za-z0-9._-]*$`

var customDatasourceNameRe = regexp.MustCompile(CustomDatasourceNamePattern)

// CustomDatasource is a version list fetched from a URL (Renovate's customDatasources).
type CustomDatasource struct {
	URL          string `yaml:"url" jsonschema:"required" jsonschema_description:"http(s) URL of the version document, fetched once per run (unauthenticated, at most 10 MiB)."`
	Format       string `yaml:"format" jsonschema:"enum=json,enum=yaml,enum=plain,default=json" jsonschema_description:"Document format: json, yaml, or plain (one version per line; empty lines and # comments are skipped)."`
	Versions     string `yaml:"versions" jsonschema:"default=.releases[].version" jsonschema_description:"json/yaml only: selector of the version strings, a jq subset of stages joined by '|': paths of .field and [] steps ('.releases[].version', '[].tag_name') and keys (sorted object keys, '.releases | keys')."`
	VersionRegex string `yaml:"versionRegex" jsonschema_description:"RE2 regexp the selected values must match; its (?P<version>) group, else the whole match, is the version (e.g. '^v?(?P<version>\\d+\\.\\d+\\.\\d+)$' strips a leading v)."`
}

// Spec returns the datasource spec of cd.
func (cd CustomDatasource) Spec() customds.Spec {
	return customds.Spec{URL: cd.URL, Format: cd.Format, Versions: cd.Versions, VersionRegex: cd.VersionRegex}
}

// KnownDatasource reports whether name is a datasource name (alias included) or custom.<name>
// of a defined custom datasource.
func (c *Config) KnownDatasource(name string) bool {
	if n, ok := strings.CutPrefix(name, CustomDatasourcePrefix); ok {
		_, ok := c.CustomDatasources[n]
		return ok
	}
	return slices.Contains(DatasourceNames, name)
}

func (c *Config) validateCustomDatasources(add addFunc) {
	for _, name := range slices.Sorted(maps.Keys(c.CustomDatasources)) {
		cd := c.CustomDatasources[name]
		f := "customDatasources." + name
		if !customDatasourceNameRe.MatchString(name) {
			add(f, "name must match %s", customDatasourceNameRe)
		}
		if u, err := url.Parse(cd.URL); cd.URL == "" {
			add(f+".url", "required")
		} else if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			add(f+".url", "must be an http(s) URL, got %q", cd.URL)
		}
		if cd.Format != "" && !slices.Contains(customds.Formats, cd.Format) {
			add(f+".format", "must be one of %s, got %q", list(customds.Formats), cd.Format)
		}
		if cd.Versions != "" {
			if cd.Format == customds.FormatPlain {
				add(f+".versions", "not allowed with format plain")
			} else if _, err := customds.ParseSelector(cd.Versions); err != nil {
				add(f+".versions", "%v", err)
			}
		}
		if cd.VersionRegex != "" {
			if _, err := regexp.Compile(cd.VersionRegex); err != nil {
				add(f+".versionRegex", "invalid RE2 regexp: %v", err)
			}
		}
	}
}

// unknownDatasource is the message of an unknown datasource name.
func unknownDatasource(name string) string {
	return fmt.Sprintf("must be one of %s or custom.<name> of customDatasources, got %q", list(DatasourceNames), name)
}

// templateVarRe finds {{...}} template references.
var templateVarRe = regexp.MustCompile(`\{\{[^{}]*\}\}`)

// validateTemplate reports references other than {{version}} and unbalanced braces.
func validateTemplate(field, t string, add addFunc) {
	for _, v := range templateVarRe.FindAllString(t, -1) {
		if v != "{{version}}" {
			add(field, "unknown template variable %s (only {{version}})", v)
		}
	}
	if rest := templateVarRe.ReplaceAllString(t, ""); strings.Contains(rest, "{{") || strings.Contains(rest, "}}") {
		add(field, "unbalanced template braces in %q", t)
	}
}

func (ch *Checksum) validate(f, datasource string, add addFunc) {
	switch {
	case ch.Asset == "" && ch.URL == "":
		add(f, "one of asset and url is required")
	case ch.Asset != "" && ch.URL != "":
		add(f, "asset and url are mutually exclusive")
	}
	if ch.Asset != "" && CanonicalDatasource(datasource) != "github-releases" {
		add(f+".asset", "requires datasource github-releases")
	}
	if ch.File != "" && ch.URL == "" {
		add(f+".file", "only allowed with url")
	}
	validateTemplate(f+".asset", ch.Asset, add)
	validateTemplate(f+".url", ch.URL, add)
	validateTemplate(f+".file", ch.File, add)
}

// Error marks an invalid configuration (parse, unknown key or validation
// failure, or an unreadable config file). Callers use errors.As to map it to
// a usage exit code.
type Error struct {
	Err error
	// File is the config file the problems are in; "" when none was read.
	File string
	// Problems locate each problem in File, in the order of Err's messages; empty when the
	// file could not be read.
	Problems []Problem
}

// Problem is one located configuration problem.
type Problem struct {
	Line int // 1-based line in Error.File; 0 = unknown
	Msg  string
}

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// FieldError is one validation problem of a field: "<path>: <field>: <msg>".
type FieldError struct {
	Path, Field, Msg string
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Field + ": " + e.Msg }

// Default returns the configuration used when no file exists.
func Default() *Config {
	return &Config{Version: 1, Pin: PinKeep, BranchPrefix: DefaultBranchPrefix, Monorepos: new(true), IgnorePaths: slices.Clone(DefaultIgnorePaths)}
}

// Load reads explicitPath (which must exist) or, if empty, .deps.yaml then
// .deps.yml in dir, falling back to Default. The files its extends entries name are read
// with fetch (nil: extends is an error). All failures are *Error.
func Load(ctx context.Context, dir, explicitPath string, fetch Fetcher) (*Config, error) {
	var f *os.File
	var p, display string
	if explicitPath != "" {
		p, display = explicitPath, explicitPath
		var err error
		if f, err = os.Open(p); err != nil {
			return nil, &Error{Err: err}
		}
	} else {
		for _, n := range []string{".deps.yaml", ".deps.yml"} {
			cand := filepath.Join(dir, n)
			h, err := os.Open(cand)
			if err == nil {
				f, p, display = h, cand, n
				break
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, &Error{Err: err}
			}
		}
		if f == nil {
			return Default(), nil
		}
	}
	defer f.Close()

	src, err := io.ReadAll(f)
	if err != nil {
		return nil, &Error{Err: fmt.Errorf("%s: %w", p, err), File: p}
	}
	l := &loader{ctx: ctx, fetch: fetch, path: p, seen: map[string]bool{}}
	if err := l.add(src, p, display, -1); err != nil {
		return nil, err
	}
	c := &Config{}
	if len(l.layers) > 1 {
		// The files decoded strictly one by one, so their merge does too.
		if src, err = yaml.Marshal(merge(l.layers)); err != nil {
			return nil, &Error{Err: fmt.Errorf("%s: merge extends: %w", p, err), File: p}
		}
		c.layers = l.layers
	}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, &Error{Err: fmt.Errorf("%s: %w", p, err), File: p, Problems: decodeProblems(err)}
	}
	c.Extends = l.extends // the merge drops the key
	if c.IgnorePaths == nil && !hasTopLevelKey(src, "ignorePaths") {
		c.IgnorePaths = slices.Clone(DefaultIgnorePaths)
	}
	c.path = p
	c.setSources(l.layers)
	if c.Version == 0 {
		c.Version = 1
	}
	if c.Pin == "" {
		c.Pin = PinKeep
	}
	for i := range c.Custom {
		if c.Custom[i].Versioning == "" {
			c.Custom[i].Versioning = "semver"
		}
	}
	if err := c.Validate(); err != nil {
		extended := func(path string) (int, bool) {
			for _, ly := range l.layers {
				if ly.via >= 0 && ly.name == path {
					return extendsLine(l.local, ly.via), true
				}
			}
			return 0, false
		}
		return nil, &Error{Err: err, File: p, Problems: fieldProblems(l.local, err, extended)}
	}
	return c, nil
}

// setSources records the file and line of each rule.
func (c *Config) setSources(layers []layer) {
	i := 0
	for _, ly := range layers {
		for _, n := range ly.sequence("rules") {
			if i < len(c.Rules) {
				c.Rules[i].source, c.Rules[i].line = ly.display, n.Line
			}
			i++
		}
	}
}

// hasTopLevelKey reports whether the yaml document src has key at the top level, also
// when its value is null (`ignorePaths:` or `ignorePaths: ~`).
func hasTopLevelKey(src []byte, key string) bool {
	var doc yaml.Node
	if yaml.Unmarshal(src, &doc) != nil || len(doc.Content) == 0 || doc.Content[0].Kind != yaml.MappingNode {
		return false
	}
	m := doc.Content[0].Content
	for i := 0; i+1 < len(m); i += 2 {
		if m[i].Value == key {
			return true
		}
	}
	return false
}

// Validate checks every field and compiles rule constraints and schedules. Errors
// are joined and formatted as "<path>: <field>: <msg>".
func (c *Config) Validate() error {
	p := c.path
	if p == "" {
		p = ".deps.yaml"
	}
	var errs []error
	add := func(field, format string, a ...any) {
		path := p
		if j, f := c.attribute(field); j >= 0 {
			field = f
			if c.layers[j].via >= 0 {
				path = c.layers[j].name
			}
		}
		errs = append(errs, &FieldError{Path: path, Field: field, Msg: fmt.Sprintf(format, a...)})
	}
	if c.Version != 0 && c.Version != 1 {
		add("version", "must be 1, got %d", c.Version)
	}
	if c.Pin != "" && c.Pin != PinKeep && c.Pin != PinDigest {
		add("pin", "must be keep or digest, got %q", c.Pin)
	}
	if c.RangeStrategy != "" && !slices.Contains(RangeStrategies, c.RangeStrategy) {
		add("rangeStrategy", "must be one of %s, got %q", list(RangeStrategies), c.RangeStrategy)
	}
	if c.BranchPrefix != "" {
		if msg := checkBranchPrefix(c.BranchPrefix); msg != "" {
			add("branchPrefix", "%s, got %q", msg, c.BranchPrefix)
		}
	}
	c.baseBranches = parseBranchPatterns("baseBranches", c.BaseBranches, false, true, add)
	if c.GitAuthor != "" {
		if _, err := ParseAuthor(c.GitAuthor); err != nil {
			add("gitAuthor", "%v", err)
		}
	}
	for i, e := range c.GitIgnoredAuthors {
		if !emailRe.MatchString(e) {
			add(fmt.Sprintf("gitIgnoredAuthors[%d]", i), "must be an email address, got %q", e)
		}
	}
	if c.PRConcurrentLimit < 0 {
		add("prConcurrentLimit", "must be >= 0, got %d", c.PRConcurrentLimit)
	}
	if c.PRHourlyLimit < 0 {
		add("prHourlyLimit", "must be >= 0, got %d", c.PRHourlyLimit)
	}
	if c.RebaseWhen != "" && !slices.Contains(RebaseWhens, c.RebaseWhen) {
		add("rebaseWhen", "must be one of %s, got %q", list(RebaseWhens), c.RebaseWhen)
	}
	if c.PlatformCommit != "" && !slices.Contains(PlatformCommits, c.PlatformCommit) {
		add("platformCommit", "must be one of %s, got %q", list(PlatformCommits), c.PlatformCommit)
	}
	validateAutomerge("", c.AutomergeStrategy, c.AutomergeType, add)
	validatePRCreation("", c.PRCreation, add)
	if c.DependencyDashboardTitle != "" && (strings.TrimSpace(c.DependencyDashboardTitle) == "" || strings.ContainsAny(c.DependencyDashboardTitle, "\r\n")) {
		add("dependencyDashboardTitle", "must be a non-blank single line, got %q", c.DependencyDashboardTitle)
	}
	validatePRMeta("", c.Labels, nil, c.Reviewers, c.Assignees, add)
	c.templates = parseTemplates("", &c.PRTitle, &c.PRHeader, &c.PRFooter, add)
	for i, l := range c.DependencyDashboardLabels {
		if strings.TrimSpace(l) == "" || strings.Contains(l, ",") {
			add(fmt.Sprintf("dependencyDashboardLabels[%d]", i), "must be a non-blank label name without ',', got %q", l)
		}
	}
	for _, l := range []struct{ key, v string }{{"rebaseLabel", c.RebaseLabel}, {"stopUpdatingLabel", c.StopUpdatingLabel}} {
		if l.v != "" && (strings.TrimSpace(l.v) == "" || strings.Contains(l.v, ",")) {
			add(l.key, "must be a non-blank label name without ',', got %q", l.v)
		}
	}
	if strings.EqualFold(c.RebaseLabelName(), c.StopUpdatingLabelName()) {
		add("stopUpdatingLabel", "must differ from rebaseLabel, got %q", c.StopUpdatingLabelName())
	}
	c.minAge = 0
	if c.MinimumReleaseAge != "" {
		if d, err := ParseAge(c.MinimumReleaseAge); err != nil {
			add("minimumReleaseAge", "%v", err)
		} else {
			c.minAge = d
		}
	}
	validateAgeBehaviour("", c.MinimumReleaseAgeBehaviour, add)
	c.loc = time.UTC
	if c.Timezone != "" {
		if loc, err := loadTimezone(c.Timezone); err != nil {
			add("timezone", "%v", err)
		} else {
			c.loc = loc
		}
	}
	c.abandon = 0
	if c.AbandonmentThreshold != "" {
		if d, err := parseLongAge(c.AbandonmentThreshold); err != nil {
			add("abandonmentThreshold", "%v", err)
		} else {
			c.abandon = d
		}
	}
	c.schedule = c.parseSchedule("schedule", c.Schedule, add)
	c.lfmSchedule = c.schedule
	if c.LockFileMaintenance.Schedule != nil {
		c.lfmSchedule = c.parseSchedule("lockFileMaintenance.schedule", c.LockFileMaintenance.Schedule, add)
	}
	for i, g := range c.IgnorePaths {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("ignorePaths[%d]", i), "invalid glob %q: %v", g, err)
		}
	}
	for i := range c.Rules {
		c.validateRule(i, add)
	}
	// Approval happens on the dashboard: without it, updates needing approval never get a PR.
	if !c.DependencyDashboard {
		if c.DashboardApproval {
			add("dashboardApproval", "requires dependencyDashboard: true")
		}
		for i, r := range c.Rules {
			if r.DashboardApproval != nil && *r.DashboardApproval {
				add(fmt.Sprintf("rules[%d].dashboardApproval", i), "requires dependencyDashboard: true")
			}
		}
	}
	for i := range c.Custom {
		c.Custom[i].validate(fmt.Sprintf("custom[%d]", i), c.KnownDatasource, add)
	}
	c.validateCustomDatasources(add)
	c.GitLabCI.validate(add)
	return errors.Join(errs...)
}

type addFunc func(field, format string, a ...any)

// loadTimezone loads an IANA time zone; "Local" is rejected since it depends on the runner.
func loadTimezone(name string) (*time.Location, error) {
	if name == "Local" {
		return nil, fmt.Errorf("must be an IANA time zone like Europe/Berlin, got %q", name)
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown IANA time zone %q", name)
	}
	return loc, nil
}

// parseSchedule validates each entry separately for precise errors and returns the
// parsed set with windows in the config's time zone, or nil when exprs is empty or invalid.
func (c *Config) parseSchedule(field string, exprs []string, add addFunc) *schedule.Set {
	ok := true
	for i, e := range exprs {
		if err := schedule.Validate(e); err != nil {
			add(fmt.Sprintf("%s[%d]", field, i), "%q: %v", e, err)
			ok = false
		}
	}
	if !ok {
		return nil
	}
	s, _ := schedule.Parse(exprs, c.loc) // entries are valid here
	return s
}

func (c *Config) validateRule(i int, add addFunc) {
	r := &c.Rules[i]
	f := fmt.Sprintf("rules[%d]", i)
	if r.Manager != "" && !slices.Contains(Managers, r.Manager) {
		add(f+".manager", "must be one of %s, got %q", list(Managers), r.Manager)
	}
	validateMatchGlobs(f+".names", r.Names, add)
	for j, t := range r.DepTypes {
		if !slices.Contains(DepTypes, t) {
			add(fmt.Sprintf("%s.depTypes[%d]", f, j), "must be one of %s, got %q", list(DepTypes), t)
		}
	}
	validateMatchGlobs(f+".paths", r.Paths, add)
	for j, ds := range r.Datasources {
		if !c.KnownDatasource(ds) {
			add(fmt.Sprintf("%s.datasources[%d]", f, j), "%s", unknownDatasource(ds))
		}
	}
	r.baseBranches = parseBranchPatterns(f+".baseBranches", r.BaseBranches, true, false, add)
	r.currentVersion = nil
	if r.CurrentVersion != "" {
		if a, err := ParseAllowed(r.CurrentVersion); err != nil {
			add(f+".currentVersion", "%v", err)
		} else {
			r.currentVersion = a
		}
		if r.Versioning != "" {
			add(f+".currentVersion", "cannot be combined with versioning: the current version is compared under the scheme other rules decide")
		}
	}
	r.currentAge = nil
	if r.CurrentAge != "" {
		if a, err := parseAgeRange(r.CurrentAge); err != nil {
			add(f+".currentAge", "%v", err)
		} else {
			r.currentAge = a
		}
		if r.Versioning != "" || r.ExtractVersion != "" {
			add(f+".currentAge", "cannot be combined with versioning or extractVersion: the release time is looked up after both are decided")
		}
	}
	if r.UpdateTypes != nil && len(r.UpdateTypes) == 0 {
		add(f+".updateTypes", "must not be empty (omit it to match every update type)")
	}
	for j, u := range r.UpdateTypes {
		if !slices.Contains(UpdateTypeSelectors, dep.UpdateType(u)) {
			add(fmt.Sprintf("%s.updateTypes[%d]", f, j), "must be one of %s, %s, got %q", updateTypeList, dep.Replacement, u)
		}
	}
	if len(r.UpdateTypes) > 0 && r.Pin != "" {
		add(f+".pin", "cannot be combined with updateTypes: the pin mode is decided before the update type is known")
	}
	if r.Pin != "" && r.Pin != PinKeep && r.Pin != PinDigest {
		add(f+".pin", "must be keep or digest, got %q", r.Pin)
	}
	if r.RangeStrategy != "" && !slices.Contains(RangeStrategies, r.RangeStrategy) {
		add(f+".rangeStrategy", "must be one of %s, got %q", list(RangeStrategies), r.RangeStrategy)
	}
	if len(r.UpdateTypes) > 0 && r.RangeStrategy != "" {
		add(f+".rangeStrategy", "cannot be combined with updateTypes: the range strategy is decided before the update type is known")
	}
	r.updates = nil
	if r.Updates != nil {
		r.updates = updateSet(r.Updates)
	}
	r.allow = nil
	if r.Allow != "" {
		a, err := ParseAllowed(r.Allow)
		if err != nil {
			add(f+".allow", "%v", err)
		} else {
			r.allow = a
		}
	}
	if r.Versioning != "" {
		if _, err := versioning.Parse(r.Versioning); err != nil {
			add(f+".versioning", "%v", err)
		}
		if len(r.UpdateTypes) > 0 {
			add(f+".versioning", "cannot be combined with updateTypes: the versioning scheme is decided before the update type is known")
		}
	}
	if r.ExtractVersion != "" {
		if _, err := versioning.CompileExtract(r.ExtractVersion); err != nil {
			add(f+".extractVersion", "%v", err)
		}
		if len(r.UpdateTypes) > 0 {
			add(f+".extractVersion", "cannot be combined with updateTypes: release names are mapped before the update type is known")
		}
	}
	switch {
	case r.ReplacementName != "" && len(r.UpdateTypes) > 0:
		add(f+".replacementName", "cannot be combined with updateTypes: the replacement is decided before the update type is known")
	case r.ReplacementName != "" && r.CurrentAge != "":
		add(f+".replacementName", "cannot be combined with currentAge: the replacement is decided before the age of the current version is known")
	}
	if r.ReplacementName == "" {
		if r.ReplacementVersion != "" {
			add(f+".replacementVersion", "requires replacementName")
		}
		if r.ReplacementPrefix != "" {
			add(f+".replacementPrefix", "requires replacementName")
		}
	}
	for j, u := range r.Updates {
		if !slices.Contains(dep.UpdateTypes, dep.UpdateType(u)) {
			add(fmt.Sprintf("%s.updates[%d]", f, j), "must be one of %s, got %q", updateTypeList, u)
		}
	}
	if r.CommitType != "" && !slices.Contains(CommitTypes, r.CommitType) {
		add(f+".commitType", "must be one of %s, got %q", list(CommitTypes), r.CommitType)
	}
	if r.CommitScope != nil && *r.CommitScope != "" && !commitScopeRe.MatchString(*r.CommitScope) {
		add(f+".commitScope", "must match %s, got %q", commitScopeRe, *r.CommitScope)
	}
	validateAutomerge(f+".", r.AutomergeStrategy, r.AutomergeType, add)
	validatePRCreation(f+".", r.PRCreation, add)
	validatePRMeta(f+".", r.Labels, r.AddLabels, r.Reviewers, r.Assignees, add)
	if t := r.PostUpgradeTasks; t != nil {
		key := f + ".postUpgradeTasks.commands"
		if t.list {
			key = f + ".postUpgradeTasks"
		}
		for j, c := range t.Commands {
			if strings.TrimSpace(c) == "" {
				add(fmt.Sprintf("%s[%d]", key, j), "must be a non-blank command")
			}
		}
		validateMatchGlobs(f+".postUpgradeTasks.files", t.Files, add)
		if t.ExecutionMode != "" && !slices.Contains(ExecutionModes, t.ExecutionMode) {
			add(f+".postUpgradeTasks.executionMode", "must be one of %s, got %q", list(ExecutionModes), t.ExecutionMode)
		}
	}
	r.schedule = c.parseSchedule(f+".schedule", r.Schedule, add)
	r.minAge = nil
	if r.MinimumReleaseAge != "" {
		if d, err := ParseAge(r.MinimumReleaseAge); err != nil {
			add(f+".minimumReleaseAge", "%v", err)
		} else {
			r.minAge = &d
		}
	}
	validateAgeBehaviour(f+".", r.MinimumReleaseAgeBehaviour, add)
	r.abandon = nil
	if r.AbandonmentThreshold != "" {
		if d, err := parseLongAge(r.AbandonmentThreshold); err != nil {
			add(f+".abandonmentThreshold", "%v", err)
		} else {
			r.abandon = &d
		}
	}
	r.templates = parseTemplates(f+".", r.PRTitle, r.PRHeader, r.PRFooter, add)
}

// parseTemplates parses the prTitle, prHeader and prFooter templates of the top level (f "")
// or a rule (f "rules[i]."); a nil or empty source, or an invalid one, leaves its member nil.
func parseTemplates(f string, title, header, footer *string, add addFunc) prtemplate.Set {
	one := func(key string, src *string, parse func(string) (*prtemplate.Template, error)) *prtemplate.Template {
		if src == nil || *src == "" {
			return nil
		}
		t, err := parse(*src)
		if err != nil {
			add(f+key, "%v", err)
		}
		return t
	}
	return prtemplate.Set{
		Title:  one("prTitle", title, prtemplate.ParseTitle),
		Header: one("prHeader", header, prtemplate.Parse),
		Footer: one("prFooter", footer, prtemplate.Parse),
	}
}

// PRTemplates returns the parsed top-level prTitle, prHeader and prFooter.
func (c *Config) PRTemplates() prtemplate.Set { return c.templates }

// validateAgeBehaviour checks minimumReleaseAgeBehaviour of the top level (f "") or a rule
// (f "rules[i].").
func validateAgeBehaviour(f, b string, add addFunc) {
	if b != "" && !slices.Contains(MinimumReleaseAgeBehaviours, b) {
		add(f+"minimumReleaseAgeBehaviour", "must be one of %s, got %q", list(MinimumReleaseAgeBehaviours), b)
	}
}

// validateMatchGlobs checks the doublestar globs of a rule's names or paths (field), each
// optionally negated with a leading '!'.
func validateMatchGlobs(field string, globs []string, add addFunc) {
	for j, g := range globs {
		pat, neg := strings.CutPrefix(g, "!")
		if neg && pat == "" {
			add(fmt.Sprintf("%s[%d]", field, j), "invalid glob %q: missing pattern after !", g)
			continue
		}
		if _, err := doublestar.Match(pat, ""); err != nil {
			add(fmt.Sprintf("%s[%d]", field, j), "invalid glob %q: %v", g, err)
		}
	}
}

func (cu *Custom) validate(f string, known func(string) bool, add addFunc) {
	if len(cu.Files) == 0 {
		add(f+".files", "must not be empty")
	}
	for j, g := range cu.Files {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("%s.files[%d]", f, j), "invalid glob %q: %v", g, err)
		}
	}
	if cu.Datasource != "" && !known(cu.Datasource) {
		add(f+".datasource", "%s", unknownDatasource(cu.Datasource))
	}
	if cu.Versioning != "" {
		if _, err := versioning.Parse(cu.Versioning); err != nil {
			add(f+".versioning", "%v", err)
		}
	}
	if cu.Match == "" {
		if cu.Checksum != nil {
			add(f+".checksum", "requires match (regex mode)")
		}
		return
	}
	re, err := regexp.Compile(cu.Match)
	if err != nil {
		add(f+".match", "invalid RE2 regexp: %v", err)
		return
	}
	groups := re.SubexpNames()
	if !slices.Contains(groups, "version") && !slices.Contains(groups, "currentValue") {
		add(f+".match", "must have a (?P<version>...) or (?P<currentValue>...) group")
	}
	if cu.Datasource == "" && !slices.Contains(groups, "datasource") {
		add(f+".datasource", "required unless match has a (?P<datasource>...) group")
	}
	if cu.Name == "" && !slices.ContainsFunc(groups, func(g string) bool { return g == "name" || g == "depName" || g == "packageName" }) {
		add(f+".name", "required unless match has a (?P<name>...), (?P<depName>...) or (?P<packageName>...) group")
	}
	hasGroup := slices.Contains(groups, "checksum")
	switch {
	case cu.Checksum != nil && !hasGroup:
		add(f+".checksum", "requires a (?P<checksum>...) group in match")
	case cu.Checksum == nil && hasGroup:
		add(f+".match", "(?P<checksum>...) group requires checksum")
	}
	if cu.Checksum != nil {
		cu.Checksum.validate(f+".checksum", cu.Datasource, add)
	}
}

func list(vs []string) string { return strings.Join(vs, ", ") }

// commitScopeRe is the accepted conventional-commit scope syntax.
var commitScopeRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// checkBranchPrefix returns why prefix cannot start a git branch name (git check-ref-format
// rules for the prefix's components), or "" when it is valid.
func checkBranchPrefix(prefix string) string {
	if !strings.HasSuffix(prefix, "/") {
		return "must end with /"
	}
	if strings.ContainsFunc(prefix, func(r rune) bool { return r < 0x20 || r == 0x7f || strings.ContainsRune(" ~^:?*[\\", r) }) ||
		strings.Contains(prefix, "..") || strings.Contains(prefix, "@{") {
		return "must be a valid git ref prefix (no spaces, control characters, ~^:?*[\\, '..' or '@{')"
	}
	for comp := range strings.SplitSeq(strings.TrimSuffix(prefix, "/"), "/") {
		if comp == "" || strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".lock") {
			return "must be a valid git ref prefix (empty components, components starting with '.' or ending with '.lock')"
		}
	}
	return ""
}
