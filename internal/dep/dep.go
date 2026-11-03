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
	Manager     string // "github-actions" | "gomod" | "docker" | "npm" | "custom"
	Datasource  string // "git-tags" | "go" | "go-toolchain" | "docker" | "github-releases" | "npm" | "node-version" | "custom.<name>"
	Versioning  string // "tag" | "go" | "docker" | "semver" | "loose" | "regex:<RE2>" (rules[].versioning overrides the manager's)
	DepType     string // gomod: "direct"|"indirect"|"go"|"toolchain"; actions: "action"|"setup"; docker: "dockerfile"|"compose"|"workflow"|"action"; npm: "dependencies"|"devDependencies"|"optionalDependencies"|"peerDependencies"|"packageManager"|"engines"|"volta"; custom: "annotation"|"regex"
	File        string // slash path relative to --dir
	Line        int    // 1-based
	Name        string // actions: owner/repo[/sub]; gomod: module path
	Source      string // actions: https://<host>/<owner>/<repo>; gomod: ""
	Version     string // current version (ref tag, comment version, or module version)
	Digest      string // actions: full SHA when pinned, else ""
	RefSpan     Span   // actions: ref after '@'; gomod: version token
	VersionSpan Span   // actions: version token in trailing comment (pinned only)
	DigestSpan  Span   // docker/custom: digest token, when the digest is stored apart from RefSpan
	InsertAt    int    // manager-defined insert offset (actions: after the uses value incl. closing quote)
	Skip        string // non-empty = not updatable, with the reason

	Checksum       string          // custom: current sha256/sha512 hex pinned next to the version
	ChecksumSpan   Span            // custom: checksum token
	ChecksumSource *ChecksumSource // custom: where the checksum of a new version comes from; nil = none
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
	NewDigest   string        // "" unless the result is digest-pinned
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
}

// FromTo renders the old and new version; digest-pinned sides are `<version>@<digest7>`.
func (u Update) FromTo() (from, to string) {
	from, to = u.Dep.Version, u.NewVersion
	if u.Dep.Digest != "" {
		from += "@" + ShortDigest(u.Dep.Digest)
	}
	if u.NewDigest != "" {
		to += "@" + ShortDigest(u.NewDigest)
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
