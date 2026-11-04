package releasenotes

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Datasource names (see dep.Dep.Datasource).
const (
	dsGitTags        = "git-tags"
	dsGitHubReleases = "github-releases"
	dsGo             = "go"
	dsNpm            = "npm"
	dsDocker         = "docker"
	dsPypi           = "pypi"
	dsMaven          = "maven"
)

// forgeKind selects the release API of a repository.
type forgeKind int

const (
	github forgeKind = iota + 1 // REST v3: api.github.com or <host>/api/v3
	gitea                       // Gitea/Forgejo: <host>/api/v1
)

// repoRef is a source repository whose releases are listed.
type repoRef struct {
	kind              forgeKind
	host, owner, name string
	// tagPrefix precedes the version in tag names (`sub/` for a Go module in a subdirectory).
	tagPrefix string
}

// web returns the repository's web URL.
func (r repoRef) web() string { return "https://" + r.host + "/" + r.owner + "/" + r.name }

// goVanity maps module path prefixes without a forge host to their GitHub repositories. The
// value is the "owner/repo" for an exact module or "owner/" for a prefix whose next path
// element names the repository.
var goVanity = map[string]string{
	"golang.org/x/":              "golang/",
	"k8s.io/":                    "kubernetes/",
	"sigs.k8s.io/":               "kubernetes-sigs/",
	"google.golang.org/grpc":     "grpc/grpc-go",
	"google.golang.org/protobuf": "protocolbuffers/protobuf-go",
	"go.yaml.in/yaml":            "yaml/go-yaml",
}

// resolve returns the source repository of d's release notes, or ok=false when unknown.
func (n *Notes) resolve(ctx context.Context, d dep.Dep, version string) (repoRef, bool) {
	switch d.Datasource {
	case dsGitTags:
		return parseRepo(d.Source, false)
	case dsGitHubReleases:
		src := d.Source
		if src == "" {
			src = d.Name
		}
		// github-releases always speaks the GitHub API, on GitHub Enterprise Server too.
		r, ok := parseRepo(src, true)
		r.kind = github
		return r, ok
	case dsGo:
		return n.goRepo(d.Name)
	case dsNpm, dsDocker, dsPypi, dsMaven:
		sr, ok := n.sources[d.Datasource].(datasource.SourceRepo)
		if !ok {
			return repoRef{}, false
		}
		raw, err := sr.SourceRepo(ctx, d, version)
		if err != nil {
			slog.DebugContext(ctx, "release notes: source repository unknown", "name", d.Name, "err", err)
			return repoRef{}, false
		}
		return parseRepo(raw, true)
	}
	return repoRef{}, false
}

// goRepo maps a module path to its repository: `github.com/<o>/<r>[/sub][/vN]`, the vanity
// table, or `<host>/<o>/<r>[/sub]` on a host deps holds a token for (the Gitea/Forgejo
// platform). Tags of a module in a subdirectory are prefixed with that directory.
func (n *Notes) goRepo(mod string) (repoRef, bool) {
	host, rest, _ := strings.Cut(mod, "/")
	segs := strings.Split(rest, "/")
	var r repoRef
	switch host {
	case "github.com":
		r = repoRef{kind: github, host: host}
	default:
		for prefix, target := range goVanity {
			if !strings.HasPrefix(mod, prefix) {
				continue
			}
			tail := strings.TrimPrefix(mod, prefix)
			owner, repo, ok := strings.Cut(target, "/")
			if repo == "" { // prefix mapping: the next element is the repository
				repo, tail, _ = strings.Cut(tail, "/")
			} else if tail != "" && tail[0] != '/' {
				continue // "google.golang.org/grpcx" is not grpc
			}
			if !ok || repo == "" {
				return repoRef{}, false
			}
			r = repoRef{kind: github, host: "github.com", owner: owner, name: repo, tagPrefix: subdir(strings.Split(strings.Trim(tail, "/"), "/"))}
			return r, true
		}
		if _, _, ok := n.auth.Static(strings.Split(host, ":")[0]); !ok {
			return repoRef{}, false
		}
		r = repoRef{kind: gitea, host: host}
	}
	if len(segs) < 2 || segs[0] == "" || segs[1] == "" {
		return repoRef{}, false
	}
	r.owner, r.name, r.tagPrefix = segs[0], segs[1], subdir(segs[2:])
	return r, true
}

// subdir returns the tag prefix of a module in the subdirectory segs: the path without a
// trailing major version element, plus "/"; "" for the repository root.
func subdir(segs []string) string {
	if k := len(segs); k > 0 && isMajor(segs[k-1]) {
		segs = segs[:k-1]
	}
	if len(segs) == 0 || segs[0] == "" {
		return ""
	}
	return strings.Join(segs, "/") + "/"
}

func isMajor(s string) bool {
	if len(s) < 2 || s[0] != 'v' {
		return false
	}
	for _, c := range s[1:] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// parseRepo normalizes a repository reference: http(s)/git/git+https/git+ssh/ssh URLs,
// scp-like `git@host:o/r.git`, and (with shorthand) npm's `github:o/r` and `o/r`. Only the
// first two path elements are kept (`.../o/r/tree/main/pkg` → o/r). github.com repositories
// use the GitHub API, every other host the Gitea/Forgejo API.
func parseRepo(raw string, shorthand bool) (repoRef, bool) {
	raw = strings.TrimSpace(raw)
	var host, p string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(strings.TrimPrefix(raw, "git+"))
		if err != nil || u.Hostname() == "" {
			return repoRef{}, false
		}
		host, p = u.Hostname(), u.Path
		switch u.Scheme {
		case "http", "https":
			host = u.Host // a web port is part of the API address; an SSH port is not
		case "git", "ssh":
		default:
			return repoRef{}, false
		}
	case strings.HasPrefix(raw, "git@"):
		h, path, ok := strings.Cut(strings.TrimPrefix(raw, "git@"), ":")
		if !ok {
			return repoRef{}, false
		}
		host, p = h, path
	case shorthand && strings.HasPrefix(raw, "github:"):
		host, p = "github.com", strings.TrimPrefix(raw, "github:")
	case shorthand && !strings.Contains(raw, ":") && strings.Count(raw, "/") == 1:
		host, p = "github.com", raw
	default:
		return repoRef{}, false
	}
	owner, rest, _ := strings.Cut(strings.Trim(p, "/"), "/")
	name, _, _ := strings.Cut(rest, "/")
	name = strings.TrimSuffix(name, ".git")
	if owner == "" || name == "" {
		return repoRef{}, false
	}
	kind := gitea
	if strings.EqualFold(host, "github.com") {
		kind, host = github, "github.com"
	}
	return repoRef{kind: kind, host: host, owner: owner, name: name}, true
}
