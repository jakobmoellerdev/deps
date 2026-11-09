package ant

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// extractFS extracts and links every file of fsys the manager matches, in path order, and
// returns the deps of each extracted file (keyed by slash path).
func extractFS(t *testing.T, fsys fs.FS) map[string][]dep.Dep {
	t.Helper()
	m := New()
	var files []string
	err := fs.WalkDir(fsys, ".", func(p string, e fs.DirEntry, err error) error {
		if err == nil && !e.IsDir() && m.Match(p) {
			files = append(files, p)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	env := manager.Env{ReadFile: func(p string) ([]byte, error) { return fs.ReadFile(fsys, p) }}
	all := make([][]dep.Dep, len(files))
	for i, f := range files {
		src, err := fs.ReadFile(fsys, f)
		if err != nil {
			t.Fatal(err)
		}
		if all[i], err = m.Extract(f, src, env); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
	}
	m.(manager.Linker).Link(all)
	out := map[string][]dep.Dep{}
	for i, f := range files {
		out[f] = all[i]
	}
	return out
}

// extractDir is extractFS of the testdata directory dir.
func extractDir(t *testing.T, dir string) map[string][]dep.Dep {
	t.Helper()
	return extractFS(t, os.DirFS(filepath.Join("testdata", dir)))
}

// mapFS returns an in-memory file system of path → content.
func mapFS(files map[string]string) fstest.MapFS {
	out := fstest.MapFS{}
	for p, s := range files {
		out[p] = &fstest.MapFile{Data: []byte(s)}
	}
	return out
}

// summary renders a dep as `name version [type] @file:line ref=<ref> src=<source> (skip)`.
func summary(d dep.Dep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s [%s] @%s:%d", d.Name, d.Version, d.DepType, d.File, d.Line)
	if d.VersionRef != "" {
		fmt.Fprintf(&b, " ref=%s", d.VersionRef)
	}
	if d.Source != "" {
		fmt.Fprintf(&b, " src=%s", d.Source)
	}
	if d.Skip != "" {
		fmt.Fprintf(&b, " (%s)", d.Skip)
	}
	return b.String()
}

// summaries renders the deps of every file, in file order.
func summaries(files map[string][]dep.Dep) []string {
	var out []string
	keys := make([]string, 0, len(files))
	for f := range files {
		keys = append(keys, f)
	}
	slices.Sort(keys)
	for _, f := range keys {
		for _, d := range files[f] {
			out = append(out, summary(d))
		}
	}
	return out
}

// checkSpans fails unless every updatable dep's RefSpan (in the file the dep names) reads its
// version, or its range.
func checkSpans(t *testing.T, fsys fs.FS, files map[string][]dep.Dep) {
	t.Helper()
	for _, deps := range files {
		for _, d := range deps {
			if d.Skip != "" {
				continue
			}
			src, err := fs.ReadFile(fsys, d.File)
			if err != nil {
				t.Fatal(err)
			}
			want := d.Version
			if d.Range != "" {
				want = d.Range
			}
			if got := string(src[d.RefSpan.Start:d.RefSpan.End]); got != want {
				t.Errorf("%s: RefSpan reads %q, want %q", summary(d), got, want)
			}
			for _, p := range d.NameParts {
				if got := string(src[p.Span.Start:p.Span.End]); got != p.Text {
					t.Errorf("%s: name part reads %q, want %q", summary(d), got, p.Text)
				}
			}
		}
	}
}
