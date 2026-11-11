package sbt

import (
	"path"
	"regexp"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// ScalafmtName is the name of the `.scalafmt.conf` manager (Renovate's).
const ScalafmtName = "scalafmt"

// DepTypeScalafmt is the dep type of the scalafmt version.
const DepTypeScalafmt = "scalafmt"

// scalafmtVersionRe matches the top-level `version` key of a HOCON `.scalafmt.conf`
// (`version = 3.8.3`, `version = "3.8.3"`, `version: 3.8.3`); the version is group 1.
var scalafmtVersionRe = regexp.MustCompile(`(?m)^[ \t]*version[ \t]*[=:][ \t]*"?(\d+\.\d+\.\d+[0-9A-Za-z.+-]*)"?[ \t]*(?:#.*|//.*)?\r?$`)

type scalafmtManager struct{}

// NewScalafmt returns the scalafmt manager.
func NewScalafmt() manager.Manager { return scalafmtManager{} }

func (scalafmtManager) Name() string { return ScalafmtName }

// Match reports whether the slash path is a `.scalafmt.conf`.
func (scalafmtManager) Match(p string) bool { return path.Base(p) == ".scalafmt.conf" }

// MatchHints lets discovery skip this manager for paths it cannot match.
func (scalafmtManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{".scalafmt.conf"}}
}

// Extract returns the scalafmt version of the `version` key as a github-releases dep of
// scalameta/scalafmt (tags `v<version>`).
func (scalafmtManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	m := scalafmtVersionRe.FindSubmatchIndex(src)
	if m == nil {
		return nil, nil
	}
	v := string(src[m[2]:m[3]])
	return []dep.Dep{{
		Manager: ScalafmtName, Datasource: "github-releases", Versioning: "semver", DepType: DepTypeScalafmt,
		File: file, Line: lineAt(src, m[2]), Name: "scalameta/scalafmt", Source: "https://github.com/scalameta/scalafmt",
		Version: v, RefSpan: dep.Span{Start: m[2], End: m[3]},
	}}, nil
}

// Edits replaces the version.
func (scalafmtManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// lineAt returns the 1-based line of offset off.
func lineAt(src []byte, off int) int {
	n := 1
	for _, c := range src[:off] {
		if c == '\n' {
			n++
		}
	}
	return n
}
