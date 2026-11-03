// Package dep holds the types shared by managers, datasources, the engine and the bot.
package dep

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
	Manager       string // "github-actions" | "gomod"
	File          string // slash path relative to --dir
	Line          int    // 1-based
	Name          string // actions: owner/repo[/sub]; gomod: module path
	Source        string // actions: https://<host>/<owner>/<repo>; gomod: ""
	Version       string // current version (ref tag, comment version, or module version)
	Digest        string // actions: full SHA when pinned, else ""
	RefSpan       Span   // actions: ref after '@'; gomod: version token
	VersionSpan   Span   // actions: version token in trailing comment (pinned only)
	CommentInsert int    // actions: offset after the uses value incl. closing quote
	Skip          string // non-empty = not updatable, with the reason
}

// Update is a proposed change to one Dep.
type Update struct {
	Dep        Dep
	Type       UpdateType
	NewVersion string
	NewDigest  string // "" unless the result is digest-pinned
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
