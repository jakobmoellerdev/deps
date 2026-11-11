// Package mint extracts the Swift command line tools a Mintfile pins: one
// `<repo>@<version>` package per line, read as Mint reads it.
package mint

import (
	"bytes"
	"path"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "mint"

// Dep facts: every package of a Mintfile is a git repository whose tags are its versions.
const (
	DepType    = "package"
	Datasource = "git-tags"
	Versioning = "tag"
)

// Skip reasons.
const (
	skipUnspecified = "unspecified version"
	skipRepo        = "unrecognized repository"
)

type mintManager struct{}

// New returns the mint manager.
func New() manager.Manager { return mintManager{} }

func (mintManager) Name() string { return Name }

// Match reports whether the slash path is a Mintfile.
func (mintManager) Match(p string) bool { return path.Base(p) == "Mintfile" }

// MatchHints lets discovery skip this manager for paths it cannot match.
func (mintManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{"Mintfile"}}
}

// Extract returns one Dep per package line of src, in line order. Lines are read as Mint's
// Mintfile does: whitespace-trimmed, `#` starts a comment, blank lines are ignored.
func (mintManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var deps []dep.Dep
	line := 0
	for off := 0; off < len(src); {
		end := len(src)
		if i := bytes.IndexByte(src[off:], '\n'); i >= 0 {
			end = off + i
		}
		line++
		if d, ok := lineDep(file, src, off, end, line); ok {
			deps = append(deps, d)
		}
		off = end + 1
	}
	return deps, nil
}

// part is a trimmed `@`-separated part of a package line and its start offset in src.
type part struct {
	text  string
	start int
}

// lineDep returns the dep of the line src[start:end] (number line), false for a blank or
// comment line.
func lineDep(file string, src []byte, start, end, line int) (dep.Dep, bool) {
	l := src[start:end]
	if i := bytes.IndexByte(l, '#'); i >= 0 {
		l = l[:i]
	}
	if len(bytes.TrimSpace(l)) == 0 {
		return dep.Dep{}, false
	}
	var parts []part
	for off := 0; ; {
		i := bytes.IndexByte(l[off:], '@')
		seg := l[off:]
		if i >= 0 {
			seg = l[off : off+i]
		}
		lead := len(seg) - len(bytes.TrimLeft(seg, " \t\r"))
		parts = append(parts, part{text: string(bytes.TrimSpace(seg)), start: start + off + lead})
		if i < 0 {
			break
		}
		off += i + 1
	}
	repo, version := split(parts)
	d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepType, File: file, Line: line, Name: repo}
	if name, source := manager.GitRepo(gitURL(repo)); source != "" {
		d.Name, d.Source = name, source
	} else {
		d.Skip = skipRepo
		return d, true
	}
	if version.text == "" {
		d.Skip = skipUnspecified
		return d, true
	}
	d.Version, d.RefSpan = version.text, dep.Span{Start: version.start, End: version.start + len(version.text)}
	return d, true
}

// split returns the repository and the version of a line's `@`-separated parts, as Mint's
// PackageReference(package:) splits them: `repo@version`, `git@host:owner/repo@version`
// and `ssh://git@host/owner/repo@version`; an scp or ssh URL without a version, and a line
// with more `@`, has no version.
func split(parts []part) (string, part) {
	switch {
	case len(parts) == 3:
		return parts[0].text + "@" + parts[1].text, parts[2]
	case len(parts) == 2 && !strings.Contains(parts[1].text, ":") && !strings.Contains(parts[0].text, "ssh://"):
		return parts[0].text, parts[1]
	}
	texts := make([]string, len(parts))
	for i, p := range parts {
		texts[i] = p.text
	}
	return strings.Join(texts, "@"), part{}
}

// gitURL returns the URL Mint clones repo from (PackageReference.gitPath): a URL or an scp
// address as written, `github.com/owner/repo` and `host.tld/owner/repo` over https, and
// GitHub for `owner/repo`; a `.git` the shorthands end in is dropped (Mint appends another).
func gitURL(repo string) string {
	switch {
	case strings.Contains(repo, "://") || strings.Contains(repo, "@"):
		return repo
	case strings.Contains(repo, "github.com"):
		return "https://" + strings.TrimSuffix(repo, ".git")
	}
	if first, _, _ := strings.Cut(repo, "/"); strings.Contains(first, ".") {
		return "https://" + repo
	}
	return "https://github.com/" + strings.TrimSuffix(repo, ".git")
}

// Edits replaces the version.
func (mintManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
