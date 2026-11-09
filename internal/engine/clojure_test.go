package engine

import (
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/clojure"
)

// Clojure projects: a Leiningen `~v` version shared by two deps (one grouped PR editing the
// def), a deps.edn Maven lib, a git lib with :git/tag and a short :git/sha (the tag's commit
// written at the sha's length) and one with :git/sha alone (digest update of the default
// branch head).
func TestClojureProjects(t *testing.T) {
	const (
		tagged = "https://github.com/o/tagged"
		head   = "https://github.com/o/head"
		newSHA = "2222222222222222222222222222222222222222"
	)
	ms := []manager.Manager{clojure.NewLeiningen(), clojure.NewDepsEdn()}
	opts := Options{Managers: ms, Datasources: map[string]Datasource{
		"clojure":  fakeReleases{"1.11.0", "1.12.0"},
		"git-tags": fakeTags{tagged: {"v1.0.0", "v1.1.0"}},
		"git-refs": fakeBranches{head + "#HEAD": newSHA},
	}}
	oldTagged := sha(tagged + "@v1.0.0")
	files := map[string]string{
		"project.clj": "(def v \"1.11.0\")\n(defproject a/a \"0.1.0\"\n  :dependencies [[org.clojure/clojure ~v] [org.clojure/core.async ~v]])\n",
		"deps.edn": `{:deps {org.clojure/clojure {:mvn/version "1.11.0"}
        io.github.o/tagged {:git/tag "v1.0.0" :git/sha "` + oldTagged[:7] + `"}
        io.github.o/head {:git/sha "1111111"}}}
`,
	}
	root, cfg := setup(t, "separateMajorMinor: false\n", files)
	res, err := Resolve(t.Context(), root, cfg, env, opts)
	if err != nil || len(res.Errors) > 0 {
		t.Fatalf("err = %v, errors = %v", err, res.Errors)
	}
	type up struct {
		name, version, digest, group string
		kind                         dep.UpdateType
	}
	var got []up
	for _, u := range res.Updates {
		got = append(got, up{u.Dep.Name, u.NewVersion, u.NewDigest, u.Group, u.Type})
	}
	want := []up{
		{"io.github.o/head", "HEAD", newSHA, "", dep.Digest},
		{"io.github.o/tagged", "v1.1.0", sha(tagged + "@v1.1.0"), "", dep.Minor},
		{"org.clojure:clojure", "1.12.0", "", "", dep.Minor},
		{"org.clojure:clojure", "1.12.0", "", "v", dep.Minor},
		{"org.clojure:core.async", "1.12.0", "", "v", dep.Minor},
	}
	slices.SortFunc(got, func(a, b up) int { return strings.Compare(a.name+a.group, b.name+b.group) })
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
	if v := byPath["project.clj"]; !strings.HasPrefix(v, "(def v \"1.12.0\")\n") || !strings.Contains(v, "[org.clojure/clojure ~v]") {
		t.Errorf("project.clj = %q", v)
	}
	wantEdn := `{:deps {org.clojure/clojure {:mvn/version "1.12.0"}
        io.github.o/tagged {:git/tag "v1.1.0" :git/sha "` + sha(tagged + "@v1.1.0")[:7] + `"}
        io.github.o/head {:git/sha "2222222"}}}
`
	if v := byPath["deps.edn"]; v != wantEdn {
		t.Errorf("deps.edn = %q\nwant       %q", v, wantEdn)
	}
}
