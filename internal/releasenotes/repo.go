package releasenotes

import (
	"context"
	"log/slog"
	"net/url"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	forgeds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
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
	dsVcpkg          = "vcpkg"
	dsClojure        = "clojure"
)

// forgeKind selects the release API of a repository.
type forgeKind int

const (
	github forgeKind = iota + 1 // REST v3: api.github.com or <host>/api/v3
	gitea                       // Gitea/Forgejo: <host>/api/v1
	gitlab                      // GitLab: <host>[<root>]/api/v4; owner may hold subgroups
)

// repoRef is a source repository whose releases are listed.
type repoRef struct {
	kind              forgeKind
	host, owner, name string
	// scheme is the URL scheme of the forge; empty means https.
	scheme string
	// root is the path of a GitLab instance under a relative URL root (`/gitlab`); "" at the
	// host's root.
	root string
	// tagPrefix precedes the version in tag names (`sub/` for a Go module in a subdirectory).
	tagPrefix string
}

// base returns the forge's base URL, `<scheme>://<host>[<root>]`.
func (r repoRef) base() string {
	if r.scheme == "" {
		return "https://" + r.host + r.root
	}
	return r.scheme + "://" + r.host + r.root
}

// web returns the repository's web URL.
func (r repoRef) web() string { return r.base() + "/" + r.owner + "/" + r.name }

// compare returns the web URL comparing tags from and to.
func (r repoRef) compare(from, to string) string {
	sep := "/compare/"
	if r.kind == gitlab {
		sep = "/-/compare/"
	}
	return r.web() + sep + platform.EscapePath(from) + "..." + platform.EscapePath(to)
}

// gitlabRef returns the repository of the GitLab project path on the instance at base.
func gitlabRef(base *url.URL, path string) (repoRef, bool) {
	path = strings.Trim(path, "/")
	i := strings.LastIndexByte(path, '/')
	if i <= 0 || i == len(path)-1 || strings.Contains(path, "//") {
		return repoRef{}, false
	}
	r := repoRef{kind: gitlab, host: base.Host, root: strings.TrimRight(base.Path, "/"), owner: path[:i], name: path[i+1:]}
	if base.Scheme == "http" {
		r.scheme = "http"
	}
	return r, true
}

// gitlabBase returns the GitLab instance of bases on host hostname whose path p lies under,
// and the project path in p: up to GitLab's `/-/` page separator, without `.git`.
func gitlabBase(bases []string, hostname, p string) (*url.URL, string, bool) {
	for _, b := range bases {
		u, err := url.Parse(b)
		if err != nil || !strings.EqualFold(u.Hostname(), hostname) {
			continue
		}
		rest := "/" + strings.Trim(p, "/")
		if root := strings.TrimRight(u.Path, "/"); root != "" {
			if len(rest) <= len(root) || rest[len(root)] != '/' || !strings.EqualFold(rest[:len(root)], root) {
				continue
			}
			rest = rest[len(root):]
		}
		if i := strings.Index(rest+"/", "/-/"); i >= 0 {
			rest = rest[:i]
		}
		return u, strings.TrimSuffix(strings.Trim(rest, "/"), ".git"), true
	}
	return nil, "", false
}

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
		return parseRepo(d.Source, false, n.gitlab)
	case dsGitHubReleases:
		src := d.Source
		if src == "" {
			src = d.Name
		}
		// github-releases always speaks the GitHub API, on GitHub Enterprise Server too.
		r, ok := parseRepo(src, true, nil)
		r.kind = github
		return r, ok
	case forgeds.GiteaReleases, forgeds.GiteaTags, forgeds.ForgejoReleases, forgeds.ForgejoTags:
		// These always speak the Gitea API; an instance under a path prefix is not supported.
		fr, err := forgeds.ParseRepo(d.Datasource, d.Name, d.Source)
		if err != nil {
			return repoRef{}, false
		}
		u, err := url.Parse(fr.Base)
		if err != nil || strings.Trim(u.Path, "/") != "" {
			return repoRef{}, false
		}
		owner, name, _ := strings.Cut(fr.Path, "/")
		ref := repoRef{kind: gitea, host: u.Host, owner: owner, name: name}
		if u.Scheme == "http" {
			ref.scheme = "http"
		}
		return ref, true
	case forgeds.GitLabReleases, forgeds.GitLabTags:
		fr, err := forgeds.ParseRepo(d.Datasource, d.Name, d.Source)
		if err != nil {
			return repoRef{}, false
		}
		u, err := url.Parse(fr.Base)
		if err != nil {
			return repoRef{}, false
		}
		return gitlabRef(u, fr.Path)
	case dsGo:
		return n.goRepo(d.Name)
	case dsNpm, dsDocker, dsPypi, dsMaven, dsVcpkg, dsClojure:
		sr, ok := n.sources[d.Datasource].(datasource.SourceRepo)
		if !ok {
			return repoRef{}, false
		}
		raw, err := sr.SourceRepo(ctx, d, version)
		if err != nil {
			slog.DebugContext(ctx, "release notes: source repository unknown", "name", d.Name, "err", err)
			return repoRef{}, false
		}
		return parseRepo(raw, true, n.gitlab)
	}
	return repoRef{}, false
}

// goRepo maps a module path to its repository: `github.com/<o>/<r>[/sub][/vN]`, the vanity
// table, `<host>/<group>[/<subgroup>…]/<project>[/vN]` on a GitLab instance (WithGitLab; the
// module path is taken as the project path, as a subdirectory cannot be told from a
// subgroup), or `<host>/<o>/<r>[/sub]` on a host deps holds a token for (the Gitea/Forgejo
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
		if u, _, ok := gitlabBase(n.gitlab, strings.Split(host, ":")[0], ""); ok && strings.Trim(u.Path, "/") == "" {
			if k := len(segs); k > 2 && isMajor(segs[k-1]) {
				segs = segs[:k-1]
			}
			return gitlabRef(u, strings.Join(segs, "/"))
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
// scp-like `git@host:o/r.git`, and (with shorthand) npm's `github:o/r`, `gitlab:g/p` and
// `o/r`. github.com repositories use the GitHub API, those on one of the GitLab instances
// gitlab the GitLab API with the whole project path (`.../g/sub/p/-/tree/main` → g/sub/p),
// every other host the Gitea/Forgejo API with the first two path elements
// (`.../o/r/tree/main/pkg` → o/r).
func parseRepo(raw string, shorthand bool, gitlab []string) (repoRef, bool) {
	raw = strings.TrimSpace(raw)
	var host, hostname, p string
	switch {
	case strings.Contains(raw, "://"):
		u, err := url.Parse(strings.TrimPrefix(raw, "git+"))
		if err != nil || u.Hostname() == "" {
			return repoRef{}, false
		}
		host, hostname, p = u.Hostname(), u.Hostname(), u.Path
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
		host, hostname, p = h, h, path
	case shorthand && strings.HasPrefix(raw, "github:"):
		host, hostname, p = "github.com", "github.com", strings.TrimPrefix(raw, "github:")
	case shorthand && strings.HasPrefix(raw, "gitlab:"):
		host, hostname, p = "gitlab.com", "gitlab.com", strings.TrimPrefix(raw, "gitlab:")
	case shorthand && !strings.Contains(raw, ":") && strings.Count(raw, "/") == 1:
		host, hostname, p = "github.com", "github.com", raw
	default:
		return repoRef{}, false
	}
	if u, path, ok := gitlabBase(gitlab, hostname, p); ok {
		return gitlabRef(u, path)
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
