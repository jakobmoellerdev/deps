package jvmtool

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/jvmtool/properties"
)

// Wrapper files, as slash paths relative to the project directory.
const (
	GradleWrapperProperties = "gradle/wrapper/gradle-wrapper.properties"
	GradleWrapperJar        = "gradle/wrapper/gradle-wrapper.jar"
	MavenWrapperProperties  = ".mvn/wrapper/maven-wrapper.properties"
	MavenWrapperJar         = ".mvn/wrapper/maven-wrapper.jar"
	MavenWrapperDownloader  = ".mvn/wrapper/MavenWrapperDownloader.java"
)

// canonicalGradle is where `gradle wrapper --gradle-version` points distributionUrl.
const canonicalGradle = "https://services.gradle.org/distributions/"

var (
	gradleDistRe = regexp.MustCompile(`(?:^|/)gradle-(\d[0-9A-Za-z.+-]*)-(bin|all)\.zip$`)
	mavenDistRe  = regexp.MustCompile(`/apache-maven-([^/]+)-bin\.zip$`)
	mavenJarRe   = regexp.MustCompile(`/maven-wrapper-([^/]+)\.jar$`)
)

// wrapperScope returns the project directory of a wrapper properties file at manifest.
func wrapperScope(manifest, props string) (string, bool) {
	if manifest == props {
		return ".", true
	}
	if dir, ok := strings.CutSuffix(manifest, "/"+props); ok {
		return dir, true
	}
	return "", false
}

// GradleWrapper is the gradle-wrapper post-processor (engine.PostProcessor): after deps
// edited gradle-wrapper.properties it regenerates the wrapper jar and scripts with
// `gradle wrapper` on a temporary copy of the project.
type GradleWrapper struct{}

// Scope is the project directory, two levels above gradle/wrapper/gradle-wrapper.properties.
func (GradleWrapper) Scope(_ context.Context, _, manifest string) (string, bool) {
	return wrapperScope(manifest, GradleWrapperProperties)
}

// Siblings lists the wrapper jar and scripts.
func (GradleWrapper) Siblings() []string { return []string{GradleWrapperJar, "gradlew", "gradlew.bat"} }

// Process runs `wrapper` with the distribution of the patched properties and returns the
// regenerated jar and scripts. The patched properties are kept unless Gradle's rewrite
// differs in more than formatting (a key the new Gradle version adds).
func (GradleWrapper) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	patched, ok := manifests[GradleWrapperProperties]
	if !ok {
		return nil, errors.New(GradleWrapperProperties + " was not patched")
	}
	want := properties.Values(patched)
	u := want["distributionUrl"]
	m := gradleDistRe.FindStringSubmatch(u)
	if m == nil {
		return nil, fmt.Errorf("unsupported distributionUrl %q", u)
	}
	if checkJava() != nil {
		slog.WarnContext(ctx, "java not found: keeping the Gradle wrapper jar and scripts, only "+GradleWrapperProperties+" is updated", "dir", dir)
		return nil, nil
	}
	tmp, err := copyBuild(dir, manifests, gradleSkip)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(tmp))
	tool, err := toolPath(tmp, "gradlew", "gradle")
	if err != nil {
		return nil, errors.New("gradle not found (needed to regenerate the Gradle wrapper)")
	}
	argv := []string{tool, "--console=plain", "--no-daemon", "-q", "wrapper", "--gradle-version", m[1], "--distribution-type", m[2]}
	if u != canonicalGradle+"gradle-"+m[1]+"-"+m[2]+".zip" {
		argv = append(argv, "--gradle-distribution-url", u)
	}
	if sum := want["distributionSha256Sum"]; sum != "" {
		argv = append(argv, "--gradle-distribution-sha256-sum", sum)
	}
	if err := run(ctx, tmp, argv); err != nil {
		return nil, fmt.Errorf("gradle in %s: %w", dir, err)
	}
	out, err := readBack(tmp, files, GradleWrapperJar, "gradlew", "gradlew.bat", GradleWrapperProperties)
	if err != nil {
		return nil, err
	}
	if b, ok := out[GradleWrapperProperties]; ok && maps.Equal(properties.Values(b), want) {
		delete(out, GradleWrapperProperties)
	}
	return out, nil
}

// MavenWrapper is the maven-wrapper post-processor (engine.PostProcessor): after deps edited
// maven-wrapper.properties it regenerates the wrapper scripts (and jar or downloader source,
// by distribution type) with the maven-wrapper-plugin on a temporary copy of the project.
type MavenWrapper struct{}

// Scope is the project directory, two levels above .mvn/wrapper/maven-wrapper.properties.
func (MavenWrapper) Scope(_ context.Context, _, manifest string) (string, bool) {
	return wrapperScope(manifest, MavenWrapperProperties)
}

// Siblings lists the wrapper scripts, jar and downloader source.
func (MavenWrapper) Siblings() []string {
	return []string{"mvnw", "mvnw.cmd", MavenWrapperJar, MavenWrapperDownloader}
}

// Process runs the wrapper goal of the wrapperVersion's maven-wrapper-plugin for the patched
// Maven version and distribution type and returns the regenerated files; the patched
// properties are kept (the plugin rewrites them from scratch, dropping comments and mirrors).
func (MavenWrapper) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	patched, ok := manifests[MavenWrapperProperties]
	if !ok {
		return nil, errors.New(MavenWrapperProperties + " was not patched")
	}
	argv, err := mavenArgs(properties.Values(patched), files)
	if err != nil {
		return nil, err
	}
	if checkJava() != nil {
		slog.WarnContext(ctx, "java not found: keeping the Maven wrapper scripts, only "+MavenWrapperProperties+" is updated", "dir", dir)
		return nil, nil
	}
	tmp, err := copyBuild(dir, manifests, mavenSkip)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(filepath.Dir(tmp))
	tool, err := toolPath(tmp, "mvnw", "mvn")
	if err != nil {
		return nil, errors.New("mvn not found (needed to regenerate the Maven wrapper)")
	}
	if err := run(ctx, tmp, append([]string{tool}, argv...)); err != nil {
		return nil, fmt.Errorf("mvn in %s: %w", dir, err)
	}
	return readBack(tmp, files, "mvnw", "mvnw.cmd", MavenWrapperJar, MavenWrapperDownloader)
}

// mavenArgs returns the arguments of the wrapper goal for the properties props; files are
// the existing wrapper files.
func mavenArgs(props map[string]string, files map[string][]byte) ([]string, error) {
	m := mavenDistRe.FindStringSubmatch(props["distributionUrl"])
	if m == nil {
		return nil, fmt.Errorf("unsupported distributionUrl %q", props["distributionUrl"])
	}
	wv := props["wrapperVersion"]
	if wv == "" {
		if jm := mavenJarRe.FindStringSubmatch(props["wrapperUrl"]); jm != nil {
			wv = jm[1]
		}
	}
	goal := "wrapper:wrapper"
	if wv != "" {
		goal = "org.apache.maven.plugins:maven-wrapper-plugin:" + wv + ":wrapper"
	}
	return []string{"-B", "-q", goal, "-Dmaven=" + m[1], "-Dtype=" + mavenType(props, files)}, nil
}

// mavenType is the wrapper distribution type: distributionType, else derived from the files
// the older wrappers without it have.
func mavenType(props map[string]string, files map[string][]byte) string {
	switch {
	case props["distributionType"] != "":
		return props["distributionType"]
	case files[MavenWrapperJar] != nil:
		return "bin"
	case files[MavenWrapperDownloader] != nil:
		return "source"
	case props["wrapperUrl"] != "":
		return "script"
	}
	return "only-script"
}

// mavenSkip reports the directories that are not copied: .git and a module's target
// directory.
func mavenSkip(p string, d fs.DirEntry) bool {
	switch d.Name() {
	case ".git":
		return true
	case "target":
		return hasAny(filepath.Dir(p), "pom.xml")
	}
	return false
}
