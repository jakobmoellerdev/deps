package engine

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// suffixManager matches every file named go.mod; only Match is used by discover.
type suffixManager struct{ manager.Manager }

func (suffixManager) Match(p string) bool { return strings.HasSuffix(p, "go.mod") }

func TestDiscoverIgnorePaths(t *testing.T) {
	files := []string{
		"go.mod", "test/go.mod", "a/test/go.mod", "a/test/deep/go.mod", "a/tests/go.mod", "testx/go.mod",
		"a/testing/go.mod", "examples/x/go.mod", "pkg/__tests__/go.mod", "pkg/__fixtures__/go.mod",
		"web/bower_components/go.mod", "lib/go.mod",
	}
	root := t.TempDir()
	for _, f := range files {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		name   string
		ignore []string
		want   []string
	}{
		{"defaults prune dirs at any depth", config.DefaultIgnorePaths, []string{"a/testing/go.mod", "go.mod", "lib/go.mod", "testx/go.mod"}},
		{"explicit empty list scans everything", []string{}, slices.Sorted(slices.Values(files))},
		{"globs keep case", []string{"A/**", "LIB/go.mod"}, slices.Sorted(slices.Values(files))},
		{"custom dir glob", []string{"a/**"}, []string{"examples/x/go.mod", "go.mod", "lib/go.mod", "pkg/__fixtures__/go.mod", "pkg/__tests__/go.mod", "test/go.mod", "testx/go.mod", "web/bower_components/go.mod"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := discover(root, tc.ignore, true, []manager.Manager{suffixManager{}})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got[0], tc.want) {
				t.Errorf("discovered %v, want %v", got[0], tc.want)
			}
		})
	}
}

// TestDiscoverGitIgnore: with gitIgnore on, the repository's .gitignore files and
// .git/info/exclude keep paths out of discovery; off, only ignorePaths does.
func TestDiscoverGitIgnore(t *testing.T) {
	root := t.TempDir()
	for f, body := range map[string]string{
		".git/HEAD":             "ref: refs/heads/main\n",
		".git/info/exclude":     "excluded/\n",
		".gitignore":            "dist/\ngenerated/go.mod\n",
		"go.mod":                "",
		"dist/go.mod":           "",
		"generated/go.mod":      "",
		"excluded/go.mod":       "",
		"lib/go.mod":            "",
		"lib/.gitignore":        "*.mod\n!go.mod\n",
		"lib/deep/vendored.mod": "",
	} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, tc := range []struct {
		gitIgnore bool
		want      []string
	}{
		{true, []string{"go.mod", "lib/go.mod"}},
		{false, []string{"dist/go.mod", "excluded/go.mod", "generated/go.mod", "go.mod", "lib/go.mod"}},
	} {
		got, err := discover(root, []string{}, tc.gitIgnore, []manager.Manager{suffixManager{}})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got[0], tc.want) {
			t.Errorf("gitIgnore=%v: discovered %v, want %v", tc.gitIgnore, got[0], tc.want)
		}
	}
}

func TestDiscoverRootForms(t *testing.T) {
	tmp := t.TempDir()
	for _, f := range []string{"x/go.mod", "x/a/go.mod", "x/a/b/go.mod", "y/z/go.sum"} {
		p := filepath.Join(tmp, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	inX := []string{"a/b/go.mod", "a/go.mod", "go.mod"}
	inTmp := []string{"x/a/b/go.mod", "x/a/go.mod", "x/go.mod"}
	abs := filepath.Join(tmp, "x")
	for _, tc := range []struct {
		cwd, root string
		want      []string
	}{
		{"", abs, inX},
		{"", abs + "/", inX},
		{"", abs + "/../x", inX},
		{"", ".", inTmp},
		{"", "./", inTmp},
		{"", "y/..", inTmp},
		{"", "x", inX},
		{"", "x/", inX},
		{"", "./x/", inX},
		{"", "x/a/..", inX},
		{"", "y/z/../../x/", inX},
		{"y/z", "../../x", inX},
		{"y/z", "../../x/", inX},
		{"x/a", "..", inX},
		{"x/a", "../", inX},
	} {
		t.Run(tc.cwd+":"+tc.root, func(t *testing.T) {
			t.Chdir(filepath.Join(tmp, tc.cwd))
			got, err := discover(tc.root, nil, true, []manager.Manager{suffixManager{}})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got[0], tc.want) {
				t.Errorf("discovered %v, want %v", got[0], tc.want)
			}
		})
	}
}

// TestDiscoverSymlinkRoot: a --dir that is itself a symlink to a directory discovers the same
// files as the directory, .gitignore included.
func TestDiscoverSymlinkRoot(t *testing.T) {
	tmp := t.TempDir()
	repo := filepath.Join(tmp, "repo")
	for f, body := range map[string]string{
		".git/HEAD":   "ref: refs/heads/main\n",
		".gitignore":  "dist/\n",
		"go.mod":      "",
		"a/go.mod":    "",
		"dist/go.mod": "",
	} {
		p := filepath.Join(repo, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	want, err := discover(repo, nil, true, []manager.Manager{suffixManager{}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(want[0], []string{"a/go.mod", "go.mod"}) {
		t.Fatalf("real root discovered %v", want[0])
	}
	for _, tc := range []struct{ cwd, root string }{
		{"", link},
		{"", link + string(filepath.Separator)},
		{tmp, "link"},
	} {
		t.Run(tc.root, func(t *testing.T) {
			if tc.cwd != "" {
				t.Chdir(tc.cwd)
			}
			got, err := discover(tc.root, nil, true, []manager.Manager{suffixManager{}})
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got[0], want[0]) {
				t.Errorf("discovered %v, want %v", got[0], want[0])
			}
		})
	}
}

// TestRelPathMatchesRel pins relPath to filepath.Rel for roots that cannot be walked in a
// test, such as the filesystem root.
func TestRelPathMatchesRel(t *testing.T) {
	for _, root := range []string{"/", "//", "/a", "/a/", ".", "./", "..", "../", "../a/./b/", "a//b", ""} {
		base := filepath.Clean(root)
		for _, sub := range []string{"f", "d/f", ".git"} {
			p := filepath.Join(root, sub)
			want, wantErr := filepath.Rel(root, p)
			got, err := relPath(base, root, p)
			if got != want || (err == nil) != (wantErr == nil) {
				t.Errorf("relPath(%q, %q) = %q, %v; filepath.Rel = %q, %v", root, p, got, err, want, wantErr)
			}
		}
	}
}

type namedManager struct {
	manager.Manager
	name string
}

func (m namedManager) Name() string { return m.name }

func TestDiscoveryAttrs(t *testing.T) {
	ms := []manager.Manager{namedManager{name: "gomod"}, namedManager{name: "helm"}, namedManager{name: "docker"}, namedManager{name: "custom"}, namedManager{name: "custom"}}
	found := [][]string{{"a"}, nil, {"b", "c", "d"}, {".env", "a"}, {".env", "e"}}
	got := discoveryAttrs(ms, found, 5)
	want := []any{"files", 8, "duration", time.Duration(5), "managers", "docker 3, custom 3, gomod 1"}
	if !slices.Equal(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestDiscoverUnreadableDir: a directory the walk cannot read is skipped, not fatal: every
// other file of the tree is still discovered.
func TestDiscoverUnreadableDir(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root reads every directory")
	}
	root := t.TempDir()
	for _, f := range []string{"go.mod", "a/go.mod", "secret/go.mod", "z/go.mod"} {
		p := filepath.Join(root, filepath.FromSlash(f))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	secret := filepath.Join(root, "secret")
	if err := os.Chmod(secret, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(secret, 0o755) })

	got, err := discover(root, nil, true, []manager.Manager{suffixManager{}})
	if err != nil {
		t.Fatalf("discover: %v", err)
	}
	if want := []string{"a/go.mod", "go.mod", "z/go.mod"}; !slices.Equal(got[0], want) {
		t.Errorf("discovered %v, want %v", got[0], want)
	}
}
