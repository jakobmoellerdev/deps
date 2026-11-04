// Package gradle updates Maven dependencies and Gradle plugins declared in Gradle version
// catalogs (*.versions.toml), Gradle build scripts, gradle.properties and buildSrc /
// convention-plugin sources.
package gradle

import (
	"path"
	"regexp"
	"sort"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "gradle"

// Dep types.
const (
	DepTypeDependencies = "dependencies" // catalog [libraries] and build-script dependencies
	DepTypePlugin       = "plugin"       // catalog [plugins] and build-script plugins
)

// Repositories (dep.Dep.Source) of deps whose build declares none, queried in order. Google
// Maven serves AndroidX and the com.android.* plugin markers; the Gradle plugin portal serves
// plugin artifacts and markers declared as libraries (buildSrc, convention plugins).
const (
	LibraryRepositories = centralRepo + " " + googleRepo + " " + portalRepo
	PluginRepositories  = portalRepo + " " + googleRepo
)

var (
	catalogRe    = regexp.MustCompile(`(^|/)[^/]+\.versions\.toml$`)
	scriptRe     = regexp.MustCompile(`(^|/)[^/]+\.gradle(\.kts)?$`)
	propertiesRe = regexp.MustCompile(`(^|/)gradle\.properties$`)
	// sourceRe: buildSrc sources and the Kotlin sources of build-logic style included builds.
	sourceRe = regexp.MustCompile(`(^|/)buildSrc/.+\.(kt|groovy)$|(^|/)(build-logic|build-conventions|convention-plugins)/(.+/)?src/main/kotlin/.+\.kt$`)
)

// fileKind is the role of a matched file.
type fileKind uint8

const (
	kindScript     fileKind = iota // *.gradle(.kts) other than settings
	kindSettings                   // settings.gradle(.kts)
	kindProperties                 // gradle.properties
	kindCatalog                    // *.versions.toml
	kindSource                     // buildSrc / convention plugin source
)

func kindOf(file string) fileKind {
	switch base := path.Base(file); {
	case catalogRe.MatchString(file):
		return kindCatalog
	case base == "gradle.properties":
		return kindProperties
	case base == "settings.gradle" || base == "settings.gradle.kts":
		return kindSettings
	case scriptRe.MatchString(file):
		return kindScript
	}
	return kindSource
}

// isBuildScript reports whether file is the build script of a project.
func isBuildScript(file string) bool {
	base := path.Base(file)
	return base == "build.gradle" || base == "build.gradle.kts"
}

// depClass selects the repositories of a dep.
type depClass uint8

const (
	classLibrary   depClass = iota
	classPlugin             // plugins { } and catalog [plugins]
	classClasspath          // buildscript { dependencies { classpath … } }
)

// depMeta is what Link needs to know about an extracted dep beyond dep.Dep.
type depMeta struct {
	class depClass
	ref   string // key of the variable the version refers to; "" = none
}

// fileState is what Extract recorded about one file for Link.
type fileState struct {
	kind  fileKind
	defs  defs
	meta  []depMeta // by dep index
	repos repoDecls
}

type gradleManager struct {
	mu    sync.Mutex
	files map[string]*fileState // every file extracted in this run
	lock  lockState             // left by Link for ReadLocks
}

// New returns the gradle manager.
func New() manager.Manager { return &gradleManager{files: map[string]*fileState{}} }

func (*gradleManager) Name() string { return Name }

func (*gradleManager) Match(p string) bool {
	if sourceRe.MatchString(p) {
		return !strings.Contains(p, "buildSrc/build/")
	}
	return catalogRe.MatchString(p) || scriptRe.MatchString(p) || propertiesRe.MatchString(p)
}

// Extract returns the deps of a version catalog, build script or buildSrc source and records
// the file's variable definitions and repositories for Link; gradle.properties only defines
// variables. A catalog that is not valid TOML is an error.
func (m *gradleManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	st := &fileState{kind: kindOf(file)}
	var deps []dep.Dep
	switch st.kind {
	case kindCatalog:
		var err error
		if deps, err = extractCatalog(file, src); err != nil {
			return nil, err
		}
		st.meta = make([]depMeta, len(deps))
		for i, d := range deps {
			if d.DepType == DepTypePlugin {
				st.meta[i].class = classPlugin
			}
		}
	case kindProperties:
		st.defs = properties(src)
	default:
		deps, st.meta, st.defs, st.repos = extractScript(file, src, st.kind)
	}
	m.mu.Lock()
	m.files[file] = st
	m.mu.Unlock()
	return deps, nil
}

// Edits replaces the version token; deps sharing a definition share the span. A range dep
// (dynamic version) keeps its form: see rangeEdit.
func (*gradleManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	if d.Range != "" {
		e, err := rangeEdit(d, u.NewVersion)
		if err != nil {
			return nil, err
		}
		return []patch.Edit{e}, nil
	}
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

func newDep(file, depType string, line int) dep.Dep {
	src := LibraryRepositories
	if depType == DepTypePlugin {
		src = PluginRepositories
	}
	return dep.Dep{Manager: Name, Datasource: "maven", Versioning: "maven", DepType: depType, File: file, Line: line, Source: src}
}

// lines maps byte offsets to 1-based line numbers.
type lines []int // offsets of '\n'

func newLines(src []byte) lines {
	var l lines
	for i, c := range src {
		if c == '\n' {
			l = append(l, i)
		}
	}
	return l
}

func (l lines) at(off int) int { return sort.SearchInts(l, off) + 1 }
