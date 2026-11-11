package gradle

import (
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/patch"
)

// Build fixtures (fetched 2026-11-04):
//   - testdata/ktor-get-started/: https://raw.githubusercontent.com/ktorio/ktor-get-started-sample/main/{build.gradle.kts,gradle.properties,settings.gradle.kts}
//   - testdata/kafka/: https://raw.githubusercontent.com/apache/kafka/trunk/{gradle.properties,gradle/dependencies.gradle,settings.gradle},
//     build.gradle: its first 42 lines (buildscript and plugins blocks)
//   - testdata/iosched/: https://raw.githubusercontent.com/google/iosched/main/{build.gradle.kts,settings.gradle.kts,buildSrc/src/main/java/Versions.kt}
//   - testdata/nebula/: https://raw.githubusercontent.com/nebula-plugins/nebula-plugin-plugin/main/{build.gradle.kts,gradle.lockfile,settings.gradle}
//   - testdata/markwon.build.gradle: https://raw.githubusercontent.com/noties/Markwon/master/build.gradle
//   - testdata/mentorship.Dependencies.kt: https://raw.githubusercontent.com/anitab-org/mentorship-android/develop/buildSrc/src/main/kotlin/Dependencies.kt
//   - testdata/nowinandroid.settings.gradle.kts: https://raw.githubusercontent.com/android/nowinandroid/main/settings.gradle.kts
//   - testdata/savvagen.build.gradle: https://raw.githubusercontent.com/savvagen/github-actions-sample-project/master/build.gradle
//   - testdata/bisq/ (fetched 2026-11-09): https://github.com/bisq-network/bisq at 6e1ea27f,
//     {settings.gradle,build.gradle,core/build.gradle,gradle/libs.versions.toml,build-logic/settings.gradle,
//     build-logic/commons/build.gradle,build-logic/commons/src/main/groovy/bisq.java-conventions.gradle}

// loadDir returns the files below testdata/dir by slash path.
func loadDir(t *testing.T, dir string) map[string]string {
	t.Helper()
	root := filepath.Join("testdata", dir)
	files := map[string]string{}
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		b, err := os.ReadFile(p)
		files[filepath.ToSlash(rel)] = string(b)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}

// runBuild extracts the matched files in path order (reading other files from files) and
// links them; with locks it writes every file to a temporary root and reads the lockfiles too.
// It returns the deps of every file by its path and checks the spans of updatable deps.
func runBuild(t *testing.T, files map[string]string, locks bool) map[string][]dep.Dep {
	t.Helper()
	m := New()
	var paths []string
	for _, p := range slices.Sorted(maps.Keys(files)) {
		if m.Match(p) {
			paths = append(paths, p)
		}
	}
	env := manager.Env{ReadFile: func(p string) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		return nil, fs.ErrNotExist
	}}
	all := make([][]dep.Dep, len(paths))
	for i, p := range paths {
		ds, err := m.Extract(p, []byte(files[p]), env)
		if err != nil {
			t.Fatal(err)
		}
		all[i] = ds
	}
	m.(manager.Linker).Link(all)
	if locks {
		root := writeTree(t, files)
		if errs := m.(manager.LockReader).ReadLocks(root, all); len(errs) > 0 {
			t.Fatal(errs)
		}
	}
	out := map[string][]dep.Dep{}
	for i, p := range paths {
		out[p] = all[i]
		for _, d := range all[i] {
			if d.Skip == "" && d.Range == "" {
				if got := files[d.File][d.RefSpan.Start:d.RefSpan.End]; got != d.Version {
					t.Errorf("%s in %s: %s[RefSpan] = %q, Version %q", d.Name, p, d.File, got, d.Version)
				}
			}
		}
	}
	return out
}

func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for p, c := range files {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// get returns the dep named name (and of depType when set) declared in file.
func get(t *testing.T, deps map[string][]dep.Dep, file, name string) dep.Dep {
	t.Helper()
	for _, d := range deps[file] {
		if d.Name == name {
			return d
		}
	}
	t.Fatalf("%s: no dep %s in %+v", file, name, deps[file])
	return dep.Dep{}
}

// linked is the compared part of a linked dep.
type linked struct {
	file, version, ref, skip string
	line                     int
}

func linkedOf(d dep.Dep) linked {
	return linked{file: d.File, version: d.Version, ref: d.VersionRef, skip: d.Skip, line: d.Line}
}

func TestLinkResolutionOrder(t *testing.T) {
	files := map[string]string{
		"settings.gradle.kts": "rootProject.name = \"x\"\n",
		"gradle.properties":   "shared=1.0\nsub=9.9\nsub2=0\ndyn=1.+\n",
		"build.gradle.kts": "val local = \"3.0\"\n" +
			"dependencies {\n" +
			"  implementation(\"g:local:$local\")\n" +
			"  implementation(\"g:shared:${project.property(\"shared\")}\")\n" +
			"  implementation(\"g:kotlin:${Versions.kotlin}\")\n" +
			"  implementation(\"g:missing:$missing\")\n" +
			"  implementation(\"g:dyn:$dyn\")\n" +
			"}\n",
		"gradle/deps.gradle": "ext.fromGradleDir = '4.0'\n",
		"buildSrc/src/main/kotlin/Versions.kt": "object Versions {\n    const val kotlin = \"5.0\"\n}\n" +
			"object Deps { const val lib = \"g:buildsrc:${Versions.kotlin}\" }\n",
		"buildSrc/build.gradle.kts": "dependencies { implementation(\"g:bs:$shared\") }\n",
		"app/gradle.properties":     "sub=7.0\nsub2=0\n",
		"app/build.gradle.kts": "val sub2 = \"8.0\"\n" +
			"dependencies {\n" +
			"  implementation(\"g:shared:$shared\")\n" +
			"  implementation(\"g:sub:${sub}\")\n" +
			"  implementation(\"g:sub2:$sub2\")\n" +
			"  implementation(\"g:local:$local\")\n" +
			"  implementation(\"g:gd:${fromGradleDir}\")\n" +
			"}\n",
		"nested/settings.gradle": "",
		"nested/build.gradle":    "dependencies { implementation \"g:nested:$shared\" }\n",
	}
	deps := runBuild(t, files, false)
	for _, tc := range []struct {
		file, name string
		want       linked
	}{
		{"build.gradle.kts", "g:local", linked{"build.gradle.kts", "3.0", "local", "", 1}},
		{"build.gradle.kts", "g:shared", linked{"gradle.properties", "1.0", "shared", "", 1}},
		{"build.gradle.kts", "g:kotlin", linked{"buildSrc/src/main/kotlin/Versions.kt", "5.0", "Versions.kotlin", "", 2}},
		{"build.gradle.kts", "g:missing", linked{"build.gradle.kts", "", "", "unresolved variable missing", 6}},
		{"build.gradle.kts", "g:dyn", linked{"gradle.properties", "1.+", "dyn", "dynamic version 1.+", 4}},
		{"buildSrc/src/main/kotlin/Versions.kt", "g:buildsrc", linked{"buildSrc/src/main/kotlin/Versions.kt", "5.0", "Versions.kotlin", "", 2}},
		{"buildSrc/build.gradle.kts", "g:bs", linked{"buildSrc/build.gradle.kts", "", "", "unresolved variable shared", 1}},
		{"app/build.gradle.kts", "g:shared", linked{"gradle.properties", "1.0", "shared", "", 1}},
		{"app/build.gradle.kts", "g:sub", linked{"app/gradle.properties", "7.0", "sub", "", 1}},
		{"app/build.gradle.kts", "g:sub2", linked{"app/build.gradle.kts", "8.0", "sub2", "", 1}},
		{"app/build.gradle.kts", "g:local", linked{"build.gradle.kts", "3.0", "local", "", 1}},
		{"app/build.gradle.kts", "g:gd", linked{"gradle/deps.gradle", "4.0", "fromGradleDir", "", 1}},
		{"nested/build.gradle", "g:nested", linked{"nested/build.gradle", "", "", "unresolved variable shared", 1}},
	} {
		if got := linkedOf(get(t, deps, tc.file, tc.name)); got != tc.want {
			t.Errorf("%s %s: got %+v, want %+v", tc.file, tc.name, got, tc.want)
		}
	}
	if d := get(t, deps, "build.gradle.kts", "g:dyn"); d.Range != ">=1.0.0-0 <2.0.0-0" || files[d.File][d.RefSpan.Start:d.RefSpan.End] != "1" {
		t.Errorf("dynamic variable: Range %q, span %v", d.Range, d.RefSpan)
	}
}

func TestLinkSkipsMismatchedFiles(t *testing.T) {
	m := New()
	ds, _ := m.Extract("build.gradle", []byte("dependencies { implementation \"g:a:$v\" }\n"), manager.Env{})
	files := [][]dep.Dep{{}, append(ds, dep.Dep{File: "build.gradle"}), {{File: "unknown.gradle"}}}
	m.(manager.Linker).Link(files)
	if files[1][0].Skip != "unresolved variable v" || files[2][0].Source != "" {
		t.Errorf("got %+v", files)
	}
}

func TestVariableSyntaxes(t *testing.T) {
	// Each case defines the variable in a file and references it from build.gradle(.kts)
	// in the same directory.
	for _, tc := range []struct {
		name, defFile, def, file, use, ref string
	}{
		{"properties key=value", "gradle.properties", "v=1.2.3\n", "build.gradle", `implementation "g:a:$v"`, "v"},
		{"properties key: value", "gradle.properties", "  v :  1.2.3\n", "build.gradle", `implementation "g:a:${v}"`, "v"},
		{"properties key value", "gradle.properties", "# c\n! c\nv 1.2.3 \r\n", "build.gradle", `implementation "g:a:${project.v}"`, "v"},
		{"properties dotted key", "gradle.properties", "kotlin.version=1.2.3\n", "build.gradle.kts", `implementation("g:a:${property("kotlin.version")}")`, "kotlin.version"},
		{"groovy def", "build.gradle", "def v = '1.2.3'\n", "build.gradle", `implementation "g:a:$v"`, "v"},
		{"groovy final def", "build.gradle", "final def v = \"1.2.3\"\n", "build.gradle", `implementation "g:a:$v"`, "v"},
		{"groovy String", "build.gradle", "String v = '1.2.3'\n", "build.gradle", `implementation "g:a:$v"`, "v"},
		{"ext.x", "build.gradle", "ext.v = '1.2.3'\n", "build.gradle", `implementation "g:a:${rootProject.v}"`, "v"},
		{"project.ext.x", "build.gradle", "project.ext.v = '1.2.3'\n", "build.gradle", `implementation "g:a:${project.ext.v}"`, "v"},
		{"ext block", "build.gradle", "ext {\n  v = '1.2.3'\n}\n", "build.gradle", `implementation "g:a:$v"`, "v"},
		{"ext block set", "build.gradle", "ext {\n  set('v', '1.2.3')\n}\n", "build.gradle", `implementation "g:a:$v"`, "v"},
		{"ext.set", "build.gradle", "ext.set('v', \"1.2.3\")\n", "build.gradle", `implementation "g:a:${ext.v}"`, "v"},
		{"ext index", "build.gradle", "ext['v'] = '1.2.3'\n", "build.gradle", `implementation "g:a:${ext['v']}"`, "v"},
		{"groovy map", "build.gradle", "ext.versions = [\n  'v': '1.2.3', other: x ? '1' : '2',\n  nested: [deep: '9'],\n]\n", "build.gradle", `implementation "g:a:$versions.v"`, "versions.v"},
		{"groovy map +=", "build.gradle", "ext { versions = [:] }\nversions += [v: \"1.2.3\"]\n", "build.gradle", `implementation "g:a:${versions.v}"`, "versions.v"},
		{"groovy map assign", "build.gradle", "def versions = [:]\nversions.v = '1.2.3'\n", "build.gradle", `implementation "g:a:${versions['v']}"`, "versions.v"},
		{"groovy map index assign", "build.gradle", "def versions = [:]\nversions['v'] = '1.2.3'\n", "build.gradle", `implementation "g:a:${versions.v}"`, "versions.v"},
		{"groovy nested map", "build.gradle", "ext.libs = [versions: [v: '1.2.3']]\n", "build.gradle", `implementation "g:a:$libs.versions.v"`, "libs.versions.v"},
		{"kotlin val", "build.gradle.kts", "val v = \"1.2.3\"\n", "build.gradle.kts", `implementation("g:a:$v")`, "v"},
		{"kotlin typed val", "build.gradle.kts", "val v: String = \"1.2.3\"\n", "build.gradle.kts", `implementation("g:a:$v")`, "v"},
		{"kotlin by extra", "build.gradle.kts", "val v by extra(\"1.2.3\")\n", "build.gradle.kts", `implementation("g:a:${extra["v"]}")`, "v"},
		{"kotlin typed by extra", "build.gradle.kts", "val v: String by project.extra(\"1.2.3\")\n", "build.gradle.kts", `implementation("g:a:${rootProject.extra["v"]}")`, "v"},
		{"kotlin extra index", "build.gradle.kts", "extra[\"v\"] = \"1.2.3\"\n", "build.gradle.kts", `implementation("g:a:${extra["v"] as String}")`, "v"},
		{"kotlin extra.set", "build.gradle.kts", "extra.set(\"v\", \"1.2.3\")\n", "build.gradle.kts", `implementation("g:a:${property("v")}")`, "v"},
		{"kotlin extra.apply", "build.gradle.kts", "extra.apply {\n  set(\"v\", \"1.2.3\")\n}\n", "build.gradle.kts", `implementation("g:a:${project.property("v")}")`, "v"},
		{"gradleProperty provider", "gradle.properties", "v=1.2.3\n", "build.gradle.kts", `implementation("g:a:${providers.gradleProperty("v").get()}")`, "v"},
		{"by project", "gradle.properties", "v=1.2.3\n", "build.gradle.kts", "val v: String by project\ndependencies { implementation(\"g:a:$v\") }", "v"},
		{"buildSrc const val", "buildSrc/src/main/kotlin/Versions.kt", "object Versions {\n  const val v = \"1.2.3\"\n}\n", "build.gradle.kts", `implementation("g:a:${Versions.v}")`, "Versions.v"},
		{"buildSrc nested object", "buildSrc/src/main/kotlin/Deps.kt", "object Deps {\n  object Versions { val v = \"1.2.3\" }\n}\n", "build.gradle.kts", `implementation("g:a:${Deps.Versions.v}")`, "Deps.Versions.v"},
		{"buildSrc short name", "buildSrc/src/main/kotlin/Deps.kt", "object Deps {\n  object Versions { val v = \"1.2.3\" }\n}\n", "build.gradle.kts", `implementation("g:a:${Versions.v}")`, "Deps.Versions.v"},
		{"buildSrc groovy class", "buildSrc/src/main/groovy/Versions.groovy", "class Versions {\n  static final String v = '1.2.3'\n}\n", "build.gradle", `implementation "g:a:${Versions.v}"`, "Versions.v"},
		{"map notation bare", "gradle.properties", "v=1.2.3\n", "build.gradle", `implementation group: 'g', name: 'a', version: v`, "v"},
		{"map notation kotlin bare", "gradle.properties", "v=1.2.3\n", "build.gradle.kts", `implementation(group = "g", name = "a", version = v)`, "v"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			files := map[string]string{tc.defFile: tc.def}
			files[tc.file] += "dependencies {\n  " + tc.use + "\n}\n"
			deps := runBuild(t, files, false)
			d := get(t, deps, tc.file, "g:a")
			if d.Skip != "" || d.Version != "1.2.3" || d.VersionRef != tc.ref || d.File != tc.defFile {
				t.Errorf("got %+v", linkedOf(d))
			}
		})
	}
}

func TestUnresolvableReferences(t *testing.T) {
	for _, tc := range []struct{ def, use, skip string }{
		{"def v = \"1.$x\"\n", `implementation "g:a:$v"`, "unresolved variable v"},
		{"def v = 'a\\tb'\n", `implementation "g:a:$v"`, "unresolved variable v"},
		{"ext { v = x }\n", `implementation "g:a:$v"`, "unresolved variable v"},
		{"v = '1'\n", `implementation "g:a:$v"`, "unresolved variable v"}, // not an ext block
		{"set('v', '1')\n", `implementation "g:a:$v"`, "unresolved variable v"},
		{"versions.v = '1'\n", `implementation "g:a:$versions.v"`, "unresolved variable versions.v"}, // undeclared map
		{"", `implementation "g:a:${v}-jre"`, "version variable"},
		{"", `implementation "g:a:${a}${b}"`, "version variable"},
		{"", `implementation "g:a:${v.substring(1)}"`, "version variable"},
		{"", `implementation "$g:a:1"`, "version variable"},
		{"", `implementation group: 'g', name: 'a', version: libs.versions.x.get()`, "version variable"},
		{"", `implementation group: 'g', name: 'a', version: "${x}.1"`, "version variable"},
	} {
		files := map[string]string{"build.gradle": tc.def + "dependencies {\n  " + tc.use + "\n}\n"}
		var d dep.Dep
		for _, x := range runBuild(t, files, false)["build.gradle"] {
			if strings.HasSuffix(x.Name, ":a") {
				d = x
			}
		}
		if d.Skip != tc.skip {
			t.Errorf("%q / %q: skip %q, want %q", tc.def, tc.use, d.Skip, tc.skip)
		}
	}
	// Kotlin `$a.b` is `${a}.b`.
	deps := runBuild(t, map[string]string{"build.gradle.kts": "val versions = \"x\"\ndependencies { implementation(\"g:a:$versions.v\") }\n"}, false)
	if d := get(t, deps, "build.gradle.kts", "g:a"); d.Skip != "version variable" {
		t.Errorf("kotlin dotted template: %q", d.Skip)
	}
}

func TestRefKey(t *testing.T) {
	for expr, want := range map[string]string{
		"x":                                   "x",
		"project.x":                           "x",
		"rootProject.ext.x":                   "x",
		"project.rootProject.x":               "x",
		`property("x")`:                       "x",
		`findProperty('x')`:                   "x",
		`project.property("x.y")`:             "x.y",
		`providers.gradleProperty("x").get()`: "x",
		`ext.get("x")`:                        "x",
		`extra["x"]`:                          "x",
		`rootProject.extra["x"] as String`:    "x",
		`properties['x']`:                     "x",
		`versions["kotlin"]`:                  "versions.kotlin",
		"Versions.kotlin":                     "Versions.kotlin",
		"kotlinVersion!!":                     "kotlinVersion",
		"a + b":                               "",
		`libs.versions.kotlin.get()`:          "",
		`if (x) "a" else "b"`:                 "",
		`project.findProperty("x") ?: "1"`:    "",
		`"literal"`:                           "",
		"1x":                                  "",
		"rootProject.extra.get(\"x\") as String?": "x",
	} {
		if got, _ := refKey(expr); got != want {
			t.Errorf("refKey(%q) = %q, want %q", expr, got, want)
		}
	}
	for _, tc := range []struct {
		s      string
		groovy bool
		key    string
		ref    bool
	}{
		{"1.0", false, "", false},
		{"$v", false, "v", true},
		{"$a.b", true, "a.b", true},
		{"$a.b", false, "", true},
		{"${a.b}", false, "a.b", true},
		{"${a}x", false, "", true},
		{"${a}${b}", false, "", true},
		{"${a", false, "", true},
		{"$", false, "", true},
		{"x$v", false, "", true},
	} {
		if key, ref := templateRef(tc.s, tc.groovy); key != tc.key || ref != tc.ref {
			t.Errorf("templateRef(%q, %v) = %q, %v", tc.s, tc.groovy, key, ref)
		}
	}
}

func TestProperties(t *testing.T) {
	src := "a=1\n" +
		"b: 2\n" +
		"c 3\n" +
		"  d  =  4  \n" +
		"# e=0\n" +
		"! f=0\n" +
		"# comment ending in a backslash \\\n" +
		"g=5\n" +
		"cont=1 \\\n" +
		"  next=2\n" +
		"esc=a\\tb\n" +
		"key\\:x=1\n" +
		"empty=\n" +
		"crlf=6\r\n" +
		"even=7\\\\\n" +
		"dup=8\ndup=9\n" +
		"last=10"
	got := map[string]string{}
	for k, v := range properties([]byte(src)) {
		got[k] = v.value
		if src[v.span.Start:v.span.End] != v.value {
			t.Errorf("%s: span %v", k, v.span)
		}
	}
	want := map[string]string{"a": "1", "b": "2", "c": "3", "d": "4", "g": "5", "crlf": "6", "dup": "8", "last": "10"}
	if !maps.Equal(got, want) {
		t.Errorf("got %v", got)
	}
	if d := properties([]byte(src))["g"]; d.line != 8 {
		t.Errorf("line of g = %d", d.line)
	}
}

func TestMapNotation(t *testing.T) {
	lib := func(name, version string, line int, skip string) want {
		return want{DepTypeDependencies, name, version, line, skip}
	}
	for _, tc := range []struct {
		name, file, src string
		want            []want
	}{
		{name: "groovy", file: "build.gradle", src: "dependencies {\n" +
			"  implementation group: 'g', name: 'a', version: '1.0'\n" +
			"  testImplementation(name: 'b', version: \"2.0\", group: 'g', classifier: 'tests', ext: 'jar')\n" +
			"  compile group: 'g',\n      name: 'c',\n      version: '3'\n" +
			"  runtimeOnly group: 'g', name: 'nov'\n" +
			"  implementation group: 'g', name: 'empty', version: ''\n" +
			"  implementation group: \"$grp\", name: 'var', version: '1'\n" +
			"  implementation group: 'g', name: 'ref', version: \"$v\"\n" +
			"  implementation group: 'g', name: 'bare', version: versions.x\n" +
			"  implementation group: 'g', name:\n" +
			"  exclude group: 'x', module: 'y'\n" +
			"}\n" +
			"buildscript { dependencies { classpath group: 'g', name: 'cp', version: '4' } }\n",
			want: []want{lib("g:a", "1.0", 2, ""), lib("g:b", "2.0", 3, ""), lib("g:c", "3", 4, ""), lib("$grp:var", "", 9, "version variable"),
				lib("g:ref", "", 10, "unresolved variable v"), lib("g:bare", "", 11, "unresolved variable versions.x"), lib("g:cp", "4", 15, "")}},
		{name: "kotlin", file: "build.gradle.kts", src: "dependencies {\n" +
			"  implementation(group = \"g\", name = \"a\", version = \"1.0\")\n" +
			"  api(group = \"g\", name = \"b\", version = Versions.b, classifier = \"x\")\n" +
			"  implementation(group = \"g\", name = \"c\", version = 5)\n" +
			"}\n",
			want: []want{lib("g:a", "1.0", 2, ""), lib("g:b", "", 3, "unresolved variable Versions.b")}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []want
			for _, d := range extract(t, tc.file, []byte(tc.src)) {
				got = append(got, summary(d))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestPluginVersionReferences(t *testing.T) {
	files := map[string]string{
		"gradle.properties": "kotlinVersion=2.0.0\nspotless=6.0\n",
		"build.gradle.kts": "val kotlinVersion: String by project\nval local = \"1.1\"\n" +
			"plugins {\n" +
			"  kotlin(\"jvm\") version kotlinVersion\n" +
			"  id(\"a\") version(kotlinVersion)\n" +
			"  id(\"b\").version(local)\n" +
			"  id(\"c\") version Versions.c apply false\n" +
			"  id(\"d\") version \"${property(\"spotless\")}\"\n" +
			"  id(\"e\") version libs.versions.e.get()\n" +
			"}\n",
		"buildSrc/src/main/kotlin/Versions.kt": "object Versions { const val c = \"3.0\" }\n",
	}
	deps := runBuild(t, files, false)
	for name, want := range map[string]linked{
		"org.jetbrains.kotlin.jvm": {"gradle.properties", "2.0.0", "kotlinVersion", "", 1},
		"a":                        {"gradle.properties", "2.0.0", "kotlinVersion", "", 1},
		"b":                        {"build.gradle.kts", "1.1", "local", "", 2},
		"c":                        {"buildSrc/src/main/kotlin/Versions.kt", "3.0", "Versions.c", "", 1},
		"d":                        {"gradle.properties", "6.0", "spotless", "", 2},
		"e":                        {"build.gradle.kts", "", "", "version variable", 9},
	} {
		if got := linkedOf(get(t, deps, "build.gradle.kts", name)); got != want {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
}

func TestRepositories(t *testing.T) {
	files := map[string]string{
		"settings.gradle.kts": "pluginManagement {\n" +
			"  repositories {\n    gradlePluginPortal()\n    maven(\"https://p.example/m2\")\n    if (x) { maven(\"https://cond.example\") }\n  }\n" +
			"}\n" +
			"dependencyResolutionManagement {\n  repositories {\n" +
			"    mavenCentral()\n" +
			"    maven { url = uri(\"https://lib.example/repo\"); name = \"corp\" }\n" +
			"    mavenLocal()\n" +
			"    maven { url \"file:///x\" }\n" +
			"    maven { url = uri(\"$base/x\") }\n" +
			"    exclusiveContent { forRepository { maven(\"https://excl.example\") } }\n" +
			"    mavenCentral { content { includeGroup(\"x\") } }\n" +
			"  }\n}\n" +
			"buildscript { repositories { maven(\"https://settings-cp.example\") } }\n" +
			"repositories { maven(\"https://ignored.example\") }\n",
		"build.gradle.kts": "buildscript {\n  repositories { google() }\n  dependencies { classpath(\"g:cp:1\") }\n}\n" +
			"plugins { id(\"p\") version \"1\" }\n" +
			"allprojects { repositories { maven(url = \"https://all.example\") } }\n" +
			"subprojects { repositories { maven { setUrl(\"https://sub.example/\") } } }\n" +
			"repositories { maven(\"https://root.example\") }\n" +
			"publishing { repositories { maven(\"https://publish.example\") } }\n" +
			"dependencies { implementation(\"g:root:1\") }\n",
		"app/build.gradle": "repositories {\n" +
			"  maven { url 'https://app.example' }\n" +
			"  maven { name 'named'; url uri('https://named.example') }\n" +
			"  maven { url = 'https://assign.example/' }\n" +
			"  jcenter()\n" +
			"}\n" +
			"dependencies { implementation 'g:app:1' }\n",
		"app/extra.gradle":              "dependencies { implementation 'g:applied:1' }\n",
		"lib/build.gradle.kts":          "dependencies { implementation(\"g:lib:1\") }\n",
		"gradle/libs.versions.toml":     "[libraries]\nc = \"g:cat:1\"\n[plugins]\ncp = \"cat.plugin:1\"\n",
		"buildSrc/src/main/kotlin/D.kt": "object D { const val x = \"g:bs:1.0\" }\n",
		"other/settings.gradle": "dependencyResolutionManagement {\n  repositoriesMode.set(RepositoriesMode.PREFER_SETTINGS)\n" +
			"  repositories { maven { url 'https://settings.example' } }\n}\n",
		"other/build.gradle":       "repositories { mavenCentral() }\ndependencies { implementation 'g:other:1' }\n",
		"bare/settings.gradle.kts": "",
		"bare/build.gradle.kts":    "plugins { id(\"bp\") version \"1\" }\ndependencies { implementation(\"g:bare:1\") }\n",
	}
	deps := runBuild(t, files, false)
	const (
		all  = "https://all.example/"
		sub  = "https://sub.example/"
		pm   = portalRepo + " https://p.example/m2/"
		drm  = centralRepo + " corp=https://lib.example/repo/"
		root = all + " https://root.example/"
		app  = " https://app.example/ named=https://named.example/ https://assign.example/ " + centralRepo
	)
	for _, tc := range []struct{ file, name, want string }{
		{"build.gradle.kts", "g:cp", googleRepo},
		{"build.gradle.kts", "p", pm},
		{"build.gradle.kts", "g:root", root},
		{"app/build.gradle", "g:app", all + " " + sub + app},
		{"app/extra.gradle", "g:applied", all + " " + sub + app},
		{"lib/build.gradle.kts", "g:lib", all + " " + sub},
		// Catalog and buildSrc deps: the repositories of every project, root project first.
		{"gradle/libs.versions.toml", "g:cat", root + " " + sub + app},
		{"gradle/libs.versions.toml", "cat.plugin", pm},
		{"buildSrc/src/main/kotlin/D.kt", "g:bs", root + " " + sub + app},
		{"other/build.gradle", "g:other", "https://settings.example/"},
		{"bare/build.gradle.kts", "bp", PluginRepositories},
		{"bare/build.gradle.kts", "g:bare", "central=https://repo.maven.apache.org/maven2/ google=https://dl.google.com/dl/android/maven2/ gradlePluginPortal=https://plugins.gradle.org/m2/"},
	} {
		if got := get(t, deps, tc.file, tc.name).Source; got != tc.want {
			t.Errorf("%s %s: Source %q, want %q", tc.file, tc.name, got, tc.want)
		}
	}
	// dependencyResolutionManagement without project repositories; google() serves only the
	// groups its content filter includes.
	deps = runBuild(t, map[string]string{
		"settings.gradle.kts":       string(fixture(t, "nowinandroid.settings.gradle.kts")),
		"gradle/libs.versions.toml": string(fixture(t, "nowinandroid.libs.versions.toml")),
	}, false)
	for name, want := range map[string]string{
		"junit:junit":                             centralRepo,
		"androidx.core:core-ktx":                  googleRepo + " " + centralRepo,
		"org.jetbrains.kotlin:kotlin-stdlib-jdk8": centralRepo,
		"com.android.application":                 googleRepo + " " + centralRepo + " " + portalRepo,
		"com.google.devtools.ksp":                 googleRepo + " " + centralRepo + " " + portalRepo,
		"org.jetbrains.kotlin.jvm":                centralRepo + " " + portalRepo,
	} {
		if got := get(t, deps, "gradle/libs.versions.toml", name).Source; got != want {
			t.Errorf("nowinandroid %s: Source %q, want %q", name, got, want)
		}
	}
}

func TestRepoEntry(t *testing.T) {
	for _, tc := range []struct{ name, url, want string }{
		{"", "https://x.example", "https://x.example/"},
		{"id", "http://x.example/", "id=http://x.example/"},
		{"bad id", "https://x.example", "https://x.example/"},
		{"", "file:///x", ""},
		{"", "https://$host/x", ""},
		{"", "", ""},
	} {
		if got := repoEntry(tc.name, tc.url); got != tc.want {
			t.Errorf("repoEntry(%q, %q) = %q", tc.name, tc.url, got)
		}
	}
	a := repoList{{entry: "a=https://x/"}, {entry: "https://y/"}}
	b := repoList{{entry: "b=https://y/"}, {entry: "https://z/"}, {entry: "https://x/"}, {entry: "https://x/", exclusive: true}}
	if got := joinRepos(a, b).render("g", "a"); got != "https://x/" {
		t.Errorf("joinRepos exclusive = %q", got)
	}
	if got := joinRepos(a, b[:3]).render("g", "a"); got != "a=https://x/ https://y/ https://z/" {
		t.Errorf("joinRepos = %q", got)
	}
}

func TestReadLocks(t *testing.T) {
	header := "# This is a Gradle generated file for dependency locking.\n# Manual edits can break the build and are not advised.\n"
	files := map[string]string{
		"settings.gradle": "",
		"build.gradle": "buildscript { dependencies { classpath 'g:cp:2.+' } }\n" +
			"dependencies {\n" +
			"  implementation 'g:a:1.+'\n" +
			"  implementation 'g:b:[1.0,2.0)'\n" +
			"  implementation 'g:c:1.+'\n" +
			"  implementation 'g:d:latest.release'\n" +
			"  implementation 'g:e:1.0'\n" +
			"}\n",
		"gradle.lockfile": header + "g:a:1.4.0=compileClasspath\ng:a:1.10.0=testCompileClasspath\ng:a:1.5.0=runtimeClasspath\n" +
			"g:b:1.2=compileClasspath\ng:cp:9.9=compileClasspath\nmalformed\n\nempty=annotationProcessor\n",
		"buildscript-gradle.lockfile":                                header + "g:cp:2.3.0=classpath\n",
		"app/build.gradle":                                           "dependencies { implementation 'g:legacy:3.+' }\n",
		"app/gradle/dependency-locks/compileClasspath.lockfile":      "g:legacy:3.1.0\n",
		"app/gradle/dependency-locks/buildscript-classpath.lockfile": "g:legacy:9.0.0\n",
		"app/gradle/dependency-locks/notes.txt":                      "g:legacy:8.0.0\n",
		"gradle/libs.versions.toml":                                  "[libraries]\nx = \"g:x:1.+\"\ny = \"g:y:2.+\"\n",
		"lib/build.gradle":                                           "",
		"lib/gradle.lockfile":                                        "g:x:1.9=compileClasspath\n",
	}
	deps := runBuild(t, files, true)
	for _, tc := range []struct {
		file, name, version, locked, skip string
	}{
		{"build.gradle", "g:cp", "2.3.0", "2.3.0", ""},
		{"build.gradle", "g:a", "1.10.0", "1.10.0", ""},
		{"build.gradle", "g:b", "1.2", "1.2", ""},
		{"build.gradle", "g:c", "1.+", "", "dynamic version 1.+"},
		{"build.gradle", "g:d", "latest.release", "", "dynamic version latest.release"},
		{"build.gradle", "g:e", "1.0", "", ""},
		{"app/build.gradle", "g:legacy", "3.1.0", "3.1.0", ""},
		{"gradle/libs.versions.toml", "g:x", "1.9", "1.9", ""},
		{"gradle/libs.versions.toml", "g:y", "2.+", "", "dynamic version 2.+"},
	} {
		d := get(t, deps, tc.file, tc.name)
		if d.Version != tc.version || d.Locked != tc.locked || d.Skip != tc.skip {
			t.Errorf("%s %s: Version %q Locked %q Skip %q", tc.file, tc.name, d.Version, d.Locked, d.Skip)
		}
	}
	if d := get(t, deps, "build.gradle", "g:d"); d.Range != ">=0.0.0" {
		t.Errorf("unlocked latest.release has Range %q", d.Range)
	}

	// Without Link, the lockfile of the dep's directory; unreadable lockfiles are errors.
	root := writeTree(t, map[string]string{
		"gradle.lockfile":                           "g:a:1.2.0=x\n",
		"bad/gradle.lockfile/.keep":                 "",
		"bad/gradle/dependency-locks/x.lockfile/.k": "",
		"bad/gradle/dependency-locks/y.lockfile/.k": "",
	})
	m := New()
	a, _ := m.Extract("build.gradle", []byte("dependencies { implementation 'g:a:1.+' }\n"), manager.Env{})
	b, _ := m.Extract("bad/build.gradle", []byte("dependencies { implementation 'g:b:1.+' }\n"), manager.Env{})
	errs := m.(manager.LockReader).ReadLocks(root, [][]dep.Dep{a, b})
	if a[0].Locked != "1.2.0" || len(errs) != 1 {
		t.Errorf("unlinked: %+v, errs %v", a[0], errs)
	}
	if errs := (&gradleManager{}).ReadLocks(root, nil); errs != nil {
		t.Errorf("empty: %v", errs)
	}
}

func TestTranslate(t *testing.T) {
	for _, tc := range []struct {
		v, rng, sub string
		ok          bool
	}{
		{"1.+", ">=1.0.0-0 <2.0.0-0", "1", true},
		{"1.2.+", ">=1.2.0-0 <1.3.0-0", "1.2", true},
		{"1.2.3.+", ">=1.2.3-0 <1.2.4-0", "1.2.3", true},
		{"+", ">=0.0.0-0", "", true},
		{"[1.0,2.0)", ">=1.0, <2.0", "2.0", true},
		{"[1.0, 2.0]", ">=1.0, <=2.0", "2.0", true},
		{"(1.0,2.0)", ">1.0, <2.0", "2.0", true},
		{"]1.0,2.0[", ">1.0, <2.0", "2.0", true},
		{"[1.0,)", ">=1.0", "", true},
		{"(,2.0]", "<=2.0", "2.0", true},
		{"1.2+", "", "", false},
		{"01.+", "", "", false},
		{"x.+", "", "", false},
		{"1.2.3.4.+", "", "", false},
		{"[,)", "", "", false},
		{"[1,2,3)", "", "", false},
		{"[1,2}", "", "", false},
		{"latest.release", ">=0.0.0", "latest.release", true},
		{"latest.integration", ">=0.0.0-0", "latest.integration", true},
		{"latest.milestone", "", "", false},
	} {
		rng, sub, ok := translate(tc.v)
		if rng != tc.rng || ok != tc.ok || ok && tc.v[sub.Start:sub.End] != tc.sub {
			t.Errorf("translate(%q) = %q, %v, %v", tc.v, rng, sub, ok)
		}
	}
	for v, want := range map[string]bool{"1.+": true, "[1,2)": true, "latest.integration": true, "1.0": false, "[1.0]": false, "": false} {
		if dynamic(v) != want {
			t.Errorf("dynamic(%q) != %v", v, want)
		}
	}
	var d dep.Dep
	setVersion(&d, "[a,b)", dep.Span{Start: 10, End: 15})
	if d.Range != "" || d.Skip != "dynamic version [a,b)" {
		t.Errorf("invalid range: %+v", d)
	}
}

func TestRangeEdits(t *testing.T) {
	for _, tc := range []struct{ v, to, want, err string }{
		{"1.+", "2.1.0", "2.+", ""},
		{"1.2.+", "1.3.4", "1.3.+", ""},
		{"1.+", "1.7", "1.+", ""},
		{"1.2.3.+", "1.2.4", "1.2.4.+", ""},
		{"+", "9.0", "+", ""},
		{"1.2.+", "2", "", "fewer than 2 components"},
		{"1.+", "v2", "", "cannot write v2"},
		{"[1.0,2.0)", "2.5.1", "[1.0,3.0)", ""},
		{"[1.0, 2.0)", "2.5.1", "[1.0, 3.0)", ""},
		{"[1.0,2.0]", "2.5", "[1.0,2.5]", ""},
		{"[1.2,1.3)", "1.4.0", "[1.2,1.5)", ""},
		{"[1.2,1.2.5)", "1.2.7", "[1.2,1.2.8)", ""},
		{"(,2.0)", "3.1", "(,4.0)", ""},
		{"]1.0,2.0[", "2.1", "]1.0,3.0[", ""},
		{"[1.0,2.0)", "1.5", "[1.0,2.0)", ""},
		{"[1.0,)", "5", "[1.0,)", ""},
		{"[1.0,2.0)", "2.0-jre", "[1.0,3.0)", ""},
		{"[1.0,2.0)", "abc", "", "cannot derive"},
		{"latest.release", "2.0", "", "only its lockfile"},
		{"latest.integration", "2.0-rc1", "", "only its lockfile"},
		{"[1.0,2.0)", "", "", "cannot derive"},
		{"[1.2,1.3)", "x.4", "", "cannot derive"},
	} {
		src := "dependencies { implementation 'g:a:" + tc.v + "' }\n"
		ds := extract(t, "build.gradle", []byte(src))
		d := ds[0]
		d.Version, d.Locked = "1.0.0", "1.0.0"
		edits, err := New().Edits(dep.Update{Dep: d, NewVersion: tc.to})
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("%s -> %s: err %v, want %q", tc.v, tc.to, err, tc.err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		out, err := patch.Apply([]byte(src), edits)
		if err != nil {
			t.Fatalf("%s -> %s: %v", tc.v, tc.to, err)
		}
		if want := "dependencies { implementation 'g:a:" + tc.want + "' }\n"; string(out) != want {
			t.Errorf("%s -> %s: got %q", tc.v, tc.to, out)
		}
	}
}

func TestEditsMovedDeps(t *testing.T) {
	files := map[string]string{
		"gradle.properties":             "# versions\nktor=2.1.3\n",
		"build.gradle.kts":              "dependencies {\n  implementation(\"io.ktor:a:$ktor\")\n  implementation(\"io.ktor:b:$ktor\")\n}\n",
		"buildSrc/src/main/kotlin/V.kt": "object V { const val k = \"1.0\" }\n",
		"app/build.gradle":              "dependencies { implementation \"g:k:${V.k}\" }\n",
	}
	deps := runBuild(t, files, false)
	a, b := get(t, deps, "build.gradle.kts", "io.ktor:a"), get(t, deps, "build.gradle.kts", "io.ktor:b")
	if a.File != "gradle.properties" || a.RefSpan != b.RefSpan {
		t.Fatalf("a %+v b %+v", a, b)
	}
	var edits []patch.Edit
	for _, d := range []dep.Dep{a, b} {
		e, err := New().Edits(dep.Update{Dep: d, NewVersion: "3.0.0"})
		if err != nil {
			t.Fatal(err)
		}
		edits = append(edits, e...)
	}
	out, err := patch.Apply([]byte(files["gradle.properties"]), edits)
	if err != nil || string(out) != "# versions\nktor=3.0.0\n" {
		t.Errorf("gradle.properties: %q, %v", out, err)
	}
	k := get(t, deps, "app/build.gradle", "g:k")
	e, _ := New().Edits(dep.Update{Dep: k, NewVersion: "1.1"})
	if out, err := patch.Apply([]byte(files[k.File]), e); err != nil || string(out) != "object V { const val k = \"1.1\" }\n" {
		t.Errorf("buildSrc: %q, %v", out, err)
	}
}

func TestCatalogVersionRef(t *testing.T) {
	ds := extract(t, "gradle/libs.versions.toml", []byte("[versions]\nk = \"1.0\"\nd = \"1.+\"\n[libraries]\na = { module = \"g:a\", version.ref = \"k\" }\nb = \"g:b:2.0\"\n"+
		"c = { module = \"g:c\", version.ref = \"d\" }\n"))
	if ds[0].VersionRef != "k" || ds[1].VersionRef != "" || ds[2].VersionRef != "d" || ds[2].Range == "" || ds[2].Skip != "dynamic version 1.+" {
		t.Errorf("got %+v", ds)
	}
}

func TestScanner(t *testing.T) {
	src := []byte("val a = \"x${b[\"k\"]}y\" // c\n" +
		"val t = \"${\"nested ${q}\"}\"\n" +
		"val u = \"${unterminated\n" +
		"val v = '''multi\nline'''\n" +
		"object O : Base() {\n  fun f(x: Int) {\n  }\n}\n" +
		"broken(\n{\n")
	s := scan(src, false)
	var got []string
	for _, l := range s.strs {
		got = append(got, s.text(l))
	}
	if want := []string{`x${b["k"]}y`, `${"nested ${q}"}`}; !slices.Equal(got, want) {
		t.Errorf("strings %q", got)
	}
	if strings.Contains(string(s.code), "multi") || strings.Contains(string(s.code), "unterminated") {
		t.Errorf("code %q", s.code)
	}
	var labels []string
	for _, b := range s.blocks {
		labels = append(labels, b.label)
	}
	if !slices.Equal(labels, []string{"O", "f", ""}) || !s.blocks[0].object || s.blocks[1].object || s.blocks[2].end != len(src) {
		t.Errorf("blocks %+v", s.blocks)
	}
	if s.enclosing(len(src)-1) != 2 || s.enclosing(0) != -1 {
		t.Errorf("enclosing")
	}
	if _, ok := s.litAfter(len(src)); ok {
		t.Errorf("litAfter past end")
	}
}

func TestBuildFixtures(t *testing.T) {
	type check struct {
		file, name string
		want       linked
		source     string // "" = not checked
	}
	for _, tc := range []struct {
		name   string
		files  map[string]string
		locks  bool
		checks []check
	}{
		{name: "ktor-get-started gradle.properties", files: loadDir(t, "ktor-get-started"), checks: []check{
			{"build.gradle.kts", "io.ktor:ktor-server-core-jvm", linked{"gradle.properties", "2.1.3", "ktor_version", "", 1},
				centralRepo + " https://maven.pkg.jetbrains.space/public/p/ktor/eap/"},
			{"build.gradle.kts", "org.jetbrains.kotlin:kotlin-test-junit", linked{"gradle.properties", "1.7.10", "kotlin_version", "", 2}, ""},
			{"build.gradle.kts", "ch.qos.logback:logback-classic", linked{"gradle.properties", "1.2.11", "logback_version", "", 3}, ""},
			{"build.gradle.kts", "io.ktor.plugin", linked{"build.gradle.kts", "2.1.3", "", "", 8}, PluginRepositories},
		}},
		{name: "kafka versions map", files: loadDir(t, "kafka"), checks: []check{
			{"gradle/dependencies.gradle", "com.fasterxml.jackson.core:jackson-databind", linked{"gradle/dependencies.gradle", "2.22.4", "versions.jackson", "", 63}, LibraryRepositories},
			{"gradle/dependencies.gradle", "io.swagger.core.v3:swagger-annotations", linked{"gradle.properties", "2.2.55", "swaggerVersion", "", 20}, ""},
			{"gradle/dependencies.gradle", "org.scala-lang:scala-library", linked{"gradle/dependencies.gradle", "", "", "unresolved variable versions.scala", 227}, ""},
			{"build.gradle", "io.swagger.core.v3.swagger-gradle-plugin", linked{"gradle.properties", "2.2.55", "swaggerVersion", "", 20}, PluginRepositories},
			{"build.gradle", "org.owasp.dependencycheck", linked{"build.gradle", "12.2.2", "", "", 31}, ""},
			{"settings.gradle", "com.gradle.develocity", linked{"settings.gradle", "3.19", "", "", 26}, ""},
		}},
		{name: "iosched buildSrc", files: loadDir(t, "iosched"), checks: []check{
			{"build.gradle.kts", "com.android.tools.build:gradle", linked{"buildSrc/src/main/java/Versions.kt", "7.2.0", "Versions.ANDROID_GRADLE_PLUGIN", "", 26},
				googleRepo + " " + centralRepo},
			{"build.gradle.kts", "org.jetbrains.kotlin:kotlin-gradle-plugin", linked{"buildSrc/src/main/java/Versions.kt", "1.6.10", "Versions.KOTLIN", "", 32}, ""},
			{"build.gradle.kts", "com.diffplug.gradle.spotless", linked{"build.gradle.kts", "3.27.1", "", "", 42}, PluginRepositories},
		}},
		{name: "nebula lockfile", files: loadDir(t, "nebula"), locks: true, checks: []check{
			{"build.gradle.kts", "com.netflix.nebula:nebula-archrules-gradle-plugin", linked{"build.gradle.kts", "1.5.0", "", "", 34}, ""},
			{"build.gradle.kts", "com.fasterxml.jackson:jackson-bom", linked{"build.gradle.kts", "2.14.3", "", "", 50}, ""},
			{"build.gradle.kts", "com.netflix.nebula:nebula-test", linked{"build.gradle.kts", "12.13.1", "", "", 51}, ""},
			{"build.gradle.kts", "com.netflix.nebula:nebula-release-plugin", linked{"build.gradle.kts", "21.1.3", "", "", 42}, ""},
			{"build.gradle.kts", "org.mock-server:mockserver-netty", linked{"build.gradle.kts", "5.15.0", "", "", 53}, ""},
			{"settings.gradle", "com.netflix.nebula.oss.settings", linked{"settings.gradle", "25.+", "", "dynamic version 25.+", 3}, ""},
		}},
		{name: "markwon ext", files: map[string]string{"build.gradle": string(fixture(t, "markwon.build.gradle"))}, checks: []check{
			{"build.gradle", "org.jetbrains.kotlin:kotlin-gradle-plugin", linked{"build.gradle", "1.4.10", "kotlin_version", "", 2}, googleRepo + " " + centralRepo},
			{"build.gradle", "com.atlassian.commonmark:commonmark", linked{"build.gradle", "0.13.0", "commonMarkVersion", "", 59}, googleRepo + " " + centralRepo},
			{"build.gradle", "androidx.core:core", linked{"build.gradle", "1.0.2", "", "", 71}, ""},
			{"build.gradle", "junit:junit", linked{"build.gradle", "4.12", "", "", 103}, ""},
		}},
		{name: "mentorship buildSrc Dependencies.kt", files: map[string]string{"buildSrc/src/main/kotlin/Dependencies.kt": string(fixture(t, "mentorship.Dependencies.kt"))}, checks: []check{
			{"buildSrc/src/main/kotlin/Dependencies.kt", "com.squareup.retrofit2:retrofit", linked{"buildSrc/src/main/kotlin/Dependencies.kt", "2.9.0", "Versions.retrofitVersion", "", 22}, LibraryRepositories},
			{"buildSrc/src/main/kotlin/Dependencies.kt", "de.hdodenhof:circleimageview", linked{"buildSrc/src/main/kotlin/Dependencies.kt", "3.0.1", "Versions.circleImageView", "", 29}, ""},
		}},
		{name: "savvagen map notation", files: map[string]string{"build.gradle": string(fixture(t, "savvagen.build.gradle"))}, checks: []check{
			{"build.gradle", "com.fasterxml.jackson.core:jackson-databind", linked{"build.gradle", "2.11.0", "", "", 27}, centralRepo},
			{"build.gradle", "org.slf4j:slf4j-simple", linked{"build.gradle", "1.8.0-beta4", "", "", 44}, ""},
			{"build.gradle", "com.github.javafaker:javafaker", linked{"build.gradle", "1.0.2", "", "", 34}, ""},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps := runBuild(t, tc.files, tc.locks)
			for _, c := range tc.checks {
				d := get(t, deps, c.file, c.name)
				if got := linkedOf(d); got != c.want {
					t.Errorf("%s: got %+v, want %+v", c.name, got, c.want)
				}
				if c.source != "" && d.Source != c.source {
					t.Errorf("%s: Source %q, want %q", c.name, d.Source, c.source)
				}
			}
		})
	}
}

func TestFixtureCounts(t *testing.T) {
	count := func(deps map[string][]dep.Dep, file string, pred func(dep.Dep) bool) int {
		n := 0
		for _, d := range deps[file] {
			if pred(d) {
				n++
			}
		}
		return n
	}
	kafka := runBuild(t, loadDir(t, "kafka"), false)
	jackson := count(kafka, "gradle/dependencies.gradle", func(d dep.Dep) bool { return d.VersionRef == "versions.jackson" })
	resolved := count(kafka, "gradle/dependencies.gradle", func(d dep.Dep) bool { return d.Skip == "" })
	if jackson != 6 || resolved != 96 || len(kafka["gradle/dependencies.gradle"]) != 98 {
		t.Errorf("kafka: %d jackson, %d resolved of %d", jackson, resolved, len(kafka["gradle/dependencies.gradle"]))
	}
	markwon := runBuild(t, map[string]string{"build.gradle": string(fixture(t, "markwon.build.gradle"))}, false)
	if n := len(markwon["build.gradle"]); n != 34 {
		t.Errorf("markwon: %d deps", n)
	}
	m := runBuild(t, map[string]string{"buildSrc/src/main/kotlin/Dependencies.kt": string(fixture(t, "mentorship.Dependencies.kt"))}, false)
	if n := count(m, "buildSrc/src/main/kotlin/Dependencies.kt", func(d dep.Dep) bool { return d.Skip == "" }); n != 24 {
		t.Errorf("mentorship: %d resolved", n)
	}
	s := runBuild(t, map[string]string{"build.gradle": string(fixture(t, "savvagen.build.gradle"))}, false)
	if n := count(s, "build.gradle", func(d dep.Dep) bool { return d.DepType == DepTypeDependencies }); n != 18 {
		t.Errorf("savvagen: %d libraries", n)
	}
	nebula := runBuild(t, loadDir(t, "nebula"), true)
	locked := count(nebula, "build.gradle.kts", func(d dep.Dep) bool { return d.Locked != "" })
	if locked != 15 { // 5 prefix versions and 10 latest.release
		t.Errorf("nebula: %d locked", locked)
	}
	d := get(t, nebula, "build.gradle.kts", "com.fasterxml.jackson:jackson-bom")
	e, err := New().Edits(dep.Update{Dep: d, NewVersion: "2.17.1"})
	src := loadDir(t, "nebula")["build.gradle.kts"]
	if out, aerr := patch.Apply([]byte(src), e); err != nil || aerr != nil || !strings.Contains(string(out), `platform("com.fasterxml.jackson:jackson-bom:2.17.+")`) {
		t.Errorf("jackson-bom bump: %v %v", err, aerr)
	}
}

func TestRepositoryContentFilters(t *testing.T) {
	files := map[string]string{
		"build.gradle.kts": "repositories {\n" +
			"  google { content { includeGroupByRegex(\"com\\\\.android.*\"); includeGroupAndSubgroups(\"androidx\"); includeGroupByRegex(\"(\") } }\n" +
			"  maven(\"https://corp.example\") { content { includeModule(\"corp\", \"lib\"); excludeGroup(\"corp.internal\") } }\n" +
			"  maven { url = uri(\"https://snap.example\"); mavenContent { snapshotsOnly() } }\n" +
			"  maven { url = uri(\"https://ex.example\"); content { excludeGroupByRegex(\"org\\\\..*\"); excludeVersion(\"g\", \"n\", \"1\"); includeModule(\"short\") } }\n" +
			"  mavenCentral()\n" +
			"  exclusiveContent {\n    forRepository { maven(\"https://excl.example\") }\n    filter { includeGroup(\"excl\"); includeModuleByRegex(\"re\", \"mod.*\"); includeVersion(\"ver\", \"v\", \"1\") }\n  }\n" +
			"  exclusiveContent { forRepository { maven(\"https://nofilter.example\") } }\n" +
			"}\n" +
			"dependencies {\n" +
			"  implementation(\"com.android.tools:x:1\")\n  implementation(\"androidx.core:c:1\")\n  implementation(\"androidx:a:1\")\n" +
			"  implementation(\"corp:lib:1\")\n  implementation(\"corp:other:1\")\n  implementation(\"excl:x:1\")\n  implementation(\"re:module:1\")\n" +
			"  implementation(\"ver:v:1\")\n  implementation(\"org.x:y:1\")\n" +
			"}\n",
		"app/build.gradle": "repositories {\n  maven { url 'https://g.example'; content { includeGroup 'gg' } }\n  mavenCentral()\n}\n" +
			"dependencies { implementation 'gg:a:1'; implementation 'other:a:1' }\n",
		"only/build.gradle": "repositories { google { content { includeGroup 'x' } } }\ndependencies { implementation 'y:a:1' }\n",
	}
	deps := runBuild(t, files, false)
	const ex, central = "https://ex.example/", centralRepo
	for _, tc := range []struct{ file, name, want string }{
		{"build.gradle.kts", "com.android.tools:x", googleRepo + " " + ex + " " + central},
		{"build.gradle.kts", "androidx.core:c", googleRepo + " " + ex + " " + central},
		{"build.gradle.kts", "androidx:a", googleRepo + " " + ex + " " + central},
		{"build.gradle.kts", "corp:lib", "https://corp.example/ " + ex + " " + central},
		{"build.gradle.kts", "corp:other", ex + " " + central},
		{"build.gradle.kts", "excl:x", "https://excl.example/"},
		{"build.gradle.kts", "re:module", "https://excl.example/"},
		{"build.gradle.kts", "ver:v", "https://excl.example/"},
		{"build.gradle.kts", "org.x:y", central},
		{"app/build.gradle", "gg:a", "https://g.example/ " + central},
		{"app/build.gradle", "other:a", central},
		{"only/build.gradle", "y:a", LibraryRepositories}, // every declared repository excludes it
	} {
		if got := get(t, deps, tc.file, tc.name).Source; got != tc.want {
			t.Errorf("%s: Source %q, want %q", tc.name, got, tc.want)
		}
	}
	for _, tc := range []struct {
		kind string
		args []string
		ok   bool
	}{
		{"Group", nil, false}, {"GroupByRegex", nil, false}, {"Module", []string{"g"}, false},
		{"ModuleByRegex", []string{"g"}, false}, {"ModuleByRegex", []string{"(", "x"}, false}, {"VersionByRegex", []string{"g", "n", "v"}, true},
	} {
		if _, ok := moduleRule(tc.kind, tc.args); ok != tc.ok {
			t.Errorf("moduleRule(%s, %q) ok = %v", tc.kind, tc.args, ok)
		}
	}
}

// The bisq catalog's jitpack deps are used by projects applying the bisq.java-conventions
// precompiled script plugin, which declares jitpack; no build script or settings file does.
// build-logic imports the catalog with from(files(…)): its plugin libraries come from the
// plugin portal its commons project declares.
func TestConventionPluginRepositories(t *testing.T) {
	deps := runBuild(t, loadDir(t, "bisq"), false)
	const jitpack = centralRepo + " https://jitpack.io/ " + portalRepo
	for _, name := range []string{"com.github.bisq-network:bitcoinj", "com.github.bisq-network:jsonrpc4j", "com.google.guava:guava", "org.gradlex:reproducible-builds"} {
		if got := get(t, deps, "gradle/libs.versions.toml", name).Source; got != jitpack {
			t.Errorf("%s: Source %q, want %q", name, got, jitpack)
		}
	}
	deps = runBuild(t, map[string]string{
		"settings.gradle.kts": "includeBuild(\"build-logic\")\n",
		"build-logic/settings.gradle.kts": "dependencyResolutionManagement {\n  versionCatalogs {\n" +
			"    create(\"libs\") { from(files(\"../gradle/libs.versions.toml\")) }\n" +
			"    create(\"abs\") { from(files(\"/abs.toml\")) }\n    create(\"out\") { from(files(\"../../out.toml\")) }\n" +
			"  }\n}\nfrom(files(\"elsewhere.toml\"))\n",
		"build-logic/build.gradle.kts": "repositories { gradlePluginPortal() }\n",
		"build-logic/src/main/kotlin/com/acme/base.gradle.kts": "package com.acme\n" +
			"repositories { maven(\"https://base.example\") }\n",
		"build-logic/src/main/kotlin/lib.gradle.kts":     "plugins { id(\"com.acme.base\") }\nrepositories { maven(\"https://lib.example\") }\n",
		"build-logic/src/main/groovy/groovy-conv.gradle": "repositories { maven { url 'https://groovy.example' } }\n",
		"app/build.gradle.kts":                           "plugins { `lib` }\ndependencies { implementation(\"g:app:1\") }\n",
		"app/extra.gradle.kts":                           "dependencies { implementation(\"g:extra:1\") }\n",
		"tool/build.gradle":                              "apply plugin: 'groovy-conv'\nrepositories { mavenCentral() }\ndependencies { implementation 'g:tool:1' }\n",
		"other/build.gradle.kts":                         "plugins { id(\"unknown\") }\nid(\"not-a-plugin\")\ndependencies { implementation(\"g:other:1\") }\n",
		"gradle/libs.versions.toml":                      "[libraries]\nc = \"g:cat:1\"\n",
	}, false)
	const conv = "https://base.example/ https://lib.example/"
	for _, tc := range []struct{ file, name, want string }{
		{"app/build.gradle.kts", "g:app", conv},
		{"app/extra.gradle.kts", "g:extra", conv},
		{"tool/build.gradle", "g:tool", "https://groovy.example/ " + centralRepo},
		{"other/build.gradle.kts", "g:other", LibraryRepositories},
		{"gradle/libs.versions.toml", "g:cat", conv + " https://groovy.example/ " + centralRepo + " " + portalRepo},
	} {
		if got := get(t, deps, tc.file, tc.name).Source; got != tc.want {
			t.Errorf("%s %s: Source %q, want %q", tc.file, tc.name, got, tc.want)
		}
	}
	// The root project exists without a build script: it uses the settings repositories.
	deps = runBuild(t, map[string]string{
		"settings.gradle.kts":       "dependencyResolutionManagement { repositories { mavenCentral() } }\n",
		"sub/build.gradle":          "repositories { maven { url 'https://jitpack.io' } }\n",
		"gradle/libs.versions.toml": "[libraries]\nc = \"g:cat:1\"\n",
	}, false)
	if got := get(t, deps, "gradle/libs.versions.toml", "g:cat").Source; got != centralRepo+" https://jitpack.io/" {
		t.Errorf("root without build script: Source %q", got)
	}
}

// exclusiveContent naming its repositories by reference: a Kotlin function reference, a
// variable bound to a repository and a repository call as forRepositories arguments.
func TestExclusiveContentByReference(t *testing.T) {
	deps := runBuild(t, map[string]string{
		"build.gradle.kts": "repositories {\n" +
			"  val corp = maven(\"https://corp.example\")\n" +
			"  mavenCentral()\n" +
			"  exclusiveContent {\n    forRepositories(corp, gradlePluginPortal())\n    filter { includeGroup(\"corp\") }\n  }\n" +
			"  exclusiveContent {\n    forRepository(::google)\n    filter { includeGroupByRegex(\"androidx.*\") }\n  }\n" +
			"  exclusiveContent {\n    forRepositories(unknown, listOf(1), 1)\n    filter { includeGroup(\"u\") }\n  }\n" +
			"}\n" +
			"dependencies {\n  implementation(\"corp:a:1\")\n  implementation(\"androidx.core:core:1\")\n  implementation(\"org:x:1\")\n  implementation(\"u:x:1\")\n}\n",
		"app/build.gradle": "def jit = repositories.maven { url 'https://jitpack.io' }\n" +
			"repositories {\n  mavenCentral()\n  exclusiveContent {\n    forRepositories(jit)\n    filter { includeGroup 'com.github.x' }\n  }\n}\n" +
			"dependencies { implementation 'com.github.x:y:1'; implementation 'org:z:1' }\n",
	}, false)
	for _, tc := range []struct{ file, name, want string }{
		{"build.gradle.kts", "corp:a", "https://corp.example/ " + portalRepo},
		{"build.gradle.kts", "androidx.core:core", googleRepo},
		{"build.gradle.kts", "org:x", centralRepo}, // corp serves only its exclusive group
		{"build.gradle.kts", "u:x", centralRepo},
		{"app/build.gradle", "com.github.x:y", "https://jitpack.io/"},
		{"app/build.gradle", "org:z", centralRepo},
	} {
		if got := get(t, deps, tc.file, tc.name).Source; got != tc.want {
			t.Errorf("%s %s: Source %q, want %q", tc.file, tc.name, got, tc.want)
		}
	}
}

// Settings catalog fixtures (fetched 2026-11-04):
//   - testdata/sonar-kotlin.settings.gradle.kts, testdata/sonar-kotlin.gradle.properties:
//     https://raw.githubusercontent.com/SonarSource/sonar-kotlin/master/{settings.gradle.kts,gradle.properties}
//   - the Groovy catalog below is the settings.gradle sample of
//     https://docs.gradle.org/current/userguide/version_catalogs.html, plus edge cases.
func TestSettingsCatalogs(t *testing.T) {
	deps := runBuild(t, map[string]string{
		"settings.gradle.kts": string(fixture(t, "sonar-kotlin.settings.gradle.kts")),
		"gradle.properties":   string(fixture(t, "sonar-kotlin.gradle.properties")),
	}, false)
	const f = "settings.gradle.kts"
	for name, want := range map[string]linked{
		"com.google.code.gson:gson":                                   {f, "2.11.0", "gson", "", 44},
		"com.google.code.findbugs:jsr305":                             {f, "3.0.2", "", "", 45},
		"org.jetbrains.kotlin:kotlin-compiler":                        {"gradle.properties", "2.4.21", "kotlinVersion", "", 9},
		"org.sonarsource.analyzer-commons:sonar-analyzer-recognizers": {f, "2.35.0.15509", "analyzerCommonsVersionStr", "", 35},
		"org.sonarsource.api.plugin:sonar-plugin-api":                 {f, "13.4.2.4284", "sonarPluginApi", "", 36},
		"io.gitlab.arturbosch.detekt:detekt-cli":                      {f, "1.23.3", "detekt", "", 66},
		"org.jetbrains.kotlin.jvm":                                    {"gradle.properties", "2.4.21", "kotlinVersion", "", 9},
		"com.gradle.develocity":                                       {f, "4.6.1", "", "", 113},
	} {
		if got := linkedOf(get(t, deps, f, name)); got != want {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
	libs := 0
	for _, d := range deps[f] {
		if d.DepType == DepTypeDependencies {
			libs++
		}
	}
	if libs != 33 {
		t.Errorf("%d libraries", libs)
	}

	groovy := "dependencyResolutionManagement {\n" +
		"    versionCatalogs {\n" +
		"        libs {\n" +
		"            version('groovy', '3.0.5')\n" +
		"            version('checkstyle', '8.37')\n" +
		"            library('groovy-core', 'org.codehaus.groovy', 'groovy').versionRef('groovy')\n" +
		"            library('groovy-json', 'org.codehaus.groovy', 'groovy-json').versionRef('groovy')\n" +
		"            library('commons-lang3', 'org.apache.commons', 'commons-lang3').version {\n" +
		"                strictly '[3.8, 4.0['\n" +
		"                prefer '3.9'\n" +
		"            }\n" +
		"            bundle('groovy', ['groovy-core', 'groovy-json'])\n" +
		"            plugin('versions', 'com.github.ben-manes.versions').version('0.45.0')\n" +
		"            library('short', 'g:short:1.0')\n" +
		"            library('nov', 'g:nov')\n" +
		"            library('managed', 'g', 'managed').withoutVersion()\n" +
		"            library('noversion', 'g', 'noversion')\n" +
		"            library('missing', 'g', 'missing').versionRef('nope')\n" +
		"            version('rich') { strictly '1.0' }\n" +
		"            library('richref', 'g', 'richref').versionRef('rich')\n" +
		"            version('dyn', '1.+')\n" +
		"            library('dyn', 'g', 'dyn').versionRef('dyn')\n" +
		"            plugin('pref', 'p.ref').versionRef('groovy')\n" +
		"            plugin('bad', 'p.bad', 'x').version('1')\n" +
		"            library('empty', 'g', 'empty').version('')\n" +
		"            library('broken', 'g'\n" +
		"        }\n" +
		"    }\n" +
		"}\n"
	deps = runBuild(t, map[string]string{"settings.gradle": groovy}, false)
	var got []want
	for _, d := range deps["settings.gradle"] {
		got = append(got, summary(d))
	}
	if want := []want{
		{DepTypeDependencies, "org.codehaus.groovy:groovy", "3.0.5", 6, ""},
		{DepTypeDependencies, "org.codehaus.groovy:groovy-json", "3.0.5", 7, ""},
		{DepTypeDependencies, "org.apache.commons:commons-lang3", "", 8, "rich version"},
		{DepTypePlugin, "com.github.ben-manes.versions", "0.45.0", 13, ""},
		{DepTypeDependencies, "g:short", "1.0", 14, ""},
		{DepTypeDependencies, "g:missing", "", 18, "unresolved version.ref nope"},
		{DepTypeDependencies, "g:richref", "1.0", 20, ""},
		{DepTypeDependencies, "g:dyn", "1.+", 22, "dynamic version 1.+"},
		{DepTypePlugin, "p.ref", "3.0.5", 23, ""},
	}; !slices.Equal(got, want) {
		t.Errorf("groovy catalog:\n got  %+v\n want %+v", got, want)
	}
	if a, b := get(t, deps, "settings.gradle", "org.codehaus.groovy:groovy"), get(t, deps, "settings.gradle", "p.ref"); a.RefSpan != b.RefSpan || a.VersionRef != "groovy" {
		t.Errorf("shared version: %+v %+v", a.RefSpan, b.RefSpan)
	}
}

func TestKotlinMaps(t *testing.T) {
	src := "val versions = mapOf(\"kotlin\" to \"1.9.0\", \"expr\" to x, \"nested\" to mapOf(\"x\" to \"2\"))\n" +
		"val more: Map<String, String> = mutableMapOf<String, String>(\n  \"a\" to \"3\", // c\n)\n" +
		"val libs = mapOf(\"okhttp\" to \"com.squareup.okhttp3:okhttp:${versions[\"kotlin\"]}\")\n" +
		"val bad = mapOf(x to \"1\")\nval list = mapOf(\"k\" too \"1\")\n" +
		"dependencies {\n" +
		"  implementation(\"g:a:${versions[\"kotlin\"]}\")\n" +
		"  implementation(\"g:b:${more.getValue(\"a\")}\")\n" +
		"  implementation(group = \"g\", name = \"c\", version = versions[\"kotlin\"]!!)\n" +
		"  implementation(\"g:d:${versions[\"expr\"]}\")\n" +
		"}\n"
	deps := runBuild(t, map[string]string{"build.gradle.kts": src}, false)
	for name, want := range map[string]linked{
		"g:a":                         {"build.gradle.kts", "1.9.0", "versions.kotlin", "", 1},
		"g:b":                         {"build.gradle.kts", "3", "more.a", "", 3},
		"g:c":                         {"build.gradle.kts", "1.9.0", "versions.kotlin", "", 1},
		"g:d":                         {"build.gradle.kts", "", "", "unresolved variable versions.expr", 12},
		"com.squareup.okhttp3:okhttp": {"build.gradle.kts", "1.9.0", "versions.kotlin", "", 1},
	} {
		if got := linkedOf(get(t, deps, "build.gradle.kts", name)); got != want {
			t.Errorf("%s: got %+v, want %+v", name, got, want)
		}
	}
	s := scan([]byte(src), false)
	keys := map[string]bool{}
	for _, a := range s.assignments() {
		keys[a.keys[0]] = true
	}
	if !keys["versions.nested.x"] || keys["versions.expr"] || keys["list.k"] {
		t.Errorf("keys %v", keys)
	}
}

// testdata/bitwarden.buildscript-gradle.lockfile: https://raw.githubusercontent.com/bitwarden/android/main/buildscript-gradle.lockfile
// (fetched 2026-11-04); it locks the plugin markers `<id>:<id>.gradle.plugin:<version>=classpath`.
func TestPluginLocks(t *testing.T) {
	files := map[string]string{
		"settings.gradle.kts":      "plugins { id(\"org.gradle.toolchains.foojay-resolver-convention\") version \"1.+\" }\n",
		"settings-gradle.lockfile": "org.gradle.toolchains.foojay-resolver-convention:org.gradle.toolchains.foojay-resolver-convention.gradle.plugin:1.0.0=classpath\n",
		"build.gradle.kts": "plugins {\n  id(\"com.android.application\") version \"9.+\" apply false\n  id(\"com.google.devtools.ksp\") version \"2.3.+\" apply false\n" +
			"  id(\"not.locked\") version \"1.+\"\n}\n",
		"buildscript-gradle.lockfile": string(fixture(t, "bitwarden.buildscript-gradle.lockfile")),
		"gradle/libs.versions.toml":   "[plugins]\nlib = { id = \"com.android.library\", version = \"9.4.+\" }\n",
	}
	deps := runBuild(t, files, true)
	for _, tc := range []struct{ file, name, version, skip string }{
		{"settings.gradle.kts", "org.gradle.toolchains.foojay-resolver-convention", "1.0.0", ""},
		{"build.gradle.kts", "com.android.application", "9.4.1", ""},
		{"build.gradle.kts", "com.google.devtools.ksp", "2.3.12", ""},
		{"build.gradle.kts", "not.locked", "1.+", "dynamic version 1.+"},
		{"gradle/libs.versions.toml", "com.android.library", "9.4.1", ""},
	} {
		if d := get(t, deps, tc.file, tc.name); d.Version != tc.version || d.Skip != tc.skip || tc.skip == "" && d.Locked != tc.version {
			t.Errorf("%s: Version %q Locked %q Skip %q", tc.name, d.Version, d.Locked, d.Skip)
		}
	}
	// Without Link the dep's kind comes from its file.
	m := New()
	ps, _ := m.Extract("settings.gradle.kts", []byte(files["settings.gradle.kts"]), manager.Env{})
	root := writeTree(t, files)
	if errs := (&gradleManager{files: map[string]*fileState{}}).ReadLocks(root, [][]dep.Dep{ps}); errs != nil || ps[0].Locked != "1.0.0" {
		t.Errorf("unlinked settings plugin: %+v %v", ps[0], errs)
	}
}

// TestLinkedRename: Rename chooses the repositories of the new name as Link chose them for
// the old one (content filters, exclusiveContent, pluginManagement); a dep resolved to a
// variable defined in another file is not renamable (its edits apply to that file).
func TestLinkedRename(t *testing.T) {
	files := map[string]string{
		"settings.gradle.kts": "pluginManagement { repositories { maven(\"https://plugins.example\") { content { includeGroup(\"x.y\") } }; gradlePluginPortal() } }\n",
		"gradle.properties":   "v=1.0\n",
		"build.gradle.kts": "plugins { id(\"x.y\") version \"1.0\" }\n" +
			"repositories {\n  maven(\"https://corp.example\") { content { includeGroup(\"corp\") } }\n  mavenCentral()\n" +
			"  exclusiveContent {\n    forRepository { maven(\"https://excl.example\") }\n    filter { includeGroup(\"excl\") }\n  }\n}\n" +
			"val w = \"2.0\"\n" +
			"dependencies {\n  implementation(\"corp:lib:1\")\n  implementation(\"g:a:$w\")\n  implementation(\"g:b:$v\")\n}\n",
	}
	m := New()
	var all [][]dep.Dep
	for _, p := range slices.Sorted(maps.Keys(files)) {
		ds, err := m.Extract(p, []byte(files[p]), manager.Env{})
		if err != nil {
			t.Fatal(err)
		}
		all = append(all, ds)
	}
	m.(manager.Linker).Link(all)
	deps := map[string][]dep.Dep{"build.gradle.kts": all[0]}
	r := m.(manager.Renamer)
	for _, tc := range []struct{ dep, name, source string }{
		{"corp:lib", "excl:lib", "https://excl.example/"},
		{"corp:lib", "corp:other", "https://corp.example/ " + centralRepo},
		{"g:a", "h:a", centralRepo},
		{"x.y", "z.w", portalRepo},
	} {
		d := get(t, deps, "build.gradle.kts", tc.dep)
		got, err := r.Rename(d, tc.name)
		if err != nil || got.Name != tc.name || got.Source != tc.source {
			t.Errorf("Rename(%s, %s) = %s from %q, %v; want source %q", tc.dep, tc.name, got.Name, got.Source, err, tc.source)
		}
	}
	if d := get(t, deps, "build.gradle.kts", "g:a"); !d.Renamable() || d.File != "build.gradle.kts" {
		t.Errorf("same-file variable: %+v", d)
	} else if e, err := m.Edits(dep.Update{Dep: d, NewVersion: "2.1", NewName: "h:b"}); err != nil {
		t.Error(err)
	} else if out, err := patch.Apply([]byte(files[d.File]), e); err != nil || !strings.Contains(string(out), "val w = \"2.1\"\n") || !strings.Contains(string(out), `implementation("h:b:$w")`) {
		t.Errorf("same-file variable edits: %s %v", out, err)
	}
	if d := get(t, deps, "build.gradle.kts", "g:b"); d.Renamable() || d.File != "gradle.properties" {
		t.Errorf("gradle.properties variable: %+v", d)
	}
}

// A catalog imported in settings with from(files(…)) from a path the catalog patterns do not
// match is extracted with the settings file, once, and linked as a catalog of the importing
// builds.
func TestImportedCatalogs(t *testing.T) {
	files := map[string]string{
		"settings.gradle.kts": "dependencyResolutionManagement {\n  repositories { mavenCentral() }\n  versionCatalogs {\n" +
			"    create(\"libs\") { from(files(\"deps.toml\")) }\n" +
			"    create(\"tools\") { from(files(\"gradle/tools.versions.toml\")) }\n" +
			"    create(\"missing\") { from(files(\"missing.toml\")) }\n  }\n}\n",
		"deps.toml":                  "[versions]\nx = \"1.0\"\n[libraries]\na = { module = \"g:a\", version.ref = \"x\" }\n[plugins]\np = { id = \"p.id\", version = \"2.0\" }\n",
		"gradle/tools.versions.toml": "[libraries]\nt = \"g:t:3.0\"\n",
		"sub/settings.gradle":        "dependencyResolutionManagement { versionCatalogs { libs { from(files('../deps.toml')) } } }\n",
		"sub/build.gradle":           "repositories { maven { url 'https://sub.example' } }\n",
	}
	deps := runBuild(t, files, false)
	a := get(t, deps, "settings.gradle.kts", "g:a")
	if a.File != "deps.toml" || a.Version != "1.0" || a.VersionRef != "x" || a.Source != centralRepo+" https://sub.example/" || !a.Renamable() {
		t.Errorf("g:a: %+v", a)
	}
	if p := get(t, deps, "settings.gradle.kts", "p.id"); p.File != "deps.toml" || p.Version != "2.0" || p.Source != PluginRepositories {
		t.Errorf("p.id: %+v", p)
	}
	n := 0
	for _, ds := range deps {
		for _, d := range ds {
			if d.Name == "g:a" || d.Name == "g:t" {
				n++
			}
		}
	}
	if n != 2 || len(deps["sub/settings.gradle"]) != 0 {
		t.Errorf("imported catalogs extracted %d times, sub/settings.gradle: %+v", n, deps["sub/settings.gradle"])
	}
	_, err := New().Extract("settings.gradle", []byte("dependencyResolutionManagement { versionCatalogs { libs { from(files('bad.toml')) } } }\n"),
		manager.Env{ReadFile: func(string) ([]byte, error) { return []byte("[libraries\n"), nil }})
	if err == nil || !strings.Contains(err.Error(), "catalog bad.toml imported by settings.gradle") {
		t.Errorf("invalid imported catalog: %v", err)
	}
}
