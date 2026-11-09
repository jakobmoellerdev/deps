package gradle

import (
	"io/fs"
	"path"
	"path/filepath"
	"testing"
)

// regexpKindOf is kindOf without the suffix checks that skip the regexps.
func regexpKindOf(file string) fileKind {
	switch base := path.Base(file); {
	case catalogRe.MatchString(file):
		return kindCatalog
	case base == "gradle.properties":
		return kindProperties
	case base == "settings.gradle" || base == "settings.gradle.kts":
		return kindSettings
	case scriptRe.MatchString(file):
		return kindScript
	}
	return kindSource
}

var kindSeeds = []string{
	"build.gradle", "a/build.gradle.kts", "settings.gradle", "gradle/libs.versions.toml", "x.versions.toml",
	"a/gradle/b/c.toml", "gradle.toml", "a.gradle.kt", ".gradle", "/.gradle.kts", "buildSrc/src/main/kotlin/a.gradle.kts",
	"gradle.properties", "a/b.toml", "", "src/main/groovy/x.gradle", "a.toml\n", "a.gradle\n",
}

// TestKindOfParity compares kindOf with the regexps alone and conventions' literal check with
// conventionRe over every path of this repository.
func TestKindOfParity(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	paths := kindSeeds
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		rel, _ := filepath.Rel(root, p)
		paths = append(paths, filepath.ToSlash(rel))
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		checkKindOf(t, p)
	}
}

func checkKindOf(t *testing.T, p string) {
	t.Helper()
	if got, want := kindOf(p), regexpKindOf(p); got != want {
		t.Errorf("kindOf(%q) = %v, regexps say %v", p, got, want)
	}
	var r repoDecls
	(&source{}).conventions(p, &r)
	want := ""
	if m := conventionRe.FindStringSubmatch(p); len(m) > 1 {
		want = m[1]
	}
	if r.convention != want {
		t.Errorf("conventions(%q) convention %q, conventionRe says %q", p, r.convention, want)
	}
}

func FuzzKindOf(f *testing.F) {
	for _, p := range kindSeeds {
		f.Add(p)
	}
	f.Fuzz(checkKindOf)
}
