// Package releasenotes renders the release age and upstream release notes of updates for pull
// request bodies.
//
// Release notes come from the GitHub, GitLab or Gitea/Forgejo releases API of the dependency's
// source repository: the action's repository, a Go module's forge repository, an npm package's
// `repository`, a PyPI project's source URL, a Maven POM's `<scm>`, a vcpkg port's forge
// `homepage`, an image's
// `org.opencontainers.image.source` label, or the repository of a forge releases or tags dep. One page of releases is listed per repository and
// run. Every failure only omits the notes; it never fails a PR.
package releasenotes

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource"
	forgeds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/forge"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/versioning"
)

const (
	maxBody    = 2 << 10  // bytes per release body
	maxSection = 40 << 10 // bytes per Section
	timeout    = 30 * time.Second
	omitted    = "\n_Further release notes omitted (size limit)._\n"
)

// Notes looks up release times and release notes; results are cached for the run.
type Notes struct {
	client  *http.Client
	sem     chan struct{}
	auth    *httpx.Auth
	gh      *githubapi.Client
	sources map[string]datasource.Datasource
	now     func() time.Time
	gitlab  []string // GitLab instance URLs (WithGitLab)

	// lists holds one page of releases per kind + repository URL; a listing failed by its
	// caller's context (the per-PR deadline) is not shared.
	lists datasource.Memo[string, []release]
}

// New returns Notes using the shared client and request semaphore for Gitea/Forgejo and gh for
// GitHub. auth supplies tokens for the platform host (nil = anonymous); sources are the run's
// datasources by name, used for release times (datasource.Timer), source repositories
// (datasource.SourceRepo) and git tags.
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth, gh *githubapi.Client, sources map[string]datasource.Datasource) *Notes {
	return &Notes{
		client: client, sem: sem, auth: auth, gh: gh, sources: sources,
		now: time.Now, gitlab: []string{forgeds.DefaultGitLabURL},
	}
}

// WithGitLab names the GitLab instances (`https://gitlab.com`, a self-managed
// `https://host[/path]`) whose repositories' notes come from the GitLab API, replacing the
// default gitlab.com.
func (n *Notes) WithGitLab(bases []string) *Notes {
	n.gitlab = bases
	return n
}

// Age renders when u.NewVersion was released, e.g. "released 2026-10-30 (4 days ago)"; "" when
// the datasource does not know, and for a replacement (the new package is looked up under
// another name and source). A major module path update asks about the new path.
func (n *Notes) Age(ctx context.Context, u dep.Update) string {
	timer, ok := n.sources[u.Dep.Datasource].(datasource.Timer)
	if !ok || u.Type == dep.Replacement {
		return ""
	}
	d := u.Dep
	if u.NewName != "" {
		d.Name = u.NewName
	}
	t, err := timer.ReleaseTime(ctx, d, u.NewVersion)
	if err != nil || t.IsZero() {
		slog.DebugContext(ctx, "release time unknown", "name", d.Name, "version", u.NewVersion, "err", err)
		return ""
	}
	var ago string
	switch days := int(n.now().Sub(t) / (24 * time.Hour)); {
	case days <= 0:
		ago = "today"
	case days == 1:
		ago = "1 day ago"
	default:
		ago = fmt.Sprintf("%d days ago", days)
	}
	return "released " + t.UTC().Format(time.DateOnly) + " (" + ago + ")"
}

// item is one dependency of a Section: the lowest current and the highest new version over
// its updates.
type item struct {
	d        dep.Dep
	from, to string
}

// block is the rendered notes of one item.
type block struct {
	title   string
	entries []string // one per release, newest first
	compare string   // compare URL or ""
}

// Section renders a "Release notes" section with one collapsed block per dependency of ups:
// the release bodies of the versions in (current, new], newest first, each cut to 2 KiB, and a
// compare link; at most 40 KiB in total. It returns "" when nothing is known.
func (n *Notes) Section(ctx context.Context, ups []dep.Update) string {
	items := collect(ups)
	if len(items) == 0 {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	blocks := make([]block, len(items))
	var wg sync.WaitGroup
	for i, it := range items {
		wg.Go(func() { blocks[i] = n.block(ctx, it) })
	}
	wg.Wait()
	return render(blocks)
}

// collect merges ups per dependency, skipping updates that keep the version (digest, pin) and
// replacements (the versions are of different packages). A lockfile-only update starts at the
// locked version.
func collect(ups []dep.Update) []item {
	var items []item
	idx := map[string]int{}
	for _, u := range ups {
		from := u.Dep.Version
		if u.Lockfile {
			from = u.Dep.Locked
		}
		if from == u.NewVersion || u.Type == dep.Replacement {
			continue
		}
		key := u.Dep.Datasource + "\x00" + u.Dep.Name + "\x00" + u.Dep.Source
		i, ok := idx[key]
		if !ok {
			idx[key] = len(items)
			items = append(items, item{d: u.Dep, from: from, to: u.NewVersion})
			continue
		}
		if less(from, items[i].from) {
			items[i].from = from
		}
		if less(items[i].to, u.NewVersion) {
			items[i].to = u.NewVersion
		}
	}
	return items
}

func less(a, b string) bool {
	va, oka := parseVersion(a)
	vb, okb := parseVersion(b)
	return oka && okb && compare(va, vb) < 0
}

func (n *Notes) block(ctx context.Context, it item) block {
	b := block{title: it.d.Name + " " + it.from + " → " + it.to}
	r, ok := n.resolve(ctx, it.d, it.to)
	if !ok {
		slog.DebugContext(ctx, "release notes: no source repository", "name", it.d.Name, "datasource", it.d.Datasource)
		return b
	}
	rels, err := n.releases(ctx, r)
	if err != nil {
		slog.DebugContext(ctx, "release notes unavailable", "name", it.d.Name, "repo", r.web(), "err", err)
	}
	tagVerOf := tagVersions(it.d, r)
	from, okFrom := parseVersion(it.from)
	to, okTo := parseVersion(it.to)
	type picked struct {
		v   version
		rel release
	}
	vers := make([]tagVer, len(rels))
	for i, rel := range rels {
		vers[i].v, vers[i].ok = tagVerOf(rel.tag)
	}
	var sel []picked
	for i, rel := range rels {
		v, ok := vers[i].v, vers[i].ok
		if !ok || !okFrom || !okTo || rel.draft || (rel.pre && compareAt(v, to) != 0) {
			continue
		}
		if compareAt(v, from) > 0 && compareAt(v, to) <= 0 {
			sel = append(sel, picked{v, rel})
		}
	}
	slices.SortStableFunc(sel, func(a, b picked) int { return compare(b.v, a.v) })
	for _, p := range sel {
		b.entries = append(b.entries, entry(p.rel, r))
	}
	// The project of a Go module on GitLab is a guess (see goRepo): link only one whose
	// release list was found.
	guessed := it.d.Datasource == dsGo && r.kind == gitlab && err != nil
	if fromTag, toTag, ok := n.tagsFor(ctx, it.d, r, rels, vers, it.from, it.to); ok && !guessed {
		b.compare = r.compare(fromTag, toTag)
	}
	return b
}

// tagVersions returns the version of a tag of d's repository r: the extractVersion match
// when d has one, else the tag after r's tag prefix (or npm's `<name>@`).
func tagVersions(d dep.Dep, r repoRef) func(tag string) (version, bool) {
	if x := extractor(d); x != nil {
		return func(tag string) (version, bool) {
			v, ok := x.Version(tag)
			if !ok {
				return version{}, false
			}
			return parseVersion(v)
		}
	}
	prefixes := []string{r.tagPrefix}
	switch d.Datasource {
	case dsNpm:
		prefixes = append(prefixes, d.Name+"@")
	case dsMaven: // maven-release-plugin's default tag: <artifactId>-<version>
		if _, artifact, ok := strings.Cut(d.Name, ":"); ok {
			prefixes = append(prefixes, artifact+"-")
		}
	}
	return func(tag string) (version, bool) { return tagVersion(tag, prefixes) }
}

// extractor returns d's compiled extractVersion, nil when it has none (or an invalid one,
// which the engine already reported).
func extractor(d dep.Dep) *versioning.Extractor {
	if d.ExtractVersion == "" {
		return nil
	}
	x, err := versioning.CompileExtract(d.ExtractVersion)
	if err != nil {
		return nil
	}
	return x
}

// tagVer is the parsed version of a release tag; ok is false when the tag has none.
type tagVer struct {
	v  version
	ok bool
}

// tagsFor returns the tags of versions from and to in r, ok when both exist: Go module
// versions are tags by definition, git-tags and gitlab-releases/gitlab-tags versions are tag
// names (or extracted from them) when the tag exists (in the git-tags, resp. gitlab-tags
// list: a gitlab-releases version may start at a tag without a release), and others must
// have a release. vers are the parsed tags of rels.
func (n *Notes) tagsFor(ctx context.Context, d dep.Dep, r repoRef, rels []release, vers []tagVer, from, to string) (string, string, bool) {
	switch d.Datasource {
	case dsGo:
		return r.tagPrefix + from, r.tagPrefix + to, true
	case dsGitTags, forgeds.GitLabReleases, forgeds.GitLabTags:
		ds := n.sources[dsGitTags]
		if d.Datasource != dsGitTags {
			ds = n.sources[forgeds.GitLabTags]
		}
		if ds == nil {
			return "", "", false
		}
		tags, err := ds.Releases(ctx, d, "")
		if err != nil {
			return "", "", false
		}
		x := extractor(d)
		find := func(ver string) (string, bool) {
			for _, t := range tags {
				v, ok := t.Version, true
				if x != nil {
					v, ok = x.Version(t.Version)
				}
				if ok && v == ver {
					return t.Version, true
				}
			}
			return "", false
		}
		fromTag, okF := find(from)
		toTag, okT := find(to)
		return fromTag, toTag, okF && okT
	}
	find := func(ver string) (string, bool) {
		want, ok := parseVersion(ver)
		if !ok {
			return "", false
		}
		for i, rel := range rels {
			if v := vers[i]; v.ok && len(v.v.parts) == len(want.parts) && compareAt(v.v, want) == 0 {
				return rel.tag, true
			}
		}
		return "", false
	}
	fromTag, okF := find(from)
	toTag, okT := find(to)
	return fromTag, toTag, okF && okT
}

// entry renders one release: a heading linking to it and its sanitized, cut body.
func entry(rel release, r repoRef) string {
	var b strings.Builder
	b.WriteString("#### ")
	if rel.url != "" {
		b.WriteString("[" + rel.tag + "](" + redirect(rel.url) + ")")
	} else {
		b.WriteString(rel.tag)
	}
	b.WriteString("\n\n")
	if body := cutBody(sanitize(rel.body, r), rel.cut); body != "" {
		b.WriteString(body)
		b.WriteString("\n\n")
	}
	return b.String()
}

// render assembles the blocks within maxSection; blocks without notes or link are dropped.
func render(blocks []block) string {
	const head, closeTag = "### Release notes\n", "</details>\n"
	var b strings.Builder
	b.WriteString(head)
	full := false
	for _, bl := range blocks {
		if len(bl.entries) == 0 && bl.compare == "" {
			continue
		}
		open := "\n<details>\n<summary>Release notes: " + html.EscapeString(bl.title) + "</summary>\n\n"
		compare := ""
		if bl.compare != "" {
			compare = "[Compare changes](" + bl.compare + ")\n\n"
		}
		if b.Len()+len(open)+len(compare)+len(closeTag)+len(omitted) > maxSection {
			full = true
			break
		}
		b.WriteString(open)
		for _, e := range bl.entries {
			if b.Len()+len(e)+len(compare)+len(closeTag)+len(omitted) > maxSection {
				full = true
				break
			}
			b.WriteString(e)
		}
		b.WriteString(compare)
		b.WriteString(closeTag)
		if full {
			break
		}
	}
	if b.Len() == len(head) {
		return ""
	}
	if full {
		b.WriteString(omitted)
	}
	return b.String()
}

const ellipsis = "…"

// truncate cuts s to at most limit bytes on a rune boundary, ending in "…" (within the limit)
// when it cut.
func truncate(s string, limit int) (string, bool) {
	if len(s) <= limit {
		return s, false
	}
	return prefix(s, limit-len(ellipsis)) + ellipsis, true
}

// prefix returns the longest prefix of s of at most limit bytes ending on a rune boundary.
func prefix(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit]
}

// cutBody caps a sanitized body at maxBody and makes it safe to quote inside the pull
// request's own <details> block: cut reports that the stored body was already cut (and ends
// in "…"). A closing tag of the body that closes nothing of it would close that block, so it
// is escaped, and a <details>, <summary> or code fence the body or the cut leaves open is
// closed within the cap, so that the rest of the pull request body stays visible.
func cutBody(body string, cut bool) string {
	body = escapeStrayTags(body)
	tail := closeTags(body)
	if !cut && len(body)+len(tail) <= maxBody {
		return body + tail
	}
	body = strings.TrimSuffix(body, ellipsis)
	limit := maxBody - len(ellipsis)
	out := prefix(body, limit)
	tail = closeTags(out)
	// Cutting shorter to make room for the closing tags can open or close others, so the
	// limit is lowered until they fit. It only ever shrinks, so this ends.
	for len(out)+len(ellipsis)+len(tail) > maxBody {
		next := maxBody - len(ellipsis) - len(tail)
		if next >= limit || next < 0 {
			break // nested too deeply to close within the cap
		}
		limit, out = next, prefix(body, next)
		tail = closeTags(out)
	}
	return out + ellipsis + tail
}

// escapeStrayTags escapes the closing tags of body that close no tag of body itself: they
// would close the <details> block the body is quoted in and hide the rest of the pull
// request body.
func escapeStrayTags(body string) string {
	_, stray, _ := tagState(body)
	if len(stray) == 0 {
		return body
	}
	var b strings.Builder
	b.Grow(len(body) + 3*len(stray))
	last := 0
	for _, i := range stray {
		b.WriteString(body[last:i])
		b.WriteString("&lt;")
		last = i + 1
	}
	b.WriteString(body[last:])
	return b.String()
}

// closeTags returns what body must be followed by to leave no code fence and no <details> or
// <summary> block open, the innermost block first.
func closeTags(body string) string {
	open, _, fence := tagState(body)
	if !fence && len(open) == 0 {
		return ""
	}
	var b strings.Builder
	if fence {
		b.WriteString("\n```")
	}
	for _, name := range slices.Backward(open) {
		b.WriteString("\n</")
		b.WriteString(name)
		b.WriteString(">")
	}
	return b.String()
}

// htmlTag is a <details> or <summary> tag, or one of their closing tags, of a body.
type htmlTag struct {
	at      int    // offset of the '<'
	name    string // "details" or "summary"
	closing bool
}

// tagState walks the <details> and <summary> tags of body that GitHub reads as HTML (those
// in a code fence or an inline code span are text) and returns the blocks left open
// (outermost first), the offsets of the closing tags that close none of them, and whether
// body ends inside a code fence.
func tagState(body string) (open []string, stray []int, fence bool) {
	var tags []htmlTag
	for off := 0; off < len(body); {
		line := body[off:]
		if i := strings.IndexByte(line, '\n'); i >= 0 {
			line = line[:i+1]
		}
		switch {
		case strings.HasPrefix(strings.TrimSpace(line), "```"):
			fence = !fence
		case !fence:
			// Even segments are outside inline `code`, as in sanitize.
			at := off
			for j, seg := range strings.Split(line, "`") {
				if j%2 == 0 {
					tags = appendTags(tags, seg, at)
				}
				at += len(seg) + 1
			}
		}
		off += len(line)
	}
	for _, t := range tags {
		switch {
		case !t.closing:
			open = append(open, t.name)
		case len(open) > 0 && open[len(open)-1] == t.name:
			open = open[:len(open)-1]
		default:
			stray = append(stray, t.at)
		}
	}
	return open, stray, fence
}

// appendTags adds the tags of seg, which starts at offset at of its body, to dst.
func appendTags(dst []htmlTag, seg string, at int) []htmlTag {
	for i := 0; i < len(seg); i++ {
		if seg[i] != '<' {
			continue
		}
		name, closing, n := parseTag(seg[i:])
		if n == 0 {
			continue
		}
		dst = append(dst, htmlTag{at: at + i, name: name, closing: closing})
		i += n - 1
	}
	return dst
}

// parseTag parses a <details> or <summary> tag, opening or closing and with any attributes,
// at the start of s; a length of 0 reports that s starts with something else.
func parseTag(s string) (name string, closing bool, n int) {
	i := 1
	if i < len(s) && s[i] == '/' {
		closing, i = true, 2
	}
	j := i
	for j < len(s) && isLetter(s[j]) {
		j++
	}
	switch {
	case strings.EqualFold(s[i:j], "details"):
		name = "details"
	case strings.EqualFold(s[i:j], "summary"):
		name = "summary"
	default:
		return "", false, 0
	}
	end := strings.IndexByte(s[j:], '>')
	switch {
	case end < 0:
		return "", false, 0
	case closing && strings.TrimSpace(s[j:j+end]) != "": // "</details x>" is not a tag
		return "", false, 0
	case !closing && end > 0 && !isSpace(s[j]): // the name must end the tag or the attributes start
		return "", false, 0
	}
	return name, closing, j + end + 1
}

func isLetter(c byte) bool { return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' }

func isSpace(c byte) bool { return c == ' ' || c == '\t' || c == '\r' || c == '\n' }

// redirect points github.com links at redirect.github.com, so a PR quoting them does not
// create backlinks in the upstream issues and pull requests.
func redirect(s string) string {
	return strings.ReplaceAll(s, "https://github.com/", "https://redirect.github.com/")
}

// sanitize makes a release body safe to quote in a PR outside code: @mentions get a zero-width
// space so nobody is notified, `#123` and `owner/repo#123` (on GitLab also merge requests
// `!123` and `group/sub/project#123`/`!123`) become links to the upstream repository (a bare
// reference would point at the PR's repository), and github.com links are redirected (see
// redirect).
func sanitize(body string, r repoRef) string {
	base := r.web()
	if r.host == "github.com" {
		base = "https://redirect.github.com/" + r.owner + "/" + r.name
	}
	var b strings.Builder
	b.Grow(len(body) + len(body)/8)
	fence := false
	for i, line := range strings.SplitAfter(body, "\n") {
		if i > 0 && line == "" {
			break
		}
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			fence = !fence
			b.WriteString(line)
			continue
		}
		if fence {
			b.WriteString(line)
			continue
		}
		// Even segments are outside inline `code`.
		for j, seg := range strings.Split(line, "`") {
			if j > 0 {
				b.WriteByte('`')
			}
			if j%2 == 1 {
				b.WriteString(seg)
				continue
			}
			writeProse(&b, seg, base, r)
		}
	}
	return redirect(b.String())
}

// writeProse writes s with mentions neutralized and issue references of r's forge linked.
func writeProse(b *strings.Builder, s, base string, r repoRef) {
	origin := r.base()
	gl := r.kind == gitlab
	// ref reports whether s[i] starts a reference (#, or GitLab's ! for merge requests) and
	// returns the path of its kind under a repository's web URL.
	ref := func(i int) (string, bool) {
		switch {
		case s[i] == '#' && gl:
			return "/-/issues/", true
		case s[i] == '#':
			return "/issues/", true
		case s[i] == '!' && gl:
			return "/-/merge_requests/", true
		}
		return "", false
	}
	// boundary reports whether s[i] can start a mention or reference: GitHub autolinks one
	// after any non-word character, and the markdown around it ("**@user**", "_#1_") is gone
	// by the time it does, so only a letter or digit in front rules it out.
	boundary := func(i int) bool {
		return i == 0 || !isAlnum(s[i-1])
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		kind, isRef := ref(i)
		switch {
		case c == '@' && boundary(i) && i+1 < len(s) && isWord(s[i+1]):
			b.WriteString("@\u200b")
		case isRef && boundary(i):
			if j := digitsEnd(s, i+1); j > i+1 && (j == len(s) || !isWord(s[j]) && s[j] != ']') {
				fmt.Fprintf(b, "[%s](%s%s%s)", s[i:j], base, kind, s[i+1:j])
				i = j - 1
				continue
			}
			b.WriteByte(c)
		case boundary(i) && isWord(c):
			// owner/repo#123; on GitLab also group/sub/project!123
			k := i
			for k < len(s) && (isWord(s[k]) || s[k] == '.' || s[k] == '/') {
				k++
			}
			repo := s[i:k]
			slashes := strings.Count(repo, "/")
			refKind, isRepoRef := "", false
			if k < len(s) {
				refKind, isRepoRef = ref(k)
			}
			if isRepoRef && (slashes == 1 || gl && slashes > 1) && !strings.HasPrefix(repo, "/") && !strings.HasSuffix(repo, "/") && !strings.Contains(repo, "//") {
				if j := digitsEnd(s, k+1); j > k+1 && (j == len(s) || !isWord(s[j]) && s[j] != ']') {
					repoBase := origin + "/" + repo
					if origin == "https://github.com" {
						repoBase = "https://redirect.github.com/" + repo
					}
					fmt.Fprintf(b, "[%s](%s%s%s)", s[i:j], repoBase, refKind, s[k+1:j])
					i = j - 1
					continue
				}
			}
			b.WriteString(s[i:k])
			i = k - 1
		default:
			b.WriteByte(c)
		}
	}
}

// isWord reports whether c is a character of a repository or user name.
func isWord(c byte) bool {
	return isAlnum(c) || c == '_' || c == '-'
}

func isAlnum(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9'
}

func digitsEnd(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
