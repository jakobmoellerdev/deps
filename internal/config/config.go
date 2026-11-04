// Package config loads and validates .deps.yaml.
package config

import (
	"bytes"
	"cmp"
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
	"go.yaml.in/yaml/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/customds"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// Pin modes.
const (
	PinKeep   = "keep"
	PinDigest = "digest"
)

// Rebase modes of rebaseWhen.
const (
	RebaseConflicted = "conflicted"
	RebaseBehindBase = "behind-base-branch"
	RebaseNever      = "never"
)

// RebaseWhens are the accepted rebaseWhen values.
var RebaseWhens = []string{RebaseConflicted, RebaseBehindBase, RebaseNever}

// Automerge strategies of automergeStrategy; auto uses the repository's default merge style.
const (
	StrategyAuto   = "auto"
	StrategyMerge  = "merge"
	StrategySquash = "squash"
	StrategyRebase = "rebase"
)

// AutomergeStrategies are the accepted automergeStrategy values.
var AutomergeStrategies = []string{StrategyAuto, StrategyMerge, StrategySquash, StrategyRebase}

// AutomergeTypePR is the only supported automergeType: the platform merges the PR.
const AutomergeTypePR = "pr"

// DefaultDashboardTitle is the dependency dashboard issue title when none is configured.
const DefaultDashboardTitle = "Dependency Dashboard"

// Known values for validation.
var (
	Managers = []string{"github-actions", "gomod", "docker", "npm", "helm", "kustomize", "pre-commit", "gitlabci", "nvm", "nodenv", "asdf", "mise", "custom"}
	DepTypes = []string{"direct", "indirect", "go", "toolchain", "action", "dockerfile", "compose", "workflow", "kubernetes", "helm", "dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "packageManager", "setup", "engines", "volta", "chart", "helmfile", "k0s", "flux", "argocd", "image", "helmChart", "resource", "repository", "service", "include", "component", "node", "tool", "annotation", "regex"}
	// Datasources are the deps datasource names (dep.Dep.Datasource); custom datasources add
	// custom.<name> (CustomDatasourcePrefix).
	Datasources = []string{"git-tags", "github-releases", "go", "go-toolchain", "docker", "npm", "node-version", "helm"}
	// DatasourceAliases maps Renovate datasource names to deps datasource names.
	DatasourceAliases = map[string]string{
		"golang-version":             "go-toolchain",
		"github-tags":                "git-tags",
		"github-release-attachments": "github-releases",
		"node":                       "node-version",
	}
	// DatasourceNames are the accepted datasource names of custom[].datasource and
	// rules[].datasources: Datasources followed by the DatasourceAliases keys (plus
	// custom.<name> of customDatasources, see Config.KnownDatasource).
	DatasourceNames = []string{"git-tags", "github-releases", "go", "go-toolchain", "docker", "npm", "node-version", "helm", "golang-version", "github-tags", "github-release-attachments", "node"}
	// Versionings are the versioning scheme names; rules[].versioning and custom[].versioning
	// also accept regex:<RE2> (versioning.RegexPrefix).
	Versionings = versioning.Names
	// CommitTypes are the accepted conventional-commit types of rules[].commitType.
	CommitTypes = []string{"fix", "chore", "build", "ci", "docs", "feat", "perf", "refactor", "revert", "style", "test"}
)

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
	Version      int      `yaml:"version" jsonschema:"enum=1,default=1" jsonschema_description:"Config format version; must be 1 if set."`
	Pin          string   `yaml:"pin" jsonschema:"enum=keep,enum=digest,default=keep" jsonschema_description:"Default pin mode for actions and docker: keep the current style, or pin to the digest with the version in a comment."`
	Schedule     []string `yaml:"schedule" jsonschema_description:"Cron expressions (5 fields) equal to cron triggers of the workflow running deps: new update PRs are created only in runs triggered by one of them (non-schedule triggers such as workflow_dispatch, push or a local run act immediately); empty means every run."`
	Security     *bool    `yaml:"security" jsonschema:"default=true" jsonschema_description:"Look up OSV advisories and propose security updates that bypass allow, updates and schedule."`
	BranchPrefix string   `yaml:"branchPrefix" jsonschema:"default=deps/" jsonschema_description:"Prefix of update branches (<prefix><manager>/<name>, <prefix>group/<group>); must end with '/'. Only branches and PRs under it are managed."`
	RebaseWhen   string   `yaml:"rebaseWhen" jsonschema:"enum=conflicted,enum=behind-base-branch,enum=never,default=conflicted" jsonschema_description:"When to rebuild an open update PR whose content is unchanged: conflicted (only when not mergeable), behind-base-branch (also when the base branch moved), never (only when its content changes)."`
	Monorepos    *bool    `yaml:"monorepos" jsonschema:"default=true" jsonschema_description:"Group deps released from one source repository (built-in list, e.g. kubernetes, opentelemetry-go, babel) into one PR; rules can override the group."`
	GitAuthor    string   `yaml:"gitAuthor" jsonschema:"default=deps[bot] <deps-bot@noreply.invalid>" jsonschema_description:"Author and committer of deps commits as 'Name <email>' (git identity). Overridden by $DEPS_GIT_AUTHOR and the --author-name/--author-email flags. With a signing key (run --signing-key), use a verified email of the key's owner on the forge, or the signature does not count as verified."`
	// GitIgnoredAuthors are emails whose commits on an update branch count as the bot's own.
	GitIgnoredAuthors []string `yaml:"gitIgnoredAuthors" jsonschema_description:"Commit author emails (case-insensitive) besides the bot's own whose commits on an update branch do not count as modifications: such branches are still updated and rebased."`
	PRConcurrentLimit int      `yaml:"prConcurrentLimit" jsonschema:"minimum=0,default=0" jsonschema_description:"Maximum number of open managed PRs; new PRs are only created below it. 0 means unlimited."`
	PRHourlyLimit     int      `yaml:"prHourlyLimit" jsonschema:"minimum=0,default=0" jsonschema_description:"Maximum number of managed PRs created per hour, counting existing PRs by creation time. 0 means unlimited."`
	Actions           Actions  `yaml:"actions" jsonschema_description:"github-actions manager settings."`
	// MinimumReleaseAge is a string, not a duration, so "28 days" style values are accepted.
	MinimumReleaseAge string   `yaml:"minimumReleaseAge" jsonschema_description:"Minimum age of a new version before it is proposed, e.g. '3 days', '12h', '90m' or a Go duration; '0' disables. Versions with an unknown release time are allowed; digest refresh, pin and security updates are exempt."`
	IgnorePaths       []string `yaml:"ignorePaths" jsonschema_description:"Doublestar globs on slash paths relative to --dir; matching files and directories are not scanned. When the key is absent, the default list (dependency trees, examples and tests) applies; any list (also []) replaces it."`
	// Separate* split the candidates of one dep into PR buckets (Renovate's options of the
	// same names); rules override them.
	SeparateMajorMinor    *bool  `yaml:"separateMajorMinor" jsonschema:"default=true" jsonschema_description:"Propose the newest major update in its own PR (branch suffix -major), apart from the newest minor/patch update; false proposes only the newest update."`
	SeparateMultipleMajor *bool  `yaml:"separateMultipleMajor" jsonschema:"default=false" jsonschema_description:"With separateMajorMinor: one PR per newer major version (branch suffix -v<N>, newest release of each major) instead of one for the newest major."`
	SeparateMinorPatch    *bool  `yaml:"separateMinorPatch" jsonschema:"default=false" jsonschema_description:"With separateMajorMinor: separate PRs for the newest minor (branch suffix -minor) and the newest patch of the current minor (-patch)."`
	UpdateNotScheduled    *bool  `yaml:"updateNotScheduled" jsonschema:"default=true" jsonschema_description:"Keep updating and rebasing open update PRs outside their schedule; only new PRs wait for the schedule window. false leaves out-of-schedule branches untouched."`
	Automerge             bool   `yaml:"automerge" jsonschema:"default=false" jsonschema_description:"Request platform auto-merge for update PRs: the forge merges them once required checks pass. Rules override it, e.g. for minor and patch updates only."`
	AutomergeStrategy     string `yaml:"automergeStrategy" jsonschema:"enum=auto,enum=merge,enum=squash,enum=rebase,default=auto" jsonschema_description:"Merge method of automerged PRs: auto (the repository's default merge style), merge, squash or rebase."`
	AutomergeType         string `yaml:"automergeType" jsonschema:"enum=pr,default=pr" jsonschema_description:"How updates are automerged; only pr (platform auto-merge of the PR) is supported."`
	// DependencyDashboard* configure the dashboard issue (Renovate's :dependencyDashboard).
	DependencyDashboard       bool     `yaml:"dependencyDashboard" jsonschema:"default=false" jsonschema_description:"Maintain a dependency dashboard issue listing pending, blocked and open updates, errors and detected dependencies; its checkboxes make the next run create, recreate or rebase PRs. false leaves an existing dashboard untouched."`
	DependencyDashboardTitle  string   `yaml:"dependencyDashboardTitle" jsonschema:"default=Dependency Dashboard" jsonschema_description:"Title of the dependency dashboard issue; the issue is found by this exact title."`
	DependencyDashboardLabels []string `yaml:"dependencyDashboardLabels" jsonschema_description:"Labels set when the dashboard issue is created (by name; on Gitea and Forgejo they must exist in the repository or its organization)."`
	Rules                     []Rule   `yaml:"rules" jsonschema_description:"Policy rules, evaluated top to bottom; every set field of a matching rule overrides earlier ones."`
	Custom                    []Custom `yaml:"custom" jsonschema_description:"Custom managers: '# deps: <datasource> <name>' annotations, or RE2 regex managers when match is set."`
	// CustomDatasources are referenced as datasource custom.<name>.
	CustomDatasources map[string]CustomDatasource `yaml:"customDatasources" jsonschema_description:"User-defined datasources by name; deps (custom[] entries, annotations, Renovate markers) refer to them as datasource custom.<name>. The version list is fetched once per run."`

	path     string        // source path for error messages
	schedule *schedule.Set // parsed Schedule
	minAge   time.Duration // parsed MinimumReleaseAge
}

// SecurityEnabled reports whether OSV lookups are on (default true).
func (c *Config) SecurityEnabled() bool { return c.Security == nil || *c.Security }

// MonoreposEnabled reports whether the built-in monorepo groups apply (default true).
func (c *Config) MonoreposEnabled() bool { return c.Monorepos == nil || *c.Monorepos }

// UpdateNotScheduledEnabled reports whether open PRs are updated outside their schedule
// (default true).
func (c *Config) UpdateNotScheduledEnabled() bool {
	return c.UpdateNotScheduled == nil || *c.UpdateNotScheduled
}

// DashboardTitle returns DependencyDashboardTitle, or DefaultDashboardTitle when unset.
func (c *Config) DashboardTitle() string {
	return cmp.Or(c.DependencyDashboardTitle, DefaultDashboardTitle)
}

// validateAutomerge checks automergeStrategy and automergeType of the top level (f "") or
// a rule (f "rules[i].").
func validateAutomerge(f, strategy, typ string, add addFunc) {
	if strategy != "" && !slices.Contains(AutomergeStrategies, strategy) {
		add(f+"automergeStrategy", "must be one of %s, got %q", list(AutomergeStrategies), strategy)
	}
	switch typ {
	case "", AutomergeTypePR:
	case "branch":
		add(f+"automergeType", "branch is not supported: deps automerges through the platform's pull request auto-merge; use automergeType: pr")
	default:
		add(f+"automergeType", "must be pr, got %q", typ)
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

// Actions holds github-actions manager settings.
type Actions struct {
	DefaultHost string `yaml:"defaultHost" jsonschema_description:"Host for owner/repo@ref actions; empty means auto-detected from the platform."`
}

// Rule overrides the policy for matching deps.
type Rule struct {
	Manager     string   `yaml:"manager" jsonschema:"enum=github-actions,enum=gomod,enum=docker,enum=npm,enum=helm,enum=kustomize,enum=pre-commit,enum=gitlabci,enum=nvm,enum=nodenv,enum=asdf,enum=mise,enum=custom" jsonschema_description:"Only match deps of this manager."`
	Names       []string `yaml:"names" jsonschema_description:"Only match deps whose name matches any of these doublestar globs ('*' stays within a path segment, '**' crosses segments, '{a,b}' alternates)."`
	DepTypes    []string `yaml:"depTypes" jsonschema:"enum=direct,enum=indirect,enum=go,enum=toolchain,enum=action,enum=dockerfile,enum=compose,enum=workflow,enum=kubernetes,enum=helm,enum=dependencies,enum=devDependencies,enum=optionalDependencies,enum=peerDependencies,enum=packageManager,enum=setup,enum=engines,enum=volta,enum=chart,enum=helmfile,enum=k0s,enum=flux,enum=argocd,enum=image,enum=helmChart,enum=resource,enum=repository,enum=service,enum=include,enum=component,enum=node,enum=tool,enum=annotation,enum=regex" jsonschema_description:"Only match deps of these types."`
	Paths       []string `yaml:"paths" jsonschema_description:"Only match deps whose file (slash path relative to --dir) matches any of these doublestar globs, e.g. 'charts/**'."`
	Datasources []string `yaml:"datasources" jsonschema:"enum=git-tags,enum=github-releases,enum=go,enum=go-toolchain,enum=docker,enum=npm,enum=node-version,enum=helm,enum=golang-version,enum=github-tags,enum=github-release-attachments,enum=node" jsonschema_description:"Only match deps looked up with these datasources, or custom.<name> of customDatasources; Renovate names (golang-version, github-tags, github-release-attachments, node) match their deps datasource."`
	UpdateTypes []string `yaml:"updateTypes" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin" jsonschema_description:"Only match candidate updates of these types: the rule applies once the type of an update is known (e.g. group minor and patch updates, ignore majors). Cannot set pin."`
	Ignore      *bool    `yaml:"ignore" jsonschema_description:"Do not update matching deps; false opts indirect Go requirements, go directives, npm peerDependencies and engines in. With updateTypes: reject updates of these types."`
	Pin         string   `yaml:"pin" jsonschema:"enum=keep,enum=digest" jsonschema_description:"Pin mode for matching deps."`
	Allow       string   `yaml:"allow" jsonschema_description:"New versions must satisfy this Masterminds semver constraint (e.g. '< 7'), or match Renovate's regex form '/<RE2>/' (e.g. '/^\\d+\\.\\d+$/'; '!/<RE2>/' rejects matches; flag i allowed) against the version as written."`
	Updates     []string `yaml:"updates" jsonschema:"enum=major,enum=minor,enum=patch,enum=digest,enum=pin" jsonschema_description:"Allowed update types; replaces the earlier list."`
	// Versioning overrides the dep's scheme before resolution, so it is a pre-policy field.
	Versioning string `yaml:"versioning" jsonschema:"pattern=^(tag|go|docker|semver|loose|regex:.+)$" jsonschema_description:"Versioning scheme overriding the manager's: tag, go, docker, semver, loose, or regex:<RE2> with named groups major (required), minor, patch, build, revision, prerelease and compatibility; (?<name>) and (?P<name>) syntax both work. Cannot be combined with updateTypes."`
	// Separate* override the top-level PR bucket options for matching deps.
	SeparateMajorMinor    *bool    `yaml:"separateMajorMinor" jsonschema_description:"Overrides the top-level separateMajorMinor for matching deps (with updateTypes: for candidates of those types)."`
	SeparateMultipleMajor *bool    `yaml:"separateMultipleMajor" jsonschema_description:"Overrides the top-level separateMultipleMajor for matching deps (with updateTypes: for candidates of those types)."`
	SeparateMinorPatch    *bool    `yaml:"separateMinorPatch" jsonschema_description:"Overrides the top-level separateMinorPatch for matching deps (with updateTypes: for candidates of those types)."`
	Group                 string   `yaml:"group" jsonschema_description:"Group name: all deps with the same group share one PR."`
	CommitType            string   `yaml:"commitType" jsonschema:"enum=fix,enum=chore,enum=build,enum=ci,enum=docs,enum=feat,enum=perf,enum=refactor,enum=revert,enum=style,enum=test" jsonschema_description:"Conventional commit type of commit and PR titles; default fix for production deps (gomod direct, npm dependencies), chore otherwise. Security updates always use fix."`
	CommitScope           *string  `yaml:"commitScope" jsonschema_description:"Conventional commit scope; default deps, empty string means no scope ('chore: ...')."`
	Schedule              []string `yaml:"schedule" jsonschema_description:"Workflow cron triggers overriding the top-level schedule; [] means every run."` // non-nil overrides the top-level schedule; [] = always
	// MinimumReleaseAge overrides the top-level minimumReleaseAge when set; "0" disables it.
	MinimumReleaseAge string `yaml:"minimumReleaseAge" jsonschema_description:"Minimum release age overriding the top-level minimumReleaseAge, e.g. '7 days'; '0' or '0 days' disables it for matching deps."`
	Automerge         *bool  `yaml:"automerge" jsonschema_description:"Overrides the top-level automerge for matching deps (with updateTypes: for updates of those types). A PR is automerged only when all of its updates are."`
	AutomergeStrategy string `yaml:"automergeStrategy" jsonschema:"enum=auto,enum=merge,enum=squash,enum=rebase" jsonschema_description:"Overrides the top-level automergeStrategy for matching deps; a PR uses the strategy of its first update."`
	AutomergeType     string `yaml:"automergeType" jsonschema:"enum=pr" jsonschema_description:"Accepted for Renovate compatibility; only pr is supported."`

	allow    *Allowed
	schedule *schedule.Set
	minAge   *time.Duration // parsed MinimumReleaseAge; nil = not set
}

// Custom is a custom manager: annotation mode when Match is empty, regex mode otherwise.
type Custom struct {
	Files      []string  `yaml:"files" jsonschema:"required,minItems=1" jsonschema_description:"Doublestar globs of the files this manager scans."`
	Match      string    `yaml:"match" jsonschema_description:"RE2 regexp with a (?P<version>) or (?P<currentValue>) group and optional name (or depName/packageName), datasource, digest and checksum groups; empty means annotation mode."`
	Datasource string    `yaml:"datasource" jsonschema:"enum=git-tags,enum=github-releases,enum=go,enum=go-toolchain,enum=docker,enum=npm,enum=node-version,enum=helm,enum=golang-version,enum=github-tags,enum=github-release-attachments,enum=node" jsonschema_description:"Datasource of the matched deps, or custom.<name> of customDatasources; required in regex mode unless match has a (?P<datasource>) group. Renovate names (golang-version, github-tags, github-release-attachments, node) are aliases."`
	Name       string    `yaml:"name" jsonschema_description:"Dependency name; required in regex mode unless match has a (?P<name>), (?P<depName>) or (?P<packageName>) group."`
	Versioning string    `yaml:"versioning" jsonschema:"pattern=^(tag|go|docker|semver|loose|regex:.+)$,default=semver" jsonschema_description:"Versioning scheme of the matched versions: tag, go, docker, semver, loose or regex:<RE2> (see rules[].versioning); annotation and regex-group versioning fields accept the scheme names."` // default semver
	Checksum   *Checksum `yaml:"checksum" jsonschema_description:"Where the checksum of a new version comes from; regex mode only, requires a (?P<checksum>) group in match."`
}

// Checksum locates the sha256/sha512 checksum written next to a version. Its fields are
// templates; {{version}} is the new version exactly as written into the file.
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
type Error struct{ Err error }

func (e *Error) Error() string { return e.Err.Error() }
func (e *Error) Unwrap() error { return e.Err }

// Default returns the configuration used when no file exists.
func Default() *Config {
	return &Config{Version: 1, Pin: PinKeep, BranchPrefix: DefaultBranchPrefix, Monorepos: new(true), IgnorePaths: slices.Clone(DefaultIgnorePaths)}
}

// Load reads explicitPath (which must exist) or, if empty, .deps.yaml then
// .deps.yml in dir, falling back to Default. All failures are *Error.
func Load(dir, explicitPath string) (*Config, error) {
	var f *os.File
	var p string
	if explicitPath != "" {
		p = explicitPath
		var err error
		if f, err = os.Open(p); err != nil {
			return nil, &Error{err}
		}
	} else {
		for _, n := range []string{".deps.yaml", ".deps.yml"} {
			cand := filepath.Join(dir, n)
			h, err := os.Open(cand)
			if err == nil {
				f, p = h, cand
				break
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, &Error{err}
			}
		}
		if f == nil {
			return Default(), nil
		}
	}
	defer f.Close()

	src, err := io.ReadAll(f)
	if err != nil {
		return nil, &Error{fmt.Errorf("%s: %w", p, err)}
	}
	c := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(src))
	dec.KnownFields(true)
	if err := dec.Decode(c); err != nil && !errors.Is(err, io.EOF) {
		return nil, &Error{fmt.Errorf("%s: %w", p, err)}
	}
	if c.IgnorePaths == nil && !hasTopLevelKey(src, "ignorePaths") {
		c.IgnorePaths = slices.Clone(DefaultIgnorePaths)
	}
	c.path = p
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
		return nil, &Error{err}
	}
	return c, nil
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
		errs = append(errs, fmt.Errorf("%s: %s: %s", p, field, fmt.Sprintf(format, a...)))
	}
	if c.Version != 0 && c.Version != 1 {
		add("version", "must be 1, got %d", c.Version)
	}
	if c.Pin != "" && c.Pin != PinKeep && c.Pin != PinDigest {
		add("pin", "must be keep or digest, got %q", c.Pin)
	}
	if c.BranchPrefix != "" {
		if msg := checkBranchPrefix(c.BranchPrefix); msg != "" {
			add("branchPrefix", "%s, got %q", msg, c.BranchPrefix)
		}
	}
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
	validateAutomerge("", c.AutomergeStrategy, c.AutomergeType, add)
	if c.DependencyDashboardTitle != "" && (strings.TrimSpace(c.DependencyDashboardTitle) == "" || strings.ContainsAny(c.DependencyDashboardTitle, "\r\n")) {
		add("dependencyDashboardTitle", "must be a non-blank single line, got %q", c.DependencyDashboardTitle)
	}
	for i, l := range c.DependencyDashboardLabels {
		if strings.TrimSpace(l) == "" || strings.Contains(l, ",") {
			add(fmt.Sprintf("dependencyDashboardLabels[%d]", i), "must be a non-blank label name without ',', got %q", l)
		}
	}
	c.minAge = 0
	if c.MinimumReleaseAge != "" {
		if d, err := ParseAge(c.MinimumReleaseAge); err != nil {
			add("minimumReleaseAge", "%v", err)
		} else {
			c.minAge = d
		}
	}
	c.schedule = parseSchedule("schedule", c.Schedule, add)
	for i, g := range c.IgnorePaths {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("ignorePaths[%d]", i), "invalid glob %q: %v", g, err)
		}
	}
	for i := range c.Rules {
		c.validateRule(i, add)
	}
	for i := range c.Custom {
		c.Custom[i].validate(fmt.Sprintf("custom[%d]", i), c.KnownDatasource, add)
	}
	c.validateCustomDatasources(add)
	return errors.Join(errs...)
}

type addFunc func(field, format string, a ...any)

// parseSchedule validates each expression separately for precise errors and returns the
// parsed set, or nil when exprs is empty or invalid.
func parseSchedule(field string, exprs []string, add addFunc) *schedule.Set {
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
	s, _ := schedule.Parse(exprs) // expressions are valid here
	return s
}

func (c *Config) validateRule(i int, add addFunc) {
	r := &c.Rules[i]
	f := fmt.Sprintf("rules[%d]", i)
	if r.Manager != "" && !slices.Contains(Managers, r.Manager) {
		add(f+".manager", "must be one of %s, got %q", list(Managers), r.Manager)
	}
	for j, n := range r.Names {
		if _, err := doublestar.Match(n, ""); err != nil {
			add(fmt.Sprintf("%s.names[%d]", f, j), "invalid glob %q: %v", n, err)
		}
	}
	for j, t := range r.DepTypes {
		if !slices.Contains(DepTypes, t) {
			add(fmt.Sprintf("%s.depTypes[%d]", f, j), "must be one of %s, got %q", list(DepTypes), t)
		}
	}
	for j, g := range r.Paths {
		if _, err := doublestar.Match(g, ""); err != nil {
			add(fmt.Sprintf("%s.paths[%d]", f, j), "invalid glob %q: %v", g, err)
		}
	}
	for j, ds := range r.Datasources {
		if !c.KnownDatasource(ds) {
			add(fmt.Sprintf("%s.datasources[%d]", f, j), "%s", unknownDatasource(ds))
		}
	}
	if r.UpdateTypes != nil && len(r.UpdateTypes) == 0 {
		add(f+".updateTypes", "must not be empty (omit it to match every update type)")
	}
	for j, u := range r.UpdateTypes {
		if !slices.Contains(dep.UpdateTypes, dep.UpdateType(u)) {
			add(fmt.Sprintf("%s.updateTypes[%d]", f, j), "must be one of major, minor, patch, digest, pin, got %q", u)
		}
	}
	if len(r.UpdateTypes) > 0 && r.Pin != "" {
		add(f+".pin", "cannot be combined with updateTypes: the pin mode is decided before the update type is known")
	}
	if r.Pin != "" && r.Pin != PinKeep && r.Pin != PinDigest {
		add(f+".pin", "must be keep or digest, got %q", r.Pin)
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
	for j, u := range r.Updates {
		if !slices.Contains(dep.UpdateTypes, dep.UpdateType(u)) {
			add(fmt.Sprintf("%s.updates[%d]", f, j), "must be one of major, minor, patch, digest, pin, got %q", u)
		}
	}
	if r.CommitType != "" && !slices.Contains(CommitTypes, r.CommitType) {
		add(f+".commitType", "must be one of %s, got %q", list(CommitTypes), r.CommitType)
	}
	if r.CommitScope != nil && *r.CommitScope != "" && !commitScopeRe.MatchString(*r.CommitScope) {
		add(f+".commitScope", "must match %s, got %q", commitScopeRe, *r.CommitScope)
	}
	validateAutomerge(f+".", r.AutomergeStrategy, r.AutomergeType, add)
	r.schedule = parseSchedule(f+".schedule", r.Schedule, add)
	r.minAge = nil
	if r.MinimumReleaseAge != "" {
		if d, err := ParseAge(r.MinimumReleaseAge); err != nil {
			add(f+".minimumReleaseAge", "%v", err)
		} else {
			r.minAge = &d
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
