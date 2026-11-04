package kustomize

import (
	"strings"
)

// remote is a parsed remote git target of a kustomization.
type remote struct {
	name   string // host/path of the repository, without `.git`
	source string // https URL of the repository; "" = unparsable
	ref    string // value of the ref (or version) query parameter, as written
	refOff int    // byte offset of ref in the target
}

// parseRemote parses a kustomize remote git target with a `ref` (or legacy `version`) query
// parameter: `[git::]https://host/org/repo[.git][//path]`, `ssh://[user@]host[:port]/…`,
// `user@host:org/repo[.git][//path]` or `host/org/repo[//path]`. Without `//` the repository
// ends at `.git`, after `_git/<repo>` (Azure DevOps), after two segments on github.com,
// gitlab.com and bitbucket.org, or at the end of the path. ok is false for local paths and
// targets without a ref.
func parseRemote(s string) (r remote, ok bool) {
	q := strings.IndexByte(s, '?')
	if q < 0 {
		return remote{}, false
	}
	off := q + 1
	found := false
	for param := range strings.SplitSeq(s[q+1:], "&") {
		if k, v, _ := strings.Cut(param, "="); !found && (k == "ref" || k == "version") {
			r.ref, r.refOff, found = v, off+len(k)+1, true
		}
		off += len(param) + 1
	}
	if !found {
		return remote{}, false
	}
	base := strings.TrimPrefix(s[:q], "git::")
	scheme := "https"
	var host, p string
	switch {
	case strings.Contains(base, "://"):
		var rest string
		scheme, rest, _ = strings.Cut(base, "://")
		switch scheme {
		case "https", "http", "ssh", "git":
		default:
			return remote{}, false // local schemes such as file
		}
		host, p, _ = strings.Cut(rest, "/")
		if i := strings.LastIndexByte(host, '@'); i >= 0 {
			host = host[i+1:]
		}
		if scheme != "https" && scheme != "http" {
			host, _, _ = strings.Cut(host, ":") // the ssh/git port is not the https port
			scheme = "https"
		}
	case scpLike(base):
		at := strings.IndexByte(base, '@')
		host, p, _ = strings.Cut(base[at+1:], ":")
	default:
		host, p, _ = strings.Cut(base, "/")
		if !strings.Contains(host, ".") || strings.Trim(host, ".") != host {
			return remote{}, false // a local path
		}
	}
	repo := repoPath(host, p)
	if host == "" || repo == "" {
		return r, true
	}
	r.name = host + "/" + repo
	r.source = scheme + "://" + r.name
	return r, true
}

// scpLike reports whether s is an scp-like git address `user@host:path`.
func scpLike(s string) bool {
	at := strings.IndexByte(s, '@')
	colon := strings.IndexByte(s, ':')
	slash := strings.IndexByte(s, '/')
	return at > 0 && colon > at+1 && (slash < 0 || colon < slash)
}

// repoPath returns the repository part of path p on host without `.git` and surrounding
// slashes, or "" when it has an empty segment.
func repoPath(host, p string) string {
	p = strings.TrimPrefix(p, "/")
	switch {
	case strings.Contains(p, "//"):
		p, _, _ = strings.Cut(p, "//")
	case strings.Contains(p, ".git/"):
		p, _, _ = strings.Cut(p, ".git/")
	case strings.Contains(p, "_git/"):
		i := strings.Index(p, "_git/") + len("_git/")
		if j := strings.IndexByte(p[i:], '/'); j >= 0 {
			p = p[:i+j]
		}
	case host == "github.com" || host == "gitlab.com" || host == "bitbucket.org":
		if segs := strings.SplitN(p, "/", 3); len(segs) == 3 {
			p = segs[0] + "/" + segs[1]
		}
	}
	p = strings.TrimSuffix(strings.TrimSuffix(p, "/"), ".git")
	if p == "" {
		return ""
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "" {
			return ""
		}
	}
	return p
}
