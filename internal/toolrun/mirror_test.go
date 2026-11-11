package toolrun

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// tree writes files (slash paths relative to root, content) into root.
func tree(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, s := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// kind returns "link", "dir", "file" or "missing" for p.
func kind(t *testing.T, p string) string {
	t.Helper()
	st, err := os.Lstat(p)
	switch {
	case os.IsNotExist(err):
		return "missing"
	case err != nil:
		t.Fatal(err)
	case st.Mode()&fs.ModeSymlink != 0:
		return "link"
	case st.IsDir():
		return "dir"
	}
	return "file"
}

func TestMirror(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{
		".git/HEAD": "", "top.lock": "orig", "README": "", "a/b/f.yaml": "orig", "a/b/g": "",
		"a/other": "", "a/build/x": "", "c/x": "", "build/y": "",
	})
	skip := func(rel string, d fs.DirEntry) bool { return d.Name() == "build" || rel == ".git" }
	for _, tc := range []struct {
		name     string
		recreate func(string, fs.DirEntry) bool
		want     map[string]string
	}{
		{"lazy", nil, map[string]string{
			".git": "missing", "top.lock": "file", "README": "link", "a": "dir", "a/b": "dir",
			"a/b/f.yaml": "file", "a/b/g": "link", "a/other": "link", "a/build": "missing",
			"c": "link", "build": "missing",
		}},
		{"recreate", func(string, fs.DirEntry) bool { return true }, map[string]string{
			".git": "missing", "a/b/f.yaml": "file", "a/build": "missing", "c": "dir", "c/x": "link",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// top.lock is a Files key with a nil value: a real (empty) file, not a symlink
			// written through into the checkout.
			files := map[string][]byte{"top.lock": nil, "a/b/f.yaml": []byte("new")}
			tmp, base, err := Mirror(dir, MirrorOptions{Pattern: "x-*", Files: files, Skip: skip, Recreate: tc.recreate})
			if err != nil {
				t.Fatal(err)
			}
			defer os.RemoveAll(tmp)
			if base != tmp {
				t.Errorf("base = %s, want tmp %s for a repository top", base, tmp)
			}
			for rel, want := range tc.want {
				if got := kind(t, filepath.Join(base, filepath.FromSlash(rel))); got != want {
					t.Errorf("%s is %s, want %s", rel, got, want)
				}
			}
			for rel, want := range map[string]string{"top.lock": "orig", "a/b/f.yaml": "orig"} {
				if b, _ := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel))); string(b) != want {
					t.Errorf("checkout %s = %q, want %q", rel, b, want)
				}
			}
			if b, _ := os.ReadFile(filepath.Join(base, "a", "b", "f.yaml")); string(b) != "new" {
				t.Errorf("mirror a/b/f.yaml = %q", b)
			}
		})
	}
}

func TestMirrorRemovesTmpOnError(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{".git/HEAD": "", "f": "", "g": ""})
	tmpRoot := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(tmpRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmpRoot)
	for name, d := range map[string]string{"missing dir": filepath.Join(dir, "missing"), "write fails": dir} {
		// f is a regular file, so f/x cannot be written below its symlink
		if _, _, err := Mirror(d, MirrorOptions{Pattern: "x-*", Files: map[string][]byte{"f/x": nil}}); err == nil {
			t.Errorf("%s: Mirror succeeded", name)
		}
		if left, _ := os.ReadDir(os.TempDir()); len(left) > 0 {
			t.Errorf("%s: %s left behind", name, left[0].Name())
		}
	}
}

func TestMirrorAncestors(t *testing.T) {
	top := t.TempDir()
	tree(t, top, map[string]string{
		".git/HEAD": "", "lib/x": "", "app/inner/f": "", "app/y": "", "sub/.git/HEAD": "", "mod/.git": "gitdir: ../.git/modules/mod",
	})
	for _, tc := range []struct {
		dir, base string
		want      map[string]string
	}{
		{"app/inner", "app/inner", map[string]string{".git": "missing", "lib": "link", "app": "dir", "app/y": "link", "app/inner": "dir", "app/inner/f": "missing"}},
		// a repository of its own (a nested checkout) is the top, however deep the outer one
		{"sub", ".", map[string]string{"lib": "missing", "sub": "missing"}},
		// a submodule sees its superproject
		{"mod", "mod", map[string]string{".git": "missing", "lib": "link", "mod": "dir", "mod/.git": "missing"}},
		{".", ".", map[string]string{"lib": "missing"}},
	} {
		tmp := t.TempDir()
		base, err := MirrorAncestors(filepath.Join(top, filepath.FromSlash(tc.dir)), tmp)
		if err != nil {
			t.Fatal(err)
		}
		if want := filepath.Join(tmp, filepath.FromSlash(tc.base)); base != want {
			t.Errorf("%s: base = %s, want %s", tc.dir, base, want)
		}
		for rel, want := range tc.want {
			if got := kind(t, filepath.Join(tmp, filepath.FromSlash(rel))); got != want {
				t.Errorf("%s: %s is %s, want %s", tc.dir, rel, got, want)
			}
		}
	}
}

func TestMirrorSymlinkedParentOfFile(t *testing.T) {
	dir := t.TempDir()
	tree(t, dir, map[string]string{".git/HEAD": "", "real/pkg.json": "orig"})
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	tmp, base, err := Mirror(dir, MirrorOptions{Pattern: "x-*", Files: map[string][]byte{"link/pkg.json": []byte("new")}, Skip: SkipGit})
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmp)
	if got := kind(t, filepath.Join(base, "link")); got != "dir" {
		t.Errorf("mirror link is %s, want dir", got)
	}
	if b, _ := os.ReadFile(filepath.Join(dir, "real", "pkg.json")); string(b) != "orig" {
		t.Errorf("checkout real/pkg.json = %q, want orig: Mirror wrote through the symlink", b)
	}
	if b, _ := os.ReadFile(filepath.Join(base, "link", "pkg.json")); string(b) != "new" {
		t.Errorf("mirror link/pkg.json = %q, want new", b)
	}
}
