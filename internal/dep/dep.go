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
)

// UpdateTypes lists every update type in a stable order.
var UpdateTypes = []UpdateType{Major, Minor, Patch, Digest, Pin}

// Span is a byte range in a file; End is exclusive.
type Span struct{ Start, End int }

// Dep is a single dependency occurrence extracted from a file.
type Dep struct {
	Manager     string // "github-actions" | "gomod" | "docker" | "npm" | "helm" | "cargo" | "custom"
	Datasource  string // "git-tags" | "go" | "go-toolchain" | "docker" | "github-releases" | "npm" | "node-version" | "helm" | "crate" | "custom.<name>"
	Versioning  string // "tag" | "go" | "docker" | "semver" | "loose" | "regex:<RE2>" (rules[].versioning overrides the manager's)
	DepType     string // gomod: "direct"|"indirect"|"go"|"toolchain"; actions: "action"|"setup"; docker: "dockerfile"|"compose"|"workflow"|"action"|"kubernetes"|"helm"; npm: "dependencies"|"devDependencies"|"optionalDependencies"|"peerDependencies"|"packageManager"|"engines"|"volta"; helm: "chart"|"helmfile"|"k0s"|"flux"|"argocd"; cargo: "dependencies"|"dev-dependencies"|"build-dependencies"|"workspace.dependencies"; custom: "annotation"|"regex"
	File        string // slash path relative to --dir
	Line        int    // 1-based
	Name        string // actions: owner/repo[/sub]; gomod: module path; helm: chart
	Source      string // actions: https://<host>/<owner>/<repo>; gomod: ""; helm: repository URL (https:// or oci://)
	Version     string // current version (ref tag, comment version, or module version)
	Digest      string // actions: full SHA when pinned, else ""
	RefSpan     Span   // actions: ref after '@'; gomod: version token
	NameSpan    Span   // gomod: module path token of a require (rewritten by major module path updates)
	VersionSpan Span   // actions: version token in trailing comment (pinned only)
	DigestSpan  Span   // docker/custom: digest token, when the digest is stored apart from RefSpan
	InputSpan   Span   // actions: `with: version:` value equal to the current version, bumped along with the ref
	InsertAt    int    // manager-defined insert offset (actions: after the uses value incl. closing quote)
	Skip        string // non-empty = not updatable, with the reason

	Checksum       string          // custom: current sha256/sha512 hex pinned next to the version
	ChecksumSpan   Span            // custom: checksum token
	ChecksumSource *ChecksumSource // custom: where the checksum of a new version comes from; nil = none

	// npm dependency sections only; rangeStrategy applies to deps with a Range.
	Range      string // the range as written in package.json (^1.2.3); Version is its bare version
	Locked     string // version the lock root's package-lock.json resolves the dep to; "" = none
	LockUnread bool   // the lock root has a lockfile deps does not read (pnpm-lock.yaml, yarn.lock, package-lock.json v1)
}

// ChecksumSource locates the checksum of a version. Asset and URL/File are templates in which
// `{{version}}` is the new version as written into the file; exactly one of Asset and URL is set.
type ChecksumSource struct {
	Asset string // github-releases: release asset whose API digest is used
	URL   string // checksums document to download
	File  string // entry name in a sha256sum-style URL document; "" = first hex token
}

// ExpandVersion substitutes `{{version}}` in tmpl.
func ExpandVersion(tmpl, version string) string {
	return strings.ReplaceAll(tmpl, "{{version}}", version)
}

// Advisory is a security advisory fixed by an update.
type Advisory struct{ ID, Summary, URL string }

// Update is a proposed change to one Dep.
type Update struct {
	Dep         Dep
	Type        UpdateType
	NewVersion  string
	NewDigest   string        // "" unless the result is digest-pinned; gomod pseudo-versions: the new commit
	NewName     string        // gomod major module path update: the new module path (`<path>/v<N>`); "" = unchanged
	Group       string        // policy group name, "" = own PR
	Schedule    *schedule.Set // nil = always in schedule
	Advisories  []Advisory    // non-empty = security update
	NewChecksum string        // "" unless Dep.ChecksumSource is set and the version changes
	CommitType  string        // policy conventional-commit type; "" = unset (titles use chore(deps))
	CommitScope string        // policy commit scope, "" = no scope; only used when CommitType is set
	// Bucket is the PR bucket of the update among the dep's updates (separateMajorMinor and
	// friends): "" for the only bucket or the non-major one, "major", "v<N>" (one per major
	// with separateMultipleMajor), "minor" or "patch" (separateMinorPatch). A non-empty bucket
	// is appended to the branch as "-<bucket>".
	Bucket string
	// Automerge requests platform auto-merge of the update's PR with AutomergeStrategy
	// (config.AutomergeStrategies); a PR automerges only when all of its updates do.
	Automerge         bool
	AutomergeStrategy string
	// Lockfile marks a lockfile-only update (rangeStrategy update-lockfile): the range admits
	// NewVersion, so the manifest stays unchanged and only the lockfile moves from Dep.Locked.
	Lockfile bool
}

// FromTo renders the old and new version; digest-pinned sides are `<version>@<digest7>`
// (except gomod: its only digests are those of pseudo-versions, which end in their commit),
// and a new module path is rendered `<path>@<version>`. A lockfile-only update starts at the
// locked version.
func (u Update) FromTo() (from, to string) {
	from, to = u.Dep.Version, u.NewVersion
	if u.Lockfile {
		from = u.Dep.Locked
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
