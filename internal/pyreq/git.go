package pyreq

import (
	"net/url"
	"path"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

// GitTags is the datasource (with versioning "tag") of a git dependency pinned to a tag.
const GitTags = "git-tags"

// SetGitURL makes d a git-tags dep when the direct reference u (`git+https://host/path[.git]@<tag>`,
// optionally with a `#fragment`), found at file offset at (u is src[at:at+len(u)] when exact),
// pins an http(s) repository to a tag: Source is the repository URL, Version the tag and
// RefSpan its bytes. Any other reference keeps its URLSkip reason (git: a branch, a commit or
// no ref; ssh or other schemes).
func SetGitURL(d *dep.Dep, u string, at int, exact bool) {
	d.Skip = URLSkip(u)
	rest, ok := strings.CutPrefix(u, "git+")
	if !ok {
		return
	}
	if i := strings.IndexByte(rest, '#'); i >= 0 {
		rest = rest[:i]
	}
	// The ref follows the last `@` of the path (an `@` before it is userinfo).
	slash := strings.Index(rest, "://")
	if slash < 0 {
		return
	}
	pathStart := strings.IndexByte(rest[slash+3:], '/')
	refAt := strings.LastIndexByte(rest, '@')
	if pathStart < 0 || refAt < slash+3+pathStart {
		return
	}
	repo, ref := rest[:refAt], rest[refAt+1:]
	refStart := at + len("git+") + refAt + 1
	setGit(d, repo, ref, dep.Span{Start: refStart, End: refStart + len(ref)}, exact)
}

// SetGitTag makes d a git-tags dep for a dependency table with `git = repo` and the tag tag
// (Poetry and uv `tag`, Pipenv `ref`), else skips it as a git dependency.
func SetGitTag(d *dep.Dep, repo string, tag *tomlspan.Value) {
	d.Skip = SkipGit
	if tag == nil || tag.Kind != tomlspan.String {
		return
	}
	d.Line = tag.Line
	setGit(d, repo, tag.Str, dep.Span{Start: tag.Start, End: tag.End}, tag.Exact)
}

// GitTable handles a dependency table with `git` (TableSpec's SkipGit): d becomes a git-tags
// dep when its tagKey (`tag`; Pipenv `ref`) pins an http(s) repository to a version tag, else
// a skipped git dependency. It reports whether t is a git dependency.
func GitTable(d *dep.Dep, t *tomlspan.Value, tagKey string) bool {
	g := t.Get("git")
	if g == nil {
		return false
	}
	if g.Kind != tomlspan.String {
		d.Skip = SkipGit
		return true
	}
	SetGitTag(d, g.Str, t.Get(tagKey))
	return true
}

// setGit sets d's git-tags fields when repo is an http(s) URL and ref a version tag (not a
// commit).
func setGit(d *dep.Dep, repo, ref string, span dep.Span, exact bool) {
	p, err := url.Parse(repo)
	switch {
	case err != nil || (p.Scheme != "https" && p.Scheme != "http") || p.Host == "":
		return
	case ref == "" || manager.IsHex(ref, 40, 64):
		return
	}
	if _, ok := versioning.ParseTag(ref); !ok {
		return
	}
	if !exact {
		d.Skip = SkipLocate
		return
	}
	p.RawQuery, p.Fragment, p.User = "", "", nil
	d.Skip, d.Datasource, d.Versioning = "", GitTags, "tag"
	d.Source, d.ExtraSources = strings.TrimSuffix(strings.TrimSuffix(p.String(), "/"), ".git"), nil
	d.Version, d.Range, d.RefSpan = ref, "", span
}

// tagEdits replaces the tag of a git-tags dep.
func tagEdits(u dep.Update) []patch.Edit {
	return []patch.Edit{{Start: u.Dep.RefSpan.Start, End: u.Dep.RefSpan.End, Old: u.Dep.Version, New: u.NewVersion}}
}

// GitName returns the project name of a bare VCS requirement line
// (`git+https://host/org/repo.git@v1.0#egg=name`): its `egg` fragment, else the repository
// name.
func GitName(req string) string {
	u, frag, _ := strings.Cut(req, "#")
	for kv := range strings.SplitSeq(frag, "&") {
		if n, ok := strings.CutPrefix(kv, "egg="); ok && n != "" {
			return n
		}
	}
	if _, rest, ok := strings.Cut(u, "://"); ok {
		u = rest
	}
	if _, p, ok := strings.Cut(u, "/"); ok { // the path, after the host and userinfo
		u, _, _ = strings.Cut(p, "@")
	}
	return strings.TrimSuffix(path.Base(u), ".git")
}
