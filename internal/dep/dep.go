// Package dep holds the types shared by managers, datasources, the engine and the bot.
package dep

import (
	"fmt"
	"math"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/prtemplate"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

// UpdateType classifies an update.
type UpdateType string

const (
	Major  UpdateType = "major"
	Minor  UpdateType = "minor"
	Patch  UpdateType = "patch"
	Digest UpdateType = "digest"
	Pin    UpdateType = "pin"
	// Replacement moves a dep to another package (config replacementName, the built-in
	// replacements): NewName is the new name, NewVersion its version (the current one unless
	// replacementVersion is set). It replaces the dep's usual updates (Update.Declined). Rules
	// select it with updateTypes; it is not in UpdateTypes, so allowedUpdateTypes lists never drop it.
	Replacement UpdateType = "replacement"
	// LockFileMaintenance refreshes a whole lockfile (config lockFileMaintenance): Dep.File is
	// the manifest of the lock root, Dep.Name the lockfile's slash path; there are no versions.
	// It is not in UpdateTypes, so rules cannot select it.
	LockFileMaintenance UpdateType = "lockFileMaintenance"
)

// UpdateTypes lists the version update types rules can allow (updates) and select
// (updateTypes), in a stable order; updateTypes also accepts Replacement.
var UpdateTypes = []UpdateType{Major, Minor, Patch, Digest, Pin}

// The rangeStrategy values (config.RangeStrategies) that change how a manager rewrites a range
// (Update.RangeStrategy).
const (
	RangeWiden = "widen"
	RangePin   = "pin"
)

// Roles of a NamePart.
const (
	NameWhole      = ""           // the whole name
	NameRegistry   = "registry"   // the registry host of an image name (a Helm values registry key)
	NameRepository = "repository" // the image name after the registry (a Helm values repository key)
	NameGroup      = "group"      // the groupId of Maven coordinates group:artifact (a pom <groupId>)
	NameArtifact   = "artifact"   // the artifactId of Maven coordinates (a pom <artifactId>)
)

// NamePart is where a dep's name, or a part of it (Role), is written: Text as written at Span.
type NamePart struct {
	Span Span
	Text string
	Role string
}

// Span is a byte range in a file; End is exclusive.
type Span struct{ Start, End int }

// SkipError reports a dependency or an update that cannot be handled for a reason that is not
// a failure: retrying cannot help, the cause is upstream or in the repository's own
// constraints. A datasource returns it when a dep cannot be looked up (a Cargo registry name
// no configuration defines), a post-processor when an update cannot be applied (the new
// version of a Go module declares another module path). The engine and the bot report it as
// a skip with Reason, not as an error.
type SkipError struct{ Reason string }

func (e *SkipError) Error() string { return e.Reason }

// SkipUnknownRegistry is the skip reason of a dep whose manager cannot tell which registry it
// is looked up in (a Dockerfile `apk add` pin under an unknown base image, every `apt-get
// install` pin); a rule's registryUrls supplies one and lifts the skip.
const SkipUnknownRegistry = "unknown registry"

// Dep is a single dependency occurrence extracted from a file.
type Dep struct {
	Manager    string // "github-actions" | "gomod" | "docker" | "npm" | "pip_requirements" | "pep621" | "pep723" | "setup-cfg" | "pip_setup" | "poetry" | "pipenv" | "helm" | "cargo" | "maven" | "gradle" | "gradle-wrapper" | "maven-wrapper" | "git-submodules" | "devcontainer" | "bitbucket-pipelines" | "woodpecker" | "droneci" | "circleci" | "azure-pipelines" | "vcpkg" | "custom"
	Datasource string // "git-tags" | "git-refs" | "go" | "go-toolchain" | "docker" | "github-releases" | "npm" | "node-version" | "pypi" | "helm" | "crate" | "maven" | "gradle-version" | "orb" | "azure-pipelines-tasks" | "vcpkg" | "custom.<name>"
	Versioning string // "tag" | "go" | "docker" | "semver" | "pep440" | "loose" | "cargo" | "maven" | "regex:<RE2>" (rules[].versioning overrides the manager's)
	// ExtractVersion is an RE2 regexp with a `version` group mapping release names of the
	// datasource to versions as written (`^jq-(?<version>.+)$`); "" = release names are
	// versions. Set by managers and markers; rules[].extractVersion overrides.
	ExtractVersion string
	DepType        string // gomod: "direct"|"indirect"|"go"|"toolchain"; actions: "action"|"setup"|"github-runner"; docker: "dockerfile"|"compose"|"workflow"|"action"|"kubernetes"|"helm"; npm: "dependencies"|"devDependencies"|"optionalDependencies"|"peerDependencies"|"packageManager"|"engines"|"volta"|"overrides"|"resolutions"|"pnpm.overrides"; pip_requirements: "requirements"|"indirect"; pep621: "project.dependencies"|"project.optional-dependencies"|"dependency-groups"|"tool.uv.dev-dependencies"|"tool.pdm.dev-dependencies"|"build-system.requires"|"tool.uv.constraint-dependencies"|"tool.uv.override-dependencies"|"tool.uv.build-constraint-dependencies"; pep723: "project.dependencies"; setup-cfg: "install"|"setup"|"test"|"extra"; pip_setup: "install"|"setup"|"test"|"extra"|"other"; poetry: "dependencies"|"dev-dependencies"|"group"; pipenv: "packages"|"dev-packages"|"category"; helm: "chart"|"helmfile"|"k0s"|"flux"|"argocd"; cargo: "dependencies"|"dev-dependencies"|"build-dependencies"|"workspace.dependencies"; terraform: "required_provider"|"provider"|"module"|"required_version"|"helm_release"|"tfe_workspace"|resource type; terraform-version: "terraform-version"|"opentofu-version"; tflint-plugin: "plugin"; terragrunt: "github"|"gitTags"|"terragrunt"; maven: scope|"parent"|"build"; gradle: "dependencies"|"plugin"; gradle-wrapper: "wrapper"; maven-wrapper: "maven"|"wrapper"; git-submodules: "submodule"; devcontainer: "image"|"feature"|"feature-version"; custom: "annotation"|"regex"
	File           string // slash path relative to --dir
	Line           int    // 1-based
	Name           string // actions: owner/repo[/sub]; gomod: module path; helm: chart; maven datasource: group:artifact or a Gradle plugin id
	Source         string // actions: https://<host>/<owner>/<repo>; gomod: ""; helm: repository URL (https:// or oci://); maven datasource: whitespace-separated repositories `[<id>=]<url>/` in lookup order ("" = Maven Central)
	Version        string // current version (ref tag, comment version, or module version)
	Digest         string // actions: full SHA when pinned, else ""
	RefSpan        Span   // actions: ref after '@'; gomod: version token
	// NameParts locate the name as written, which major module path updates and replacements
	// rewrite (manager.NameEdits, or the manager's own Edits for roles it defines): one
	// NameWhole part for most managers, registry/repository parts for images whose registry is
	// written apart (Helm values), group/artifact parts for Maven coordinates written apart
	// (pom elements, Gradle maps); nil = the name cannot be rewritten.
	NameParts   []NamePart
	VersionSpan Span   // actions: version token in trailing comment (pinned), or the version after the per-action tag prefix of a tag ref (`@get-vault-secrets/v1.3.1`)
	DigestSpan  Span   // docker/custom: digest token, when the digest is stored apart from RefSpan
	InputSpan   Span   // actions: `with: version:` value equal to the current version, bumped along with the ref
	InsertAt    int    // manager-defined insert offset (actions: after the uses value incl. closing quote)
	Skip        string // non-empty = not updatable, with the reason
	// VersionRef names the definition the version is read from when several deps can share
	// it (a Maven property, a Gradle catalog version.ref or variable, custom entries matching
	// one version with a checksum each: their dep name), e.g. "jackson.version";
	// RefSpan then points at the definition, which may be in another file than the dep's
	// declaration (File is the definition's file). Updates of deps sharing one File+RefSpan
	// are grouped into one PR with one version. "" = the version is the dep's own.
	VersionRef string
	// Gitlink is the slash path (relative to --dir) of the submodule whose commit, recorded in
	// the git index, is the dep's Digest (git-submodules); an update moves it to NewDigest.
	// "" = none.
	Gitlink string
	// Together marks deps that move only as a whole: the downloads of a Homebrew formula or a
	// Nix flake (per-platform urls with their checksums) and the statements following them
	// (mirrors, version attributes), and custom deps sharing one version (VersionRef) with a
	// digest or checksum each. An update of a Together dep to a new version is proposed
	// only when every Together dep with its Manager, File and Name that is not skipped has an
	// update to that version; otherwise none of them is, and the engine reports the group's
	// failure (one error per group and version) besides the errors of the deps that failed.
	Together bool

	Checksum       string          // custom: current sha256/sha512 hex pinned next to the version
	ChecksumSpan   Span            // custom: checksum token
	ChecksumSource *ChecksumSource // custom: where the checksum of a new version comes from; nil = none

	// npm and cargo dependency entries, Python specifiers, terraform constraints and Gradle dynamic
	// versions; rangeStrategy applies to deps with a Range.
	Range string // the range as written (npm ^1.2.3, cargo ">=1.2, <2"; Python >=1.2,<2, RefSpan its bytes; terraform ~> 5.0); Version is its bare (npm, cargo), anchor (Python) or first (terraform) version. gradle: the Masterminds constraint of a dynamic version (1.+ → ">=1.0.0-0 <2.0.0-0"), Version is the locked version
	// Constraint is Range as a Masterminds semver constraint when the syntax differs (cargo's
	// bare "1.2" means ^1.2); "" = Range is one (or the versioning scheme matches it itself).
	Constraint string
	RangeFixed bool   // the range is never rewritten (terraform `>= 1.0, < 2.0`): only Locked may move within it
	Locked     string // version the lockfile (package-lock.json, npm-shrinkwrap.json, pnpm-lock.yaml, yarn.lock, bun.lock, Cargo.lock, uv.lock, poetry.lock, pdm.lock, Pipfile.lock, .terraform.lock.hcl, gradle.lockfile) resolves the dep to; "" = none
	LockUnread bool   // the lock root's lockfile cannot be read (it does not parse; a package-lock.json without lockfileVersion)

	// Python managers only (datasource pypi).
	ExtraSources     []string // extra simple index URLs searched besides Source (pip --extra-index-url, uv/Poetry/Pipenv/pdm secondary sources); releases are merged
	PythonConstraint string   // the project's Python version constraint (requires-python, Poetry `python`, Pipfile [requires]); releases whose requires-python does not admit all of it are dropped
	DefinedIn        string   // pep621, poetry: slash path of a scanned pyproject.toml whose project has the dep's (normalized) name; when no index has the package, the dep is skipped (pyreq.InRepository) instead of failing

	// Swift manager only. ExactLabel: the `.package` call can take an `exact:` requirement
	// (swift-tools-version 5.6 or later, no `name:` argument), which rangeStrategy pin writes;
	// otherwise it writes `.exact(…)`.
	ExactLabel bool

	// composer manager only. AutoWiden: rangeStrategy auto widens the constraint, as
	// Renovate's composer manager does for a composer.json of `"type": "typo3-cms-extension"`.
	AutoWiden bool

	// sbt manager only (sbt-plugin datasource). SbtVersion: the sbt version of the build's
	// project/build.properties, which selects the cross-version a plugin is published for
	// (sbt 1.x: Scala 2.12 / sbt 1.0; sbt 2.x: sbt2 / Scala 3; sbt 0.13: Scala 2.10 / sbt
	// 0.13); "" = sbt 1.x.
	SbtVersion string

	// vcpkg manager only (vcpkg datasource). Baseline: the key (vcpkg.BaselineKey) of the
	// updatable registry baseline at which Source reads the port's versions; when that
	// baseline moves in the same run, the port is looked up at its new commit. "" = none.
	Baseline string
}

// Renamable reports whether d's name can be rewritten in its file (NameParts are set).
func (d Dep) Renamable() bool { return len(d.NameParts) > 0 }

// ChecksumSource locates the checksum of a version. Asset, URL/File and Artifact are templates
// in which `{{version}}` is the new version as written into the file; exactly one of Asset, URL
// and Artifact is set.
type ChecksumSource struct {
	Asset string // github-releases: release asset whose API digest is used
	URL   string // checksums document to download
	File  string // entry name in a sha256sum-style URL document; "" = first hex token
	// Artifact is the URL template of the artifact itself: the checksum is its digest
	// (sha256/sha512 by the pinned hex length), computed while streaming the download.
	Artifact string
	// Attachment (Renovate's github-release-attachments digests): the checksum is the sha256
	// of the release asset whose digest equals the current checksum, under the new version's
	// name of that asset.
	Attachment bool
}

// ExpandVersion substitutes `{{version}}` in tmpl.
func ExpandVersion(tmpl, version string) string {
	return strings.ReplaceAll(tmpl, "{{version}}", version)
}

// Advisory is a security advisory fixed by an update. Besides the advisory itself it carries
// the risk data the triage sources add (see internal/datasource/triage): the CVSS base score,
// the EPSS probability of exploitation and whether CISA lists the vulnerability as known
// exploited.
type Advisory struct {
	ID, Summary, URL string
	// Aliases are the other IDs of the same vulnerability (CVE-…, GHSA-…), as the advisory
	// lists them; the CVE aliases key the EPSS and KEV lookups.
	Aliases []string
	// CVSS is the base score (0.1–10) of the advisory's own CVSS vector (v4 when it has one,
	// else v3), or deps.dev's v3 score when the record carries no vector; 0 = unknown.
	CVSS float64
	// Rating is the advisory's own qualitative severity ("critical", "high", "medium",
	// "low"), used when no score is known at all.
	Rating string
	EPSS   float64 // probability of exploitation within 30 days (0–1), 0 = unknown
	KEV    bool    // listed in CISA's Known Exploited Vulnerabilities catalog
}

// Severity is the qualitative rating of the advisory ("critical", "high", "medium", "low"):
// of the CVSS v3 base score when it is known, else the advisory's own rating. "" when neither
// is known.
func (a Advisory) Severity() string {
	switch {
	case a.CVSS >= 9:
		return "critical"
	case a.CVSS >= 7:
		return "high"
	case a.CVSS >= 4:
		return "medium"
	case a.CVSS > 0:
		return "low"
	}
	return a.Rating
}

// Risk renders the risk data as one phrase ("CVSS 9.8 critical, EPSS 94%, CISA KEV"), "" when
// no source knew anything about the advisory.
func (a Advisory) Risk() string {
	var parts []string
	switch s := a.Severity(); {
	case s == "":
	case a.CVSS > 0:
		parts = append(parts, fmt.Sprintf("CVSS %s %s", strconv.FormatFloat(a.CVSS, 'f', -1, 64), s))
	default:
		parts = append(parts, "severity "+s)
	}
	if a.EPSS > 0 {
		parts = append(parts, "EPSS "+epssPercent(a.EPSS))
	}
	if a.KEV {
		parts = append(parts, "CISA KEV")
	}
	return strings.Join(parts, ", ")
}

// epssPercent renders an EPSS probability as a percentage, with a decimal below 1% and "<0.1%"
// for the smallest scores, which most advisories have.
func epssPercent(p float64) string {
	switch pct := p * 100; {
	case pct < 0.1:
		return "<0.1%"
	case pct < 10:
		return strconv.FormatFloat(math.Round(pct*10)/10, 'f', -1, 64) + "%"
	default:
		return strconv.Itoa(int(math.Round(pct))) + "%"
	}
}

// severityRank orders the qualitative ratings; an unknown or empty rating ranks lowest.
func severityRank(s string) int {
	switch s {
	case "critical":
		return 4
	case "high":
		return 3
	case "medium":
		return 2
	case "low":
		return 1
	}
	return 0
}

// Worst summarizes advs for a one-line report: the highest severity among them (from the CVSS
// v3 score or the advisory's own rating) and whether CISA lists any of them as known exploited
// ("critical, known exploited"); "" when no risk data is known.
func Worst(advs []Advisory) string {
	top := ""
	kev := false
	for _, a := range advs {
		if s := a.Severity(); severityRank(s) > severityRank(top) {
			top = s
		}
		kev = kev || a.KEV
	}
	var parts []string
	if top != "" {
		parts = append(parts, top)
	}
	if kev {
		parts = append(parts, "known exploited")
	}
	return strings.Join(parts, ", ")
}

// Update is a proposed change to one Dep.
type Update struct {
	Dep        Dep
	Type       UpdateType
	NewVersion string
	NewDigest  string // "" unless the result is digest-pinned; gomod pseudo-versions: the new commit
	NewName    string // gomod major module path update: the new module path (`<path>/v<N>`); replacement: the new package name; "" = unchanged
	Group      string // policy group name, "" = own PR
	// SecurityGroup is the policy security group of a security update (rules[].securityGroup),
	// "" = own PR; a security update never joins Group (unless it edits a shared version).
	SecurityGroup string
	Schedule      *schedule.Set // nil = always in schedule
	Advisories    []Advisory    // non-empty = security update
	NewChecksum   string        // "" unless Dep.ChecksumSource is set and the version changes
	// PRSettings are the PR settings of the update's policy (config.Policy).
	PRSettings
	// Bucket is the PR bucket of the update among the dep's updates (the config's
	// separate): "" for the only bucket or the non-major one, "major", "v<N>" (one per major
	// with majors), "minor" or "patch" (minor-patch). A non-empty bucket
	// is appended to the branch as "-<bucket>".
	Bucket string
	// BranchTopicPrefix is the expanded additionalBranchPrefix of the update's policy: it
	// sits between the branch prefix and the topic (dependency name or group/<group>), and
	// updates with different ones never share a branch. Lock file maintenance ignores it.
	BranchTopicPrefix string
	// Lockfile marks a lockfile-only update (rangeStrategy update-lockfile): the range admits
	// NewVersion, so the manifest stays unchanged and only the lockfile moves from Dep.Locked.
	Lockfile bool
	// RangeStrategy is the rangeStrategy the range (Dep.Range) is rewritten under (a
	// config.RangeStrategies value other than auto, see manager.RangeRewriter); "" = the
	// manager's default rewrite, and for deps without a range. Managers tell RangeWiden and
	// RangePin apart; every other value is their default rewrite.
	RangeStrategy string
	// Declined holds, for a replacement, the dep's usual updates: `run` proposes them instead
	// once the replacement's PR was closed without merging. plan and apply ignore them.
	Declined []Update
	// Warnings flag the dep as deprecated or abandoned ("deprecated: package deprecated: …",
	// "abandoned: no release since …"); the PR body lists them.
	Warnings []string
}

// PRSettings are the settings of a policy (config.Policy) for the PR of an update, which
// carries them (Update.PRSettings).
type PRSettings struct {
	// CommitType is the conventional-commit type: fix for production deps, chore otherwise;
	// "" = unset (titles use chore(deps)). CommitScope is the commit scope, "" = none; only
	// used when CommitType is set.
	CommitType, CommitScope string
	// Templates are the PR title, header and footer templates (prTitle, prHeader,
	// prFooter); nil members use the defaults. A group's PR uses its first update's.
	Templates prtemplate.Set
	// Automerge requests automerge of the update's PR with AutomergeStrategy
	// (config.AutomergeStrategies), or of its branch when AutomergeType is branch
	// (config.AutomergeTypes); a PR or branch automerges only when all of its updates do.
	Automerge         bool
	AutomergeStrategy string
	AutomergeType     string
	// DashboardApproval holds back a new PR (or automerge branch) of the update until it is
	// approved on the dependency dashboard (config dashboardApproval); security updates never
	// need approval. A PR needs approval when any of its updates does.
	DashboardApproval bool
	// DraftPR opens the update's new PR as a draft; a group's PR is one when any of its
	// updates asks. PRCreation (config.PRCreations) is when the PR of a pushed branch is
	// opened; a group waits as long as its most waiting update asks. "" = immediate.
	DraftPR    bool
	PRCreation string
	// Labels, Reviewers and Assignees go on the PR when it is created; a group's PR gets the
	// union over its updates.
	Labels, Reviewers, Assignees []string
	// Tasks are the postUpgradeTasks (of the last matching rule that sets them) run after
	// the update is built (apply, run).
	Tasks Tasks
}

// Tasks are postUpgradeTasks: shell commands, run once per branch, or with Update once per
// update of the branch (executionMode update) with DEPS_* variables describing it. Files
// (config.MatchGlobs, relative to --dir) limit the changed files they contribute; none = all.
type Tasks struct {
	Commands []string
	Update   bool
	Files    []string
}

// Maintenance reports whether u is a lock file maintenance update.
func (u Update) Maintenance() bool { return u.Type == LockFileMaintenance }

// FromTo renders the old and new version; digest-pinned sides are `<version>@<digest7>`
// (except gomod: its only digests are those of pseudo-versions, which end in their commit),
// and a new module path is rendered `<path>@<version>`. A lockfile-only update starts at the
// locked version, the pin of a range (rangeStrategy pin) at the range.
func (u Update) FromTo() (from, to string) {
	from, to = u.Dep.Version, u.NewVersion
	switch {
	case u.Lockfile:
		from = u.Dep.Locked
	case u.Type == Pin && u.NewDigest == "" && u.Dep.Range != "":
		from = u.Dep.Range
	}
	if u.Dep.Digest != "" {
		from += "@" + ShortDigest(u.Dep.Digest)
	}
	if u.NewDigest != "" && u.Dep.Manager != "gomod" {
		to += "@" + ShortDigest(u.NewDigest)
	}
	if u.NewName != "" {
		to = u.NewName + "@" + to
	}
	return from, to
}

// ShortDigest returns the first 7 hex characters of a git SHA or an OCI digest
// ("sha256:abc…" → "abc…" cut to 7).
func ShortDigest(d string) string {
	if i := strings.IndexByte(d, ':'); i >= 0 {
		d = d[i+1:]
	}
	return d[:min(7, len(d))]
}
