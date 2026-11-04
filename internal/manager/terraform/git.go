package terraform

import "strings"

// gitSource is a git module source.
type gitSource struct {
	repo   string // https URL of the repository, as git-tags fetches it
	name   string // host/path without .git
	ref    string // the ?ref= value
	refOff int    // offset of ref in the source string
	hasRef bool
}

// parseGitSource parses the git module sources Terraform accepts: `git::<url>` (https, http,
// ssh or scp-like `user@host:path`), the `github.com/…` and `bitbucket.org/…` shorthands and
// scp-like `git@host:path`, each with an optional `//subdir` and `?ref=` query. SSH sources are
// looked up over HTTPS.
func parseGitSource(s string) (gitSource, bool) {
	u, off := s, 0
	forced := strings.HasPrefix(u, "git::")
	if forced {
		u, off = u[len("git::"):], len("git::")
	}
	var g gitSource
	if q := strings.IndexByte(u, '?'); q >= 0 {
		g.ref, g.refOff, g.hasRef = queryRef(u[q+1:], off+q+1)
		u = u[:q]
	}
	var host, path string
	switch {
	case strings.HasPrefix(u, "https://"), strings.HasPrefix(u, "http://"), strings.HasPrefix(u, "ssh://"):
		if !forced && !strings.HasPrefix(u, "ssh://") {
			return gitSource{}, false // a plain http(s) URL is an archive
		}
		_, rest, _ := strings.Cut(u, "://")
		hostPart, p, _ := strings.Cut(rest, "/")
		if _, h, ok := strings.Cut(hostPart, "@"); ok {
			hostPart = h
		}
		if strings.HasPrefix(u, "ssh://") {
			hostPart, _, _ = strings.Cut(hostPart, ":") // the SSH port does not apply to HTTPS
		}
		host, path = hostPart, p
	case strings.HasPrefix(u, "github.com/"), strings.HasPrefix(u, "bitbucket.org/"):
		host, path, _ = strings.Cut(u, "/")
	case strings.Contains(u, "@") && !strings.Contains(u, "://"):
		_, hp, _ := strings.Cut(u, "@")
		h, p, ok := strings.Cut(hp, ":")
		if !ok {
			return gitSource{}, false
		}
		host, path = h, p
	default:
		return gitSource{}, false
	}
	if i := strings.Index(path, "//"); i >= 0 {
		path = path[:i] // subdirectory
	}
	path = strings.Trim(path, "/")
	if host == "" || strings.Count(path, "/") < 1 {
		return gitSource{}, false
	}
	g.repo = "https://" + host + "/" + path
	g.name = host + "/" + strings.TrimSuffix(path, ".git")
	return g, true
}

// queryRef returns the value of the ref parameter of query and its offset (query starts at
// base).
func queryRef(query string, base int) (ref string, off int, ok bool) {
	for query != "" {
		param, rest, _ := strings.Cut(query, "&")
		if v, found := strings.CutPrefix(param, "ref="); found {
			return v, base + len("ref="), true
		}
		base += len(param) + 1
		query = rest
	}
	return "", 0, false
}
