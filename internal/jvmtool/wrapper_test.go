package jvmtool

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

type scoper interface {
	Scope(root, manifest string) (string, bool)
}

func TestWrapperScope(t *testing.T) {
	for _, tc := range []struct {
		p        scoper
		manifest string
		dir      string
		ok       bool
	}{
		{GradleWrapper{}, "gradle/wrapper/gradle-wrapper.properties", ".", true},
		{GradleWrapper{}, "sub/app/gradle/wrapper/gradle-wrapper.properties", "sub/app", true},
		{GradleWrapper{}, "x/gradle-wrapper.properties", "", false},
		{MavenWrapper{}, ".mvn/wrapper/maven-wrapper.properties", ".", true},
		{MavenWrapper{}, "svc/.mvn/wrapper/maven-wrapper.properties", "svc", true},
		{MavenWrapper{}, "gradle/wrapper/gradle-wrapper.properties", "", false},
	} {
		if dir, ok := tc.p.Scope("/root", tc.manifest); dir != tc.dir || ok != tc.ok {
			t.Errorf("%T.Scope(%q) = %q, %v", tc.p, tc.manifest, dir, ok)
		}
	}
}

// gradleWrapperStub writes a new jar and scripts and rewrites the properties like `gradle
// wrapper` does (escaped colons), with $STUB_EXTRA appended.
const gradleWrapperStub = `echo jar-new > gradle/wrapper/gradle-wrapper.jar
echo sh-new > gradlew
echo bat-new > gradlew.bat
{
  echo 'distributionBase=GRADLE_USER_HOME'
  echo 'distributionPath=wrapper/dists'
  echo "distributionUrl=$STUB_URL"
  echo 'zipStoreBase=GRADLE_USER_HOME'
  echo 'zipStorePath=wrapper/dists'
  [ -n "$STUB_EXTRA" ] && echo "$STUB_EXTRA"
} > gradle/wrapper/gradle-wrapper.properties
exit 0`

func TestGradleWrapperProcess(t *testing.T) {
	const props = "# pinned\ndistributionBase=GRADLE_USER_HOME\ndistributionPath=wrapper/dists\n" +
		"distributionUrl=https\\://services.gradle.org/distributions/gradle-%s\nzipStoreBase=GRADLE_USER_HOME\nzipStorePath=wrapper/dists\n"
	for _, tc := range []struct {
		name, dist, extra, sum string
		argv                   string
		keepProps              bool
	}{
		{name: "bin", dist: "9.2.0-bin.zip", keepProps: true,
			argv: "gradlew --console=plain --no-daemon -q wrapper --gradle-version 9.2.0 --distribution-type bin"},
		{name: "all with sum", dist: "9.2.0-all.zip", sum: "abc", keepProps: true,
			argv: "gradlew --console=plain --no-daemon -q wrapper --gradle-version 9.2.0 --distribution-type all --gradle-distribution-sha256-sum abc"},
		{name: "new key", dist: "9.2.0-bin.zip", extra: "networkTimeout=10000",
			argv: "gradlew --console=plain --no-daemon -q wrapper --gradle-version 9.2.0 --distribution-type bin"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, log := toolEnv(t, false)
			dir := t.TempDir()
			patched := []byte(fmt.Sprintf(props, tc.dist))
			if tc.sum != "" {
				patched = append(patched, "distributionSha256Sum="+tc.sum+"\n"...)
			}
			write(t, dir, map[string]string{"settings.gradle": "", GradleWrapperProperties: fmt.Sprintf(props, "9.1.0-bin.zip"), GradleWrapperJar: "jar", "gradlew.bat": "bat", ".git/HEAD": ""})
			script(t, filepath.Join(dir, "gradlew"), log, gradleWrapperStub)
			url := `https\://services.gradle.org/distributions/gradle-` + tc.dist
			t.Setenv("STUB_URL", url)
			t.Setenv("STUB_EXTRA", tc.extra)
			if tc.sum != "" {
				t.Setenv("STUB_EXTRA", "distributionSha256Sum="+tc.sum)
			}
			out, err := GradleWrapper{}.Process(t.Context(), dir, map[string][]byte{GradleWrapperProperties: patched}, nil)
			if err != nil {
				t.Fatal(err)
			}
			argv, files := logged(t, log)
			if len(argv) != 1 || argv[0] != tc.argv {
				t.Errorf("argv = %q\nwant %q", argv, tc.argv)
			}
			if slices.Contains(files, ".git/HEAD") {
				t.Error(".git copied")
			}
			want := []string{GradleWrapperJar, "gradlew", "gradlew.bat"}
			if !tc.keepProps {
				want = append(want, GradleWrapperProperties)
			}
			got := slices.Sorted(maps.Keys(out))
			slices.Sort(want)
			if !slices.Equal(got, want) {
				t.Errorf("out = %v, want %v", got, want)
			}
			if string(out[GradleWrapperJar]) != "jar-new\n" || string(out["gradlew"]) != "sh-new\n" {
				t.Errorf("out = %q", out)
			}
		})
	}
}

func TestGradleWrapperMirror(t *testing.T) {
	_, log := toolEnv(t, false)
	dir := t.TempDir()
	const u = "https://mirror.example.com/gradle/gradle-9.2.0-bin.zip"
	write(t, dir, map[string]string{GradleWrapperProperties: "distributionUrl=" + u + "\n"})
	script(t, filepath.Join(dir, "gradlew"), log, gradleWrapperStub)
	t.Setenv("STUB_URL", u)
	if _, err := (GradleWrapper{}).Process(t.Context(), dir, map[string][]byte{GradleWrapperProperties: []byte("distributionUrl=" + u + "\n")}, nil); err != nil {
		t.Fatal(err)
	}
	argv, _ := logged(t, log)
	if want := "gradlew --console=plain --no-daemon -q wrapper --gradle-version 9.2.0 --distribution-type bin --gradle-distribution-url " + u; len(argv) != 1 || argv[0] != want {
		t.Errorf("argv = %q", argv)
	}
}

func TestGradleWrapperErrors(t *testing.T) {
	good := map[string][]byte{GradleWrapperProperties: []byte("distributionUrl=https\\://services.gradle.org/distributions/gradle-9.2.0-bin.zip\n")}
	for _, tc := range []struct {
		name      string
		noJava    bool
		gradle    bool // gradle stub on PATH
		manifests map[string][]byte
		err       string
	}{
		{name: "no java", noJava: true, gradle: true, manifests: good, err: "java not found (needed to regenerate the Gradle wrapper)"},
		{name: "no gradle", manifests: good, err: "gradle not found (needed to regenerate the Gradle wrapper)"},
		{name: "not patched", manifests: map[string][]byte{}, err: GradleWrapperProperties + " was not patched"},
		{name: "custom url", manifests: map[string][]byte{GradleWrapperProperties: []byte("distributionUrl=https://x/custom.zip\n")}, err: `unsupported distributionUrl "https://x/custom.zip"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, log := toolEnv(t, tc.noJava)
			if tc.gradle {
				script(t, filepath.Join(bin, "gradle"), log, "exit 0")
			}
			dir := t.TempDir()
			write(t, dir, map[string]string{GradleWrapperProperties: "", "gradlew": "not executable"})
			_, err := GradleWrapper{}.Process(t.Context(), dir, tc.manifests, nil)
			if err == nil || err.Error() != tc.err {
				t.Errorf("err = %v, want %s", err, tc.err)
			}
		})
	}
}

// mavenWrapperStub writes new scripts, a jar and a rewritten properties file.
const mavenWrapperStub = `echo sh-new > mvnw
echo cmd-new > mvnw.cmd
echo jar-new > .mvn/wrapper/maven-wrapper.jar
echo 'distributionUrl=rewritten' > .mvn/wrapper/maven-wrapper.properties
exit 0`

// Maven wrapper fixtures (fetched 2026-11-04), see internal/manager/mavenwrapper/testdata:
//   - spring-petclinic: https://raw.githubusercontent.com/spring-projects/spring-petclinic/main/.mvn/wrapper/maven-wrapper.properties
//   - dubbo: https://raw.githubusercontent.com/apache/dubbo/HEAD/.mvn/wrapper/maven-wrapper.properties
func TestMavenWrapperProcess(t *testing.T) {
	const (
		petclinic = "wrapperVersion=3.3.4\ndistributionType=only-script\ndistributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.16/apache-maven-3.9.16-bin.zip\n"
		dubbo     = "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.9/apache-maven-3.9.9-bin.zip\n" +
			"wrapperUrl=https://repo.maven.apache.org/maven2/org/apache/maven/wrapper/maven-wrapper/3.1.1/maven-wrapper-3.1.1.jar\n"
	)
	for _, tc := range []struct {
		name, props string
		files       map[string]string
		mvn         bool // mvn on PATH instead of mvnw
		argv        string
	}{
		{name: "spring-petclinic", props: petclinic,
			argv: "mvnw -B -q org.apache.maven.plugins:maven-wrapper-plugin:3.3.4:wrapper -Dmaven=3.9.16 -Dtype=only-script"},
		{name: "dubbo, jar", props: dubbo, files: map[string]string{MavenWrapperJar: "jar"},
			argv: "mvnw -B -q org.apache.maven.plugins:maven-wrapper-plugin:3.1.1:wrapper -Dmaven=3.9.9 -Dtype=bin"},
		{name: "dubbo, script", props: dubbo,
			argv: "mvnw -B -q org.apache.maven.plugins:maven-wrapper-plugin:3.1.1:wrapper -Dmaven=3.9.9 -Dtype=script"},
		{name: "downloader, mvn", props: "distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.8.1/apache-maven-3.8.1-bin.zip\n",
			files: map[string]string{MavenWrapperDownloader: "class X {}"}, mvn: true,
			argv: "mvn -B -q wrapper:wrapper -Dmaven=3.8.1 -Dtype=source"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, log := toolEnv(t, false)
			dir := t.TempDir()
			write(t, dir, map[string]string{"pom.xml": "<project/>", MavenWrapperProperties: "old", ".git/HEAD": "", "target/x.jar": "", "src/target/keep": ""})
			write(t, dir, tc.files)
			if tc.mvn {
				script(t, filepath.Join(bin, "mvn"), log, mavenWrapperStub)
			} else {
				script(t, filepath.Join(dir, "mvnw"), log, mavenWrapperStub)
			}
			files := map[string][]byte{}
			for n, c := range tc.files {
				files[n] = []byte(c)
			}
			out, err := MavenWrapper{}.Process(t.Context(), dir, map[string][]byte{MavenWrapperProperties: []byte(tc.props)}, files)
			if err != nil {
				t.Fatal(err)
			}
			argv, copied := logged(t, log)
			if len(argv) != 1 || argv[0] != tc.argv {
				t.Errorf("argv = %q\nwant %q", argv, tc.argv)
			}
			if slices.Contains(copied, ".git/HEAD") || slices.Contains(copied, "target/x.jar") || !slices.Contains(copied, "src/target/keep") {
				t.Errorf("copied %v", copied)
			}
			want := []string{MavenWrapperJar, "mvnw", "mvnw.cmd"}
			if got := slices.Sorted(maps.Keys(out)); !slices.Equal(got, want) {
				t.Errorf("out = %v, want %v", got, want)
			}
			if string(out["mvnw"]) != "sh-new\n" {
				t.Errorf("mvnw = %q", out["mvnw"])
			}
			if b, _ := os.ReadFile(filepath.Join(dir, MavenWrapperProperties)); string(b) != "old" {
				t.Error("working tree touched")
			}
		})
	}
}

func TestMavenWrapperErrors(t *testing.T) {
	good := map[string][]byte{MavenWrapperProperties: []byte("distributionUrl=https://repo.maven.apache.org/maven2/org/apache/maven/apache-maven/3.9.9/apache-maven-3.9.9-bin.zip\n")}
	for _, tc := range []struct {
		name      string
		noJava    bool
		mvn       bool
		manifests map[string][]byte
		err       string
	}{
		{name: "no java", noJava: true, mvn: true, manifests: good, err: "java not found (needed to regenerate the Maven wrapper)"},
		{name: "no mvn", manifests: good, err: "mvn not found (needed to regenerate the Maven wrapper)"},
		{name: "not patched", manifests: map[string][]byte{}, err: MavenWrapperProperties + " was not patched"},
		{name: "mvnd url", manifests: map[string][]byte{MavenWrapperProperties: []byte("distributionUrl=https://x/maven-mvnd-1.0.2-bin.zip\n")},
			err: `unsupported distributionUrl "https://x/maven-mvnd-1.0.2-bin.zip"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			bin, log := toolEnv(t, tc.noJava)
			if tc.mvn {
				script(t, filepath.Join(bin, "mvn"), log, "exit 0")
			}
			_, err := MavenWrapper{}.Process(t.Context(), t.TempDir(), tc.manifests, nil)
			if err == nil || err.Error() != tc.err {
				t.Errorf("err = %v, want %s", err, tc.err)
			}
		})
	}
	t.Run("failure", func(t *testing.T) {
		bin, log := toolEnv(t, false)
		script(t, filepath.Join(bin, "mvn"), log, "echo '[ERROR] No plugin found'; exit 1")
		dir := t.TempDir()
		_, err := MavenWrapper{}.Process(t.Context(), dir, good, nil)
		if err == nil || err.Error() != "mvn in "+dir+": [ERROR] No plugin found" {
			t.Errorf("err = %v", err)
		}
	})
}
