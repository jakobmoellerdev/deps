package gradle

import (
	"path"
	"regexp"
	"slices"
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
	// applies are the ids of the plugins a build script applies (`plugins { id("x") }`,
	// `apply plugin: 'x'`): the project repositories of a convention plugin of the run apply too.
	applies []string
	// convention is the id of a precompiled script plugin (`src/main/kotlin/x.gradle.kts`);
	// "" = not one.
	convention string
	// imports are the catalog files a settings file imports with `from(files("…"))`: the
	// catalog's deps may be used by that build's projects too.
	imports []string
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
	// forReposRe: forRepository( or forRepositories( in an exclusiveContent block; the
	// arguments follow.
	forReposRe = regexp.MustCompile(`\bforRepositor(?:y|ies)[ \t]*\(`)
	// repoBindRe: `val corp = maven`, `def corp = repositories.google`, `corp: MavenArtifactRepository = maven`;
	// the argument list or block follows.
	repoBindRe = regexp.MustCompile(`\b([A-Za-z_]\w*)[ \t]*(?::[ \t]*[\w.]+[ \t]*)?=[ \t]*(?:repositories[ \t]*\.[ \t]*)?(mavenCentral|jcenter|google|gradlePluginPortal|maven)\b`)
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

var (
	// appliedRe: id("x"), id 'x' in a plugins block; apply plugin: 'x', apply(plugin = "x").
	appliedRe = regexp.MustCompile(`\bid[ \t]*\(?[ \t]*["']|\bapply[ \t]*\(?[ \t]*plugin[ \t]*[:=][ \t]*["']`)
	// backtickRe: a Kotlin plugin id in backticks (`` `my-convention` ``) in a plugins block.
	backtickRe = regexp.MustCompile("`([A-Za-z0-9_.\\-]+)`")
	// conventionRe: a precompiled script plugin; group 1 is the file name without `.gradle(.kts)`.
	conventionRe = regexp.MustCompile(`(?:^|/)src/main/(?:groovy|kotlin)/(?:.+/)?([^/]+)\.gradle(?:\.kts)?$`)
	packageRe    = regexp.MustCompile(`(?m)^[ \t]*package[ \t]+([A-Za-z_][\w.]*)`)
	// importRe: from(files("…")), from files('…') in a settings versionCatalogs block.
	importRe = regexp.MustCompile(`\bfrom[ \t]*\(?[ \t]*files[ \t]*\(?[ \t]*["']`)
)

// conventions sets the plugin ids the build script file applies and, for a precompiled script
// plugin, its id: the file name, behind the Kotlin package if one is declared.
func (s *source) conventions(file string, r *repoDecls) {
	if m := conventionRe.FindStringSubmatch(file); m != nil {
		r.convention = m[1]
		if p := packageRe.FindSubmatch(s.code); p != nil && !s.groovy {
			r.convention = string(p[1]) + "." + m[1]
		}
	}
	for _, m := range appliedRe.FindAllIndex(s.code, -1) {
		inPlugins := s.within(s.enclosing(m[0]), "plugins")
		if s.code[m[0]] == 'i' && !inPlugins {
			continue
		}
		if l, ok := s.lit(m[1]); ok && plainString(s.text(l)) {
			r.applies = append(r.applies, s.text(l))
		}
	}
	for b, blk := range s.blocks {
		if blk.label != "plugins" {
			continue
		}
		for _, m := range backtickRe.FindAllSubmatchIndex(s.code[blk.open+1:blk.end], -1) {
			if s.enclosing(blk.open+1+m[0]) == b {
				r.applies = append(r.applies, string(s.code[blk.open+1+m[2]:blk.open+1+m[3]]))
			}
		}
	}
}

// catalogImports returns the catalog files the settings file imports in its versionCatalogs
// (`from(files("../gradle/libs.versions.toml"))`), as slash paths of the run.
func (s *source) catalogImports(file string) []string {
	var out []string
	for _, m := range importRe.FindAllIndex(s.code, -1) {
		l, ok := s.lit(m[1])
		if !ok || !s.within(s.enclosing(m[0]), "versionCatalogs") || !plainString(s.text(l)) {
			continue
		}
		if p := path.Join(path.Dir(file), s.text(l)); p != ".." && !strings.HasPrefix(p, "../") && !path.IsAbs(s.text(l)) {
			out = append(out, p)
		}
	}
	return out
}

// repoEntries returns the repositories declared directly in block b (a repositories or an
// exclusiveContent forRepository block). A repository an exclusiveContent block names by
// reference is exclusive only: its plain declaration is dropped.
func (s *source) repoEntries(b int, exclusive bool) repoList {
	blk := s.blocks[b]
	var out, excl repoList
	for _, m := range repoRe.FindAllSubmatchIndex(s.code[blk.open+1:blk.end], -1) {
		off := blk.open + 1 + m[0]
		if s.enclosing(off) != b {
			continue
		}
		j := s.skipSpace(blk.open + 1 + m[1])
		if j >= len(s.code) || s.code[j] != '(' && s.code[j] != '{' {
			continue
		}
		name := string(s.code[off : blk.open+1+m[1]])
		if name == "exclusiveContent" {
			if !exclusive {
				excl = joinRepos(excl, s.exclusiveContent(s.blockAt(j)))
			}
			continue
		}
		if r := s.repoCall(name, j); r.entry != "" {
			r.exclusive = exclusive
			out = joinRepos(out, repoList{r})
		}
	}
	if len(excl) == 0 {
		return out
	}
	out = slices.DeleteFunc(out, func(r repo) bool {
		return slices.ContainsFunc(excl, func(x repo) bool { return repoURL(x.entry) == repoURL(r.entry) })
	})
	return joinRepos(out, excl)
}

// repoCall parses the repository declared by calling name (mavenCentral, google, maven, …)
// with the argument list or block at j ('(' or '{'); its entry is "" when deps cannot query
// it.
func (s *source) repoCall(name string, j int) repo {
	var r repo
	var body int
	switch name {
	case "mavenCentral", "jcenter": // JCenter is sunset; its artifacts are on Maven Central
		r.entry, body = centralRepo, s.trailingBlock(j)
	case "google":
		r.entry, body = googleRepo, s.trailingBlock(j)
	case "gradlePluginPortal":
		r.entry, body = portalRepo, s.trailingBlock(j)
	default:
		r.entry, body = s.mavenRepo(j)
	}
	if r.entry != "" {
		r.filter = s.filter(body)
	}
	return r
}

// wellKnownRepos are the entries of the repository shortcuts a Kotlin function reference
// (`forRepository(::google)`) can name.
var wellKnownRepos = map[string]string{"mavenCentral": centralRepo, "jcenter": centralRepo, "google": googleRepo, "gradlePluginPortal": portalRepo}

// repoNames are the RepositoryHandler methods that declare a repository deps can query.
var repoNames = map[string]bool{"mavenCentral": true, "jcenter": true, "google": true, "gradlePluginPortal": true, "maven": true}

// exclusiveContent returns the repositories of an exclusiveContent block b (`forRepository
// { … }`, `forRepository(::google)`, `forRepositories(repo, google())` with repositories
// declared by a call or named by a variable bound to one), exclusive for the modules its
// `filter { … }` includes.
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
	blk := s.blocks[b]
	for _, m := range forReposRe.FindAllSubmatchIndex(s.code[blk.open+1:blk.end], -1) {
		if s.enclosing(blk.open+1+m[0]) != b {
			continue
		}
		for _, a := range s.callArgs(blk.open + 1 + m[1]) {
			if r := s.repoArg(a); r.entry != "" {
				repos = joinRepos(repos, repoList{r})
			}
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

// repoArg returns the repository an argument of forRepository/forRepositories names: a
// function reference (`::google`), a repository call (`google()`, `maven("…")`) or a
// variable bound to one (`val corp = maven("…")`).
func (s *source) repoArg(a [2]int) repo {
	i := s.skipBlank(a[0])
	ref := i+1 < a[1] && s.code[i] == ':' && s.code[i+1] == ':'
	if ref {
		i += 2
	}
	j := i
	for j < a[1] && isIdent(s.code[j]) {
		j++
	}
	name := string(s.code[i:j])
	k := s.skipBlank(j)
	switch {
	case name == "":
		return repo{}
	case ref:
		return repo{entry: wellKnownRepos[name]}
	case k < a[1] && (s.code[k] == '(' || s.code[k] == '{') && repoNames[name]:
		return s.repoCall(name, k)
	case k == a[1]:
		return s.namedRepo(name)
	}
	return repo{}
}

// namedRepo returns the repository the variable name is bound to (`val corp = maven("…")`,
// `def corp = repositories.maven { … }`); the last binding wins.
func (s *source) namedRepo(name string) repo {
	var r repo
	for _, m := range repoBindRe.FindAllSubmatchIndex(s.code, -1) {
		if string(s.code[m[2]:m[3]]) != name {
			continue
		}
		if j := s.skipSpace(m[1]); j < len(s.code) && (s.code[j] == '(' || s.code[j] == '{') {
			r = s.repoCall(string(s.code[m[4]:m[5]]), j)
		}
	}
	return r
}

// callArgs returns the spans of the comma-separated arguments of the call whose argument list
// starts at i (after the '('), nested parentheses, brackets and braces skipped.
func (s *source) callArgs(i int) [][2]int {
	var out [][2]int
	depth, start := 0, i
	for ; i < len(s.code); i++ {
		switch s.code[i] {
		case '(', '[', '{':
			depth++
		case ']', '}':
			depth--
		case ')':
			if depth == 0 {
				if s.skipBlank(start) < i {
					out = append(out, [2]int{start, i})
				}
				return out
			}
			depth--
		case ',':
			if depth == 0 {
				out = append(out, [2]int{start, i})
				start = i + 1
			}
		}
	}
	return out
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
