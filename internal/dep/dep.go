// Package dep holds the types shared by managers, datasources, the engine and the bot.
package dep

import "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"

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
	Manager     string // "github-actions" | "gomod" | "docker" | "custom"
	Datasource  string // "git-tags" | "go" | "go-toolchain" | "docker" | "github-releases"
	Versioning  string // "tag" | "go" | "docker" | "semver"
	DepType     string // gomod: "direct"|"indirect"|"go"|"toolchain"; actions: "action"; docker: "dockerfile"|"compose"|"workflow"|"action"; custom: "annotation"|"regex"
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
}

// Advisory is a security advisory fixed by an update.
type Advisory struct{ ID, Summary, URL string }

// Update is a proposed change to one Dep.
type Update struct {
	Dep        Dep
	Type       UpdateType
	NewVersion string
	NewDigest  string        // "" unless the result is digest-pinned
	Group      string        // policy group name, "" = own PR
	Schedule   *schedule.Set // nil = always in schedule
	Advisories []Advisory    // non-empty = security update
}

// FromTo renders the old and new version; digest-pinned sides are `<version>@<sha7>`.
func (u Update) FromTo() (from, to string) {
	from, to = u.Dep.Version, u.NewVersion
	if u.Dep.Digest != "" {
		from += "@" + u.Dep.Digest[:min(7, len(u.Dep.Digest))]
	}
	if u.NewDigest != "" {
		to += "@" + u.NewDigest[:min(7, len(u.NewDigest))]
	}
	return from, to
}
