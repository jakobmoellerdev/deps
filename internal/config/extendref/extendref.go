// Package extendref is the grammar of `extends` entries, `<repo>[//<path>][#<ref>]`: the
// config loader reads the files they name and the deps-config manager updates their refs.
package extendref

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// DefaultPath is the file an extends entry without //<path> reads.
const DefaultPath = ".deps.yaml"

// Extend is one extends entry, written <repo>[//<path>][#<ref>].
type Extend struct {
	// Repo is owner/name of a repository on the forge of the checkout, or a git URL
	// (https://, http://, ssh://, git://, file:// or user@host:path).
	Repo string
	Path string // slash path of the file in the repository; DefaultPath when not written
	Ref  string // branch, tag or commit; "" = the repository's default branch
}

var (
	// extendRepoRe is owner/name, with GitLab-style subgroups (group/sub/name).
	extendRepoRe = regexp.MustCompile(`^[A-Za-z0-9_.][A-Za-z0-9_.-]*(/[A-Za-z0-9_.][A-Za-z0-9_.-]*)+$`)
	// scpRe is git's scp-like syntax user@host:path.
	scpRe = regexp.MustCompile(`^[A-Za-z0-9._-]+@[A-Za-z0-9.-]+:[^/]`)
	// extendRefRe is a git ref name or commit; a leading '-' would read as an option.
	extendRefRe = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._/+-]*$`)
)

// ValidRef reports whether s is a ref an entry may name after '#': a git ref name or commit.
func ValidRef(s string) bool {
	return extendRefRe.MatchString(s) && !strings.Contains(s, "..")
}

// ValidPath reports whether s is a path an entry may name after '//': a clean relative
// slash path inside the repository.
func ValidPath(s string) bool {
	return s != "" && !path.IsAbs(s) && path.Clean(s) == s && s != ".." && !strings.HasPrefix(s, "../")
}

// Parse parses an extends entry.
func Parse(s string) (Extend, error) {
	rest := strings.TrimSpace(s)
	var e Extend
	if i := strings.LastIndexByte(rest, '#'); i >= 0 {
		rest, e.Ref = rest[:i], rest[i+1:]
		if !ValidRef(e.Ref) {
			return Extend{}, fmt.Errorf("invalid ref %q after '#'", e.Ref)
		}
	}
	start := 0
	if i := strings.Index(rest, "://"); i >= 0 {
		start = i + len("://")
	}
	e.Path = DefaultPath
	if i := strings.Index(rest[start:], "//"); i >= 0 {
		rest, e.Path = rest[:start+i], rest[start+i+2:]
		if !ValidPath(e.Path) {
			return Extend{}, fmt.Errorf("invalid path %q after '//': must be a clean relative slash path", e.Path)
		}
	}
	e.Repo = rest
	switch {
	case start > 0:
		u, err := url.Parse(rest)
		if err != nil {
			return Extend{}, fmt.Errorf("invalid repository URL: %w", err)
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git":
			if u.Host == "" {
				return Extend{}, fmt.Errorf("repository URL %q has no host", rest)
			}
		case "file":
		default:
			return Extend{}, fmt.Errorf("repository URL %q: scheme must be https, http, ssh, git or file", rest)
		}
		if strings.Trim(u.Path, "/") == "" {
			return Extend{}, fmt.Errorf("repository URL %q has no path", rest)
		}
		if u.User != nil {
			if _, hasPass := u.User.Password(); hasPass {
				return Extend{}, fmt.Errorf("repository URL %q must not contain a password; the platform token or git credentials authenticate", u.Redacted())
			}
		}
	case scpRe.MatchString(rest):
	case extendRepoRe.MatchString(rest):
	default:
		return Extend{}, fmt.Errorf("repository %q must be owner/name or a git URL", rest)
	}
	return e, nil
}

// String renders e as <repo>//<path>[#<ref>], the name of the file in messages and in
// deps explain.
func (e Extend) String() string {
	s := e.Repo + "//" + e.Path
	if e.Ref != "" {
		s += "#" + e.Ref
	}
	return s
}

// IsURL reports whether e.Repo is a git URL rather than owner/name.
func (e Extend) IsURL() bool { return strings.Contains(e.Repo, "://") || scpRe.MatchString(e.Repo) }
