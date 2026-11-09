// Package homebrew updates the downloads of Homebrew formulae (Formula/*.rb): every `url` with
// the `sha256` after it, of the stable download (also per platform in `on_macos`, `on_linux`,
// `on_arm`, `on_intel` blocks) and of each `resource`, their `mirror`s and the `version`
// statement. Downloads are GitHub, Gitea/Forgejo and GitLab tag archives and release assets,
// and npm registry tarballs; `head`, `livecheck`, `patch`, `bottle`, `test` and method bodies
// are not downloads to update.
package homebrew

import (
	"bytes"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
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

	// forgeArchiveRe is a GitHub, Gitea or Forgejo tag archive (group 1 the host, 2
	// owner/repo, 3 the tag), forgeReleaseRe a release asset of one (1 the host, 2 owner/repo,
	// 3 the tag, 4 the asset). gitlabArchiveRe is a GitLab tag archive (1 the host, 2 the
	// project path, 3 the tag), gitlabReleaseRe a GitLab release asset link (1 the host, 2 the
	// project path, 3 the tag). npmRe is an npm registry tarball (1 the package, 2 its unscoped
	// name, 3 the file name without `.tgz`).
	forgeArchiveRe  = regexp.MustCompile(`^https://([^/]+)/([\w.-]+/[\w.-]+)/archive/(?:refs/tags/)?(.+?)\.(?:tar\.gz|tgz|zip)$`)
	forgeReleaseRe  = regexp.MustCompile(`^https://([^/]+)/([\w.-]+/[\w.-]+)/releases/download/([^/]+)/([^/]+)$`)
	gitlabArchiveRe = regexp.MustCompile(`^https://([^/]+)/((?:[\w.-]+/)+[\w.-]+)/-/archive/([^/]+)/[^/]+\.(?:tar\.gz|tar\.bz2|tar|tgz|zip)$`)
	gitlabReleaseRe = regexp.MustCompile(`^https://([^/]+)/((?:[\w.-]+/)+[\w.-]+)/-/releases/([^/]+)/downloads/.+$`)
	npmRe           = regexp.MustCompile(`^https://registry\.npmjs\.org/((?:@[^/]+/)?([^/@]+))/-/([^/]+)\.tgz$`)
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
			if !locate(&d, s.value, s.at, env) {
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
		if d, ok := follow(out[m.of], DepTypeMirror, m.s); ok {
			out = append(out, d)
		}
	}
	for _, v := range versions {
		for _, d := range out[downloads:] {
			if d.DepType == sc.depType && d.Skip == "" && d.Version == v.value {
				d, _ := follow(d, DepTypeVersion, v)
				out = append(out, d)
				break
			}
		}
	}
	return out
}

// follow returns the dep of statement s (a mirror or version statement) that moves with the
// download of dep d: d's lookup, with spans at the occurrences of d's version in s's value
// (after the host of a URL; one to three of them).
func follow(d dep.Dep, depType string, s stmt) (dep.Dep, bool) {
	if d.Skip != "" {
		return dep.Dep{}, false
	}
	from := 0
	if i := strings.Index(s.value, "://"); i >= 0 {
		if j := strings.IndexByte(s.value[i+3:], '/'); j >= 0 {
			from = i + 3 + j
		}
	}
	var spans []int
	for _, at := range occurrences(s.value, d.Version, from) {
		if at == 0 || !isDigit(s.value[at-1]) && s.value[at-1] != '.' {
			spans = append(spans, at)
		}
	}
	if len(spans) == 0 || len(spans) > 3 {
		return dep.Dep{}, false
	}
	f := dep.Dep{
		Manager: Name, DepType: depType, File: d.File, Line: s.line, Datasource: d.Datasource, Versioning: d.Versioning,
		Name: d.Name, Source: d.Source, ExtractVersion: d.ExtractVersion, Version: d.Version,
	}
	setSpans(&f, spans, s.at, len(d.Version))
	return f, true
}

// setSpans sets the version spans of d to the offsets spans (at most three) relative to off.
func setSpans(d *dep.Dep, spans []int, off, n int) {
	targets := [...]*dep.Span{&d.RefSpan, &d.VersionSpan, &d.InputSpan}
	for i, at := range spans {
		*targets[i] = dep.Span{Start: off + at, End: off + at + n}
	}
}

// download is the lookup of a forge download URL: datasource, name, source and the offsets
// of the tag in the URL.
type download struct {
	ds, name, source string
	tagStart, tagEnd int
}

// forgeDownload recognizes tag archives and release assets: of github.com; of Gitea and
// Forgejo on the checkout's forge (env.ForgeURL), gitea.com, codeberg.org and
// code.forgejo.org; and of GitLab on any host (its `/-/` paths). Archives are looked up as
// the repository's tags (git-tags), assets as its releases.
func forgeDownload(u string, env manager.Env) (download, bool) {
	if m := gitlabArchiveRe.FindStringSubmatchIndex(u); m != nil {
		host, path := u[m[2]:m[3]], u[m[4]:m[5]]
		return download{"git-tags", path, "https://" + host + "/" + path, m[6], m[7]}, true
	}
	if m := gitlabReleaseRe.FindStringSubmatchIndex(u); m != nil {
		return download{forge.GitLabReleases, u[m[4]:m[5]], "https://" + u[m[2]:m[3]], m[6], m[7]}, true
	}
	if m := forgeArchiveRe.FindStringSubmatchIndex(u); m != nil {
		host, repo := u[m[2]:m[3]], u[m[4]:m[5]]
		if _, ok := forgeKind(host, env); !ok {
			return download{}, false
		}
		return download{"git-tags", repo, "https://" + host + "/" + repo, m[6], m[7]}, true
	}
	if m := forgeReleaseRe.FindStringSubmatchIndex(u); m != nil {
		host, repo := u[m[2]:m[3]], u[m[4]:m[5]]
		ds, ok := forgeKind(host, env)
		if !ok {
			return download{}, false
		}
		source := "https://" + host
		if ds == "github-releases" {
			source += "/" + repo
		}
		return download{ds, repo, source, m[6], m[7]}, true
	}
	return download{}, false
}

// forgeKind is the release datasource of a GitHub, Gitea or Forgejo host; false for other
// hosts, whose `/<owner>/<repo>/archive/…` URLs may be anything.
func forgeKind(host string, env manager.Env) (string, bool) {
	switch strings.ToLower(host) {
	case "github.com":
		return "github-releases", true
	case "codeberg.org", "code.forgejo.org":
		return forge.ForgejoReleases, true
	case "gitea.com":
		return forge.GiteaReleases, true
	}
	if f, err := url.Parse(env.ForgeURL); err == nil && f.Host != "" && strings.EqualFold(f.Host, host) {
		return forge.GiteaReleases, true
	}
	return "", false
}

// locate sets the datasource, name, source, version and version spans of d from the URL u
// at offset off, and the artifact template of its checksum; false = not a supported URL.
// The version is the tag after its prefix (`v1.2.3`, `jq-1.8.2`, `libpng16-1.6.43`), which
// the dep's extractVersion maps back; every occurrence of it in the tag and asset is replaced.
// An npm tarball is named `<unscoped name>-<version>.tgz`.
func locate(d *dep.Dep, u string, off int, env manager.Env) bool {
	var tagAt int // offset in u of the version search
	if m := npmRe.FindStringSubmatchIndex(u); m != nil {
		file, base := u[m[6]:m[7]], u[m[4]:m[5]]
		if len(file) <= len(base)+1 || !strings.HasPrefix(file, base) || file[len(base)] != '-' || !isDigit(file[len(base)+1]) {
			return false
		}
		d.Datasource, d.Versioning, d.Name = "npm", "semver", u[m[2]:m[3]]
		d.Version, tagAt = file[len(base)+1:], m[6]+len(base)+1
	} else {
		dl, ok := forgeDownload(u, env)
		if !ok {
			return false
		}
		d.Datasource, d.Versioning, d.Name, d.Source = dl.ds, "loose", dl.name, dl.source
		tag := u[dl.tagStart:dl.tagEnd]
		i := versionStart(tag)
		if i < 0 {
			return false
		}
		if i > 0 {
			d.ExtractVersion = "^" + regexp.QuoteMeta(tag[:i]) + `(?<version>\d.*)$`
		}
		d.Version, tagAt = tag[i:], dl.tagStart+i
	}
	spans := occurrences(u, d.Version, tagAt)
	if len(spans) == 0 || len(spans) > 3 {
		return false
	}
	tmpl := u
	for _, at := range slices.Backward(spans) {
		tmpl = tmpl[:at] + "{{version}}" + tmpl[at+len(d.Version):]
	}
	d.ChecksumSource = &dep.ChecksumSource{Artifact: tmpl}
	setSpans(d, spans, off, len(d.Version))
	return true
}

// versionStart is the offset of the version in tag, -1 for none or a commit hash: the first
// digit at a boundary (the tag start, after `-` or `_`, or after a `v` at the tag start or
// after `-` or `_`), so digits in a name prefix (`wget2-`, `k3s-v`) are skipped; without
// one, the first digit (`r1234`).
func versionStart(tag string) int {
	if manager.IsHex(tag, 40, 64) {
		return -1
	}
	for i := range len(tag) {
		if !isDigit(tag[i]) {
			continue
		}
		if i == 0 || isSep(tag[i-1]) || (tag[i-1] == 'v' || tag[i-1] == 'V') && (i == 1 || isSep(tag[i-2])) {
			return i
		}
	}
	return strings.IndexAny(tag, "0123456789")
}

func isSep(c byte) bool { return c == '-' || c == '_' }

// occurrences are the offsets in u, from the first at from, of v not followed by more
// version (a digit, or a dot and a digit).
func occurrences(u, v string, from int) []int {
	var out []int
	for i := from; ; {
		j := strings.Index(u[i:], v)
		if j < 0 {
			return out
		}
		i += j
		end := i + len(v)
		if !(end < len(u) && isDigit(u[end])) && !(end+1 < len(u) && u[end] == '.' && isDigit(u[end+1])) {
			out = append(out, i)
		}
		i = end
	}
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

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
