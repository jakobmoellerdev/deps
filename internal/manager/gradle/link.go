package gradle

import (
	"path"
	"slices"
	"sort"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// index locates the files of a run by directory and build.
type index struct {
	files       map[string]*fileState
	settings    map[string]string   // dir → settings file
	builds      map[string]string   // dir → build script
	byDir       map[string][]string // dir → scripts (build script first) and gradle.properties last; settings excluded
	sources     []string            // buildSrc / convention plugin sources, sorted
	roots       map[string]string   // memo of root
	conventions map[string]string   // precompiled script plugin id → file
	importers   map[string][]string // catalog file → roots of the other builds importing it (from(files(…)))
	buildRepos  map[string]repoList // memo of libraryRepos, by build root
}

func newIndex(files map[string]*fileState) *index {
	ix := &index{files: files, settings: map[string]string{}, builds: map[string]string{}, byDir: map[string][]string{}, roots: map[string]string{},
		conventions: map[string]string{}, importers: map[string][]string{}, buildRepos: map[string]repoList{}}
	names := make([]string, 0, len(files))
	for f := range files {
		names = append(names, f)
	}
	sort.Strings(names)
	props := map[string]string{}
	for _, f := range names {
		dir := path.Dir(f)
		switch files[f].kind {
		case kindSettings:
			if ix.settings[dir] == "" {
				ix.settings[dir] = f
			}
			for _, c := range files[f].repos.imports {
				ix.importers[c] = append(ix.importers[c], dir)
			}
		case kindScript:
			ix.byDir[dir] = append(ix.byDir[dir], f)
			if isBuildScript(f) && ix.builds[dir] == "" {
				ix.builds[dir] = f
			}
			if id := files[f].repos.convention; id != "" && ix.conventions[id] == "" {
				ix.conventions[id] = f
			}
		case kindProperties:
			props[dir] = f
		case kindSource:
			ix.sources = append(ix.sources, f)
		}
	}
	for _, fs := range ix.byDir {
		sort.SliceStable(fs, func(i, j int) bool { return isBuildScript(fs[i]) && !isBuildScript(fs[j]) })
	}
	for dir, f := range props {
		ix.byDir[dir] = append(ix.byDir[dir], f)
	}
	return ix
}

// root returns the root directory of the build containing dir: the nearest directory at or
// above dir holding a settings file or named buildSrc, else ".".
func (ix *index) root(dir string) string {
	if r, ok := ix.roots[dir]; ok {
		return r
	}
	r := dir
	for r != "." && ix.settings[r] == "" && path.Base(r) != "buildSrc" {
		r = path.Dir(r)
	}
	ix.roots[dir] = r
	return r
}

// consumer returns the build whose projects use the deps of file: buildSrc serves the build
// containing it.
func (ix *index) consumer(file string) string {
	root := ix.root(path.Dir(file))
	if path.Base(root) == "buildSrc" && ix.files[file] != nil && ix.files[file].kind == kindSource {
		return ix.root(path.Dir(root))
	}
	return root
}

// Link resolves the variable references of the run's deps (moving resolved deps to the
// definition) and sets their repositories, then forgets the run's files; what ReadLocks and
// Rename need is kept until the next run. The deps of a catalog a settings file imports are
// linked as that catalog's (see levels).
func (m *gradleManager) Link(files [][]dep.Dep) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ix := newIndex(m.files)
	ls := lockState{dirs: make([][]string, len(files)), meta: make([][]depMeta, len(files)), settings: make([]bool, len(files))}
	renames := map[namePos]nameRepos{}
	var catalogs map[string]*repoLevels // imported catalog file → its levels
	for fi, deps := range files {
		if len(deps) == 0 {
			continue
		}
		file := deps[0].File
		if s, ok := m.imported[file]; ok { // a settings file without deps of its own
			file = s
		}
		st := m.files[file]
		if st == nil || len(st.meta) != len(deps) {
			continue
		}
		lv := ix.levels(file, st)
		for i := range deps {
			d, l := &deps[i], &lv
			if st.meta[i].imported {
				if catalogs == nil {
					catalogs = map[string]*repoLevels{}
				}
				if l = catalogs[d.File]; l == nil {
					c := ix.levels(d.File, &fileState{kind: kindCatalog})
					l, catalogs[d.File] = &c, &c
				}
			}
			d.Source = l.source(d.Name, st.meta[i].class)
			if key := st.meta[i].ref; key != "" {
				ix.resolve(file, d, key)
			}
			if d.Renamable() {
				renames[namePos{d.File, d.NameParts[0].Span.Start}] = nameRepos{levels: l, class: st.meta[i].class}
			}
		}
		ls.dirs[fi], ls.meta[fi], ls.settings[fi] = ix.lockDirs(file, st), st.meta, st.kind == kindSettings
	}
	m.lock, m.renames = ls, renames
	m.files, m.imported = map[string]*fileState{}, map[string]string{}
}

// resolve points d at the definition of key visible from file: the file itself, the build
// scripts and gradle.properties of its directory and of each parent directory up to the build
// root, the scripts in the root's gradle/ directory, then the buildSrc and convention-plugin
// sources of the build. Unresolved deps keep their skip reason. A dep moved to another file
// is no longer renamable: its edits apply to the definition's file.
func (ix *index) resolve(file string, d *dep.Dep, key string) {
	f, v, ok := ix.lookup(file, key)
	if !ok {
		return
	}
	if f != file {
		d.NameParts = nil
	}
	d.File, d.Line, d.VersionRef, d.Skip = f, v.line, v.name, ""
	setVersion(d, v.value, v.span)
}

func (ix *index) lookup(file, key string) (string, def, bool) {
	find := func(f string) (def, bool) {
		st := ix.files[f]
		if st == nil {
			return def{}, false
		}
		v, ok := st.defs[key]
		return v, ok
	}
	if v, ok := find(file); ok {
		return file, v, true
	}
	dir := path.Dir(file)
	root := ix.root(dir)
	for d := dir; ; d = path.Dir(d) {
		for _, f := range ix.byDir[d] {
			if v, ok := find(f); ok && f != file {
				return f, v, true
			}
		}
		if d == root || d == "." {
			break
		}
	}
	for _, f := range ix.byDir[path.Join(root, "gradle")] {
		if v, ok := find(f); ok && f != file {
			return f, v, true
		}
	}
	bs := root
	if path.Base(root) != "buildSrc" {
		bs = path.Join(root, "buildSrc")
	}
	for _, f := range ix.sources {
		if r := ix.root(path.Dir(f)); r == root || r == bs {
			if v, ok := find(f); ok && f != file {
				return f, v, true
			}
		}
	}
	return "", def{}, false
}

// repoLevels are the repository declarations that apply to the deps of one file, by class,
// in precedence order.
type repoLevels struct {
	plugins, classpath, libraries []repoList
}

// levels returns the repositories of the deps of file: plugins use the settings
// pluginManagement repositories; buildscript classpath deps the buildscript repositories;
// other deps of a build script the project's repositories (its own, those of the convention
// plugins it applies, plus allprojects/subprojects of the parent build scripts), else the
// settings dependencyResolutionManagement repositories (first under PREFER_SETTINGS).
// Catalog, settings and source deps may be used by any project of the build: they take the
// repositories of every project (see libraryRepos).
func (ix *index) levels(file string, st *fileState) repoLevels {
	root := ix.consumer(file)
	var set repoDecls
	if sf := ix.settings[root]; sf != "" {
		set = ix.files[sf].repos
	}
	var project repoList
	if st.kind == kindScript {
		project = ix.projectRepos(file, st, root)
	} else {
		project = ix.libraryRepos(root)
		for _, r := range ix.importers[file] {
			if r != root {
				project = joinRepos(project, ix.libraryRepos(r))
			}
		}
	}
	lv := repoLevels{plugins: []repoList{set.plugins}, classpath: []repoList{st.repos.buildscript}, libraries: []repoList{project, set.libraries}}
	if set.preferSettings && len(set.libraries) > 0 {
		lv.libraries = []repoList{set.libraries}
	}
	return lv
}

// libraryRepos returns the repositories of the projects of the build at root, root project
// first (it exists without a build script too), then by directory: those of each project's
// build script (see projectRepos), or the settings repositories for a project that declares
// none (Gradle's PREFER_PROJECT default); only the settings ones under PREFER_SETTINGS. The
// result is clipped, so appending copies.
func (ix *index) libraryRepos(root string) repoList {
	if r, ok := ix.buildRepos[root]; ok {
		return r
	}
	var set repoDecls
	if sf := ix.settings[root]; sf != "" {
		set = ix.files[sf].repos
	}
	var out repoList
	if set.preferSettings && len(set.libraries) > 0 {
		out = set.libraries
	} else {
		for _, dir := range ix.projectDirs(root) {
			var r repoList
			if bf := ix.builds[dir]; bf != "" {
				r = ix.projectRepos(bf, ix.files[bf], root)
			}
			if len(r) == 0 {
				r = set.libraries
			}
			out = joinRepos(out, r)
		}
	}
	out = slices.Clip(out)
	ix.buildRepos[root] = out
	return out
}

// source returns the Source of the dep named name: the first level whose repositories
// serve it (content filters, exclusiveContent), else the defaults (Maven Central then Google
// Maven; plugins the Gradle plugin portal then Google Maven). A plugin is looked up as its
// marker artifact `<id>:<id>.gradle.plugin`.
func (lv repoLevels) source(name string, class depClass) string {
	group, module, _ := strings.Cut(name, ":")
	levels, def := lv.libraries, LibraryRepositories
	switch class {
	case classPlugin:
		levels, def, module = lv.plugins, PluginRepositories, name+".gradle.plugin"
	case classClasspath:
		levels = lv.classpath
	}
	for _, l := range levels {
		if s := l.render(group, module); s != "" {
			return s
		}
	}
	return def
}

// projectRepos returns the repositories of the project of the script file: allprojects of
// the build scripts from the root down, subprojects of the parent ones, then its own (for an
// applied script also those of its directory's build script).
func (ix *index) projectRepos(file string, st *fileState, root string) repoList {
	dir := path.Dir(file)
	var chain []string
	for d := dir; ; d = path.Dir(d) {
		chain = append(chain, d)
		if d == root || d == "." {
			break
		}
	}
	var out repoList
	for _, d := range slices.Backward(chain) {
		bf := ix.builds[d]
		if bf == "" {
			continue
		}
		r := ix.files[bf].repos
		out = joinRepos(out, r.allprojects)
		if d != dir {
			out = joinRepos(out, r.subprojects)
		} else if bf != file {
			out = ix.conventionRepos(out, bf)
			out = joinRepos(out, r.project)
		}
	}
	out = ix.conventionRepos(out, file)
	return joinRepos(out, st.repos.project)
}

// conventionRepos appends to out the project repositories of the convention plugins (precompiled
// script plugins of the run) the scripts apply, and of those these apply, in order.
func (ix *index) conventionRepos(out repoList, scripts ...string) repoList {
	if len(ix.conventions) == 0 {
		return out
	}
	seen := map[string]bool{}
	var walk func(ids []string)
	walk = func(ids []string) {
		for _, id := range ids {
			f := ix.conventions[id]
			if f == "" || seen[f] {
				continue
			}
			seen[f] = true
			r := ix.files[f].repos
			walk(r.applies)
			out = joinRepos(out, r.project)
		}
	}
	for _, f := range scripts {
		if st := ix.files[f]; st != nil {
			walk(st.repos.applies)
		}
	}
	return out
}

// lockDirs returns the project directories whose lockfiles may lock the deps of file: the
// project of a build script, else every project of the consuming build, root project first.
func (ix *index) lockDirs(file string, st *fileState) []string {
	if st.kind == kindScript && isBuildScript(file) {
		return []string{path.Dir(file)}
	}
	return ix.projectDirs(ix.consumer(file))
}

// projectDirs returns the directories of the build at root holding a build script, root
// first (whether or not it has one), the others sorted.
func (ix *index) projectDirs(root string) []string {
	var dirs []string
	for dir := range ix.builds {
		if dir != root && ix.root(dir) == root {
			dirs = append(dirs, dir)
		}
	}
	slices.Sort(dirs)
	return append([]string{root}, dirs...)
}
