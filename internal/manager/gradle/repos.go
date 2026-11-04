package gradle

import (
	"regexp"
	"sort"
	"strings"
)

// Well-known repositories in the dep.Dep.Source format (`<id>=<url>/`).
const (
	centralRepo = "central=https://repo.maven.apache.org/maven2/"
	googleRepo  = "google=https://dl.google.com/dl/android/maven2/"
	portalRepo  = "gradlePluginPortal=https://plugins.gradle.org/m2/"
)

// repo is a declared repository.
type repo struct {
	entry     string      // `[<id>=]<url>/`
	filter    *repoFilter // content filter; nil = every module
	exclusive bool        // exclusiveContent: the only repository of the modules its filter includes
}

// repoList is the repositories of one declaration site, in lookup order.
type repoList []repo

// repoDecls are the repositories a file declares.
type repoDecls struct {
	plugins, libraries repoList // settings: pluginManagement, dependencyResolutionManagement
	preferSettings     bool     // settings: repositoriesMode PREFER_SETTINGS
	buildscript        repoList // build script: buildscript { repositories }
	project            repoList // build script: top-level repositories
	allprojects        repoList // build script: allprojects { repositories }
	subprojects        repoList // build script: subprojects { repositories }
}

// repoFilter is a repository content filter: a module is served when it matches no exclude
// and, if there are includes, at least one include.
type repoFilter struct {
	includes, excludes []moduleMatch
	none               bool // mavenContent { snapshotsOnly() }: serves no release
}

// moduleMatch matches group:module; nil regular expressions and "" names match anything.
type moduleMatch struct {
	group, module     string
	subgroups         bool // includeGroupAndSubgroups: group or a dotted subgroup of it
	groupRe, moduleRe *regexp.Regexp
}

func (m moduleMatch) matches(group, module string) bool {
	switch {
	case m.groupRe != nil && !m.groupRe.MatchString(group),
		m.moduleRe != nil && !m.moduleRe.MatchString(module),
		m.module != "" && m.module != module:
		return false
	case m.group == "" || m.group == group:
		return true
	}
	return m.subgroups && strings.HasPrefix(group, m.group+".")
}

func (f *repoFilter) allows(group, module string) bool {
	if f == nil {
		return true
	}
	if f.none {
		return false
	}
	for _, m := range f.excludes {
		if m.matches(group, module) {
			return false
		}
	}
	if len(f.includes) == 0 {
		return true
	}
	for _, m := range f.includes {
		if m.matches(group, module) {
			return true
		}
	}
	return false
}

// render returns the Source of the module group:module: the exclusive repositories whose
// filter includes it, else every other repository whose filter allows it; "" when none does.
func (l repoList) render(group, module string) string {
	var b strings.Builder
	add := func(e string) {
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(e)
	}
	for _, r := range l {
		if r.exclusive && r.filter.allows(group, module) {
			add(r.entry)
		}
	}
	if b.Len() > 0 {
		return b.String()
	}
	for _, r := range l {
		if !r.exclusive && r.filter.allows(group, module) {
			add(r.entry)
		}
	}
	return b.String()
}

// joinRepos appends the repositories of b whose URL a does not have yet.
func joinRepos(a, b repoList) repoList {
	for _, r := range b {
		u := repoURL(r.entry)
		dup := false
		for _, x := range a {
			if repoURL(x.entry) == u && x.exclusive == r.exclusive {
				dup = true
				break
			}
		}
		if !dup {
			a = append(a, r)
		}
	}
	return a
}

func repoURL(entry string) string {
	if _, u, ok := strings.Cut(entry, "="); ok {
		return u
	}
	return entry
}

var (
	repoRe = regexp.MustCompile(`\b(mavenCentral|jcenter|google|gradlePluginPortal|maven|exclusiveContent)\b`)
	// repoFieldRe: url = uri("…"), url '…', url = "…", setUrl("…"), name = "x" in a maven block.
	repoFieldRe = regexp.MustCompile(`\b(url|setUrl|name)\b[ \t]*(?:=[ \t]*|\([ \t]*)?(?:(?:uri|URI|java\.net\.URI|URI\.create)\([ \t]*)?["']`)
	// repoArgRe: maven("…"), maven(url = "…"), maven(url: '…'), maven(uri("…")).
	repoArgRe        = regexp.MustCompile(`^\([ \t]*(?:url[ \t]*[=:][ \t]*)?(?:(?:uri|URI|java\.net\.URI|URI\.create)\([ \t]*)?["']`)
	preferSettingsRe = regexp.MustCompile(`\bRepositoriesMode\.PREFER_SETTINGS\b`)
	// filterRe: includeGroup("g"), excludeModule 'g', 'n', includeGroupByRegex("re"), …; the
	// arguments follow the match.
	filterRe        = regexp.MustCompile(`\b(include|exclude)(GroupAndSubgroups|GroupByRegex|Group|ModuleByRegex|Module|VersionByRegex|Version)\b[ \t]*\(?[ \t]*`)
	snapshotsOnlyRe = regexp.MustCompile(`\bsnapshotsOnly\b`)
	// unescape decodes the escapes of Groovy and Kotlin string literals used in patterns.
	unescape = strings.NewReplacer(`\\`, `\`, `\"`, `"`, `\'`, `'`, `\$`, `$`)
)

// repositories returns the repositories declared by a settings file or a build script.
func (s *source) repositories(settings bool) repoDecls {
	var r repoDecls
	for b, blk := range s.blocks {
		if blk.label != "repositories" {
			continue
		}
		parent := ""
		if blk.parent >= 0 {
			parent = s.blocks[blk.parent].label
		}
		var dst *repoList
		switch {
		case settings && parent == "pluginManagement":
			dst = &r.plugins
		case settings && parent == "dependencyResolutionManagement":
			dst = &r.libraries
			p := s.blocks[blk.parent]
			r.preferSettings = r.preferSettings || preferSettingsRe.Match(s.code[p.open:p.end])
		case settings:
		case parent == "":
			dst = &r.project
		case parent == "buildscript":
			dst = &r.buildscript
		case parent == "allprojects":
			dst = &r.allprojects
		case parent == "subprojects":
			dst = &r.subprojects
		}
		if dst != nil {
			*dst = joinRepos(*dst, s.repoEntries(b, false))
		}
	}
	return r
}

// repoEntries returns the repositories declared directly in block b (a repositories or an
// exclusiveContent forRepository block).
func (s *source) repoEntries(b int, exclusive bool) repoList {
	blk := s.blocks[b]
	var out repoList
	for _, m := range repoRe.FindAllSubmatchIndex(s.code[blk.open+1:blk.end], -1) {
		off := blk.open + 1 + m[0]
		if s.enclosing(off) != b {
			continue
		}
		j := s.skipSpace(blk.open + 1 + m[1])
		if j >= len(s.code) || s.code[j] != '(' && s.code[j] != '{' {
			continue
		}
		r := repo{exclusive: exclusive}
		var body int
		switch string(s.code[off : blk.open+1+m[1]]) {
		case "mavenCentral", "jcenter": // JCenter is sunset; its artifacts are on Maven Central
			r.entry, body = centralRepo, s.trailingBlock(j)
		case "google":
			r.entry, body = googleRepo, s.trailingBlock(j)
		case "gradlePluginPortal":
			r.entry, body = portalRepo, s.trailingBlock(j)
		case "exclusiveContent":
			if !exclusive {
				out = joinRepos(out, s.exclusiveContent(s.blockAt(j)))
			}
			continue
		default:
			r.entry, body = s.mavenRepo(j)
		}
		if r.entry == "" {
			continue
		}
		r.filter = s.filter(body)
		out = joinRepos(out, repoList{r})
	}
	return out
}

// exclusiveContent returns the repositories of `exclusiveContent { forRepository { … }
// filter { … } }` at block b, exclusive for the modules the filter includes.
func (s *source) exclusiveContent(b int) repoList {
	if b < 0 {
		return nil
	}
	var repos repoList
	var f *repoFilter
	for c := b + 1; c < len(s.blocks) && s.blocks[c].open < s.blocks[b].end; c++ {
		if s.blocks[c].parent != b {
			continue
		}
		switch s.blocks[c].label {
		case "forRepository":
			repos = joinRepos(repos, s.repoEntries(c, true))
		case "filter":
			f = s.filter(c)
		}
	}
	if f == nil {
		return nil // without a filter Gradle rejects the declaration
	}
	for i := range repos {
		repos[i].filter, repos[i].exclusive = f, true
	}
	return repos
}

// blockAt returns the block whose '{' is at off, -1 = none.
func (s *source) blockAt(off int) int {
	b := sort.Search(len(s.blocks), func(i int) bool { return s.blocks[i].open >= off })
	if b == len(s.blocks) || s.blocks[b].open != off {
		return -1
	}
	return b
}

// trailingBlock returns the block of `name {` or `name() {` at j ('(' or '{'), -1 = none.
func (s *source) trailingBlock(j int) int {
	if s.code[j] == '(' {
		depth := 0
		for ; j < len(s.code); j++ {
			if s.code[j] == '(' {
				depth++
			} else if s.code[j] == ')' {
				depth--
				if depth == 0 {
					break
				}
			}
		}
		if j = s.skipBlank(j + 1); j >= len(s.code) || s.code[j] != '{' {
			return -1
		}
	}
	return s.blockAt(j)
}

// mavenRepo parses `maven(…)` or `maven { … }` starting at j ('(' or '{') and returns its
// entry ("" when deps cannot query it) and its block (-1 = none).
func (s *source) mavenRepo(j int) (string, int) {
	var url, name string
	if s.code[j] == '(' {
		if m := repoArgRe.FindIndex(s.code[j:]); m != nil {
			if l, ok := s.lit(j + m[1]); ok {
				url = s.text(l)
			}
		}
	}
	b := s.trailingBlock(j)
	if b < 0 {
		return repoEntry(name, url), -1
	}
	blk := s.blocks[b]
	for _, m := range repoFieldRe.FindAllSubmatchIndex(s.code[blk.open+1:blk.end], -1) {
		if s.enclosing(blk.open+1+m[0]) != b {
			continue
		}
		l, ok := s.lit(blk.open + 1 + m[1])
		if !ok {
			continue
		}
		if string(s.code[blk.open+1+m[2]:blk.open+1+m[3]]) == "name" {
			name = s.text(l)
		} else {
			url = s.text(l)
		}
	}
	return repoEntry(name, url), b
}

// filter parses the content filter of a repository block b: its `content { }` or
// `mavenContent { }` child, or b itself when it is an exclusiveContent `filter { }`. Version
// rules include the module and exclude nothing; rules with arguments deps cannot read are
// ignored.
func (s *source) filter(b int) *repoFilter {
	if b < 0 {
		return nil
	}
	var f repoFilter
	found := false
	scanRules := func(c int) {
		blk := s.blocks[c]
		if snapshotsOnlyRe.Match(s.code[blk.open:blk.end]) {
			f.none, found = true, true
		}
		for _, m := range filterRe.FindAllSubmatchIndex(s.code[blk.open+1:blk.end], -1) {
			if s.enclosing(blk.open+1+m[0]) != c {
				continue
			}
			args := s.literalArgs(blk.open + 1 + m[1])
			include := string(s.code[blk.open+1+m[2]:blk.open+1+m[3]]) == "include"
			kind := string(s.code[blk.open+1+m[4] : blk.open+1+m[5]])
			mm, ok := moduleRule(kind, args)
			if !ok || !include && strings.HasPrefix(kind, "Version") {
				continue
			}
			found = true
			if include {
				f.includes = append(f.includes, mm)
			} else {
				f.excludes = append(f.excludes, mm)
			}
		}
	}
	if s.blocks[b].label == "filter" {
		scanRules(b)
	}
	for c := b + 1; c < len(s.blocks) && s.blocks[c].open < s.blocks[b].end; c++ {
		if l := s.blocks[c].label; s.blocks[c].parent == b && (l == "content" || l == "mavenContent") {
			scanRules(c)
		}
	}
	if !found {
		return nil
	}
	return &f
}

// literalArgs returns the comma-separated string literal arguments starting at i.
func (s *source) literalArgs(i int) []string {
	var args []string
	for {
		l, ok := s.litAfter(s.skipSpace(i))
		if !ok {
			return args
		}
		args = append(args, unescape.Replace(s.text(l)))
		i = s.skipSpace(l.end + 1)
		if i >= len(s.code) || s.code[i] != ',' {
			return args
		}
		i++
	}
}

// moduleRule builds the matcher of an include/exclude rule of kind (Group, GroupByRegex, …).
func moduleRule(kind string, args []string) (moduleMatch, bool) {
	var m moduleMatch
	re := func(p string) *regexp.Regexp {
		r, err := regexp.Compile(`^(?:` + p + `)$`)
		if err != nil {
			return nil
		}
		return r
	}
	switch kind {
	case "Group", "GroupAndSubgroups":
		if len(args) < 1 {
			return m, false
		}
		m.group, m.subgroups = args[0], kind == "GroupAndSubgroups"
	case "GroupByRegex":
		if len(args) < 1 {
			return m, false
		}
		if m.groupRe = re(args[0]); m.groupRe == nil {
			return m, false
		}
	case "Module", "Version":
		if len(args) < 2 {
			return m, false
		}
		m.group, m.module = args[0], args[1]
	default: // ModuleByRegex, VersionByRegex
		if len(args) < 2 {
			return m, false
		}
		if m.groupRe, m.moduleRe = re(args[0]), re(args[1]); m.groupRe == nil || m.moduleRe == nil {
			return m, false
		}
	}
	return m, true
}

// repoEntry formats a repository as `[<id>=]<url>/`; "" for a URL deps cannot query (not
// http(s), interpolated).
func repoEntry(name, url string) string {
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") || strings.ContainsAny(url, "$ \t") {
		return ""
	}
	if !strings.HasSuffix(url, "/") {
		url += "/"
	}
	if name != "" && !strings.ContainsAny(name, "$= \t") {
		return name + "=" + url
	}
	return url
}
