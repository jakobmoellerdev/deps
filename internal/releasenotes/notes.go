// Package releasenotes renders the release age and upstream release notes of updates for pull
// request bodies.
//
// Release notes come from the GitHub or Gitea/Forgejo releases API of the dependency's source
// repository: the action's repository, a Go module's forge repository, an npm package's
// `repository`, a PyPI project's source URL, a Maven POM's `<scm>`, or an image's
// `org.opencontainers.image.source` label. One page of releases is listed per repository and
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
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
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

	mu    sync.Mutex
	lists map[string]*listEntry // kind + repository URL
}

type listEntry struct {
	once sync.Once
	rels []release
	err  error
}

// New returns Notes using the shared client and request semaphore for Gitea/Forgejo and gh for
// GitHub. auth supplies tokens for the platform host (nil = anonymous); sources are the run's
// datasources by name, used for release times (datasource.Timer), source repositories
// (datasource.SourceRepo) and git tags.
func New(client *http.Client, sem chan struct{}, auth *httpx.Auth, gh *githubapi.Client, sources map[string]datasource.Datasource) *Notes {
	return &Notes{
		client: client, sem: sem, auth: auth, gh: gh, sources: sources,
		now: time.Now, lists: make(map[string]*listEntry),
	}
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
	tagVer := tagVersions(it.d, r)
	from, okFrom := parseVersion(it.from)
	to, okTo := parseVersion(it.to)
	type picked struct {
		v   version
		rel release
	}
	var sel []picked
	for _, rel := range rels {
		v, ok := tagVer(rel.tag)
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
	fromTag, okF := n.tagFor(ctx, it.d, r, rels, tagVer, it.from)
	toTag, okT := n.tagFor(ctx, it.d, r, rels, tagVer, it.to)
	if okF && okT {
		b.compare = r.web() + "/compare/" + platform.EscapePath(fromTag) + "..." + platform.EscapePath(toTag)
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

// tagFor returns the tag of version in r: Go module versions are tags by definition, git-tags
// versions are tag names (or extracted from them) when the tag exists, and others must have
// a release.
func (n *Notes) tagFor(ctx context.Context, d dep.Dep, r repoRef, rels []release, tagVer func(string) (version, bool), ver string) (string, bool) {
	switch d.Datasource {
	case dsGo:
		return r.tagPrefix + ver, true
	case dsGitTags:
		if ds := n.sources[dsGitTags]; ds != nil {
			tags, err := ds.Releases(ctx, d, "")
			if err != nil {
				return "", false
			}
			x := extractor(d)
			for _, t := range tags {
				v, ok := t.Version, true
				if x != nil {
					v, ok = x.Version(t.Version)
				}
				if ok && v == ver {
					return t.Version, true
				}
			}
		}
		return "", false
	}
	want, ok := parseVersion(ver)
	if !ok {
		return "", false
	}
	for _, rel := range rels {
		if v, ok := tagVer(rel.tag); ok && len(v.parts) == len(want.parts) && compareAt(v, want) == 0 {
			return rel.tag, true
		}
	}
	return "", false
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

// cutBody caps a sanitized body at maxBody. cut reports that the stored body was already cut
// (and ends in "…"). A code fence left open by the cut is closed within the cap.
func cutBody(body string, cut bool) string {
	const fence = "\n```"
	if !cut && len(body) <= maxBody {
		return body
	}
	body = strings.TrimSuffix(body, ellipsis)
	out := prefix(body, maxBody-len(ellipsis)) + ellipsis
	if strings.Count(out, "```")%2 == 1 {
		out = prefix(body, maxBody-len(ellipsis)-len(fence)) + ellipsis + fence
	}
	return out
}

// redirect points github.com links at redirect.github.com, so a PR quoting them does not
// create backlinks in the upstream issues and pull requests.
func redirect(s string) string {
	return strings.ReplaceAll(s, "https://github.com/", "https://redirect.github.com/")
}

// sanitize makes a release body safe to quote in a PR outside code: @mentions get a zero-width
// space so nobody is notified, `#123` and `owner/repo#123` become links to the upstream
// repository (a bare reference would point at the PR's repository), and github.com links
// are redirected (see redirect).
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
			writeProse(&b, seg, base, r.host)
		}
	}
	return redirect(b.String())
}

// writeProse writes s with mentions neutralized and issue references linked.
func writeProse(b *strings.Builder, s, base, host string) {
	boundary := func(i int) bool { // s[i-1] may start a mention or reference
		return i == 0 || strings.IndexByte(" \t\n(", s[i-1]) >= 0
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '@' && boundary(i) && i+1 < len(s) && isWord(s[i+1]):
			b.WriteString("@\u200b")
		case c == '#' && boundary(i):
			if j := digitsEnd(s, i+1); j > i+1 && (j == len(s) || !isWord(s[j]) && s[j] != ']') {
				fmt.Fprintf(b, "[%s](%s/issues/%s)", s[i:j], base, s[i+1:j])
				i = j - 1
				continue
			}
			b.WriteByte(c)
		case boundary(i) && isWord(c):
			// owner/repo#123
			k := i
			for k < len(s) && (isWord(s[k]) || s[k] == '.' || s[k] == '/') {
				k++
			}
			if ref := s[i:k]; k < len(s) && s[k] == '#' && strings.Count(ref, "/") == 1 && !strings.HasPrefix(ref, "/") && !strings.HasSuffix(ref, "/") {
				if j := digitsEnd(s, k+1); j > k+1 && (j == len(s) || !isWord(s[j]) && s[j] != ']') {
					repoBase := "https://" + host + "/" + ref
					if host == "github.com" {
						repoBase = "https://redirect.github.com/" + ref
					}
					fmt.Fprintf(b, "[%s](%s/issues/%s)", s[i:j], repoBase, s[k+1:j])
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

func isWord(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}

func digitsEnd(s string, i int) int {
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
}
