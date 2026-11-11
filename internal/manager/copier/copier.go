// Package copier extracts the template a Copier project was generated from: `_src_path` and
// `_commit` of its answers files (`.copier-answers.yml` and the `.copier-answers.*.yml`
// variants of projects built from several templates).
package copier

import (
	"path"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// Name is the manager name (Renovate's).
const Name = "copier"

// Dep facts (Renovate's): the one dep of an answers file is its template, a git repository
// whose tags Copier orders by PEP 440.
const (
	DepType    = "template"
	Datasource = "git-tags"
	Versioning = "pep440"
)

// Skip reasons.
const (
	skipLocal       = "local template"
	skipUnsupported = "unsupported template URL"
	skipCommit      = "untagged commit"
	skipVersion     = "unsupported version"
	skipLocate      = "cannot locate value"
)

// fileRe is Renovate's managerFilePatterns: `.copier-answers.yml`, `.copier-answers.ci.yaml`, …
var fileRe = regexp.MustCompile(`(^|/)\.copier-answers(\..+)?\.ya?ml$`)

// describeRe matches the `_commit` Copier writes for a template commit after a tag
// (`git describe --tags`: `v1.2.3-4-gabc1234`); hexRe the abbreviated commit of a template
// without tags.
var (
	describeRe = regexp.MustCompile(`-\d+-g[0-9a-f]{4,40}$`)
	hexRe      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
)

// aliases are Copier's `_src_path` shortcuts.
var aliases = map[string]string{"gh:": "https://github.com/", "gl:": "https://gitlab.com/"}

var scheme, _ = versioning.Get(Versioning)

type copierManager struct{}

// New returns the copier manager.
func New() manager.Manager { return copierManager{} }

func (copierManager) Name() string { return Name }

// Match reports whether the slash path is a Copier answers file. The name check skips the
// regexp for the paths that cannot match.
func (copierManager) Match(p string) bool {
	return strings.HasPrefix(path.Base(p), ".copier-answers") && fileRe.MatchString(p)
}

// MatchHints lets discovery skip this manager for paths it cannot match.
func (copierManager) MatchHints() manager.Hints {
	return manager.Hints{Exts: []string{".yaml", ".yml"}}
}

// Extract returns the template dep of an answers file with a string `_src_path` and
// `_commit`, none for other files (Renovate ignores them too).
func (copierManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return nil, nil
	}
	top := root.Content[0]
	srcPath, commit := yamlspan.Str(yamlspan.Get(top, "_src_path")), yamlspan.Str(yamlspan.Get(top, "_commit"))
	if srcPath == nil || commit == nil || srcPath.Value == "" || commit.Value == "" {
		return nil, nil
	}
	d := dep.Dep{Manager: Name, Datasource: Datasource, Versioning: Versioning, DepType: DepType, File: file,
		Name: srcPath.Value, Version: commit.Value, Line: commit.Line}
	name, source, skip := Repo(srcPath.Value)
	switch {
	case skip != "":
		d.Skip = skip
	case describeRe.MatchString(commit.Value) || hexRe.MatchString(commit.Value):
		d.Name, d.Source, d.Skip = name, source, skipCommit
	case !scheme.Valid(commit.Value):
		d.Name, d.Source, d.Skip = name, source, skipVersion
	default:
		d.Name, d.Source = name, source
		lines := yamlspan.LineStarts(src)
		if off := yamlspan.Locate(src, lines, commit); off >= 0 {
			d.RefSpan = dep.Span{Start: off, End: off + len(commit.Value)}
		} else {
			d.Skip = skipLocate
		}
	}
	return []dep.Dep{d}, nil
}

// Repo returns the owner/repo name and the https source git-tags reads of a `_src_path`
// (manager.GitRepo after Copier's `gh:`/`gl:` aliases and `git+` prefix), or the skip reason
// of a local path or another URL.
func Repo(srcPath string) (name, source, skip string) {
	u := strings.TrimPrefix(srcPath, "git+")
	for alias, base := range aliases {
		if rest, ok := strings.CutPrefix(u, alias); ok {
			u = base + strings.TrimPrefix(rest, "/")
		}
	}
	if strings.HasPrefix(u, "/") || strings.HasPrefix(u, ".") || strings.HasPrefix(u, "~") || strings.HasPrefix(u, "file://") {
		return "", "", skipLocal
	}
	if name, source = manager.GitRepo(u); source == "" {
		return "", "", skipUnsupported
	}
	return name, source, ""
}

// Edits replaces `_commit`. apply and run hand the update to `copier update` (Updater), which
// rewrites the answers file itself.
func (copierManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
