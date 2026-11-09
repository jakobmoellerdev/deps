package clojure

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	clojureds "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/clojure"
	mavends "git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/datasource/maven"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// render lists deps one per line: line, dep type, datasource, name, version, and the fields
// that are set.
func render(ds []dep.Dep) string {
	var b strings.Builder
	for _, d := range ds {
		fmt.Fprintf(&b, "%d %s %s %s %s", d.Line, d.DepType, d.Datasource, d.Name, d.Version)
		for _, f := range []struct{ k, v string }{{"range", d.Range}, {"ref", d.VersionRef}, {"digest", d.Digest}, {"skip", d.Skip}} {
			if f.v != "" {
				fmt.Fprintf(&b, " %s=%q", f.k, f.v)
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// extract runs m on src and checks that the spans of updatable deps hold what they claim.
func extract(t *testing.T, m manager.Manager, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := m.Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatalf("%s: %v", file, err)
	}
	for _, d := range ds {
		if d.Manager != m.Name() {
			t.Errorf("%s: manager %q", d.Name, d.Manager)
		}
		if d.Skip != "" {
			continue
		}
		if want := cmpOr(d.Range, d.Version); d.Version != "HEAD" && string(src[d.RefSpan.Start:d.RefSpan.End]) != want {
			t.Errorf("%s: RefSpan %q, want %q", d.Name, src[d.RefSpan.Start:d.RefSpan.End], want)
		}
		if d.Digest != "" && string(src[d.DigestSpan.Start:d.DigestSpan.End]) != d.Digest {
			t.Errorf("%s: DigestSpan %q, want %q", d.Name, src[d.DigestSpan.Start:d.DigestSpan.End], d.Digest)
		}
	}
	return ds
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func read(t *testing.T, file string) []byte {
	t.Helper()
	src, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	return src
}

func TestMatch(t *testing.T) {
	lein, edn := NewLeiningen(), NewDepsEdn()
	for p, want := range map[string][2]bool{
		"project.clj": {true, false}, "sub/project.clj": {true, false}, "myproject.clj": {false, false},
		"deps.edn": {false, true}, "a/b/deps.edn": {false, true}, "bb.edn": {false, true}, "x.edn": {false, false},
	} {
		if got := [2]bool{lein.Match(p), edn.Match(p)}; got != want {
			t.Errorf("Match(%q) = %v, want %v", p, got, want)
		}
	}
	if lein.Name() != LeiningenName || edn.Name() != DepsEdnName {
		t.Error("names")
	}
}

// TestLeiningenFixtures reads ring's ring-core and clj-kondo's project.clj: dependency vectors
// of the top level and of profiles.
func TestLeiningenFixtures(t *testing.T) {
	for file, want := range map[string]string{
		"testdata/ring-core/project.clj": `7 dependencies clojure org.clojure:clojure 1.9.0
8 dependencies clojure org.ring-clojure:ring-core-protocols 1.15.6
9 dependencies clojure org.ring-clojure:ring-websocket-protocols 1.15.6
10 dependencies clojure ring:ring-codec 1.3.0
11 dependencies clojure commons-io:commons-io 2.22.0
12 dependencies clojure org.apache.commons:commons-fileupload2-core 2.0.0-M5
13 dependencies clojure crypto-random:crypto-random 1.2.1
14 dependencies clojure crypto-equality:crypto-equality 1.0.1
17 dependencies clojure clj-time:clj-time 0.15.2
18 dependencies clojure org.clojure:clojure 1.10.3
19 dependencies clojure org.clojure:clojure 1.11.4
20 dependencies clojure org.clojure:clojure 1.12.6
`,
	} {
		if got := render(extract(t, NewLeiningen(), file, read(t, file))); got != want {
			t.Errorf("%s: deps =\n%s\nwant\n%s", file, got, want)
		}
	}
	const file = "testdata/clj-kondo/project.clj"
	ds := extract(t, NewLeiningen(), file, read(t, file))
	if len(ds) != 19 {
		t.Fatalf("%s: %d deps", file, len(ds))
	}
	// The :deploy-repositories are not :repositories.
	for _, d := range ds {
		if d.Source != "" || d.Skip != "" {
			t.Errorf("%s: Source %q skip %q", d.Name, d.Source, d.Skip)
		}
	}
	if got := render(ds[17:]); got != "31 dependencies clojure io.github.borkdude:deflet 0.1.0\n33 dependencies clojure com.github.clj-easy:graal-build-time 1.0.6\n" {
		t.Errorf("profile deps:\n%s", got)
	}
}

func TestLeiningenForms(t *testing.T) {
	src := []byte(`;; comment with [fake "1.0"]
(def clj-version "1.11.1")
(def ^:private other "2.0.0")
(def computed (str "1." "2"))
(defproject example/app "0.1.0-SNAPSHOT"
  :repositories [["releases" {:url "https://repo.example.com/releases" :creds :gpg}]
                 ["snapshots" "https://repo.example.com/snapshots/"]]
  :plugin-repositories ^:replace [["plugins" "https://plugins.example.com/m2"]]
  :dependencies [[org.clojure/clojure ~clj-version]
                 [other/lib ~other :exclusions [org.clojure/clojure]]
                 [missing/var ~nope]
                 [computed/lib ~(str "1." "2")]
                 #_[discarded/lib "1.0.0"]
                 [managed/lib]
                 [managed/kw :exclusions [x]]
                 [ranged/lib "[1.0,2.0)"]
                 [exact/lib "[1.5]"]
                 [bad/range "[2.0,1.0)"]
                 [dyn/lib "RELEASE"]
                 [esc/lib "1.0\u0030"]
                 ["string/name" "3.0.0"]
                 ~(when true [cond/lib "1.0"])]
  :managed-dependencies [[managed/lib "1.2.3"]]
  :plugins [[lein-ancient "0.7.0"]]
  :pom-plugins [[org.apache.maven.plugins/maven-jar-plugin "3.3.0" {:configuration [:archive]}]]
  :parent-project {:path "../parent/project.clj" :coords [parent/project "2.1.0"] :inherit [:dependencies]}
  :profiles {:dev {:dependencies [[criterium "0.4.6"]]
                   ;; a reader conditional is no vector (as in Renovate)
                   :plugins #?(:clj [[lein-cljfmt "0.9.2"]] :cljs [])}}
  :jvm-opts [#"regex" \a \newline ##Inf #inst "2020-01-01" #'var 1/2 -1 'quoted @deref #{:set}])
`)
	ds := extract(t, NewLeiningen(), "project.clj", src)
	want := `9 dependencies clojure org.clojure:clojure 1.11.1 ref="clj-version"
10 dependencies clojure other:lib 2.0.0 ref="other"
11 dependencies clojure missing:var  skip="unresolved version ~nope"
12 dependencies clojure computed:lib  skip="non-literal version"
16 dependencies clojure ranged:lib 1.0 range="[1.0,2.0)"
17 dependencies clojure exact:lib 1.5
18 dependencies clojure bad:range [2.0,1.0) skip="version range"
19 dependencies clojure dyn:lib RELEASE skip="dynamic version"
20 dependencies clojure esc:lib 1.0\u0030 skip="cannot locate value"
21 dependencies clojure string:name 3.0.0
23 managed-dependencies clojure managed:lib 1.2.3
24 plugins clojure lein-ancient:lein-ancient 0.7.0
25 pom-plugins clojure org.apache.maven.plugins:maven-jar-plugin 3.3.0
26 parent-project clojure parent:project 2.1.0
27 dependencies clojure criterium:criterium 0.4.6
`
	if got := render(ds); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
	repos := mavends.Central + " " + clojureds.Clojars + " https://repo.example.com/releases/ https://repo.example.com/snapshots/"
	for _, d := range ds {
		want := repos
		if d.DepType == DepTypePlugins {
			want = "https://plugins.example.com/m2/"
		}
		if d.Source != want {
			t.Errorf("%s: Source %q, want %q", d.Name, d.Source, want)
		}
	}
	// A ~name version points at the def's string.
	if got := string(src[ds[0].RefSpan.Start:ds[0].RefSpan.End]); got != "1.11.1" || ds[0].Line != 9 {
		t.Errorf("def span %q line %d", got, ds[0].Line)
	}
}

func TestLeiningenNoProject(t *testing.T) {
	for _, src := range []string{"", "(ns foo.core)\n(def x \"1\")", "{:not :a-project}"} {
		ds, err := NewLeiningen().Extract("project.clj", []byte(src), manager.Env{})
		if err != nil || len(ds) != 0 {
			t.Errorf("%q: %v, %v", src, ds, err)
		}
	}
}

// TestDepsEdnFixtures reads next.jdbc's (aliases, :mvn/repos), clj-kondo's and metabase's
// deps.edn (git libs with :git/sha, legacy :sha, inferred repositories, :git/tag with a short
// sha, RELEASE, :local/root).
func TestDepsEdnFixtures(t *testing.T) {
	const jdbc = "testdata/next-jdbc/deps.edn"
	ds := extract(t, NewDepsEdn(), jdbc, read(t, jdbc))
	if len(ds) != 34 {
		t.Fatalf("%s: %d deps", jdbc, len(ds))
	}
	if got := render(ds[:8]); got != `4 deps clojure org.clojure:clojure 1.10.3
5 deps clojure org.clojure:java.data 1.4.120
7 deps clojure camel-snake-kebab:camel-snake-kebab 0.4.3
10 override-deps clojure org.clojure:clojure 1.10.3
11 override-deps clojure org.clojure:clojure 1.11.4
12 override-deps clojure org.clojure:clojure 1.12.5
13 override-deps clojure org.clojure:clojure 1.13.0-alpha11
17 extra-deps clojure org.clojure:test.check 1.1.3
` {
		t.Errorf("%s: deps =\n%s", jdbc, render(ds[:8]))
	}
	wantSource := mavends.Central + " " + clojureds.Clojars + " https://oss.sonatype.org/content/repositories/snapshots/ https://s01.oss.sonatype.org/content/repositories/snapshots/"
	if ds[0].Source != wantSource {
		t.Errorf("Source %q", ds[0].Source)
	}

	const kondo = "testdata/clj-kondo/deps.edn"
	if ds := extract(t, NewDepsEdn(), kondo, read(t, kondo)); len(ds) != 30 || ds[0].Source != "" {
		t.Errorf("%s: %d deps", kondo, len(ds))
	}

	const mb = "testdata/metabase/deps.edn"
	ds = extract(t, NewDepsEdn(), mb, read(t, mb))
	var git strings.Builder
	for _, d := range ds {
		if d.Datasource != clojureds.Name || d.Skip != "" {
			git.WriteString(render([]dep.Dep{d}))
			fmt.Fprintf(&git, "  %s\n", d.Source)
		}
	}
	want := `88 deps git-refs io.github.eerohele/pp HEAD digest="ffc4edc482c057cd9412c62f018a41e9140c618b"
  https://github.com/eerohele/pp
123 deps git-refs metosin/malli HEAD digest="b4a2d78647a049870b316753d7aae7fb4ade3ae5"
  https://github.com/metabase/malli
210 deps git-refs redux/redux HEAD digest="4a37feaf817a2a6b5ef688c927f6fd4375964433"
  https://github.com/metabase/redux
303 extra-deps git-refs jonase/eastwood HEAD digest="7b2d94e47a3149e28927db197b9c113c81ed6084"
  https://github.com/crisptrutski/eastwood
514 replace-deps clojure com.github.liquidz:antq RELEASE skip="dynamic version"
  ` + mavends.Central + " " + clojureds.Clojars + ` https://s3.amazonaws.com/metabase-maven-downloads/ https://s3.amazonaws.com/redshift-maven-repository/release/ https://build.shibboleth.net/nexus/content/repositories/releases/
727 replace-deps git-refs com.github.camsaul/whitespace-linter HEAD digest="e35bc252ccf5cc74f7d543ef95ad8a3e5131f25b"
  https://github.com/camsaul/whitespace-linter
834 extra-deps git-tags io.github.seancorfield/build-uber-log4j2-handler v2.25.1 digest="1cfab45"
  https://github.com/seancorfield/build-uber-log4j2-handler
`
	if got := git.String(); got != want {
		t.Errorf("%s: git and skipped libs =\n%s\nwant\n%s", mb, got, want)
	}
	if len(ds) != 226 {
		t.Errorf("%s: %d deps", mb, len(ds))
	}
}

func TestDepsEdnForms(t *testing.T) {
	src := []byte(`{:mvn/repos {"central" nil
             "clojars" {:url "https://mirror.example.com/clojars"}
             "corp" {:url "https://corp.example.com/m2/"}
             "broken" "not-a-map"}
 :deps {org.clojure/clojure {:mvn/version "1.12.0"}
        cheshire {:mvn/version "5.13.0"}
        org.lwjgl/lwjgl$natives-linux {:mvn/version "3.3.4"}
        nsmap/lib #:mvn{:version "2.0.0"}
        local/lib {:local/root "../local"}
        gl/lib {:git/url "git@gitlab.com:group/lib.git" :git/tag "v1.0.0" :git/sha "0123456789abcdef0123456789abcdef01234567"}
        io.gitlab.grp/proj {:git/tag "v2.0.0"}
        io.bitbucket.team/repo {:git/sha "abcdef1"}
        ht.sr.~user/project {:git/tag "1.0" :git/sha "abcdef1"}
        unknown/host {:git/sha "abcdef1"}
        bad/url {:git/url "file:///tmp/repo" :git/sha "abcdef1"}
        bad/sha {:git/url "https://github.com/o/r" :git/sha "xyz"}
        no/ref {:git/url "https://github.com/o/r"}
        esc/tag {:git/url "https://github.com/o/r" :git/tag "v\u0031" :git/sha "abcdef1"}
        not-a-map "1.0"
        "string-key" {:mvn/version "1.0"}}
 :aliases {:test {:extra-deps {lambdaisland/kaocha {:mvn/version "1.91.1392"}}
                  :override-deps {org.clojure/clojure {:mvn/version "1.11.4"}}
                  :default-deps {org.clojure/core.async {:mvn/version "1.6.681"}}}
           :build {:deps {io.github.clojure/tools.build {:git/tag "v0.10.5" :git/sha "2a21b7a"}}
                   :ns-default build}
           :outdated {:replace-deps {com.github.liquidz/antq {:mvn/version "[2.0,3.0)"}}}
           :empty nil}}
`)
	ds := extract(t, NewDepsEdn(), "deps.edn", src)
	want := `5 deps clojure org.clojure:clojure 1.12.0
6 deps clojure cheshire:cheshire 5.13.0
7 deps clojure org.lwjgl:lwjgl 3.3.4
8 deps clojure nsmap:lib 2.0.0
10 deps git-tags gl/lib v1.0.0 digest="0123456789abcdef0123456789abcdef01234567"
11 deps git-tags io.gitlab.grp/proj v2.0.0
12 deps git-refs io.bitbucket.team/repo HEAD digest="abcdef1"
13 deps git-tags ht.sr.~user/project 1.0 digest="abcdef1"
14 deps git-tags unknown/host  skip="unsupported git url"
15 deps git-tags bad/url  skip="unsupported git url"
16 deps git-tags bad/sha  skip="invalid git sha"
17 deps git-tags no/ref  skip="git lib without tag or sha"
18 deps git-tags esc/tag  digest="abcdef1" skip="cannot locate value"
21 extra-deps clojure lambdaisland:kaocha 1.91.1392
22 override-deps clojure org.clojure:clojure 1.11.4
23 default-deps clojure org.clojure:core.async 1.6.681
24 replace-deps git-tags io.github.clojure/tools.build v0.10.5 digest="2a21b7a"
26 replace-deps clojure com.github.liquidz:antq 2.0 range="[2.0,3.0)"
`
	if got := render(ds); got != want {
		t.Errorf("deps =\n%s\nwant\n%s", got, want)
	}
	repos := "https://mirror.example.com/clojars/ https://corp.example.com/m2/"
	sources := map[string]string{
		"gl/lib": "https://gitlab.com/group/lib", "io.gitlab.grp/proj": "https://gitlab.com/grp/proj",
		"io.bitbucket.team/repo": "https://bitbucket.org/team/repo", "ht.sr.~user/project": "https://git.sr.ht/~user/project",
		"io.github.clojure/tools.build": "https://github.com/clojure/tools.build",
	}
	for _, d := range ds {
		want, ok := sources[d.Name]
		if d.Datasource == clojureds.Name {
			want, ok = repos, true
		}
		if ok && d.Source != want {
			t.Errorf("%s: Source %q, want %q", d.Name, d.Source, want)
		}
	}
}

func TestDepsEdnNotAMap(t *testing.T) {
	for _, src := range []string{"", "[:a :b]", "{:paths [\"src\"]}"} {
		ds, err := NewDepsEdn().Extract("deps.edn", []byte(src), manager.Env{})
		if err != nil || len(ds) != 0 {
			t.Errorf("%q: %v, %v", src, ds, err)
		}
	}
}

func TestReaderErrors(t *testing.T) {
	for _, src := range []string{
		"(defproject a \"1\"", "(defproject a \"1\"]", ")", "{:a \"unterminated}", "^:meta", "~", "#", "#_", "(a ^:m)",
		"#?[:clj 1]", "#:ns[1]", "{:a #?", "(a ~)",
	} {
		if _, err := readForms([]byte(src)); err == nil {
			t.Errorf("%q: want error", src)
		}
		if _, err := NewLeiningen().Extract("project.clj", []byte(src), manager.Env{}); err == nil {
			t.Errorf("leiningen %q: want error", src)
		}
	}
}

func TestReaderForms(t *testing.T) {
	forms, err := readForms([]byte(`^{:doc "x"} [a "b\"c" \" #?@(:clj [1]) #:ns{:k 1} #(inc %) #=(eval) #tag {:x 1} @atom ~@xs :ns/kw ::auto +1 -x]`))
	if err != nil || len(forms) != 1 {
		t.Fatalf("%v, %v", forms, err)
	}
	f := forms[0]
	if len(f.meta) != 1 || f.meta[0].mapGet("doc") == nil {
		t.Errorf("meta %+v", f.meta)
	}
	var kinds []kind
	for _, k := range f.kids {
		kinds = append(kinds, k.kind)
	}
	want := []kind{kindSymbol, kindString, kindOther, kindList, kindMap, kindList, kindList, kindMap, kindSymbol, kindSymbol, kindKeyword, kindKeyword, kindOther, kindSymbol}
	if !slices.Equal(kinds, want) {
		t.Errorf("kinds %v, want %v", kinds, want)
	}
	if s := f.kids[1]; s.plain || s.text != `b\"c` {
		t.Errorf("string %+v", s)
	}
	if m := f.kids[4]; m.text != "ns" {
		t.Errorf("namespaced map %q", m.text)
	}
	if !f.kids[9].unquote || f.kids[10].text != "ns/kw" || f.kids[11].text != "auto" {
		t.Errorf("forms %+v %+v %+v", f.kids[9], f.kids[10], f.kids[11])
	}
	if f.mapGet("x") != nil || (*form)(nil).mapGet("x") != nil {
		t.Error("mapGet of a non-map")
	}
}

func TestEdits(t *testing.T) {
	src := []byte(`(def v "1.0.0")
(defproject a "1"
  :dependencies [[a/a ~v] [b/b "2.0.0"] [c/c "[1.0,2.0)"]])
`)
	lein := NewLeiningen()
	ds := extract(t, lein, "project.clj", src)
	apply := func(d dep.Dep, u dep.Update) string {
		t.Helper()
		u.Dep = d
		es, err := lein.Edits(u)
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply(src, es)
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	if got := apply(ds[0], dep.Update{NewVersion: "1.1.0"}); !strings.Contains(got, `(def v "1.1.0")`) || !strings.Contains(got, "[a/a ~v]") {
		t.Errorf("def edit:\n%s", got)
	}
	if got := apply(ds[1], dep.Update{NewVersion: "2.1.0"}); !strings.Contains(got, `[b/b "2.1.0"]`) {
		t.Errorf("version edit:\n%s", got)
	}
	if got := apply(ds[2], dep.Update{NewVersion: "2.5.1"}); !strings.Contains(got, `[c/c "[1.0,2.6)"]`) {
		t.Errorf("range edit:\n%s", got)
	}
	if got := apply(ds[2], dep.Update{NewVersion: "2.5.1", RangeStrategy: dep.RangePin}); !strings.Contains(got, `[c/c "2.5.1"]`) {
		t.Errorf("range pin:\n%s", got)
	}
	ru := lein.(manager.RangeUpdater)
	if !ru.InRange(ds[2], "1.5") || ru.InRange(ds[2], "2.5.1") {
		t.Error("InRange")
	}
	if _, ok := ru.NewRange(dep.Dep{Range: "[2.0,1.0)"}, "3.0", ""); ok {
		t.Error("NewRange of an invalid range")
	}
	if _, err := lein.Edits(dep.Update{Dep: dep.Dep{Range: "[2.0,1.0)"}, NewVersion: "3.0"}); err == nil {
		t.Error("Edits of an invalid range: want error")
	}

	git := []byte(`{:deps {io.github.o/r {:git/tag "v1.0.0" :git/sha "1a2b3c4"}}}`)
	edn := NewDepsEdn()
	gd := extract(t, edn, "deps.edn", git)
	es, err := edn.Edits(dep.Update{Dep: gd[0], NewVersion: "v1.1.0", NewDigest: "5d6e7f80000000000000000000000000000000000"})
	if err != nil {
		t.Fatal(err)
	}
	out, err := patch.Apply(git, es)
	if err != nil || string(out) != `{:deps {io.github.o/r {:git/tag "v1.1.0" :git/sha "5d6e7f8"}}}` {
		t.Errorf("git edit %s, %v", out, err)
	}
	if es, err := edn.Edits(dep.Update{Dep: gd[0], NewVersion: "v1.0.0", NewDigest: "1A2B3C4fff"}); err != nil || len(es) != 0 {
		t.Errorf("unchanged digest: %v, %v", es, err)
	}
	if _, err := edn.Edits(dep.Update{Dep: dep.Dep{Name: "x"}, NewDigest: "abc"}); err == nil {
		t.Error("pin without a sha: want error")
	}
}
