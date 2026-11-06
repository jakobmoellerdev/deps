package terraform

import (
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/imageref"
)

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

// githubArchiveRe is a GitHub tag archive URL: `https://github.com/<owner>/<repo>/archive/`
// `[refs/tags/]<tag>.zip|.tar.gz|.tgz`; groups: owner/repo, the tag prefix, the tag.
var githubArchiveRe = regexp.MustCompile(`^https://github\.com/([0-9A-Za-z_.-]+/[0-9A-Za-z_.-]+)/archive/(refs/tags/)?([^/?#]+?)\.(?:zip|tar\.gz|tgz)$`)

// parseArchiveSource parses a GitHub tag archive URL as the git source it was cut from, the tag
// as its ref. Archives with a `//subdir` (which usually repeats the tag in the archive's
// top-level directory) or a query are not.
func parseArchiveSource(s string) (gitSource, bool) {
	m := githubArchiveRe.FindStringSubmatchIndex(s)
	if m == nil {
		return gitSource{}, false
	}
	repo := s[m[2]:m[3]]
	return gitSource{
		repo: "https://github.com/" + repo, name: "github.com/" + strings.TrimSuffix(repo, ".git"),
		ref: s[m[6]:m[7]], refOff: m[6], hasRef: true,
	}, true
}

// unsupportedSource returns the skip reason of a module source that names no git repository,
// registry or OCI repository deps can look versions up in: the go-getter S3, GCS, Mercurial
// and HTTP getters.
func unsupportedSource(s string) string {
	host, _, _ := strings.Cut(strings.TrimPrefix(strings.TrimPrefix(s, "https://"), "http://"), "/")
	switch {
	case strings.HasPrefix(s, "s3::"),
		strings.HasSuffix(host, ".amazonaws.com") && (strings.HasPrefix(host, "s3") || strings.Contains(host, ".s3")):
		return "s3 module source"
	case strings.HasPrefix(s, "gcs::"), host == "www.googleapis.com" || host == "storage.googleapis.com":
		return "gcs module source"
	case strings.HasPrefix(s, "hg::"):
		return "hg module source"
	case strings.HasPrefix(s, "https://"), strings.HasPrefix(s, "http://"):
		return "archive module source"
	}
	return "unsupported module source"
}

// ociModule fills d from the OpenTofu/Terragrunt OCI source
// `oci://<registry>/<repository>[//subdir][?tag=<tag>|?digest=<digest>]` whose text is t: a
// docker dep of the repository at the tag. Without either, OpenTofu uses `latest`.
func ociModule(d *dep.Dep, t text) {
	s := t.s
	rest := strings.TrimPrefix(s, "oci://")
	base := len("oci://")
	var tag, digest string
	tagOff := -1
	if q := strings.IndexByte(rest, '?'); q >= 0 {
		off := base + q + 1
		for query := rest[q+1:]; query != ""; {
			param, next, _ := strings.Cut(query, "&")
			if v, ok := strings.CutPrefix(param, "tag="); ok {
				tag, tagOff = v, off+len("tag=")
			} else if v, ok := strings.CutPrefix(param, "digest="); ok {
				digest = v
			}
			off += len(param) + 1
			query = next
		}
		rest = rest[:q]
	}
	if i := strings.Index(rest, "//"); i >= 0 {
		rest = rest[:i] // subdirectory
	}
	d.Datasource, d.Versioning, d.Name = "docker", "docker", rest
	r, err := imageref.Parse(rest)
	switch {
	case err != nil || r.Tag != "" || r.Digest != "" || !strings.Contains(rest, "/"):
		d.Skip = "invalid reference"
		return
	case tag == "" && digest != "":
		d.Digest, d.Skip = digest, "digest without tag"
	case tag == "":
		d.Skip = "no tag"
	}
	d.Name, d.Source = r.Familiar, r.Name
	if d.Skip != "" {
		return
	}
	v := t.lit(tagOff, len(tag))
	d.Version = tag
	if strings.Contains(tag, "%") || v.span == (dep.Span{}) {
		d.Skip = "cannot locate value"
		return
	}
	d.RefSpan = v.span
}
