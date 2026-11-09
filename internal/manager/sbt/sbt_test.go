package sbt

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

const (
	pluginSrc = "ivy=https://repo.scala-sbt.org/scalasbt/sbt-plugin-releases/ https://repo.maven.apache.org/maven2/"
)

func find(t *testing.T, deps map[string][]dep.Dep, file, name string) dep.Dep {
	t.Helper()
	for _, d := range deps[file] {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("%s: no dep %s in %v", file, name, deps[file])
	return dep.Dep{}
}

func TestMatch(t *testing.T) {
	m := New()
	for p, want := range map[string]bool{
		"build.sbt": true, "modules/core/build.sbt": true, "project/plugins.sbt": true, "project/Dependencies.scala": true,
		"sub/project/Deps.scala": true, "project/build.properties": true, "sub/project/build.properties": true, "repositories": true,
		"src/main/scala/App.scala": false, "project/sub/X.scala": false, "build.properties": false, "conf/repositories": false, "build.sbt.bak": false,
	} {
		if got := m.Match(p); got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	s := NewScalafmt()
	if !s.Match(".scalafmt.conf") || !s.Match("a/.scalafmt.conf") || s.Match("scalafmt.conf") {
		t.Error("scalafmt Match")
	}
	if New().Name() != Name || s.Name() != ScalafmtName {
		t.Error("Name")
	}
}

// scala-steward (957428de): a project/Dependencies.scala object of ModuleID vals, some
// sharing another's version through `.revision`, and scalaVersion from a build.sbt val.
func TestScalaSteward(t *testing.T) {
	deps, _ := extractDir(t, New(), "testdata/scala-steward")
	scala := find(t, deps, "build.sbt", "org.scala-lang:scala-library")
	if scala.Version != "2.13.18" || scala.VersionRef != "Scala213" || scala.Line != 15 || scala.Datasource != "maven" || scala.DepType != DepTypeScala {
		t.Errorf("scala = %+v", scala)
	}
	cats := find(t, deps, "project/Dependencies.scala", "org.typelevel:cats-core_2.13")
	laws := find(t, deps, "project/Dependencies.scala", "org.typelevel:cats-laws_2.13")
	if cats.Version != "2.13.0" || laws.Version != "2.13.0" || cats.RefSpan != laws.RefSpan || cats.VersionRef != "Dependencies.catsCore" || laws.VersionRef != cats.VersionRef {
		t.Errorf("cats-core = %+v\ncats-laws = %+v", cats, laws)
	}
	java := find(t, deps, "project/Dependencies.scala", "org.bouncycastle:bcprov-jdk15to18")
	if java.Version != "1.86" || java.VersionRef != "" || java.Source != "" || java.Datasource != "sbt-package" || java.DepType != DepTypeCompile {
		t.Errorf("bcprov = %+v", java)
	}
	if n := len(deps["project/Dependencies.scala"]); n != 42 {
		t.Errorf("Dependencies.scala: %d deps", n)
	}
	for _, d := range deps["project/Dependencies.scala"] {
		if d.Skip != "" || strings.Contains(d.Name, "mill1_3") {
			t.Errorf("unexpected %s", summary(d))
		}
	}
	sbtDep := find(t, deps, "project/build.properties", "sbt/sbt")
	if sbtDep.Version != "1.13.1" || sbtDep.Datasource != "github-releases" || sbtDep.Versioning != "semver" {
		t.Errorf("sbt = %+v", sbtDep)
	}
	plugins := deps["project/plugins.sbt"]
	if len(plugins) != 14 {
		t.Fatalf("plugins = %d", len(plugins))
	}
	p := find(t, deps, "project/plugins.sbt", "org.scalameta:sbt-scalafmt")
	if p.Version != "2.6.2" || p.Datasource != "sbt-plugin" || p.DepType != DepTypePlugin || p.SbtVersion != "1.13.1" || p.Source != pluginSrc {
		t.Errorf("sbt-scalafmt = %+v", p)
	}
	fmtDeps, _ := extractDir(t, NewScalafmt(), "testdata/scala-steward")
	if d := fmtDeps[".scalafmt.conf"]; len(d) != 1 || d[0].Version != "3.11.5" || d[0].Name != "scalameta/scalafmt" {
		t.Errorf("scalafmt = %v", d)
	}
}

// pekko-http (e2a4aea4): versions in object vals, nested objects, scalaVersion :=
// publishedScalaVersions.head, CrossVersion.full, unresolvable versions.
func TestPekkoHTTP(t *testing.T) {
	deps, _ := extractDir(t, New(), "testdata/pekko-http")
	const df = "project/Dependencies.scala"
	scala := find(t, deps, df, "org.scala-lang:scala-library")
	if scala.Version != "2.13.18" || scala.VersionRef != "Dependencies.scala213Version" {
		t.Errorf("scala = %+v", scala)
	}
	jx := find(t, deps, df, "com.fasterxml.jackson.dataformat:jackson-dataformat-xml")
	jd := find(t, deps, df, "com.fasterxml.jackson.core:jackson-databind")
	if jx.Version != "2.22.4" || jx.VersionRef != "Dependencies.jacksonDatabindVersion" || jx.RefSpan != jd.RefSpan || jx.DepType != DepTypeTest {
		t.Errorf("jackson-dataformat-xml = %+v", jx)
	}
	for name, skip := range map[string]string{
		"org.scalatestplus:scalacheck-1-19_2.13": skipNonLiteral,
		"ch.epfl.scala:scalafix-core_2.13":       "unresolved version Dependencies.scalafixVersion",
		"org.junit.jupiter:junit-jupiter-engine": "unresolved version JupiterKeys.junitJupiterVersion.value",
	} {
		if d := find(t, deps, df, name); d.Skip != skip {
			t.Errorf("%s skip = %q, want %q", name, d.Skip, skip)
		}
	}
	testkit := find(t, deps, "build.sbt", "ch.epfl.scala:scalafix-testkit_2.13.18") // CrossVersion.full
	if testkit.DepType != DepTypeTest {
		t.Errorf("scalafix-testkit = %+v", testkit)
	}
	meta := find(t, deps, "project/plugins.sbt", "org.kohsuke:github-api")
	if meta.Version != "1.330" || meta.Datasource != "sbt-package" {
		t.Errorf("meta-build library = %+v", meta)
	}
}

// play-json (cd0820cb): sbt 2, crossScalaVersions := <Seq val> when scalaVersion does not
// resolve, %%% deps, compilerPlugin with CrossVersion.full.
func TestPlayJSON(t *testing.T) {
	deps, _ := extractDir(t, New(), "testdata/play-json")
	if d := find(t, deps, "build.sbt", "org.typelevel:jawn-parser_2.12"); d.Version != "1.8.0" {
		t.Errorf("jawn = %+v", d)
	}
	if d := find(t, deps, "build.sbt", "org.scalamacros:paradise_2.12.21"); d.DepType != DepTypePlugin || d.Datasource != "sbt-package" {
		t.Errorf("paradise = %+v", d)
	}
	if d := find(t, deps, "build.sbt", "org.scala-lang:scala-library"); d.Skip != skipNonLiteral {
		t.Errorf("scala = %+v", d)
	}
	p := find(t, deps, "project/plugins.sbt", "com.github.sbt:sbt-ci-release")
	if p.SbtVersion != "2.1.0-RC2" {
		t.Errorf("plugin = %+v", p)
	}
	if d := find(t, deps, "project/plugins.sbt", "org.playframework:play-docs-sbt-plugin"); d.Skip != skipNonLiteral {
		t.Errorf("play-docs = %+v", d)
	}
}

func extractFiles(t *testing.T, files map[string]string) (map[string][]dep.Dep, map[string][]byte) {
	t.Helper()
	dir := t.TempDir()
	for f, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return extractDir(t, New(), dir)
}

func TestSettingsAndForms(t *testing.T) {
	deps, _ := extractFiles(t, map[string]string{
		"build.sbt": `
// "x" % "commented" % "1.0"
/* "x" % "block" % "1.0" /* nested */ "x" % "still" % "1.0" */
scalaVersion in ThisBuild := V.scala
resolvers += "corp" at "https://corp.example/maven/"
resolvers ++= Seq("a" at "https://a.example/m2", Resolver.mavenLocal)
publishTo := Some("rel" at "https://publish.example/")
libraryDependencies ++= Seq(
  "org.typelevel" %% "cats-core" % V.cats,
  "org.scalatest" %% "scalatest" % "3.2.19" % Test,
  "com.lihaoyi" %%% "upickle" % "4.0.0" % "test->compile",
  ("org.foo" %% "bar" % "1.0").cross(CrossVersion.for3Use2_13),
  "org.foo" % "full" % "2.0" cross CrossVersion.full,
  "org.foo" %% "nocross" % "3.0" cross CrossVersion.disabled,
  "org.foo" % "dyn" % "latest.release",
  "org.foo" % "plus" % "1.+",
  "org.foo" % "interp" % s"$x",
  "org.foo" % "escaped" % "1.0\u0030",
  "org.foo" % "prov" % "1" % Provided,
  "org.foo" % "rt" % "1" % "runtime",
  "org.foo" % "opt" % "1" % Optional,
  "org.foo" % "it" % "1" % IntegrationTest,
  "org.foo" % "custom" % "1" % Benchmark,
  "org.foo" % ` + "`odd`" + ` % "1", // a backquoted identifier, not a string: not a dep
  2 % 3 % 4,
)
`,
		"project/V.scala": `object V { val scala = """3.3.4"""; val cats = "2.12.0" }`,
	})
	b := deps["build.sbt"]
	got := map[string]dep.Dep{}
	for _, d := range b {
		got[d.Name] = d
	}
	want := map[string]string{
		"org.typelevel:cats-core_3":       "2.12.0 compile ",
		"org.scalatest:scalatest_3":       "3.2.19 test ",
		"com.lihaoyi:upickle_3":           "4.0.0 test ",
		"org.foo:bar_2.13":                "1.0 compile ",
		"org.foo:full_3.3.4":              "2.0 compile ",
		"org.foo:nocross":                 "3.0 compile ",
		"org.foo:dyn":                     "latest.release compile " + skipLatest,
		"org.foo:plus":                    "1 compile ",
		"org.foo:interp":                  " compile " + skipNonLiteral,
		"org.foo:escaped":                 " compile " + skipNonLiteral,
		"org.foo:prov":                    "1 provided ",
		"org.foo:rt":                      "1 runtime ",
		"org.foo:opt":                     "1 optional ",
		"org.foo:it":                      "1 it ",
		"org.foo:custom":                  "1 compile ",
		"org.scala-lang:scala3-library_3": "3.3.4 scala ",
	}
	for name, w := range want {
		d, ok := got[name]
		if !ok {
			t.Errorf("missing %s in %v", name, b)
			continue
		}
		if s := d.Version + " " + d.DepType + " " + d.Skip; s != w {
			t.Errorf("%s = %q, want %q", name, s, w)
		}
		if d.Datasource == "sbt-package" && d.Source != "https://repo.maven.apache.org/maven2/ https://corp.example/maven/ https://a.example/m2" {
			t.Errorf("%s source = %q", name, d.Source)
		}
	}
	if len(b) != len(want) {
		t.Errorf("%d deps, want %d: %v", len(b), len(want), b)
	}
	if d := got["org.typelevel:cats-core_3"]; d.File != "project/V.scala" || d.VersionRef != "V.cats" {
		t.Errorf("cats = %+v", d)
	}
}

func TestScalaDefaultsAndUnresolved(t *testing.T) {
	deps, _ := extractFiles(t, map[string]string{
		"a/build.sbt":                `libraryDependencies += "g" %% "a" % "1.0"`,
		"a/project/build.properties": "sbt.version = 1.10.7\n",
		"b/build.sbt":                `libraryDependencies += "g" %% "b" % "1.0"`,
		"b/project/build.properties": "sbt.version=2.0.0\n",
		"b/project/plugins.sbt":      `libraryDependencies += "g" %% "meta" % "1.0"; addSbtPlugin("p" % "q" % "0.1")`,
		"c/build.sbt":                `scalaVersion := sys.env("SCALA"); libraryDependencies += "g" %% "c" % "1.0" cross CrossVersion.full`,
		"c/core/build.sbt":           `libraryDependencies += "g" %% "d" % "1.0"`,
		"c/project/Build.scala":      `object Build { val x = 1 }`,
	})
	if d := deps["a/build.sbt"]; len(d) != 1 || d[0].Name != "g:a_2.12" {
		t.Errorf("a = %v", d)
	}
	if d := deps["b/build.sbt"]; len(d) != 1 || d[0].Name != "g:b_3" {
		t.Errorf("b = %v", d)
	}
	if d := deps["b/project/plugins.sbt"]; len(d) != 2 || d[0].Name != "g:meta_3" || d[1].SbtVersion != "2.0.0" {
		t.Errorf("b meta = %v", d)
	}
	for _, f := range []string{"c/build.sbt", "c/core/build.sbt"} {
		for _, d := range deps[f] {
			if d.DepType != DepTypeScala && d.Skip != skipScalaVersion {
				t.Errorf("%s: %s", f, summary(d))
			}
		}
	}
}

func TestRepositoriesFile(t *testing.T) {
	deps, _ := extractFiles(t, map[string]string{
		"repositories": `[repositories]
  local
  # comment
  maven-central
  my-maven: https://repo.example/maven/
  my-ivy: https://repo.example/ivy/, [organization]/[module]/(scala_[scalaVersion]/)(sbt_[sbtVersion]/)[revision]/[type]s/[artifact](-[classifier]).[ext]
  bad: ftp://x
  nocolon
`,
		"build.sbt": `libraryDependencies += "g" % "a" % "1"
scalaVersion := "2.13.15"`,
		"project/plugins.sbt": `addSbtPlugin("p" % "q" % "0.1")`,
	})
	if d := deps["build.sbt"][0]; d.Source != "https://repo.maven.apache.org/maven2/ https://repo.example/maven/ ivy=https://repo.example/ivy/" {
		t.Errorf("library source = %q", d.Source)
	}
	if d := deps["project/plugins.sbt"][0]; d.Source != "https://repo.maven.apache.org/maven2/ https://repo.example/maven/ ivy=https://repo.example/ivy/" {
		t.Errorf("plugin source = %q", d.Source)
	}
	for _, d := range deps["build.sbt"] {
		if d.DepType == DepTypeScala && d.Source != "https://repo.maven.apache.org/maven2/ https://repo.example/maven/" {
			t.Errorf("scala source = %q", d.Source)
		}
	}
	if d := deps["repositories"]; len(d) != 0 {
		t.Errorf("repositories deps = %v", d)
	}
}

func TestDefinitionsAndObjects(t *testing.T) {
	deps, _ := extractFiles(t, map[string]string{
		"build.sbt": `
val V = new {
  val akka = "2.6.20"
}
lazy val circeVersion: String = "0.14.1"
def http4s = "0.23.0"
val alias = circeVersion
val other = Other.v
libraryDependencies ++= Seq(
  "com.typesafe.akka" %% "akka-actor" % V.akka,
  "io.circe" %% "circe-core" % alias,
  "org.http4s" %% "http4s-core" % http4s,
  "x" %% "y" % other,
  "x" %% "z" % missing,
  "x" %% "w" % (base + ".0"),
  Deps.group %% Deps.artifact % "1.0",
  Deps.nope % "a" % "1.0",
  "o" % "self" % self.revision,
)
scalaVersion := "2.13.15"
ThisBuild / scalaVersion := "2.13.15"
`,
		"project/Deps.scala": `
package build
object Deps {
  val group = "org.example"
  val artifact = "lib"
  class Inner(x: Int) { val hidden = "1" }
}
object Other { val v = "9.9" }
`,
	})
	got := map[string]dep.Dep{}
	for _, d := range deps["build.sbt"] {
		got[d.Name] = d
	}
	check := func(name, version, ref, skip string) {
		t.Helper()
		d, ok := got[name]
		if !ok {
			t.Errorf("missing %s: %v", name, deps["build.sbt"])
			return
		}
		if d.Version != version || d.VersionRef != ref || d.Skip != skip {
			t.Errorf("%s = %s", name, summary(d))
		}
	}
	check("com.typesafe.akka:akka-actor_2.13", "2.6.20", "V.akka", "")
	check("io.circe:circe-core_2.13", "0.14.1", "circeVersion", "")
	check("org.http4s:http4s-core_2.13", "0.23.0", "http4s", "")
	check("x:y_2.13", "9.9", "Other.v", "")
	check("x:z_2.13", "", "", "unresolved version missing")
	check("x:w_2.13", "", "", skipNonLiteral)
	check("org.example:lib_2.13", "1.0", "", "")
	check("o:self", "", "", "unresolved version self.revision")
	check("org.scala-lang:scala-library", "2.13.15", "", "")
	if _, ok := got["Deps.nope:a"]; ok {
		t.Error("unresolvable group kept")
	}
	n := 0
	for _, d := range deps["build.sbt"] {
		if d.DepType == DepTypeScala {
			n++
		}
	}
	if n != 2 { // two literals: two deps
		t.Errorf("%d scala deps", n)
	}
	if d := got["x:y_2.13"]; d.File != "project/Deps.scala" || d.Line != 8 {
		t.Errorf("x:y location = %s", summary(d))
	}
}

// A reference resolves from its enclosing objects outward, then as a qualified path, then
// to the one definition ending with it.
func TestDefinitionScopes(t *testing.T) {
	deps, _ := extractFiles(t, map[string]string{
		"build.sbt": `
val kit = "1.0"
object Kit {
  val kit = "2.0"
  val k = "a" % "inner" % kit
}
libraryDependencies ++= Seq(
  "a" % "outer" % kit,
  "a" % "ambiguous" % cats,
  "a" % "qualified" % Test.cats,
)
`,
		"project/Deps.scala": `
object Compile { val cats = "2.9.0" }
object Test {
  val cats = "2.8.0"
  val m = "org.typelevel" % "cats-core" % cats
  object Nested { val n = "org.typelevel" % "cats-nested" % cats }
}
`,
	})
	got := map[string]dep.Dep{}
	for _, ds := range deps {
		for _, d := range ds {
			got[d.Name] = d
		}
	}
	for name, want := range map[string][3]string{
		"a:inner":                   {"2.0", "Kit.kit", ""},
		"a:outer":                   {"1.0", "kit", ""},
		"a:ambiguous":               {"", "", "ambiguous version cats"},
		"a:qualified":               {"2.8.0", "Test.cats", ""},
		"org.typelevel:cats-core":   {"2.8.0", "Test.cats", ""},
		"org.typelevel:cats-nested": {"2.8.0", "Test.cats", ""},
	} {
		if d := got[name]; d.Version != want[0] || d.VersionRef != want[1] || d.Skip != want[2] {
			t.Errorf("%s = %s, want %v", name, summary(d), want)
		}
	}
}

func TestExtractAlone(t *testing.T) {
	ds, err := New().Extract("build.sbt", []byte(`libraryDependencies += Deps.g %% "a" % "1.0"`+"\n"+`libraryDependencies += "g" % s"a$x" % "1"`), manager.Env{})
	if err != nil || len(ds) != 1 || ds[0].Skip != skipCoordinates || ds[0].Name != "Deps.g:a" {
		t.Fatalf("deps = %v, %v", ds, err)
	}
	ds, _ = New().Extract("project/build.properties", []byte("other=1\n"), manager.Env{})
	if len(ds) != 0 {
		t.Errorf("build.properties without sbt.version = %v", ds)
	}
}

func TestEditsAcrossFiles(t *testing.T) {
	m := New()
	deps, srcs := extractFiles(t, map[string]string{
		"build.sbt":                `libraryDependencies ++= Seq("a" %% "b" % Versions.cats, "a" %% "c" % "1.0")`,
		"project/Versions.scala":   `object Versions { val cats = "2.10.0" }`,
		"project/build.properties": "sbt.version=1.9.9\n",
	})
	for _, d := range append(deps["build.sbt"], deps["project/build.properties"]...) {
		edits, err := m.Edits(dep.Update{Dep: d, NewVersion: "9.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(srcs[d.File], edits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `9.0.0`) || strings.Contains(string(out), d.Version) && d.Version != "9.0.0" && d.File != "build.sbt" {
			t.Errorf("%s: %s", d.Name, out)
		}
	}
	d := deps["build.sbt"][0]
	if d.File != "project/Versions.scala" || d.VersionRef != "Versions.cats" {
		t.Errorf("cats = %s", summary(d))
	}
}

// TestIvyRevisions: Ivy dynamic revisions are read as Renovate's ivy versioning reads them.
// Ranges become range deps that admitted versions keep and newer ones extend (brackets kept),
// sub-revisions range deps that are never rewritten, `[v]` a plain version; latest.<status>
// and forms Renovate rejects stay skipped.
func TestIvyRevisions(t *testing.T) {
	src := `libraryDependencies ++= Seq(
  "a" % "range" % "[1.0,2.0)",
  "a" % "ivy" % "]1.0,2.0[",
  "a" % "open" % "[1.5,)",
  "a" % "step" % "[1,2)",
  "a" % "sub" % "1.2.+",
  "a" % "exact" % "[1.0]",
  "a" % "latest" % "latest.integration",
  "a" % "plus" % "1.0+",
  "a" % "multi" % "(,1.0],[1.2,)",
)
scalaVersion := "2.13.+"
`
	m := New()
	deps, srcs := extractFiles(t, map[string]string{"build.sbt": src})
	ru := m.(manager.RangeUpdater)
	for _, tc := range []struct {
		name, version, rng, skip string
		in, out                  string // a version the range admits, one it does not
		newVersion, want         string // NewVersion and the rewritten file text ("" = not rewritten)
	}{
		{name: "a:range", version: "1.0", rng: "[1.0,2.0)", in: "1.9.9", out: "2.0", newVersion: "2.5.1", want: `"[1.0,2.6)"`},
		{name: "a:ivy", version: "1.0", rng: "]1.0,2.0[", in: "1.0.1", out: "1.0", newVersion: "2.5.1", want: `"]1.0,2.6["`},
		{name: "a:open", version: "1.5", rng: "[1.5,)", in: "99", out: "1.4"},
		{name: "a:step", version: "1", rng: "[1,2)", in: "1.5", out: "2.0", newVersion: "4.3.2", want: `"[4,5)"`},
		{name: "a:sub", version: "1.2", rng: "1.2.+", in: "1.2.9", out: "1.20.0", newVersion: "1.3.0"},
		{name: "a:exact", version: "1.0", newVersion: "1.1", want: `"[1.1]"`},
		{name: "a:latest", version: "latest.integration", skip: skipLatest},
		{name: "a:plus", version: "1.0+", skip: skipDynamic},
		{name: "a:multi", version: "(,1.0],[1.2,)", skip: skipDynamic},
		{name: "org.scala-lang:scala-library", version: "2.13.+", skip: skipDynamic},
	} {
		d := find(t, deps, "build.sbt", tc.name)
		if d.Version != tc.version || d.Range != tc.rng || d.Skip != tc.skip {
			t.Errorf("%s = %q %q %q, want %q %q %q", tc.name, d.Version, d.Range, d.Skip, tc.version, tc.rng, tc.skip)
			continue
		}
		if d.Skip != "" {
			continue
		}
		if tc.rng != "" {
			if d.Versioning != Versioning || !ru.InRange(d, tc.in) || ru.InRange(d, tc.out) {
				t.Errorf("%s: versioning %s, InRange(%s) or !InRange(%s)", tc.name, d.Versioning, tc.in, tc.out)
			}
			if _, ok := ru.NewRange(d, tc.in, ""); ok {
				t.Errorf("%s: rewritten for admitted %s", tc.name, tc.in)
			}
			if got, ok := ru.NewRange(d, "3.0", dep.RangePin); !ok || got != "3.0" {
				t.Errorf("%s: pin = %q, %v", tc.name, got, ok)
			}
		}
		if tc.newVersion == "" {
			continue
		}
		edits, err := m.Edits(dep.Update{Dep: d, NewVersion: tc.newVersion})
		if tc.want == "" {
			if err == nil {
				t.Errorf("%s: edits %v for %s", tc.name, edits, tc.newVersion)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(srcs[d.File], edits)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"`+tc.name[2:]+`" % `+tc.want) {
			t.Errorf("%s: %s", tc.name, out)
		}
	}
}

func TestScalafmt(t *testing.T) {
	m := NewScalafmt()
	for src, want := range map[string]string{
		"version = 3.8.3\nmaxColumn = 100\n":               "3.8.3",
		"maxColumn = 120\nversion = \"3.7.17\" # pinned\n": "3.7.17",
		"  version: 3.0.0-RC1\r\n":                         "3.0.0-RC1",
		"runner.dialect = scala3\n":                        "",
		"docstrings.version = 1.2.3\n":                     "",
	} {
		ds, err := m.Extract(".scalafmt.conf", []byte(src), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		if want == "" {
			if len(ds) != 0 {
				t.Errorf("%q: %v", src, ds)
			}
			continue
		}
		if len(ds) != 1 || ds[0].Version != want || src[ds[0].RefSpan.Start:ds[0].RefSpan.End] != want {
			t.Fatalf("%q: %v", src, ds)
		}
		edits, _ := m.Edits(dep.Update{Dep: ds[0], NewVersion: "3.9.0"})
		out, err := patch.Apply([]byte(src), edits)
		if err != nil || !strings.Contains(string(out), "3.9.0") {
			t.Errorf("%q: %s, %v", src, out, err)
		}
	}
	ds, _ := m.Extract(".scalafmt.conf", []byte("a = 1\nversion = 3.8.3\n"), manager.Env{})
	if ds[0].Line != 2 {
		t.Errorf("line = %d", ds[0].Line)
	}
}

func TestLex(t *testing.T) {
	toks := lex([]byte("val `type` = 'c' + '\\n' + 'sym + 1.5e3 /* a /* b */ c */ \"\"\"tri\"ple\"\"\"\" \"un\nx"))
	var got []string
	for _, tk := range toks {
		got = append(got, tk.text)
	}
	want := []string{"val", "type", "=", "'c'", "+", "'\\n'", "+", "sym", "+", "1.5e3", "tri\"ple\"", "un", "x"}
	if !slices.Equal(got, want) {
		t.Errorf("tokens = %q", got)
	}
	if toks[len(toks)-1].line != 2 {
		t.Errorf("line = %d", toks[len(toks)-1].line)
	}
}

func TestHelpers(t *testing.T) {
	for v, want := range map[string]string{
		"2.13.12":                            "2.13",
		"2.13.15":                            "2.13",
		"2.13.13-RC1":                        "2.13",
		"2.13.0-M5":                          "2.13.0-M5",
		"2.13.12-bin-typelevel-4":            "2.13",
		"2.9.3":                              "2.9.3",
		"3.3.1":                              "3",
		"3.4.0-RC1":                          "3",
		"3.0.0-RC1":                          "3.0.0-RC1",
		"3.5.0-RC1-bin-20240417-abc-NIGHTLY": "3",
		"3":                                  "3",
	} {
		if got := scalaBinary(v); got != want {
			t.Errorf("scalaBinary(%q) = %q, want %q", v, got, want)
		}
	}
	b := &build{scalaFull: "2.13.12-bin-typelevel-4", scalaBin: "2.13"}
	for mode, want := range map[string]string{"patch": "_2.13.12", "full": "_2.13.12-bin-typelevel-4", "binary": "_2.13"} {
		if got, ok := b.crossSuffix(&module{cross: "%", crossVersion: mode}, false); !ok || got != want {
			t.Errorf("crossSuffix(%s) = %q, %v, want %q", mode, got, ok, want)
		}
	}
	if got := libraryDepType(" Configurations.Test "); got != DepTypeTest {
		t.Errorf("libraryDepType = %q", got)
	}
	if got := libraryDepType("provided; test"); got != DepTypeProvided {
		t.Errorf("libraryDepType = %q", got)
	}
}
