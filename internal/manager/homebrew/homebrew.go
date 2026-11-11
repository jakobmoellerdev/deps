// Package homebrew updates the downloads of Homebrew formulae (Formula/*.rb): every `url` with
// the `sha256` after it, of the stable download (also per platform in `on_macos`, `on_linux`,
// `on_arm`, `on_intel` blocks) and of each `resource`, their `mirror`s and the `version`
// statement. Downloads are GitHub, Gitea/Forgejo and GitLab tag archives and release assets,
// and npm registry tarballs; `head`, `livecheck`, `patch`, `bottle`, `test` and method bodies
// are not downloads to update.
package homebrew

import (
	"bytes"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/download"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "homebrew"

// Dep types.
const (
	// DepType is a `url` of the formula's stable download, outside resource blocks.
	DepType = "formula"
	// DepTypeResource is the `url` of a `resource` block.
	DepTypeResource = "resource"
	// DepTypeMirror is a `mirror` of a download: its version moves with the download's.
	DepTypeMirror = "mirror"
	// DepTypeVersion is a `version "…"` statement equal to the version of a download in the
	// same block: it moves with that download.
	DepTypeVersion = "version"
)

// Skip reasons.
const (
	SkipUnsupported = "unsupported url"
	SkipNoSHA256    = "invalid sha256"
)

var (
	// matchRe is Renovate's managerFilePatterns: Formula/<name>.rb, also sharded
	// (Formula/<letter>/<name>.rb as in homebrew-core).
	matchRe = regexp.MustCompile(`^Formula/\w*/?[^/]+\.rb$`)
	// stmtRe is a `url`, `mirror`, `sha256` or `version` statement with a string literal
	// (group 1 the keyword, 2 the string); `url :stable` and the `sha256 cellar: …` lines of a
	// bottle block are none.
	stmtRe = regexp.MustCompile(`^[ \t]*(url|mirror|sha256|version)[ \t]+"([^"]*)"`)
	// doRe is a line opening a `do` block, wordRe the first word of a line (the block's method).
	doRe   = regexp.MustCompile(`\bdo(?:[ \t]*\|[^|]*\|)?[ \t]*(?:#.*)?$`)
	wordRe = regexp.MustCompile(`^[ \t]*([a-z_]\w*[!?]?)`)
	// keywordRe is a line opening a keyword block that `end` closes, also as an assigned value
	// (`v = if …`); group 1 the keyword. oneLineRe is such a line closing it again, endlessRe an
	// endless method (`def x = …`).
	keywordRe = regexp.MustCompile(`^[ \t]*(?:[\w.@:\[\]]+[ \t]*(?:\|\||&&)?=[ \t]*)?(class|module|def|if|unless|case|while|until|begin)\b`)
	oneLineRe = regexp.MustCompile(`(?:;|\bthen\b|\bdo\b|\)).*\bend[ \t]*(?:#.*)?$`)
	endlessRe = regexp.MustCompile(`^[ \t]*def[ \t]+[\w.?!]+(?:\([^)]*\))?[ \t]*=[^=~>]`)
	endRe     = regexp.MustCompile(`^[ \t]*end\b`)
	// heredocRe is a heredoc start; group 2 its terminator.
	heredocRe = regexp.MustCompile(`<<[~-]?(['"]?)([A-Za-z_]\w*)['"]?`)
)

// ignored are the blocks (by method or keyword) whose statements are no downloads to update:
// head builds from a branch, livecheck says where to look for versions, patches are pinned,
// bottles are built by Homebrew, and method bodies (`def`) and `test` run code.
var ignored = []string{"head", "livecheck", "patch", "bottle", "test", "def", "service"}

type homebrew struct{}

// New returns the homebrew manager.
func New() manager.Manager { return homebrew{} }

func (homebrew) Name() string { return Name }

// Match reports whether the slash path is a formula: Formula/<name>.rb at the repository root,
// optionally one directory deeper. The prefix check skips the regexp for most paths.
func (homebrew) Match(path string) bool {
	return strings.HasPrefix(path, "Formula/") && strings.HasSuffix(path, ".rb") && matchRe.MatchString(path)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (homebrew) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".rb"}}
}

// stmt is a `url`, `mirror`, `sha256` or `version` statement: its keyword, string value, the
// offset of the value in the file and its 1-based line.
type stmt struct {
	kind, value string
	at, line    int
}

// scope is the statements of the formula's stable download (dep type DepType) or of one
// resource (DepTypeResource), in file order.
type scope struct {
	depType string
	stmts   []stmt
}

// scan splits src into scopes: the formula's own statements, also inside `stable`, `on_*`
// and other blocks, and one scope per `resource` block. Statements in ignored blocks are
// dropped. Blocks are tracked by `do`/keyword lines and `end`; heredoc bodies are skipped.
func scan(src []byte) []scope {
	type frame struct {
		ignored  bool
		resource int // index of the frame's scope, -1 = the enclosing one
	}
	scopes := []scope{{depType: DepType}}
	stack := []frame{{resource: 0}}
	current := func() (ignored bool, sc int) {
		sc = 0
		for _, f := range stack {
			ignored = ignored || f.ignored
			if f.resource >= 0 {
				sc = f.resource
			}
		}
		return ignored, sc
	}
	heredoc := ""
	for off, line := 0, 1; off < len(src); line++ {
		end := bytes.IndexByte(src[off:], '\n')
		if end < 0 {
			end = len(src) - off
		}
		l := string(src[off : off+end])
		start := off
		off += end + 1
		if heredoc != "" {
			if strings.TrimSpace(l) == heredoc {
				heredoc = ""
			}
			continue
		}
		trimmed := strings.TrimSpace(l)
		if trimmed == "" || trimmed[0] == '#' {
			continue
		}
		if m := heredocRe.FindStringSubmatchIndex(l); len(m) == 6 && !strings.Contains(l[:m[0]], "#") {
			heredoc = l[m[4]:m[5]]
		}
		if m := stmtRe.FindStringSubmatchIndex(l); m != nil {
			if ign, sc := current(); !ign {
				scopes[sc].stmts = append(scopes[sc].stmts, stmt{kind: l[m[2]:m[3]], value: l[m[4]:m[5]], at: start + m[4], line: line})
			}
			continue
		}
		switch {
		case endRe.MatchString(l):
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case doRe.MatchString(l):
			word := ""
			if m := wordRe.FindStringSubmatch(l); m != nil {
				word = m[1]
			}
			f := frame{ignored: slices.Contains(ignored, word), resource: -1}
			if word == "resource" {
				scopes = append(scopes, scope{depType: DepTypeResource})
				f.resource = len(scopes) - 1
			}
			stack = append(stack, f)
		default:
			if m := keywordRe.FindStringSubmatch(l); m != nil && !oneLineRe.MatchString(l) && !endlessRe.MatchString(l) {
				stack = append(stack, frame{ignored: slices.Contains(ignored, m[1]), resource: -1})
			}
		}
	}
	return scopes
}

// Extract returns, per scope, a dep for every `url` with the first `sha256` after it (before
// the next `url`), a dep for each `mirror` of a supported download that contains its version,
// and a dep for a `version` statement equal to the version of a download of the scope.
func (homebrew) Extract(file string, src []byte, env manager.Env) ([]dep.Dep, error) {
	var out []dep.Dep
	for _, sc := range scan(src) {
		out = sc.deps(file, env, out)
	}
	return out, nil
}

// deps appends the deps of the scope to out.
func (sc scope) deps(file string, env manager.Env, out []dep.Dep) []dep.Dep {
	type follower struct {
		of int // index in out of the download
		s  stmt
	}
	var mirrors []follower
	var versions []stmt
	downloads := len(out)
	cur, sha := -1, false // the last url's dep (-1 = none or unsupported); whether a sha256 followed it
	for _, s := range sc.stmts {
		switch s.kind {
		case "url":
			d := dep.Dep{Manager: Name, DepType: sc.depType, File: file, Line: s.line}
			if !download.Locate(&d, s.value, s.at, env) {
				out = append(out, dep.Dep{Manager: Name, DepType: sc.depType, File: file, Line: s.line, Name: s.value, Skip: SkipUnsupported})
				cur = -1
				continue
			}
			d.Skip = SkipNoSHA256 // until its sha256 follows
			out = append(out, d)
			cur, sha = len(out)-1, false
		case "sha256":
			if cur < 0 || sha {
				continue
			}
			sha = true
			if manager.IsSHA256(s.value) {
				out[cur].Checksum, out[cur].ChecksumSpan, out[cur].Skip = s.value, dep.Span{Start: s.at, End: s.at + len(s.value)}, ""
			}
		case "mirror":
			if cur >= 0 {
				mirrors = append(mirrors, follower{cur, s})
			}
		case "version":
			versions = append(versions, s)
		}
	}
	for _, m := range mirrors {
		if d, ok := download.Follow(out[m.of], DepTypeMirror, m.s.value, m.s.at, m.s.line); ok {
			out = append(out, d)
		}
	}
	for _, v := range versions {
		for _, d := range out[downloads:] {
			if d.DepType == sc.depType && d.Skip == "" && d.Version == v.value {
				d, _ := download.Follow(d, DepTypeVersion, v.value, v.at, v.line)
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// Edits replaces every occurrence of the version in the URL (or mirror, or version statement)
// and the sha256.
func (homebrew) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	var edits []patch.Edit
	if u.NewVersion != d.Version {
		for _, s := range [...]dep.Span{d.RefSpan, d.VersionSpan, d.InputSpan} {
			if s.End > s.Start {
				edits = append(edits, patch.Edit{Start: s.Start, End: s.End, Old: d.Version, New: u.NewVersion})
			}
		}
	}
	return manager.ChecksumEdits(edits, u)
}
