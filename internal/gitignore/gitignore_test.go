package gitignore

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// write creates the files of tree (slash paths) below dir; a value ending in "/" is a
// directory.
func write(t *testing.T, dir string, tree map[string]string) {
	t.Helper()
	for p, body := range tree {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// walk returns the paths below root that are not ignored, as a walk uses the Matcher: ignored
// directories are pruned, and Enter loads the .gitignore of every directory descended into.
func walk(t *testing.T, root string) []string {
	t.Helper()
	m := New(root)
	var kept []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || p == root {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if d.Name() == ".git" || m.Ignored(rel, true) {
				return fs.SkipDir
			}
			m.Enter(rel)
			return nil
		}
		if !m.Ignored(rel, false) {
			kept = append(kept, rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(kept)
	return kept
}

func TestMatcherRules(t *testing.T) {
	for _, tc := range []struct {
		name string
		tree map[string]string
		want []string // files kept, .gitignore files included
	}{
		{
			name: "name without a slash matches at any depth",
			tree: map[string]string{".gitignore": "*.log\n", "a.log": "", "pkg/b.log": "", "pkg/b.txt": ""},
			want: []string{".gitignore", "pkg/b.txt"},
		},
		{
			name: "leading slash anchors to the file's directory",
			tree: map[string]string{".gitignore": "/build\n", "build": "", "pkg/build": ""},
			want: []string{".gitignore", "pkg/build"},
		},
		{
			name: "an inner slash anchors too",
			tree: map[string]string{".gitignore": "pkg/out\n", "pkg/out": "", "out": "", "a/pkg/out": ""},
			want: []string{".gitignore", "a/pkg/out", "out"},
		},
		{
			name: "trailing slash matches directories only",
			tree: map[string]string{".gitignore": "dist/\n", "dist/x.txt": "", "a/dist/y.txt": "", "dist.txt": "", "b/dist": ""},
			want: []string{".gitignore", "b/dist", "dist.txt"},
		},
		{
			name: "the last matching pattern of a file wins",
			tree: map[string]string{".gitignore": "*.yaml\n!keep.yaml\n", "a.yaml": "", "keep.yaml": "", "sub/keep.yaml": ""},
			want: []string{".gitignore", "keep.yaml", "sub/keep.yaml"},
		},
		{
			name: "a deeper file overrides a shallower one",
			tree: map[string]string{".gitignore": "*.txt\n", "sub/.gitignore": "!b.txt\n", "a.txt": "", "sub/a.txt": "", "sub/b.txt": ""},
			want: []string{".gitignore", "sub/.gitignore", "sub/b.txt"},
		},
		{
			name: "a nested file only applies below its directory",
			tree: map[string]string{"sub/.gitignore": "*.txt\n", "a.txt": "", "sub/a.txt": "", "sub/deep/a.txt": ""},
			want: []string{"a.txt", "sub/.gitignore"},
		},
		{
			name: "a directory kept by a negation is still walked",
			tree: map[string]string{".gitignore": "/*\n!/keep/\n!/.gitignore\n", "keep/a.txt": "", "drop/b.txt": ""},
			want: []string{".gitignore", "keep/a.txt"},
		},
		{
			// As in git, "a/**/t.txt" matches zero or more directories between a and t.txt.
			name: "double star crosses directories",
			tree: map[string]string{".gitignore": "a/**/t.txt\n", "a/t.txt": "", "a/b/c/t.txt": "", "b/t.txt": ""},
			want: []string{".gitignore", "b/t.txt"},
		},
		{
			// git check-ignore (2.53.0) on "a/**": a is not ignored, a/f and a/b/c are. The
			// directory must stay walkable, or the negation below could never be reached.
			name: "a trailing double star spares the directory itself",
			tree: map[string]string{".gitignore": "build/**\n!build/go.mod\n", "build/go.mod": "", "build/x.o": "", "build/sub/y.o": ""},
			want: []string{".gitignore", "build/go.mod"},
		},
		{
			// git check-ignore (2.53.0) on "*.{js,ts}" ignores the file literally named
			// "a.{js,ts}" and neither a.js nor a.ts: gitignore(5) has no brace expansion.
			name: "braces are literal, not alternation",
			tree: map[string]string{".gitignore": "*.{js,ts}\nq{\n", "a.js": "", "a.ts": "", "a.{js,ts}": "", "q{": "", "q": ""},
			want: []string{".gitignore", "a.js", "a.ts", "q"},
		},
		{
			name: "comments, blank lines and escapes",
			tree: map[string]string{".gitignore": "# a comment\n\n\\#hash\n!x.log\n*.log\ntrail   \nkeep\\ me \n", "#hash": "", "x.log": "", "trail": "", "keep me": "", "ok": ""},
			want: []string{".gitignore", "ok"},
		},
		{
			name: "nothing is ignored without an ignore file",
			tree: map[string]string{"a.txt": "", "sub/b.txt": ""},
			want: []string{"a.txt", "sub/b.txt"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tc.tree)
			if got := walk(t, root); !slices.Equal(got, tc.want) {
				t.Errorf("kept %v, want %v", got, tc.want)
			}
		})
	}
}

// TestMatcherRepository: .git/info/exclude and the .gitignore files between the repository top
// and the walked root apply to a walk started below the top; ignore files above the top do
// not.
func TestMatcherRepository(t *testing.T) {
	outer := t.TempDir()
	write(t, outer, map[string]string{
		".gitignore":             "above.txt\n",
		"repo/.git/HEAD":         "ref: refs/heads/main\n",
		"repo/.git/info/exclude": "*.bak\n",
		"repo/.gitignore":        "top.txt\nmod/anchored.txt\n",
		"repo/mod/.gitignore":    "own.txt\n",
		"repo/mod/top.txt":       "",
		"repo/mod/anchored.txt":  "",
		"repo/mod/own.txt":       "",
		"repo/mod/x.bak":         "",
		"repo/mod/above.txt":     "",
		"repo/mod/keep.txt":      "",
	})
	got := walk(t, filepath.Join(outer, "repo", "mod"))
	if want := []string{".gitignore", "above.txt", "keep.txt"}; !slices.Equal(got, want) {
		t.Errorf("below the repository top: kept %v, want %v", got, want)
	}
}

// TestMatcherLinkedWorktree: in a linked worktree (.git is a file naming the worktree's git
// directory) info/exclude is read from the common directory.
func TestMatcherLinkedWorktree(t *testing.T) {
	dir := t.TempDir()
	common := filepath.Join(dir, "repo", ".git")
	wt := filepath.Join(dir, "wt")
	write(t, dir, map[string]string{
		"repo/.git/info/exclude":           "*.bak\n",
		"repo/.git/worktrees/wt/commondir": "../..\n",
		"wt/.git":                          "gitdir: " + filepath.Join(common, "worktrees", "wt") + "\n",
		"wt/a.bak":                         "",
		"wt/a.txt":                         "",
	})
	if got, want := walk(t, wt), []string{".git", "a.txt"}; !slices.Equal(got, want) {
		t.Errorf("kept %v, want %v", got, want)
	}
}

func TestParseLine(t *testing.T) {
	for _, tc := range []struct {
		line    string
		ok      bool
		glob    string
		negate  bool
		dirOnly bool
	}{
		{line: "", ok: false},
		{line: "# comment", ok: false},
		{line: "/", ok: false},
		{line: "!", ok: false},
		{line: "a.txt", ok: true, glob: "**/a.txt"},
		{line: "!a.txt", ok: true, glob: "**/a.txt", negate: true},
		{line: "dist/", ok: true, glob: "**/dist", dirOnly: true},
		{line: "/dist/", ok: true, glob: "dist", dirOnly: true},
		{line: "a/b.txt", ok: true, glob: "a/b.txt"},
		{line: "a/**/b", ok: true, glob: "a/**/b"},
		{line: "a/**", ok: true, glob: "a/**/*"}, // one element below a, never a itself
		{line: "*.{js,ts}", ok: true, glob: `**/*.\{js,ts\}`},
		{line: "a{b", ok: true, glob: `**/a\{b`}, // an unpaired brace is a name, not a broken glob
		{line: "trail   ", ok: true, glob: "**/trail"},
		{line: `keep\ me `, ok: true, glob: `**/keep\ me`},
		{line: `\#hash`, ok: true, glob: `**/\#hash`},
		{line: "[", ok: false}, // doublestar rejects it
	} {
		t.Run(strings.ReplaceAll(tc.line, " ", "_"), func(t *testing.T) {
			p, ok := parseLine(tc.line)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && (p.glob != tc.glob || p.negate != tc.negate || p.dirOnly != tc.dirOnly) {
				t.Errorf("= %+v, want glob %q negate %v dirOnly %v", p, tc.glob, tc.negate, tc.dirOnly)
			}
		})
	}
}

// TestNilMatcher: a nil Matcher ignores nothing, so callers need no check.
func TestNilMatcher(t *testing.T) {
	var m *Matcher
	m.Enter("a")
	if m.Ignored("a/b.txt", false) {
		t.Error("nil Matcher ignores a path")
	}
}
