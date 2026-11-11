package bazel

import (
	"bytes"
	"path"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// BazeliskName is the name of the `.bazelversion` manager (Renovate's).
const BazeliskName = "bazelisk"

// DepTypeBazel is the dep type of the Bazel version in `.bazelversion`.
const DepTypeBazel = "bazel"

// bazelVersionFile is the file Bazelisk reads the Bazel version from.
const bazelVersionFile = ".bazelversion"

// Skip reasons of .bazelversion.
const (
	skipFork    = "bazel fork"
	skipRolling = "non-release version"
)

type bazeliskManager struct{}

// NewBazelisk returns the bazelisk manager.
func NewBazelisk() manager.Manager { return bazeliskManager{} }

func (bazeliskManager) Name() string { return BazeliskName }

// Match reports whether the slash path is a `.bazelversion` file.
func (bazeliskManager) Match(p string) bool { return path.Base(p) == bazelVersionFile }

// MatchHints lets discovery skip this manager for paths it cannot match.
func (bazeliskManager) MatchHints() manager.Hints {
	return manager.Hints{Names: []string{bazelVersionFile}}
}

// Extract returns the Bazel version of the first line (Bazelisk reads only that one) as a
// github-releases dep of bazelbuild/bazel. Bazelisk's other values are skipped: `latest`,
// `last_green`, `last_rc`, `7.x`, `latest-1` (non-release version) and `<fork>/<version>`
// (bazel fork).
func (bazeliskManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	body := bytes.TrimPrefix(src, []byte("\ufeff"))
	line, _, _ := bytes.Cut(body, []byte("\n"))
	v := strings.TrimSpace(string(line))
	if v == "" {
		return nil, nil
	}
	start := len(src) - len(body) + bytes.Index(line, []byte(v))
	d := dep.Dep{
		Manager: BazeliskName, Datasource: "github-releases", Versioning: "semver", DepType: DepTypeBazel,
		File: file, Line: 1, Name: "bazelbuild/bazel", Source: "https://github.com/bazelbuild/bazel", Version: v,
		RefSpan: dep.Span{Start: start, End: start + len(v)},
	}
	switch {
	case strings.Contains(v, "/"):
		d.Skip = skipFork
	case strings.HasPrefix(v, "latest") || strings.HasPrefix(v, "last_") || strings.HasSuffix(v, ".x") || strings.Contains(v, "*"):
		d.Skip = skipRolling
	}
	return []dep.Dep{d}, nil
}

// Edits replaces the version.
func (bazeliskManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}
