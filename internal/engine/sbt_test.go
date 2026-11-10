package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/sbt"
)

// sbt builds: two ModuleIDs sharing a version val of project/*.scala (one grouped PR editing
// the definition), an sbt plugin, scalaVersion, sbt.version and the scalafmt version.
func TestSbtBuild(t *testing.T) {
	ms := []manager.Manager{sbt.New(), sbt.NewScalafmt()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"sbt-package":     fakeReleases{"2.10.0", "2.12.0"},
		"sbt-plugin":      fakeReleases{"2.5.0", "2.5.2"},
		"maven":           fakeReleases{"2.13.15", "2.13.16"},
		"github-releases": fakeReleases{"1.10.0", "1.10.7"},
	}}
	files := map[string]string{
		"build.sbt": `scalaVersion := "2.13.15"
libraryDependencies ++= Seq(
  "org.typelevel" %% "cats-core" % Versions.cats,
  "org.typelevel" %% "cats-laws" % Versions.cats % Test,
)
`,
		"project/Versions.scala":   "object Versions {\n  val cats = \"2.10.0\"\n}\n",
		"project/plugins.sbt":      `addSbtPlugin("org.scalameta" % "sbt-scalafmt" % "2.5.0")` + "\n",
		"project/build.properties": "sbt.version=1.10.0\n",
		".scalafmt.conf":           "version = 1.10.0\n",
	}
	root, cfg := setup(t, "separate: none\n", files)
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		name, version, group string
		kind                 dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.Group, u.Type})
	}
	want := []up{ // files in path order
		{"scalameta/scalafmt", "1.10.7", "", dep.Patch},
		{"org.scala-lang:scala-library", "2.13.16", "", dep.Patch},
		{"org.typelevel:cats-core_2.13", "2.12.0", "Versions.cats", dep.Minor},
		{"org.typelevel:cats-laws_2.13", "2.12.0", "Versions.cats", dep.Minor},
		{"org.scalameta:sbt-scalafmt", "2.5.2", "", dep.Patch},
		{"sbt/sbt", "1.10.7", "", dep.Patch},
	}
	slices.SortFunc(got, func(a, b up) int { return strings.Compare(a.name, b.name) })
	slices.SortFunc(want, func(a, b up) int { return strings.Compare(a.name, b.name) })
	if !slices.Equal(got, want) {
		t.Fatalf("updates = %+v\nwant      %+v", got, want)
	}
	changes, errs := Build(t.Context(), root, ms, res.Updates, nil)
	if len(errs) > 0 {
		t.Fatalf("errs = %v", errs)
	}
	byPath := map[string]string{}
	for _, c := range changes {
		byPath[c.Path] = string(c.New)
	}
	if v := byPath["project/Versions.scala"]; v != "object Versions {\n  val cats = \"2.12.0\"\n}\n" {
		t.Errorf("Versions.scala = %q", v)
	}
	if v := byPath["build.sbt"]; !strings.HasPrefix(v, `scalaVersion := "2.13.16"`) {
		t.Errorf("build.sbt = %q", v)
	}
	if v := byPath["project/plugins.sbt"]; !strings.Contains(v, `"2.5.2"`) {
		t.Errorf("plugins.sbt = %q", v)
	}
	if byPath["project/build.properties"] != "sbt.version=1.10.7\n" || byPath[".scalafmt.conf"] != "version = 1.10.7\n" {
		t.Errorf("changes = %v", byPath)
	}
}
