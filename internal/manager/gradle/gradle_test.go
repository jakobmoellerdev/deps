package gradle

import (
	"os"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Fixtures (fetched 2026-11-04):
//   - testdata/nowinandroid.libs.versions.toml: https://raw.githubusercontent.com/android/nowinandroid/main/gradle/libs.versions.toml
//   - testdata/okhttp.libs.versions.toml: https://raw.githubusercontent.com/square/okhttp/master/gradle/libs.versions.toml
//   - testdata/petclinic.build.gradle: https://raw.githubusercontent.com/spring-projects/spring-petclinic/main/build.gradle
//   - testdata/ktor-chat.build.gradle.kts: https://raw.githubusercontent.com/ktorio/ktor-samples/main/chat/build.gradle.kts

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// want is the compared part of a dep.
type want struct {
	depType, name, version string
	line                   int
	skip                   string
}

func summary(d dep.Dep) want {
	return want{depType: d.DepType, name: d.Name, version: d.Version, line: d.Line, skip: d.Skip}
}

func extract(t *testing.T, file string, src []byte) []dep.Dep {
	t.Helper()
	ds, err := New().Extract(file, src, manager.Env{})
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range ds {
		if d.Manager != Name || d.Datasource != "maven" || d.Versioning != "maven" || d.File != file {
			t.Errorf("dep %+v: wrong manager fields", d)
		}
		if src := map[string]string{DepTypeDependencies: LibraryRepositories, DepTypePlugin: PluginRepositories}[d.DepType]; d.Source != src {
			t.Errorf("dep %s: Source = %q", d.Name, d.Source)
		}
		if d.Skip == "" {
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != d.Version || got == "" {
				t.Errorf("dep %s: src[RefSpan] = %q, Version %q", d.Name, got, d.Version)
			}
		}
	}
	return ds
}

func TestMatch(t *testing.T) {
	m := New()
	for path, ok := range map[string]bool{
		"gradle/libs.versions.toml":                true,
		"libs.versions.toml":                       true,
		"build-logic/gradle/x.versions.toml":       true,
		"gradle/versions.toml":                     false,
		".versions.toml":                           false,
		"Cargo.toml":                               false,
		"build.gradle":                             true,
		"build.gradle.kts":                         true,
		"app/build.gradle.kts":                     true,
		"settings.gradle":                          true,
		"settings.gradle.kts":                      true,
		"gradle/libs.gradle":                       true,
		".gradle":                                  false,
		"build.gradle.kts.bak":                     false,
		"gradle.properties":                        true,
		"sub/gradle.properties":                    true,
		"gradle/wrapper/gradle-wrapper.properties": false,
		"buildSrc/src/main/kotlin/Versions.kt":     true,
		"buildSrc/src/main/java/Deps.kt":           true,
		"x/buildSrc/src/main/groovy/Libs.groovy":   true,
		"buildSrc/build/generated/X.kt":            false,
		"build-logic/convention/src/main/kotlin/AndroidConvention.kt": true,
		"build-logic/convention/src/main/java/A.kt":                   false,
		"app/src/main/kotlin/App.kt":                                  false,
		"buildSrc/build.gradle.kts":                                   true,
	} {
		if got := m.Match(path); got != ok {
			t.Errorf("Match(%q) = %v", path, got)
		}
	}
	if m.Name() != "gradle" {
		t.Errorf("Name = %q", m.Name())
	}
}

func TestExtractFixtures(t *testing.T) {
	for _, tc := range []struct {
		file, path       string
		libraries, plugs int
		skipped          int
		some             []want
	}{
		{file: "nowinandroid.libs.versions.toml", path: "gradle/libs.versions.toml", libraries: 88, plugs: 19, some: []want{
			{DepTypeDependencies, "com.google.accompanist:accompanist-permissions", "0.37.0", 72, ""},
			{DepTypeDependencies, "androidx.compose:compose-bom-alpha", "2025.09.01", 78, ""},
			{DepTypeDependencies, "javax.inject:javax.inject", "1", 138, ""},
			{DepTypeDependencies, "junit:junit", "4.13.2", 139, ""},
			{DepTypeDependencies, "org.jetbrains.kotlin:kotlin-stdlib-jdk8", "2.3.0", 141, ""},
			{DepTypeDependencies, "com.squareup.okhttp3:logging-interceptor", "4.12.0", 152, ""},
			{DepTypePlugin, "com.android.application", "9.3.2", 179, ""},
			{DepTypePlugin, "org.jetbrains.kotlin.jvm", "2.3.0", 190, ""},
		}},
		{file: "okhttp.libs.versions.toml", path: "gradle/libs.versions.toml", libraries: 79, plugs: 21, some: []want{
			{DepTypeDependencies, "software.amazon.cryptools:AmazonCorrettoCryptoProvider", "2.5.0", 67, ""},
			{DepTypeDependencies, "org.bouncycastle:bc-jdk15to18-bom", "1.86", 83, ""},
			{DepTypeDependencies, "junit:junit", "4.13.2", 98, ""},
			{DepTypeDependencies, "com.gradleup.tapmoc:com.gradleup.tapmoc.gradle.plugin", "0.5.0", 151, ""},
			{DepTypePlugin, "com.android.application", "9.1.1", 154, ""},
			{DepTypePlugin, "org.jetbrains.kotlin.jvm", "2.3.21", 164, ""},
		}},
		{file: "petclinic.build.gradle", path: "build.gradle", libraries: 5, plugs: 6, skipped: 5, some: []want{
			{DepTypeDependencies, "org.webjars:webjars-locator-lite", "", 42, "unresolved variable webjarsLocatorLiteVersion"},
			{DepTypeDependencies, "com.puppycrawl.tools:checkstyle", "", 58, "unresolved variable checkstyleVersion"},
			{DepTypePlugin, "org.springframework.boot", "4.1.0", 4, ""},
			{DepTypePlugin, "io.spring.nohttp", "0.0.11", 9, ""},
		}},
		{file: "ktor-chat.build.gradle.kts", path: "chat/build.gradle.kts", libraries: 3, plugs: 2, some: []want{
			{DepTypeDependencies, "io.ktor:ktor-bom", "3.6.1", 24, ""},
			{DepTypeDependencies, "ch.qos.logback:logback-classic", "1.5.21", 36, ""},
			{DepTypeDependencies, "org.jetbrains.kotlinx:kotlinx-coroutines-core-js", "1.10.2", 53, ""},
			{DepTypePlugin, "org.jetbrains.kotlin.multiplatform", "2.4.20", 2, ""},
			{DepTypePlugin, "org.jetbrains.kotlin.plugin.serialization", "2.4.20", 3, ""},
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			ds := extract(t, tc.path, fixture(t, tc.file))
			var libs, plugs, skipped int
			var got []want
			for _, d := range ds {
				got = append(got, summary(d))
				if d.DepType == DepTypePlugin {
					plugs++
				} else {
					libs++
				}
				if d.Skip != "" {
					skipped++
				}
			}
			if libs != tc.libraries || plugs != tc.plugs || skipped != tc.skipped {
				t.Errorf("libraries %d, plugins %d, skipped %d", libs, plugs, skipped)
			}
			for _, w := range tc.some {
				if !slices.Contains(got, w) {
					t.Errorf("missing %+v", w)
				}
			}
		})
	}
}

func TestExtractCatalog(t *testing.T) {
	lib := func(name, version string, line int, skip string) want {
		return want{DepTypeDependencies, name, version, line, skip}
	}
	plug := func(name, version string, line int, skip string) want {
		return want{DepTypePlugin, name, version, line, skip}
	}
	for _, tc := range []struct {
		name, src string
		want      []want
	}{
		{name: "string notation and comments", src: "# top\n[libraries] # libs\n" +
			"a = \"g:a:1.0\" # trailing\n" +
			"b = 'g:b:2.0'\n" +
			"bom = \"g:bom\"\n" +
			"bad = \"g:x:y:z\"\n" +
			"[plugins]\n" +
			"p = \"com.example.p:3.0\"\n" +
			"noversion = \"com.example.q\"\n",
			want: []want{lib("g:a", "1.0", 3, ""), lib("g:b", "2.0", 4, ""), plug("com.example.p", "3.0", 8, "")}},
		{name: "inline tables", src: "[versions]\nk = \"1.1\"\n\"quoted\" = '2.2'\n[libraries]\n" +
			"m = { module = \"g:m\", version = \"1\" }\n" +
			"gn = { group = \"g\", name = \"n\", version.ref = \"k\" }\n" +
			"t = { module = 'g:t', version = { ref = \"quoted\" } }\n" +
			"\"q.alias\" = {module=\"g:q\",version.ref=\"k\",}\n" +
			"nov = { module = \"g:nov\" }\n" +
			"noname = { version = \"1\" }\n" +
			"badmodule = { module = \"g\", version = \"1\" }\n" +
			"[plugins]\n" +
			"a = { id = \"p.a\", version = \"9\" }\n" +
			"b = { id = \"p.b\", version.ref = \"k\" }\n" +
			"local = { id = \"p.local\" }\n",
			want: []want{lib("g:m", "1", 5, ""), lib("g:n", "1.1", 6, ""), lib("g:t", "2.2", 7, ""), lib("g:q", "1.1", 8, ""),
				plug("p.a", "9", 13, ""), plug("p.b", "1.1", 14, "")}},
		{name: "dotted keys and sub-tables", src: "[versions.groovy]\nstrictly = \"[3.0,4.0)\"\n" +
			"[versions]\nkotlin = \"2.0.0\"\nnested.name = \"5\"\n" +
			"[libraries]\ngroovy.module = \"org.codehaus.groovy:groovy\"\ngroovy.version.ref = \"groovy\"\n" +
			"kotlin-stdlib.module = \"org.jetbrains.kotlin:kotlin-stdlib\"\nkotlin-stdlib.version.ref = \"kotlin\"\n" +
			"n = { module = \"g:n\", version.ref = \"nested.name\" }\n" +
			"[libraries.table-form]\nmodule = \"g:table\"\nversion = \"7\"\n",
			want: []want{lib("org.codehaus.groovy:groovy", "", 7, "rich version"), lib("org.jetbrains.kotlin:kotlin-stdlib", "2.0.0", 9, ""),
				lib("g:n", "5", 11, ""), lib("g:table", "7", 12, "")}},
		{name: "rich versions and unknown refs", src: "[versions]\nrich = { strictly = \"[1,2)\", prefer = \"1.5\" }\n" +
			"num = 3\n" +
			"[libraries]\n" +
			"a = { module = \"g:a\", version.ref = \"rich\" }\n" +
			"b = { module = \"g:b\", version = { strictly = \"1.0\" } }\n" +
			"c = { module = \"g:c\", version.ref = \"missing\" }\n" +
			"d = { module = \"g:d\", version.ref = \"num\" }\n" +
			"e = { module = \"g:e\", version.require = \"1.0\" }\n" +
			"[plugins]\n" +
			"p = { id = \"p\", version.ref = \"missing\" }\n",
			want: []want{lib("g:a", "", 5, "rich version"), lib("g:b", "", 6, "rich version"), lib("g:c", "", 7, "unresolved version.ref missing"),
				lib("g:d", "", 8, "cannot locate value"), lib("g:e", "", 9, "rich version"), plug("p", "", 11, "unresolved version.ref missing")}},
		{name: "escapes", src: "[versions]\nesc = \"1.\\u0030\"\n[libraries]\n" +
			"a = { module = \"g:a\", version.ref = \"esc\" }\n" +
			"b = \"g:b:1\\t\"\n" +
			"c = { module = \"g:c\", version = '1.\\0' }\n" +
			"[plugins]\np = \"p:\\\"1\\\"\"\n",
			want: []want{lib("g:a", "", 4, "cannot locate value"), lib("g:b", "", 5, "cannot locate value"), lib("g:c", "1.\\0", 6, ""),
				plug("p", "", 8, "cannot locate value")}},
		{name: "bundles and other tables", src: "[bundles]\ncompose = [\n  \"a\", # comment ]\n  \"b\",\n]\n" +
			"x = [ [\"nested\"], { a = 1 } ]\n" +
			"[metadata]\nformat.version = \"1.1\"\nweird = \"\"\"\n[libraries]\nnot = \"g:not:1\"\n\"\"\"\n" +
			"[[array.of.tables]]\nk = 'v'\n" +
			"[libraries]\nok = \"g:ok:1\"\n" +
			"empty = { module = \"g:empty\", version = \"\" }\n",
			want: []want{lib("g:ok", "1", 16, "")}},
		{name: "multi-line inline table (TOML 1.1)", src: "[libraries]\nm = {\n  module = \"g:m\", # c\n  version = \"3\",\n}\n",
			want: []want{lib("g:m", "3", 2, "")}},
		{name: "crlf", src: "[versions]\r\nk = \"1.0\"\r\n\r\n[libraries]\r\na = { module = \"g:a\", version.ref = \"k\" }\r\nb = \"g:b:2\"\r\n",
			want: []want{lib("g:a", "1.0", 5, ""), lib("g:b", "2", 6, "")}},
		{name: "empty", src: "", want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []want
			for _, d := range extract(t, "gradle/libs.versions.toml", []byte(tc.src)) {
				got = append(got, summary(d))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestExtractScript(t *testing.T) {
	lib := func(name, version string, line int, skip string) want {
		return want{DepTypeDependencies, name, version, line, skip}
	}
	plug := func(name, version string, line int, skip string) want {
		return want{DepTypePlugin, name, version, line, skip}
	}
	for _, tc := range []struct {
		name, src string
		want      []want
	}{
		{name: "configurations", src: "dependencies {\n" +
			"  implementation(\"g:a:1.0\")\n" +
			"  api 'g:b:2.0'\n" +
			"  testImplementation \"g:c:3.0:tests\"\n" +
			"  androidTestImplementation(\"g:d:4.0@aar\")\n" +
			"  implementation(platform(\"g:bom:5.0\"))\n" +
			"  compileOnlyApi 'g:e:6'\n" +
			"  kapt(\"g:f:7\") { exclude(group = \"x\") }\n" +
			"  implementation(\"g:managed\")\n" +
			"  implementation(project(\":core:data\"))\n" +
			"  implementation group: 'g', name: 'map', version: '1'\n" +
			"  myImplementation(\"g:other:1\")\n" +
			"  implementation(\"g:a:b:c:d\")\n" +
			"  implementation(\"g:empty:\")\n" +
			"}\n",
			want: []want{lib("g:a", "1.0", 2, ""), lib("g:b", "2.0", 3, ""), lib("g:c", "3.0", 4, ""), lib("g:d", "4.0", 5, ""),
				lib("g:bom", "5.0", 6, ""), lib("g:e", "6", 7, ""), lib("g:f", "7", 8, ""), lib("g:map", "1", 11, "")}},
		{name: "variables", src: "dependencies {\n  implementation(\"g:a:$v\")\n  classpath \"g:b:${b.version}\"\n  implementation(\"$coords\")\n}\n" +
			"plugins {\n  id(\"p\") version \"$pv\"\n  id(\"$id\") version \"1\"\n  id(\"q\") version qv\n}\n",
			want: []want{lib("g:a", "", 2, "unresolved variable v"), lib("g:b", "", 3, "unresolved variable b.version"),
				plug("p", "", 7, "unresolved variable pv"), plug("$id", "", 8, "version variable"), plug("q", "", 9, "unresolved variable qv")}},
		{name: "plugins", src: "plugins {\n" +
			"  id(\"com.android.application\") version \"8.5.0\" apply false\n" +
			"  id 'org.example.groovy' version '1.2'\n" +
			"  id(\"org.example.call\").version(\"3.4\")\n" +
			"  kotlin(\"jvm\") version \"2.0.21\"\n" +
			"  id(\"java\")\n" +
			"  id(\"empty\") version \"\"\n" +
			"}\n",
			want: []want{plug("com.android.application", "8.5.0", 2, ""), plug("org.example.groovy", "1.2", 3, ""),
				plug("org.example.call", "3.4", 4, ""), plug("org.jetbrains.kotlin.jvm", "2.0.21", 5, "")}},
		{name: "comments", src: "// implementation(\"g:line:1\")\n" +
			"/* implementation(\"g:block:1\")\n   api 'g:block2:1' */\n" +
			"implementation(\"g:real:1\") // trailing 'quote\n" +
			"val url = \"https://example.com/x\" ; api(\"g:afterurl:2\")\n" +
			"val s = \"\"\"\n  // not a comment implementation(\"g:raw:3\")\n\"\"\"\n" +
			"val c = '\\'' ; api(\"g:afterchar:4\")\n" +
			"val u = \"unterminated\n" +
			"api(\"g:next:5\")\n" +
			"/* open comment\nimplementation(\"g:hidden:1\")\n",
			want: []want{lib("g:real", "1", 4, ""), lib("g:afterurl", "2", 5, ""), lib("g:afterchar", "4", 9, ""),
				lib("g:next", "5", 11, "")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []want
			for _, d := range extract(t, "build.gradle.kts", []byte(tc.src)) {
				got = append(got, summary(d))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestEdits(t *testing.T) {
	for _, tc := range []struct {
		name, file, src, dep, to, want string
	}{
		{name: "version.ref", file: "gradle/libs.versions.toml",
			src: "[versions]\nkotlin = \"2.0.0\" # pinned\n[libraries]\na = { module = \"org.jetbrains.kotlin:kotlin-stdlib\", version.ref = \"kotlin\" }\n" +
				"b = { module = \"org.jetbrains.kotlin:kotlin-reflect\", version.ref = \"kotlin\" }\n",
			dep: "org.jetbrains.kotlin:kotlin-reflect", to: "2.1.0",
			want: "[versions]\nkotlin = \"2.1.0\" # pinned\n[libraries]\na = { module = \"org.jetbrains.kotlin:kotlin-stdlib\", version.ref = \"kotlin\" }\n" +
				"b = { module = \"org.jetbrains.kotlin:kotlin-reflect\", version.ref = \"kotlin\" }\n"},
		{name: "catalog string notation", file: "libs.versions.toml", src: "[libraries]\nx = 'g:x:1.0'\r\n", dep: "g:x", to: "1.10",
			want: "[libraries]\nx = 'g:x:1.10'\r\n"},
		{name: "catalog plugin", file: "libs.versions.toml", src: "[plugins]\np = \"com.example:1.0\"\n", dep: "com.example", to: "2.0-rc1",
			want: "[plugins]\np = \"com.example:2.0-rc1\"\n"},
		{name: "build script classifier", file: "build.gradle", src: "dependencies {\n  testImplementation 'g:x:1.0:tests@jar'\n}\n", dep: "g:x", to: "1.1",
			want: "dependencies {\n  testImplementation 'g:x:1.1:tests@jar'\n}\n"},
		{name: "build script plugin", file: "settings.gradle.kts", src: "plugins { kotlin(\"jvm\") version \"2.0.0\" }\n", dep: "org.jetbrains.kotlin.jvm", to: "2.1.0",
			want: "plugins { kotlin(\"jvm\") version \"2.1.0\" }\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var d *dep.Dep
			for _, x := range extract(t, tc.file, []byte(tc.src)) {
				if x.Name == tc.dep {
					d = &x
				}
			}
			if d == nil {
				t.Fatalf("dep %s not found", tc.dep)
			}
			edits, err := New().Edits(dep.Update{Dep: *d, NewVersion: tc.to})
			if err != nil {
				t.Fatal(err)
			}
			out, err := patch.Apply([]byte(tc.src), edits)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != tc.want {
				t.Errorf("got %q", out)
			}
		})
	}
}

func TestSharedVersionRefSpan(t *testing.T) {
	src := fixture(t, "okhttp.libs.versions.toml")
	spans := map[dep.Span]int{}
	for _, d := range extract(t, "gradle/libs.versions.toml", src) {
		if d.Version == "2.3.21" {
			spans[d.RefSpan]++
		}
	}
	if len(spans) != 1 {
		t.Fatalf("kotlin deps use %d spans, want 1: %v", len(spans), spans)
	}
	for _, n := range spans {
		if n < 10 {
			t.Errorf("only %d deps share the kotlin version", n)
		}
	}
}

// TestCatalogSyntaxErrors: a catalog that is not valid TOML fails the file, as it fails
// Gradle (the catalog is parsed strictly by internal/tomlspan).
func TestCatalogSyntaxErrors(t *testing.T) {
	for _, src := range []string{
		"[libraries]\nbroken = { module = \"g:broken\", version = \"1\" junk }\n",
		"[libraries]\nunterminated = \"g:u:1\n",
		"[libraries]\n= \"g:nokey:1\"\n",
		"[libraries]\nnokv \"g:noeq:1\"\n",
		"[libraries]\ntrail = \"g:trail:1\" junk\n",
		"[ broken header\n",
		"[libraries]\nok = \"g:ok:1\"\n[libraries]\n",
		"[libraries]\nopen = { module = \"g:open\"",
		"[libraries]\ndeep = " + strings.Repeat("[", 40) + "\n",
	} {
		if ds, err := New().Extract("gradle/libs.versions.toml", []byte(src), manager.Env{}); err == nil || !strings.Contains(err.Error(), "toml: line") {
			t.Errorf("%q: deps %v, err %v", src, ds, err)
		}
	}
}
