package kotlinscript

import (
	"fmt"
	"os"
	"slices"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures (fetched 2026-11-09):
//   - testdata/build.main.kts, testdata/end-to-end-tests.main.kts:
//     https://raw.githubusercontent.com/typesafegithub/github-workflows-kt/main/.github/workflows/<name>
//   - testdata/renovate-*.main.kts: Renovate's kotlin-script fixtures,
//     https://raw.githubusercontent.com/renovatebot/renovate/main/lib/modules/manager/kotlin-script/__fixtures__/<name without renovate->
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestMatch(t *testing.T) {
	m := New()
	for path, want := range map[string]bool{
		"script.main.kts":                     true,
		".github/workflows/build.main.kts":    true,
		"a/.main.kts":                         true,
		".main.kts":                           false,
		"build.gradle.kts":                    false,
		"script.kts":                          false,
		"script.main.kts.bak":                 false,
		"test.main.do-not-compile.kts":        false,
		"settings.gradle.kts":                 false,
		"src/main/kotlin/Main.kt":             false,
		"docs/example.main.kts/README.md":     false,
		"tools/release.main.kts":              true,
		"nested/dir/generate-config.main.kts": true,
	} {
		if got := m.Match(path); got != want {
			t.Errorf("Match(%q) = %v", path, got)
		}
	}
}

// line renders a dep as "<line> <name> <version>[ skip=<skip>]".
func line(d dep.Dep) string {
	s := fmt.Sprintf("%d %s %s", d.Line, d.Name, d.Version)
	if d.Skip != "" {
		s += " skip=" + d.Skip
	}
	return s
}

func TestExtract(t *testing.T) {
	const bindings = "https://bindings.krzeminski.it/"
	for _, tc := range []struct {
		name, src string
		source    string
		want      []string
	}{
		{name: "github-workflows-kt build", src: fixture(t, "build.main.kts"), source: "https://repo.maven.apache.org/maven2/ " + bindings, want: []string{
			"3 io.github.typesafegithub:github-workflows-kt 4.0.0",
			"6 actions:cache v6",
			"7 actions:checkout v7",
			"8 actions:setup-java___commit_lenient v5.2.0__be666c2fcd27ec809703dec50e508c2fdc7f6654",
			"9 gradle:actions__setup-gradle v6",
			"10 fwilhe2:setup-kotlin v2",
		}},
		{name: "github-workflows-kt end-to-end, local repository dropped", src: fixture(t, "end-to-end-tests.main.kts"), source: bindings, want: []string{
			"3 io.github.typesafegithub:github-workflows-kt 4.0.1-SNAPSHOT",
			"4 io.github.typesafegithub:action-updates-checker 4.0.1-SNAPSHOT",
			"6 actions:checkout v6",
			"7 actions:github-script v9",
			"8 actions:setup-java v5",
			"9 actions:setup-python v6",
			"10 gradle:actions__setup-gradle v6",
			"11 Wandalen:wretry.action v3",
			"12 fwilhe2:setup-kotlin v1",
		}},
		{name: "renovate generic case", src: fixture(t, "renovate-generic-case.main.kts"), want: []string{
			"2 it.krzeminski:github-actions-kotlin-dsl 0.22.0",
			"4 org.eclipse.jgit:org.eclipse.jgit 4.6.0.201612231935-r",
			"9 org.jetbrains.lets-plot:lets-plot-kotlin-jvm 3.0.2",
		}},
		{name: "renovate missing parts", src: fixture(t, "renovate-missing-parts.main.kts"), want: []string{
			"2 it.krzeminski:github-actions-kotlin-dsl 0.22.0",
		}},
		{name: "repositories after deps, deduplicated, empty dropped",
			src: "@file:Repository(\"https://jitpack.io\")\n@file:DependsOn(\"a:b:1.0\")\n" +
				"@file:Repository(\"https://some.other.repo/foo/bar/baz\")\n@file:Repository(\"\")\n@file:Repository(\"https://jitpack.io/\")\n",
			source: "https://jitpack.io/ https://some.other.repo/foo/bar/baz/", want: []string{"2 a:b 1.0"}},
		{name: "vararg annotations",
			src:    "@file:Repository(\"https://jitpack.io\", \"https://some.other.repo\")\n@file:DependsOn(\"a:b:1.0\", \"c:d:2.0\")\n",
			source: "https://jitpack.io/ https://some.other.repo/", want: []string{"2 a:b 1.0", "2 c:d 2.0"}},
		{name: "named arguments and options",
			src: "@file:Repository(\"https://maven.example.com\", options = [\"username=\\$MAVEN_USER\", \"password=x\"])\n" +
				"@file:DependsOn(artifactsCoordinates = [\"a:b:1.0\"], options = [\"transitive=false\"])\n",
			source: "https://maven.example.com/", want: []string{"2 a:b 1.0"}},
		{name: "qualified annotation name",
			src:  "@file:kotlin.script.experimental.dependencies.DependsOn(\"a:b:1.0\")\n",
			want: []string{"1 a:b 1.0"}},
		{name: "extension and classifier",
			src:  "@file:DependsOn(\"a:b:pom:1.0\")\n@file:DependsOn(\"a:c:jar:sources:2.0\")\n@file:DependsOn(\"a:d::3.0\")\n@file:DependsOn(\"a:e:jar::4.0\")\n",
			want: []string{"1 a:b 1.0", "2 a:c 2.0", "4 a:e 4.0"}},
		{name: "not coordinates",
			src:  "@file:DependsOn(\"lib/local.jar\")\n@file:DependsOn(\"a:b\")\n@file:DependsOn(\"a:b:c:d:e:1.0\")\n@file:DependsOn(\"a:b:1.0 \")\n@file:DependsOn(\"\")\n",
			want: nil},
		{name: "skips",
			src: "@file:DependsOn(\"a:b:[1.0,2.0)\")\n@file:DependsOn(\"a:c:\\$v\")\n@file:DependsOn(\"a:d:1.\\u0030\")\n" +
				"@file:DependsOn(\"a:e:LATEST\")\n@file:DependsOn(\"a:f:RELEASE\")\n",
			want: []string{
				"1 a:b [1.0,2.0) skip=" + skipRange,
				"2 a:c \\$v skip=" + skipTemplate,
				"3 a:d 1.\\u0030 skip=" + skipTemplate,
				"4 a:e LATEST skip=" + skipVersion + "LATEST",
				"5 a:f RELEASE skip=" + skipVersion + "RELEASE",
			}},
		{name: "comments and strings are not annotations",
			src: "// @file:DependsOn(\"a:a:1.0\")\n/* outer /* @file:DependsOn(\"a:b:1.0\") */ still comment @file:DependsOn(\"a:c:1.0\") */\n" +
				"val s = \"@file:DependsOn(\\\"a:d:1.0\\\")\"\nval r = \"\"\"\n@file:DependsOn(\"a:e:1.0\")\n\"\"\"\"\nval c = '\"'\n" +
				"@file:DependsOn(\"a:f:1.0\") // trailing \"comment\"\n" +
				"@file:Repository(\"https://example.com/(maven)\") /* ) */\n",
			source: "https://example.com/(maven)/", want: []string{"8 a:f 1.0"}},
		{name: "raw string and nested parentheses",
			src:  "@file:DependsOn(\"\"\"a:b:1.0\"\"\", (\"a:c:2.0\"))\n@file:DependsOn(\"a:d:3.0\")\n",
			want: []string{"1 a:b 1.0", "1 a:c 2.0", "2 a:d 3.0"}},
		{name: "spaces in annotation", src: "@file : DependsOn ( \"a:b:1.0\" )\n@ file:DependsOn(\"a:c:1.0\")\n", want: []string{"1 a:b 1.0"}},
		{name: "unterminated annotation", src: "@file:DependsOn(\"a:b:1.0\", \"a:c", want: []string{"1 a:b 1.0"}},
		{name: "unterminated raw string", src: "@file:DependsOn(\"\"\"a:b:1.0", want: nil},
		{name: "unterminated raw string in code", src: "@file:DependsOn(\"a:b:1.0\")\nval x = \"\"\"@file:DependsOn(\"a:c:1.0\")", want: []string{"1 a:b 1.0"}},
		{name: "unterminated block comment", src: "@file:DependsOn(\"a:b:1.0\")\n/* @file:DependsOn(\"a:c:1.0\")", want: []string{"1 a:b 1.0"}},
		{name: "unterminated string and escape at end", src: "@file:DependsOn(\"a:b:1.0\")\nval x = \"abc\\", want: []string{"1 a:b 1.0"}},
		{name: "line comment at end", src: "@file:DependsOn(\"a:b:1.0\") // done", want: []string{"1 a:b 1.0"}},
		{name: "no deps", src: "#!/usr/bin/env kotlin\nprintln(\"Hello world\")\n", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ds, err := New().Extract("x.main.kts", []byte(tc.src), manager.Env{})
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, d := range ds {
				got = append(got, line(d))
				if d.Manager != Name || d.Datasource != "maven" || d.Versioning != "maven" || d.DepType != DepTypeDependencies || d.File != "x.main.kts" || d.Source != tc.source {
					t.Errorf("dep = %+v", d)
				}
				if d.Skip == "" && tc.src[d.RefSpan.Start:d.RefSpan.End] != d.Version {
					t.Errorf("%s: RefSpan %v = %q", d.Name, d.RefSpan, tc.src[d.RefSpan.Start:d.RefSpan.End])
				}
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("deps =\n%q\nwant\n%q", got, tc.want)
			}
		})
	}
}

func TestEdits(t *testing.T) {
	src := fixture(t, "renovate-generic-case.main.kts")
	ds, err := New().Extract("x.main.kts", []byte(src), manager.Env{})
	if err != nil || len(ds) != 3 {
		t.Fatalf("deps %v, err %v", ds, err)
	}
	var edits []patch.Edit
	for _, u := range []dep.Update{{Dep: ds[1], NewVersion: "7.4.0.202509020913-r"}, {Dep: ds[2], NewVersion: "4.11.2"}} {
		e, err := New().Edits(u)
		if err != nil || len(e) != 1 {
			t.Fatalf("edits %v, err %v", e, err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply([]byte(src), edits)
	if err != nil {
		t.Fatal(err)
	}
	want := `#!/usr/bin/env kotlin
@file:DependsOn("it.krzeminski:github-actions-kotlin-dsl:0.22.0")
@file:DependsOn(
  "org.eclipse.jgit:org.eclipse.jgit:7.4.0.202509020913-r"
)

@file : DependsOn
  (
  "org.jetbrains.lets-plot:lets-plot-kotlin-jvm:4.11.2"

)

// ...

println("Hello world")
`
	if string(out) != want {
		t.Errorf("out =\n%s", out)
	}
}
