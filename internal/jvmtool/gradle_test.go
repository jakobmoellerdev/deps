package jvmtool

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
)

// Fixtures (fetched 2026-11-04):
//   - testdata/solr-gradle.lockfile: https://raw.githubusercontent.com/apache/solr/main/gradle.lockfile
//   - testdata/gradle-verification-metadata.xml: https://raw.githubusercontent.com/gradle/gradle/master/gradle/verification-metadata.xml,
//     trimmed to the configuration without trusted keys and the first four components.
func fixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestGradleScope(t *testing.T) {
	lock := fixture(t, "solr-gradle.lockfile")
	for _, tc := range []struct {
		name     string
		files    map[string]string
		manifest string
		dir      string
		ok       bool
	}{
		{"subproject lockfile", map[string]string{"settings.gradle": "", "app/build.gradle": "", "app/gradle.lockfile": lock}, "app/build.gradle", ".", true},
		{"catalog", map[string]string{"settings.gradle.kts": "", "gradle/libs.versions.toml": "", "gradle.lockfile": lock}, "gradle/libs.versions.toml", ".", true},
		{"no locking", map[string]string{"settings.gradle": "", "app/build.gradle": ""}, "app/build.gradle", ".", false},
		{"verification only", map[string]string{"settings.gradle": "", "build.gradle": "", "gradle/verification-metadata.xml": "<x/>"}, "build.gradle", ".", true},
		{"legacy locks", map[string]string{"settings.gradle": "", "a/build.gradle": "", "a/gradle/dependency-locks/compileClasspath.lockfile": ""}, "a/build.gradle", ".", true},
		{"nested build", map[string]string{"settings.gradle": "", "build.gradle": "", "inc/settings.gradle": "", "inc/gradle.lockfile": lock}, "build.gradle", ".", false},
		{"nested build manifest", map[string]string{"settings.gradle": "", "inc/settings.gradle": "", "inc/a/build.gradle": "", "inc/a/gradle.lockfile": lock}, "inc/a/build.gradle", "inc", true},
		{"build dir ignored", map[string]string{"settings.gradle": "", "build.gradle": "", "build/gradle.lockfile": lock, ".gradle/gradle.lockfile": lock}, "build.gradle", ".", false},
		{"no settings", map[string]string{"svc/build.gradle": "", "svc/gradle.lockfile": lock}, "svc/build.gradle", "svc", true},
		{"no settings, catalog", map[string]string{"gradle/libs.versions.toml": "", "gradle.lockfile": lock}, "gradle/libs.versions.toml", ".", true},
		{"applied script", map[string]string{"settings.gradle": "", "gradle/deps.gradle": "", "gradle.lockfile": lock}, "gradle/deps.gradle", ".", true},
		{"buildSrc source", map[string]string{"settings.gradle.kts": "", "gradle.lockfile": lock, "buildSrc/settings.gradle.kts": "", "buildSrc/src/main/kotlin/Deps.kt": ""},
			"buildSrc/src/main/kotlin/Deps.kt", ".", true},
		{"nested buildSrc source", map[string]string{"app/settings.gradle": "", "app/gradle.lockfile": lock, "app/buildSrc/src/main/groovy/Deps.groovy": ""},
			"app/buildSrc/src/main/groovy/Deps.groovy", "app", true},
		{"buildSrc script", map[string]string{"settings.gradle.kts": "", "gradle.lockfile": lock, "buildSrc/build.gradle.kts": "", "buildSrc/gradle.lockfile": lock},
			"buildSrc/build.gradle.kts", "buildSrc", true},
		{"buildSrc locks not the root's", map[string]string{"settings.gradle.kts": "", "build.gradle.kts": "", "buildSrc/gradle.lockfile": lock},
			"build.gradle.kts", ".", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tc.files)
			dir, ok := GradleLocker{}.Scope(root, tc.manifest)
			if dir != tc.dir || ok != tc.ok {
				t.Errorf("Scope = %q, %v; want %q, %v", dir, ok, tc.dir, tc.ok)
			}
		})
	}
}

func TestScopeSiblings(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{
		"settings.gradle": "", "build.gradle": "", "gradle.lockfile": "", "settings-gradle.lockfile": "",
		"a/b/build.gradle": "", "a/b/gradle.lockfile": "", "a/b/buildscript-gradle.lockfile": "",
		"c/gradle/dependency-locks/runtimeClasspath.lockfile": "", "c/gradle/dependency-locks/notes.txt": "",
		"a/build.gradle.kts": "", "a/build/gradle.lockfile": "", "inc/settings.gradle.kts": "", "inc/gradle.lockfile": "", ".git/gradle.lockfile": "",
		"gradle/verification-metadata.xml": "", "gradle/verification-keyring.keys": "",
	})
	want := []string{"a/b/buildscript-gradle.lockfile", "a/b/gradle.lockfile", "c/gradle/dependency-locks/runtimeClasspath.lockfile",
		"gradle.lockfile", "settings-gradle.lockfile", VerificationMetadata, KeysKeyring}
	if got := (GradleLocker{}).ScopeSiblings(root); !slices.Equal(got, want) {
		t.Errorf("ScopeSiblings = %v\nwant %v", got, want)
	}
}

func TestLockTasks(t *testing.T) {
	got := lockTasks([]string{
		"gradle.lockfile", "settings-gradle.lockfile", "a/b/gradle.lockfile", "a/b/buildscript-gradle.lockfile",
		"gradle/dependency-locks/compileClasspath.lockfile", "gradle/dependency-locks/runtimeClasspath.lockfile",
		"x/gradle/dependency-locks/buildscript-classpath.lockfile",
	})
	want := []string{":a:b:buildEnvironment", ":a:b:dependencies", ":dependencies", ":x:buildEnvironment"}
	if !slices.Equal(got, want) {
		t.Errorf("lockTasks = %v, want %v", got, want)
	}
}

func TestVerificationArg(t *testing.T) {
	for _, tc := range []struct{ name, meta, want string }{
		{"gradle/gradle", fixture(t, "gradle-verification-metadata.xml"), "sha256,pgp"},
		{"sha512 and sha1", `<artifact name="a.jar"><sha512 value="x"/><sha1 value="y"/></artifact>`, "sha512,sha1"},
		{"md5", `<md5 value="x"/>`, "md5"},
		{"signatures only", `<configuration><verify-signatures>true</verify-signatures></configuration>`, "sha256,pgp"},
		{"empty", `<verification-metadata/>`, "sha256"},
	} {
		if got := verificationArg([]byte(tc.meta)); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// gradleStub is a gradle stub appending "updated" to the lockfiles and the verification
// metadata of its working directory and printing the patched build.gradle.
const gradleStub = `for f in gradle.lockfile app/gradle.lockfile gradle/verification-metadata.xml; do [ -f "$f" ] && echo updated >> "$f"; done
[ -n "$STUB_FAIL" ] && { echo "progress"; while IFS= read -r l; do echo "$l"; done < build.gradle; echo "FAILURE: Build failed"; exit 1; }
exit 0`

func TestGradleLockerCommands(t *testing.T) {
	lock := fixture(t, "solr-gradle.lockfile")
	meta := fixture(t, "gradle-verification-metadata.xml")
	base := map[string]string{"settings.gradle": "include 'app'\n", "build.gradle": "old\n", "app/build.gradle": "",
		".git/HEAD": "", ".gradle/x": "", "build/out.jar": "", "app/build/out.jar": "", "app/src/build/keep.txt": ""}
	for _, tc := range []struct {
		name    string
		files   map[string]string
		gradlew bool
		patched string // new build.gradle content, empty for an unchanged one
		locks   map[string][]dep.Update
		argv    string
		changed []string
	}{
		{
			name: "write locks", files: map[string]string{"gradle.lockfile": lock, "app/gradle.lockfile": lock}, patched: "new\n",
			argv: "gradle --console=plain --no-daemon -q :app:dependencies :dependencies --write-locks", changed: []string{"app/gradle.lockfile", "gradle.lockfile"},
		},
		{
			name: "update locks", files: map[string]string{"app/gradle.lockfile": lock},
			locks: map[string][]dep.Update{"app/build.gradle": {
				{Dep: dep.Dep{Name: "org.slf4j:slf4j-api"}, Lockfile: true, NewVersion: "2.0.21"},
				{Dep: dep.Dep{Name: "com.diffplug.spotless", DepType: "plugin"}, Lockfile: true, NewVersion: "8.0.0"},
				{Dep: dep.Dep{Name: "org.slf4j:slf4j-api"}, Lockfile: true, NewVersion: "2.0.21"},
			}},
			argv:    "gradle --console=plain --no-daemon -q :app:dependencies --update-locks com.diffplug.spotless:com.diffplug.spotless.gradle.plugin,org.slf4j:slf4j-api",
			changed: []string{"app/gradle.lockfile"},
		},
		{
			name: "verification only, gradlew", files: map[string]string{VerificationMetadata: meta, KeysKeyring: "keys"}, gradlew: true, patched: "new\n",
			argv:    "gradlew --console=plain --no-daemon -q dependencies --write-verification-metadata sha256,pgp --dependency-verification lenient --export-keys",
			changed: []string{VerificationMetadata},
		},
		{
			name: "locks and verification", files: map[string]string{"gradle.lockfile": lock, VerificationMetadata: `<sha512 value="x"/>`}, patched: "new\n",
			argv:    "gradle --console=plain --no-daemon -q :dependencies --write-locks --write-verification-metadata sha512 --dependency-verification lenient",
			changed: []string{"gradle.lockfile", VerificationMetadata},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, log := toolEnv(t, false)
			dir := t.TempDir()
			write(t, dir, base)
			write(t, dir, tc.files)
			if tc.gradlew {
				script(t, filepath.Join(dir, "gradlew"), log, gradleStub)
			} else {
				script(t, filepath.Join(bin, "gradle"), log, gradleStub)
			}
			manifests := map[string][]byte{"build.gradle": []byte("old\n")}
			if tc.patched != "" {
				manifests["build.gradle"] = []byte(tc.patched)
			}
			p := GradleLocker{}
			siblings := map[string][]byte{}
			for _, n := range p.ScopeSiblings(dir) {
				siblings[n] = []byte(tc.files[n])
			}
			out, err := p.ProcessLocked(t.Context(), dir, manifests, siblings, tc.locks)
			if err != nil {
				t.Fatal(err)
			}
			argv, files := logged(t, log)
			if len(argv) != 1 || argv[0] != tc.argv {
				t.Errorf("argv = %q\nwant %q", argv, tc.argv)
			}
			var changed []string
			for n, b := range out {
				changed = append(changed, n)
				if string(b) != tc.files[n]+"updated\n" {
					t.Errorf("%s = %q", n, b)
				}
			}
			slices.Sort(changed)
			if !slices.Equal(changed, tc.changed) {
				t.Errorf("changed = %v, want %v", changed, tc.changed)
			}
			for _, f := range files {
				if strings.HasPrefix(f, ".git/") || strings.HasPrefix(f, ".gradle/") || strings.HasPrefix(f, "build/") || strings.HasPrefix(f, "app/build/") {
					t.Errorf("copied %s", f)
				}
			}
			if !slices.Contains(files, "app/src/build/keep.txt") {
				t.Errorf("source directory named build not copied: %v", files)
			}
			if b, _ := os.ReadFile(filepath.Join(dir, "build.gradle")); string(b) != "old\n" {
				t.Error("working tree touched")
			}
		})
	}
}

func TestGradleLockerErrors(t *testing.T) {
	lock := map[string][]byte{"gradle.lockfile": []byte("x\n")}
	manifests := map[string][]byte{"build.gradle": []byte("new\n")}
	newDir := func(t *testing.T) string {
		dir := t.TempDir()
		write(t, dir, map[string]string{"settings.gradle": "", "build.gradle": "old\n", "gradle.lockfile": "x\n"})
		return dir
	}
	t.Run("no java", func(t *testing.T) {
		bin, log := toolEnv(t, true)
		script(t, filepath.Join(bin, "gradle"), log, gradleStub)
		_, err := GradleLocker{}.Process(t.Context(), newDir(t), manifests, lock)
		if err == nil || err.Error() != "java not found (needed to update gradle.lockfile)" {
			t.Errorf("err = %v", err)
		}
		_, err = GradleLocker{}.Process(t.Context(), newDir(t), manifests, map[string][]byte{VerificationMetadata: []byte("<x/>")})
		if err == nil || err.Error() != "java not found (needed to update gradle/verification-metadata.xml)" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("no gradle", func(t *testing.T) {
		toolEnv(t, false)
		_, err := GradleLocker{}.Process(t.Context(), newDir(t), manifests, lock)
		if err == nil || err.Error() != "gradle not found (needed to update gradle.lockfile)" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("failure", func(t *testing.T) {
		bin, log := toolEnv(t, false)
		script(t, filepath.Join(bin, "gradle"), log, gradleStub)
		t.Setenv("STUB_FAIL", "1")
		dir := newDir(t)
		_, err := GradleLocker{}.Process(t.Context(), dir, manifests, lock)
		if err == nil || err.Error() != "gradle in "+dir+": progress\nnew\nFAILURE: Build failed" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("lockfile-only without lockfile", func(t *testing.T) {
		toolEnv(t, false)
		_, err := GradleLocker{}.ProcessLocked(t.Context(), newDir(t), manifests, map[string][]byte{VerificationMetadata: []byte("<x/>")},
			map[string][]dep.Update{"build.gradle": {{Lockfile: true}}})
		if err == nil || err.Error() != "lockfile-only updates need a gradle.lockfile" {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("nothing to do", func(t *testing.T) {
		if out, err := (GradleLocker{}).Process(t.Context(), t.TempDir(), manifests, nil); out != nil || err != nil {
			t.Errorf("out %v, err %v", out, err)
		}
	})
}
