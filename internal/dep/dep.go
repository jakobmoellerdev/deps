// Package dep holds the types shared by managers, datasources, the engine and the bot.
package dep

import (
	"strings"

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
	// LockFileMaintenance refreshes a whole lockfile (config lockFileMaintenance): Dep.File is
	// the manifest of the lock root, Dep.Name the lockfile's slash path; there are no versions.
	// It is not in UpdateTypes, so rules cannot select it.
	LockFileMaintenance UpdateType = "lockFileMaintenance"
)

// UpdateTypes lists every update type rules can select, in a stable order.
var UpdateTypes = []UpdateType{Major, Minor, Patch, Digest, Pin}

// The rangeStrategy values (config.RangeStrategies) that change how a manager rewrites a range
// (Update.RangeStrategy).
const (
	RangeWiden = "widen"
	RangePin   = "pin"
)

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

// Dep is a single dependency occurrence extracted from a file.
type Dep struct {
	Manager    string // "github-actions" | "gomod" | "docker" | "npm" | "pip_requirements" | "pep621" | "poetry" | "pipenv" | "helm" | "cargo" | "maven" | "gradle" | "gradle-wrapper" | "maven-wrapper" | "custom"
	Datasource string // "git-tags" | "go" | "go-toolchain" | "docker" | "github-releases" | "npm" | "node-version" | "pypi" | "helm" | "crate" | "maven" | "gradle-version" | "custom.<name>"
	Versioning string // "tag" | "go" | "docker" | "semver" | "pep440" | "loose" | "cargo" | "maven" | "regex:<RE2>" (rules[].versioning overrides the manager's)
	// ExtractVersion is an RE2 regexp with a `version` group mapping release names of the
	// datasource to versions as written (`^jq-(?<version>.+)$`); "" = release names are
	// versions. Set by managers and markers; rules[].extractVersion overrides.
	ExtractVersion string
	DepType        string // gomod: "direct"|"indirect"|"go"|"toolchain"; actions: "action"|"setup"; docker: "dockerfile"|"compose"|"workflow"|"action"|"kubernetes"|"helm"; npm: "dependencies"|"devDependencies"|"optionalDependencies"|"peerDependencies"|"packageManager"|"engines"|"volta"; pip_requirements: "requirements"|"indirect"; pep621: "project.dependencies"|"project.optional-dependencies"|"dependency-groups"|"tool.uv.dev-dependencies"|"tool.pdm.dev-dependencies"; poetry: "dependencies"|"dev-dependencies"|"group"; pipenv: "packages"|"dev-packages"|"category"; helm: "chart"|"helmfile"|"k0s"|"flux"|"argocd"; cargo: "dependencies"|"dev-dependencies"|"build-dependencies"|"workspace.dependencies"; terraform: "required_provider"|"provider"|"module"|"required_version"|"helm_release"|"tfe_workspace"|resource type; terraform-version: "terraform-version"|"opentofu-version"; tflint-plugin: "plugin"; terragrunt: "github"|"gitTags"|"terragrunt"; maven: scope|"parent"|"build"; gradle: "dependencies"|"plugin"; gradle-wrapper: "wrapper"; maven-wrapper: "maven"|"wrapper"; custom: "annotation"|"regex"
	File           string // slash path relative to --dir
	Line           int    // 1-based
	Name           string // actions: owner/repo[/sub]; gomod: module path; helm: chart; maven datasource: group:artifact or a Gradle plugin id
	Source         string // actions: https://<host>/<owner>/<repo>; gomod: ""; helm: repository URL (https:// or oci://); maven datasource: whitespace-separated repositories `[<id>=]<url>/` in lookup order ("" = Maven Central)
	Version        string // current version (ref tag, comment version, or module version)
	Digest         string // actions: full SHA when pinned, else ""
	RefSpan        Span   // actions: ref after '@'; gomod: version token
	NameSpan       Span   // gomod: module path token of a require (rewritten by major module path updates)
	VersionSpan    Span   // actions: version token in trailing comment (pinned), or the version after the per-action tag prefix of a tag ref (`@get-vault-secrets/v1.3.1`)
	DigestSpan     Span   // docker/custom: digest token, when the digest is stored apart from RefSpan
	InputSpan      Span   // actions: `with: version:` value equal to the current version, bumped along with the ref
	InsertAt       int    // manager-defined insert offset (actions: after the uses value incl. closing quote)
	Skip           string // non-empty = not updatable, with the reason
	// VersionRef names the definition the version is read from when several deps can share
	// it (a Maven property, a Gradle catalog version.ref or variable), e.g. "jackson.version";
	// RefSpan then points at the definition, which may be in another file than the dep's
	// declaration (File is the definition's file). Updates of deps sharing one File+RefSpan
	// are grouped into one PR with one version. "" = the version is the dep's own.
	VersionRef string

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
	Locked     string // version the lockfile (package-lock.json, Cargo.lock, uv.lock, poetry.lock, pdm.lock, Pipfile.lock, .terraform.lock.hcl, gradle.lockfile) resolves the dep to; "" = none
	LockUnread bool   // the lock root has a lockfile deps does not read (pnpm-lock.yaml, yarn.lock, package-lock.json v1)

	// Python managers only (datasource pypi).
	ExtraSources     []string // extra simple index URLs searched besides Source (pip --extra-index-url, uv/Poetry/Pipenv/pdm secondary sources); releases are merged
	PythonConstraint string   // the project's Python version constraint (requires-python, Poetry `python`, Pipfile [requires]); releases whose requires-python does not admit all of it are dropped
}

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

// Advisory is a security advisory fixed by an update.
type Advisory struct{ ID, Summary, URL string }

// Update is a proposed change to one Dep.
type Update struct {
	Dep        Dep
	Type       UpdateType
	NewVersion string
	NewDigest  string // "" unless the result is digest-pinned; gomod pseudo-versions: the new commit
	NewName    string // gomod major module path update: the new module path (`<path>/v<N>`); "" = unchanged
	Group      string // policy group name, "" = own PR
	// SecurityGroup is the policy security group of a security update (rules[].securityGroup),
	// "" = own PR; a security update never joins Group (unless it edits a shared version).
	SecurityGroup string
	Schedule      *schedule.Set // nil = always in schedule
	Advisories    []Advisory    // non-empty = security update
	NewChecksum   string        // "" unless Dep.ChecksumSource is set and the version changes
	CommitType    string        // policy conventional-commit type; "" = unset (titles use chore(deps))
	CommitScope   string        // policy commit scope, "" = no scope; only used when CommitType is set
	// Bucket is the PR bucket of the update among the dep's updates (separateMajorMinor and
	// friends): "" for the only bucket or the non-major one, "major", "v<N>" (one per major
	// with separateMultipleMajor), "minor" or "patch" (separateMinorPatch). A non-empty bucket
	// is appended to the branch as "-<bucket>".
	Bucket string
	// Automerge requests automerge of the update's PR with AutomergeStrategy
	// (config.AutomergeStrategies), or of its branch when AutomergeType is branch
	// (config.AutomergeTypes); a PR or branch automerges only when all of its updates do.
	Automerge         bool
	AutomergeStrategy string
	AutomergeType     string
	// Lockfile marks a lockfile-only update (rangeStrategy update-lockfile): the range admits
	// NewVersion, so the manifest stays unchanged and only the lockfile moves from Dep.Locked.
	Lockfile bool
	// RangeStrategy is the rangeStrategy the range (Dep.Range) is rewritten under (a
	// config.RangeStrategies value other than auto, see manager.RangeRewriter); "" = the
	// manager's default rewrite, and for deps without a range. Managers tell RangeWiden and
	// RangePin apart; every other value is their default rewrite.
	RangeStrategy string
	// Labels, Reviewers and Assignees go on the PR when it is created; a group's PR gets the
	// union over its updates.
	Labels, Reviewers, Assignees []string
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
