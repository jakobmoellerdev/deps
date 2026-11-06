// Package sbt updates the dependencies of sbt builds (*.sbt, project/*.scala,
// project/build.properties) and the scalafmt version of .scalafmt.conf.
package sbt

import (
	"bufio"
	"bytes"
	"cmp"
	"path"
	"regexp"
	"slices"
	"strings"
	"sync"

	sbtds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/sbt"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool/properties"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Name is the manager name (Renovate's).
const Name = "sbt"

// Dep types. Library dep types are the first configuration of `% config`.
const (
	DepTypeCompile  = "compile"
	DepTypeTest     = "test"
	DepTypeProvided = "provided"
	DepTypeRuntime  = "runtime"
	DepTypeOptional = "optional"
	DepTypeIt       = "it"
	DepTypePlugin   = "plugin"
	DepTypeScala    = "scala"
	DepTypeSbt      = "sbt"
)

// central is Maven Central (the maven datasource replaces it by DEPS_MAVEN_CENTRAL_URL).
const central = "https://repo.maven.apache.org/maven2/"

// Skip reasons.
const (
	skipNonLiteral   = "non-literal version"
	skipDynamic      = "dynamic version"
	skipScalaVersion = "unresolved Scala version"
	skipCoordinates  = "unresolved group or artifact"
)

// skipUnresolved is the skip reason of a version reference no definition of the build
// resolves.
func skipUnresolved(ref string) string { return "unresolved version " + ref }

// termText returns how a group or artifact term is written: the literal or the reference.
func termText(t term) string {
	if t.kind == termStr {
		return t.tok.text
	}
	return t.ref
}

// fileKind is the role of a matched file.
type fileKind uint8

const (
	kindBuild        fileKind = iota // *.sbt outside project/: the build definition
	kindMeta                         // project/*.sbt: the meta-build (plugins)
	kindProjectScala                 // project/*.scala: Scala sources of the build definition
	kindProperties                   // project/build.properties
	kindRepositories                 // the root `repositories` file (sbt's proxy repositories)
)

var (
	scalaRe      = regexp.MustCompile(`(^|/)project/[^/]+\.scala$`)
	propertiesRe = regexp.MustCompile(`(^|/)project/build\.properties$`)
)

// metaKind tells Link what an extracted dep was made from.
type metaKind uint8

const (
	metaFixed  metaKind = iota // complete at extraction (sbt.version)
	metaModule                 // a ModuleID
	metaScala                  // a scalaVersion setting
)

type depMeta struct {
	kind  metaKind
	found found   // metaModule
	scala setting // metaScala
}

// fileState is what Extract recorded about one file for Link.
type fileState struct {
	kind       fileKind
	scan       fileScan
	meta       []depMeta // by dep index
	sbtVersion string    // kindProperties
	proxies    []string  // kindRepositories: dep Source entries
}

type sbtManager struct {
	mu    sync.Mutex
	files map[string]*fileState // every file extracted in this run
}

// New returns the sbt manager.
func New() manager.Manager { return &sbtManager{files: map[string]*fileState{}} }

func (*sbtManager) Name() string { return Name }

// Match reports whether the slash path is an sbt file, a Scala file directly in a project/
// directory, a project/build.properties, or the `repositories` file at the root.
func (*sbtManager) Match(p string) bool {
	switch {
	case strings.HasSuffix(p, ".sbt"):
		return true
	case strings.HasSuffix(p, ".scala"):
		return scalaRe.MatchString(p)
	case strings.HasSuffix(p, "build.properties"):
		return propertiesRe.MatchString(p)
	}
	return p == "repositories"
}

func kindOf(file string) fileKind {
	dir := path.Dir(file)
	inProject := path.Base(dir) == "project"
	switch {
	case file == "repositories":
		return kindRepositories
	case strings.HasSuffix(file, "build.properties"):
		return kindProperties
	case strings.HasSuffix(file, ".scala"):
		return kindProjectScala
	case inProject:
		return kindMeta
	}
	return kindBuild
}

// Extract returns the deps of a file and records its definitions and settings for Link,
// which resolves references across the files of the build. Before Link, deps see only
// the file's own definitions.
func (m *sbtManager) Extract(file string, src []byte, _ manager.Env) ([]dep.Dep, error) {
	st := &fileState{kind: kindOf(file)}
	var deps []dep.Dep
	switch st.kind {
	case kindRepositories:
		st.proxies = proxies(src)
	case kindProperties:
		p, ok := properties.Parse(src)["sbt.version"]
		if !ok || p.Value == "" {
			break
		}
		st.sbtVersion = p.Value
		deps = append(deps, dep.Dep{
			Manager: Name, Datasource: "github-releases", Versioning: "semver", DepType: DepTypeSbt,
			File: file, Line: p.Line, Name: "sbt/sbt", Source: "https://github.com/sbt/sbt", Version: p.Value,
			RefSpan: dep.Span{Start: p.Start, End: p.End},
		})
		st.meta = append(st.meta, depMeta{kind: metaFixed})
	default:
		st.scan = scan(src)
		for _, f := range st.scan.modules {
			// A ModuleID whose group and artifact are literals that are not plain strings can
			// never resolve; references may resolve against another file of the build.
			if f.mod.group.kind == termStr && f.mod.artifact.kind == termStr && (!f.mod.group.tok.plain || !f.mod.artifact.tok.plain) {
				continue
			}
			st.meta = append(st.meta, depMeta{kind: metaModule, found: f})
		}
		for _, s := range st.scan.scala {
			st.meta = append(st.meta, depMeta{kind: metaScala, scala: s})
		}
		b := newBuild(".", map[string]*fileState{file: st}, []string{file})
		for _, mt := range st.meta {
			d, keep := b.dep(file, st, mt)
			if !keep { // group or artifact refers to a definition of another file
				d = dep.Dep{Manager: Name, Datasource: sbtds.PackageName, Versioning: "maven", DepType: libraryDepType(mt.found.mod.config),
					File: file, Line: mt.found.mod.line, Name: termText(mt.found.mod.group) + ":" + termText(mt.found.mod.artifact), Skip: skipCoordinates}
			}
			deps = append(deps, d)
		}
	}
	m.mu.Lock()
	m.files[file] = st
	m.mu.Unlock()
	return deps, nil
}

// Edits replaces the version; deps sharing a definition share the span.
func (*sbtManager) Edits(u dep.Update) ([]patch.Edit, error) {
	d := u.Dep
	return []patch.Edit{{Start: d.RefSpan.Start, End: d.RefSpan.End, Old: d.Version, New: u.NewVersion}}, nil
}

// Link resolves the deps of each build against the definitions, settings and repositories
// of all its files, drops ModuleIDs whose group or artifact is not a string, and marks the
// deps sharing one version literal with their definition (VersionRef), then forgets the
// run's files.
func (m *sbtManager) Link(files [][]dep.Dep) {
	m.mu.Lock()
	states := m.files
	m.files = map[string]*fileState{}
	m.mu.Unlock()
	builds := groupBuilds(states)
	type key struct {
		file string
		span dep.Span
	}
	type member struct {
		d     *dep.Dep
		owner string // the definition whose ModuleID holds the version; "" = none
	}
	shared := map[key][]member{}
	scalaSeen := map[key]bool{}
	for fi, deps := range files {
		if len(deps) == 0 {
			continue
		}
		file := deps[0].File
		st := states[file]
		if st == nil || len(st.meta) != len(deps) {
			continue
		}
		b := builds[file]
		out := deps[:0]
		var owners []string
		for i, mt := range st.meta {
			d, keep := deps[i], true
			if mt.kind != metaFixed {
				d, keep = b.dep(file, st, mt)
			}
			if !keep {
				continue
			}
			if mt.kind == metaScala && d.Skip == "" {
				k := key{d.File, d.RefSpan}
				if scalaSeen[k] { // several scalaVersion settings referring to one definition
					continue
				}
				scalaSeen[k] = true
			}
			out = append(out, d)
			owners = append(owners, mt.found.owner)
		}
		files[fi] = out
		for i := range out {
			if out[i].Skip == "" {
				k := key{out[i].File, out[i].RefSpan}
				shared[k] = append(shared[k], member{&out[i], owners[i]})
			}
		}
	}
	for _, ms := range shared {
		if len(ms) < 2 {
			continue
		}
		ref := ""
		for _, mb := range ms {
			ref = cmp.Or(ref, mb.d.VersionRef, mb.owner)
		}
		for _, mb := range ms {
			mb.d.VersionRef = cmp.Or(mb.d.VersionRef, mb.owner, ref)
		}
	}
}

// proxies reads sbt's repositories file: `maven-central` and `name: url[, ivy pattern]`
// entries as dep Source entries (`ivy=<url>` for entries with an Ivy pattern).
func proxies(src []byte) []string {
	var out []string
	sc := bufio.NewScanner(bytes.NewReader(src))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "maven-central":
			out = append(out, central)
			continue
		case line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "["):
			continue
		}
		_, rest, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		u, pattern, _ := strings.Cut(strings.TrimSpace(rest), ",")
		u = strings.TrimSpace(u)
		if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
			continue
		}
		if strings.Contains(pattern, "[organi") { // [organization], also in Ivy's British spelling
			u = sbtds.IvyID + "=" + u
		}
		out = append(out, u)
	}
	return out
}

// libraryDepType returns the dep type of a `% config` (first configuration, lower case).
func libraryDepType(config string) string {
	c := strings.ToLower(strings.TrimSpace(config))
	if i := strings.IndexAny(c, ";,-"); i >= 0 {
		c = strings.TrimSpace(c[:i])
	}
	c = strings.TrimPrefix(c, "configurations.")
	switch c {
	case DepTypeTest, DepTypeProvided, DepTypeRuntime, DepTypeOptional, DepTypeIt:
		return c
	case "integrationtest":
		return DepTypeIt
	}
	return DepTypeCompile
}

// dynamic reports whether v is an Ivy dynamic revision (`latest.release`, `1.+`, `[1.0,2.0)`).
func dynamic(v string) bool {
	return strings.HasPrefix(v, "latest.") || strings.Contains(v, "+") || strings.ContainsAny(v[:1], "[]()")
}

// Scala version shapes, ported from sbt librarymanagement CrossVersionUtil.
var (
	scalaReleaseV    = regexp.MustCompile(`^(\d{1,19})\.(\d{1,19})\.(\d{1,19})(-\d+)?$`)
	scalaBinCompatV  = regexp.MustCompile(`^(\d{1,19})\.(\d{1,19})\.(\d{1,19})(-\w+(?:\.\w+)*)?-bin(-.*)?$`)
	scalaNonReleaseN = regexp.MustCompile(`^(\d{1,19})\.(\d{1,19})\.(\d{1,19})((?:-\w+(?:\.\w+)*)*)$`)
	scalaNonRelease1 = regexp.MustCompile(`^(\d{1,19})\.(\d{1,19})\.(\d{1,19})(-\w+(?:\.\w+)*)$`)
)

// scalaBinary returns the Scala binary version of a full Scala version like sbt's
// CrossVersion.binaryScalaVersion: `3` for Scala 3 releases and for prereleases past 3.0.0,
// `2.13` for 2.13.x releases, `-bin` builds and one-word prereleases with a patch above 0;
// other forms (2.13.0-M5, 3.0.0-RC1) are their own binary version.
func scalaBinary(v string) string {
	if v == "3" {
		return v
	}
	if strings.HasPrefix(v, "3.") {
		if m := scalaBinCompatV.FindStringSubmatch(v); m != nil {
			v = m[1] + "." + m[2] + "." + m[3] + m[4]
		}
		if scalaReleaseV.MatchString(v) {
			return "3"
		}
		if m := scalaNonReleaseN.FindStringSubmatch(v); len(m) == 5 && (m[2] != "0" || m[3] != "0") {
			return "3"
		}
		return v
	}
	m := scalaReleaseV.FindStringSubmatch(v)
	if m == nil {
		m = scalaBinCompatV.FindStringSubmatch(v)
	}
	if m == nil {
		if m = scalaNonRelease1.FindStringSubmatch(v); len(m) == 5 && strings.Trim(m[3], "0") == "" {
			m = nil
		}
	}
	// Binary versions start at Scala 2.10; earlier versions are their own.
	if len(m) < 3 || m[1] == "2" && len(m[2]) == 1 {
		return v
	}
	return m[1] + "." + m[2]
}

// scalaPatch returns the CrossVersion.patch suffix of a full Scala version: the version
// without a `-bin...` build suffix.
func scalaPatch(v string) string {
	if m := scalaBinCompatV.FindStringSubmatch(v); m != nil {
		return m[1] + "." + m[2] + "." + m[3] + m[4]
	}
	return v
}

// sources joins dep Source entries, without duplicates.
func sources(entries ...[]string) string {
	var out []string
	for _, es := range entries {
		for _, e := range es {
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return strings.Join(out, " ")
}
