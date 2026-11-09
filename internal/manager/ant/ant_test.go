package ant

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

const central = " central=https://repo.maven.apache.org/maven2/"

func TestMatch(t *testing.T) {
	for p, want := range map[string]bool{
		"build.xml": true, "sub/dir/build.xml": true,
		"mybuild.xml": false, "build.xml.bak": false, "pom.xml": false, "build.properties": false,
	} {
		if got := New().Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	if New().Name() != Name {
		t.Errorf("Name() = %q", New().Name())
	}
}

// TestFixtures extracts real-world build files: the Maven Ant Tasks (Esri's spatial framework,
// Cassandra 3.11 with an imported Resolver build file and a properties file) and the Maven
// Resolver Ant Tasks (VBuilder's property versions in coords, ball-http's scoped coords).
func TestFixtures(t *testing.T) {
	tests := []struct {
		dir   string
		count int
		want  []string // a subset, in order
	}{
		{dir: "esri", count: 6, want: []string{
			"org.apache.hadoop:hadoop-client 2.2.0 [compile] @build.xml:7",
			"org.apache.hive:hive-exec 0.12.0 [compile] @build.xml:8",
			"org.apache.hive:hive-serde 0.12.0 [compile] @build.xml:9",
			"com.esri.geometry:esri-geometry-api 2.2.4 [compile] @build.xml:10",
			"com.fasterxml.jackson.core:jackson-core 2.10.5 [compile] @build.xml:11",
			"com.fasterxml.jackson.core:jackson-databind 2.10.5 [compile] @build.xml:12",
		}},
		{dir: "vbuilder", count: 5, want: []string{
			"org.jetbrains:annotations 20.1.0 [compile] @build.xml:58 ref=annotationsVersion",
			"org.violetlib:vtypes 1 [compile] @build.xml:59 ref=vtypesVersion",
			"org.violetlib:vcollections 1 [compile] @build.xml:60 ref=vcollectionsVersion",
			"org.apache.maven.resolver:maven-resolver-ant-tasks 1.5.0 [compile] @build.xml:66 ref=mavenResolverAntTasksVersion",
			"commons-logging:commons-logging 1.3.0 [compile] @build.xml:67",
		}},
		{dir: "ball-http", count: 1, want: []string{
			// version is the third part of `group:artifact:version:type:scope`; the import of
			// `${dev.hcf.ball:parent:xml}` (set by the resolve task at run time) is ignored
			"dev.hcf.ball:parent 2025.0.2 [compile] @build.xml:31",
		}},
		{dir: "cassandra", count: 87, want: []string{
			"org.xerial.snappy:snappy-java 1.1.10.4 [compile] @build.xml:337",
			"commons-io:commons-io 2.6 [test] @build.xml:351",
			"com.github.jbellis:jamm 0.3.0 [compile] @build.xml:372 ref=jamm.version",
			"org.apache.hadoop:hadoop-core 1.0.3 [provided] @build.xml:390",
			"org.jacoco:org.jacoco.agent 0.7.5.201505241946 [compile] @build.xml:404 ref=jacoco.version",
			"org.jacoco:org.jacoco.ant 0.7.5.201505241946 [compile] @build.xml:405 ref=jacoco.version",
			"org.apache.cassandra:cassandra-all ${base.version}-SNAPSHOT [compile] @build.xml:420 (unsupported version ${base.version}-SNAPSHOT)",
			"io.netty:netty-bom 4.1.58.Final [provided] @build.xml:432",
			"org.ow2.asm:asm-tree 5.0.4 [test] @build.xml:463 ref=asm.version",
			// the imported .build/build-resolver.xml (`${basedir}/.build/…`); its jacoco version
			// is the property of build.xml
			"com.datastax.wikitext:wikitext-core-ant 1.3 [compile] @.build/build-resolver.xml:63",
			"org.jacoco:org.jacoco.ant 0.7.5.201505241946 [compile] @build.xml:129 ref=jacoco.version",
			"de.jflex:jflex 1.6.0 [compile] @.build/build-resolver.xml:94",
		}},
	}
	for _, tt := range tests {
		t.Run(tt.dir, func(t *testing.T) {
			files := extractDir(t, tt.dir)
			checkSpans(t, os.DirFS(filepath.Join("testdata", tt.dir)), files)
			got := summaries(files)
			if len(got) != tt.count {
				t.Errorf("%d deps, want %d:\n%s", len(got), tt.count, strings.Join(got, "\n"))
			}
			i := 0
			for _, w := range tt.want {
				j := slices.Index(got[i:], w)
				if j < 0 {
					t.Errorf("missing (in order) %q in:\n%s", w, strings.Join(got, "\n"))
					continue
				}
				i += j + 1
			}
		})
	}
}

func TestExtract(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name: "attributes, scopes and coords",
			files: map[string]string{"build.xml": `<project xmlns:artifact="antlib:org.apache.maven.artifact.ant" xmlns:resolver="antlib:org.apache.maven.resolver.ant">
  <artifact:dependencies pathId="cp">
    <dependency groupId="junit" artifactId="junit" version="4.13.2" scope="test"/>
    <dependency groupId='org.slf4j' artifactId='slf4j-api' version='2.0.9' scope="runtime"/>
    <dependency groupId="a" artifactId="b" version="1.0" scope="import"/>
    <dependency groupId="a" artifactId="managed"/>
    <dependency groupId="a" artifactId="empty" version=""/>
    <dependency artifactId="nogroup" version="1"/>
  </artifact:dependencies>
  <resolver:resolve>
    <resolver:dependencies>
      <resolver:dependency coords="org.python:jython-standalone:2.7.2"/>
      <dependency coords="g:four:1.0:provided"/>
      <dependency coords="g:five:2.0:pom:system"/>
      <dependency coords="g:six:3.0:jar:sources:test"/>
      <dependency coords="g:scopeless:4.0:custom" scope="test"/>
      <dependency coords="g:a"/>
      <dependency coords="g::1.0"/>
      <dependency coords="g:a:1:b:c:d:e"/>
      <dependency coords="g:a:1 .0"/>
    </resolver:dependencies>
  </resolver:resolve>
</project>`},
			want: []string{
				"junit:junit 4.13.2 [test] @build.xml:3",
				"org.slf4j:slf4j-api 2.0.9 [runtime] @build.xml:4",
				"a:b 1.0 [compile] @build.xml:5",
				"org.python:jython-standalone 2.7.2 [compile] @build.xml:12",
				"g:four 1.0 [provided] @build.xml:13",
				"g:five 2.0 [system] @build.xml:14",
				"g:six 3.0 [test] @build.xml:15",
				"g:scopeless 4.0 [compile] @build.xml:16",
			},
		},
		{
			name: "inline properties: first definition wins, chains, cycles, mixed values",
			files: map[string]string{"build.xml": `<project>
  <property name="v" value="1.0"/>
  <property name="v" value="2.0"/>
  <property name="alias" value="${v}"/>
  <property name="a" value="${b}"/>
  <property name="b" value="${a}"/>
  <property name="mixed" value="${v}-jre"/>
  <property name="ent" value="1.0&amp;"/>
  <property name="empty" value=""/>
  <target name="t">
    <property name="late" value="3.0"/>
  </target>
  <dependencies>
    <dependency groupId="g" artifactId="direct" version="${v}"/>
    <dependency coords="g:alias:${alias}"/>
    <dependency groupId="g" artifactId="cycle" version="${a}"/>
    <dependency groupId="g" artifactId="mixed" version="${mixed}"/>
    <dependency groupId="g" artifactId="partial" version="${v}.1"/>
    <dependency groupId="g" artifactId="undefined" version="${nope}"/>
    <dependency groupId="g" artifactId="empty" version="${empty}"/>
    <dependency groupId="g" artifactId="entity" version="${ent}"/>
    <dependency groupId="g" artifactId="literal-entity" version="1&amp;"/>
    <dependency groupId="g" artifactId="late" version="${late}"/>
    <dependency groupId="g" artifactId="basedir" version="${basedir}"/>
  </dependencies>
</project>`},
			want: []string{
				"g:direct 1.0 [compile] @build.xml:14 ref=v",
				"g:alias 1.0 [compile] @build.xml:15 ref=v",
				"g:cycle ${a} [compile] @build.xml:16 (property cycle ${a})",
				"g:mixed ${v}-jre [compile] @build.xml:17 (unsupported version ${v}-jre)",
				"g:partial ${v}.1 [compile] @build.xml:18 (unsupported version ${v}.1)",
				"g:undefined ${nope} [compile] @build.xml:19 (unresolved property ${nope})",
				"g:empty ${empty} [compile] @build.xml:20 (unresolved property ${empty})",
				"g:entity 1.0& [compile] @build.xml:21 (cannot locate value)",
				"g:literal-entity 1& [compile] @build.xml:22 (cannot locate value)",
				"g:late 3.0 [compile] @build.xml:23 ref=late",
				"g:basedir ${basedir} [compile] @build.xml:24 (unresolved property ${basedir})",
			},
		},
		{
			name: "properties files: basedir, prefix, inline definitions first, last key of a file wins",
			files: map[string]string{
				"sub/build.xml": `<project basedir="..">
  <property name="guava.version" value="33.0.0-jre"/>
  <property file="versions.properties"/>
  <property file="${conf}/more.properties" prefix="m"/>
  <property file="versions.properties"/>
  <property file="missing.properties"/>
  <property file="${undefined}/x.properties"/>
  <property file="/etc/x.properties"/>
  <property file="../outside.properties"/>
  <dependency groupId="com.google.guava" artifactId="guava" version="${guava.version}"/>
  <dependency groupId="org.slf4j" artifactId="slf4j-api" version="${slf4j.version}"/>
  <dependency groupId="org.slf4j" artifactId="slf4j-simple" version="${slf4j.version}" scope="test"/>
  <dependency groupId="x" artifactId="prefixed" version="${m.x.version}"/>
  <dependency groupId="x" artifactId="escaped" version="${escaped}"/>
  <dependency groupId="x" artifactId="outside" version="${outside}"/>
</project>`,
				"versions.properties":  "# versions\n! more\n\nguava.version=1.0\nslf4j.version = 1.7.0\nslf4j.version: 2.0.9  \r\nescaped=1\\.0\nconf conf\n",
				"conf/more.properties": "x.version 4.2\n",
				"outside.properties":   "outside=1\n",
			},
			want: []string{
				"com.google.guava:guava 33.0.0-jre [compile] @sub/build.xml:10 ref=guava.version",
				"org.slf4j:slf4j-api 2.0.9 [compile] @versions.properties:6 ref=slf4j.version",
				"org.slf4j:slf4j-simple 2.0.9 [test] @versions.properties:6 ref=slf4j.version",
				"x:prefixed 4.2 [compile] @conf/more.properties:1 ref=m.x.version",
				"x:escaped 1.0 [compile] @sub/build.xml:14 (cannot locate value)",
				"x:outside ${outside} [compile] @sub/build.xml:15 (unresolved property ${outside})",
			},
		},
		{
			name: "imports: relative to the importer, ${basedir}, properties shared in document order",
			files: map[string]string{
				"build.xml": `<?xml version="1.0" encoding="ISO-8859-1"?>
<!DOCTYPE project [<!ENTITY common SYSTEM "common.xml">]>
<project name="p" basedir=".">
  &common;
  <property name="lib.dir" value="${basedir}/lib"/>
  <import file="${lib.dir}/deps.xml"/>
  <import file="build.xml"/>
  <import file="missing.xml"/>
  <import file="${user.home}/x.xml"/>
  <import file="../../outside.xml"/>
  <import file="lib/broken.xml"/>
  <dependency groupId="g" artifactId="root" version="${shared}"/>
</project>`,
				"lib/deps.xml": `<project>
  <property name="shared" value="1.0"/>
  <import file="nested/more.xml"/>
  <dependency groupId="g" artifactId="imported" version="${shared}"/>
</project>`,
				"lib/nested/more.xml": `<project><dependency groupId="g" artifactId="nested" version="2.0"/><import file="../deps.xml"/></project>`,
				"lib/broken.xml":      `<project><dependency groupId="g" artifactId="broken" version="1"/>`,
			},
			want: []string{
				"g:nested 2.0 [compile] @lib/nested/more.xml:1",
				"g:imported 1.0 [compile] @lib/deps.xml:4 ref=shared",
				"g:root 1.0 [compile] @lib/deps.xml:2 ref=shared",
			},
		},
		{
			name: "groups and artifacts with properties",
			files: map[string]string{"build.xml": `<project>
  <property name="group" value="org.example"/>
  <dependency groupId="${group}" artifactId="lib" version="1.0"/>
  <dependency coords="${group}:lib2:2.0"/>
  <dependency groupId="${nope}" artifactId="lib" version="1.0"/>
  <dependency coords="g:${a:b}:1.0"/>
</project>`},
			want: []string{
				"org.example:lib 1.0 [compile] @build.xml:3",
				"org.example:lib2 2.0 [compile] @build.xml:4",
				"${nope}:lib 1.0 [compile] @build.xml:5 (unresolved property in ${nope}:lib)",
				"g:${a:b} 1.0 [compile] @build.xml:6 (unresolved property in g:${a:b})",
			},
		},
		{
			name: "repositories",
			files: map[string]string{
				"build.xml": `<project xmlns:artifact="antlib:org.apache.maven.artifact.ant">
  <property name="repo.url" value="https://repo.example.com/maven2"/>
  <artifact:remoteRepository id="shared" url="${repo.url}"/>
  <artifact:remoteRepository id="broken" url="${nope}"/>
  <dependency groupId="g" artifactId="central" version="1"/>
  <artifact:dependencies>
    <remoteRepository id="corp" url="https://corp.example.com/maven/"/>
    <remoteRepository url="https://repo.example.com/maven2"/>
    <remoteRepository url="${nope}"/>
    <dependency groupId="g" artifactId="corp" version="1"/>
    <dependency coords="g:corp-coords:1"/>
  </artifact:dependencies>
  <artifact:dependencies>
    <remoteRepository refid="shared"/>
    <remoteRepository refid="broken"/>
    <remoteRepository refid="undefined"/>
    <dependency groupId="g" artifactId="ref" version="1"/>
  </artifact:dependencies>
  <artifact:dependencies settingsFile="conf/settings.xml">
    <remoteRepository id="central" url="https://repo1.maven.org/maven2"/>
    <dependency groupId="g" artifactId="settings" version="1"/>
  </artifact:dependencies>
  <artifact:dependencies settingsFile="missing.xml">
    <dependency groupId="g" artifactId="missing-settings" version="1"/>
  </artifact:dependencies>
  <artifact:dependencies settingsFile="broken-settings.xml">
    <dependency groupId="g" artifactId="broken-settings" version="1"/>
  </artifact:dependencies>
  <target name="t">
    <artifact:dependencies>
      <remoteRepository url="https://outer.example.com/"/>
      <group>
        <remoteRepository url="https://inner.example.com/"/>
        <dependency groupId="g" artifactId="inner" version="1"/>
      </group>
      <other><dependency groupId="g" artifactId="outer" version="1"/></other>
    </artifact:dependencies>
  </target>
</project>`,
				"conf/settings.xml": `<settings xmlns="http://maven.apache.org/SETTINGS/1.0.0">
  <mirrors><mirror><id>mirror</id><mirrorOf>*</mirrorOf><url>https://mirror.example.com/maven</url></mirror></mirrors>
  <profiles>
    <profile><repositories><repository><id>profile</id><url> https://profile.example.com/maven2/ </url></repository><repository><id>nourl</id></repository></repositories></profile>
  </profiles>
</settings>`,
				"broken-settings.xml": `<settings><mirrors>`,
			},
			want: []string{
				"g:central 1 [compile] @build.xml:5",
				"g:corp 1 [compile] @build.xml:10 src=corp=https://corp.example.com/maven/ https://repo.example.com/maven2/" + central,
				"g:corp-coords 1 [compile] @build.xml:11 src=corp=https://corp.example.com/maven/ https://repo.example.com/maven2/" + central,
				"g:ref 1 [compile] @build.xml:17 src=shared=https://repo.example.com/maven2/" + central,
				"g:settings 1 [compile] @build.xml:21 src=mirror=https://mirror.example.com/maven/ profile=https://profile.example.com/maven2/ central=https://repo1.maven.org/maven2/",
				"g:missing-settings 1 [compile] @build.xml:24",
				"g:broken-settings 1 [compile] @build.xml:27",
				"g:inner 1 [compile] @build.xml:34 src=https://inner.example.com/" + central,
				"g:outer 1 [compile] @build.xml:36 src=https://outer.example.com/" + central,
			},
		},
		{
			name: "Maven ranges",
			files: map[string]string{"build.xml": `<project>
  <property name="r" value="[1.0,2.0)"/>
  <dependency groupId="g" artifactId="range" version="${r}"/>
  <dependency groupId="g" artifactId="exact" version="[1.5]"/>
  <dependency coords="g:bad:[2.0,1.0)"/>
</project>`},
			want: []string{
				"g:range 1.0 [compile] @build.xml:3 ref=r",
				"g:exact 1.5 [compile] @build.xml:4",
				"g:bad [2.0,1.0) [compile] @build.xml:5 (version range)",
			},
		},
		{
			name:  "byte order mark",
			files: map[string]string{"build.xml": "\xef\xbb\xbf<project><dependency groupId=\"g\" artifactId=\"a\" version=\"1\"/></project>"},
			want:  []string{"g:a 1 [compile] @build.xml:1"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fsys := mapFS(tt.files)
			files := extractFS(t, fsys)
			checkSpans(t, fsys, files)
			if got := summaries(files); !slices.Equal(got, tt.want) {
				t.Errorf("got:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestExtractMalformed(t *testing.T) {
	for _, src := range []string{
		`<project><dependency groupId="g" artifactId="a" version="1"/>`,
		`<project></target></project>`,
		`</project>`,
		`<project><a b="c></project>`,
	} {
		if _, err := New().Extract("build.xml", []byte(src), manager.Env{}); err == nil {
			t.Errorf("Extract(%q) succeeded", src)
		}
	}
}

// TestExtractWithoutReadFile: without manager.Env.ReadFile, referenced files are not read.
func TestExtractWithoutReadFile(t *testing.T) {
	src := `<project><property file="v.properties"/><import file="common.xml"/>
<artifact:dependencies settingsFile="settings.xml"><dependency groupId="g" artifactId="a" version="${v}"/></artifact:dependencies></project>`
	deps, err := New().Extract("build.xml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	if len(deps) != 1 || deps[0].Skip != "unresolved property ${v}" || deps[0].Source != "" {
		t.Errorf("deps = %+v", deps)
	}
}

// TestLinkSharedFiles: a build file imported by two build files (and itself a build.xml) and a
// properties definition both read yield their deps once.
func TestLinkSharedFiles(t *testing.T) {
	fsys := mapFS(map[string]string{
		"a/build.xml": `<project><property file="../versions.properties"/><import file="../common/build.xml"/>
<dependency groupId="g" artifactId="v" version="${v}"/></project>`,
		"b/build.xml": `<project><property file="../versions.properties"/><import file="../common/build.xml"/>
<dependency groupId="g" artifactId="v" version="${v}"/><dependency groupId="g" artifactId="v" version="${v}" scope="test"/></project>`,
		"common/build.xml":    `<project><dependency groupId="g" artifactId="common" version="1.0"/></project>`,
		"versions.properties": "v=2.0\n",
	})
	files := extractFS(t, fsys)
	want := map[string][]string{
		"a/build.xml":      {"g:common 1.0 [compile] @common/build.xml:1", "g:v 2.0 [compile] @versions.properties:1 ref=v"},
		"b/build.xml":      {"g:v 2.0 [test] @versions.properties:1 ref=v"},
		"common/build.xml": nil,
	}
	for f, w := range want {
		var got []string
		for _, d := range files[f] {
			got = append(got, summary(d))
		}
		if !slices.Equal(got, w) {
			t.Errorf("%s: got %q, want %q", f, got, w)
		}
	}
}

func TestEdits(t *testing.T) {
	src := `<project>
  <property name="r" value="[1.0,2.0)"/>
  <dependency groupId="org.example" artifactId="lib" version="1.0"/>
  <dependency coords="org.example:coords:1.0:jar:test"/>
  <dependency groupId="g" artifactId="range" version="${r}"/>
</project>`
	deps, err := New().Extract("build.xml", []byte(src), manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	m := New()
	tests := []struct {
		name string
		u    dep.Update
		want string // the dependency line after the edits
		err  bool
	}{
		{name: "attribute", u: dep.Update{Dep: deps[0], NewVersion: "1.2"}, want: `<dependency groupId="org.example" artifactId="lib" version="1.2"/>`},
		{name: "coords", u: dep.Update{Dep: deps[1], NewVersion: "2.0"}, want: `<dependency coords="org.example:coords:2.0:jar:test"/>`},
		{name: "replacement", u: dep.Update{Dep: deps[0], NewVersion: "3.0", NewName: "com.example:lib2"}, want: `<dependency groupId="com.example" artifactId="lib2" version="3.0"/>`},
		{name: "coords replacement", u: dep.Update{Dep: deps[1], NewVersion: "2.0", NewName: "org.other:coords"}, want: `<dependency coords="org.other:coords:2.0:jar:test"/>`},
		{name: "range extended", u: dep.Update{Dep: deps[2], NewVersion: "2.5"}, want: `<property name="r" value="[1.0,2.6)"/>`},
		{name: "range pinned", u: dep.Update{Dep: deps[2], NewVersion: "2.5", RangeStrategy: dep.RangePin}, want: `<property name="r" value="2.5"/>`},
		{name: "range admits", u: dep.Update{Dep: deps[2], NewVersion: "1.5"}, err: true},
		{name: "bad name", u: dep.Update{Dep: deps[0], NewVersion: "1.2", NewName: "nocolon"}, err: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			edits, err := m.Edits(tt.u)
			if tt.err {
				if err == nil {
					t.Fatalf("Edits succeeded: %v", edits)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply([]byte(src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("got:\n%s\nwant line %s", out, tt.want)
			}
		})
	}
}

func TestRanges(t *testing.T) {
	m := New().(manager.RangeUpdater)
	d := dep.Dep{Range: "[1.0,2.0)"}
	if !m.InRange(d, "1.5") || m.InRange(d, "2.0") || m.InRange(dep.Dep{Range: "[2.0,1.0)"}, "1.5") {
		t.Error("InRange")
	}
	if _, ok := m.NewRange(dep.Dep{Range: "[2.0,1.0)"}, "3.0", ""); ok {
		t.Error("NewRange of an invalid range")
	}
	if got, ok := m.NewRange(d, "[1.0,2.0)", dep.RangePin); ok || got != "[1.0,2.0)" {
		t.Errorf("NewRange pin to itself = %q, %v", got, ok)
	}
}

func TestRename(t *testing.T) {
	r := New().(manager.Renamer)
	d, err := r.Rename(dep.Dep{Name: "a:b"}, "c:d")
	if err != nil || d.Name != "c:d" {
		t.Errorf("Rename = %v, %v", d.Name, err)
	}
	if _, err := r.Rename(dep.Dep{Name: "a:b"}, "c"); err == nil {
		t.Error("Rename to c succeeded")
	}
}
