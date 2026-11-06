package sbt

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// extractDir extracts and links every file of dir m matches, in path order, and returns the
// deps of each file (keyed by slash path relative to dir) with the sources read.
func extractDir(t *testing.T, m manager.Manager, dir string) (map[string][]dep.Dep, map[string][]byte) {
	t.Helper()
	var files []string
	srcs := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
		if err != nil || e.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		rel = filepath.ToSlash(rel)
		if m.Match(rel) {
			files = append(files, rel)
			b, err := os.ReadFile(p)
			srcs[rel] = b
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(files)
	all := make([][]dep.Dep, len(files))
	for i, f := range files {
		ds, err := m.Extract(f, srcs[f], manager.Env{})
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		all[i] = ds
	}
	if l, ok := m.(manager.Linker); ok {
		l.Link(all)
	}
	out := map[string][]dep.Dep{}
	for i, f := range files {
		out[f] = all[i]
	}
	return out, srcs
}

// summary renders a dep as `name version [type] datasource @file:line (skip)`.
func summary(d dep.Dep) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s [%s] %s", d.Name, d.Version, d.DepType, d.Datasource)
	if d.VersionRef != "" {
		fmt.Fprintf(&b, " ref=%s", d.VersionRef)
	}
	fmt.Fprintf(&b, " @%s:%d", d.File, d.Line)
	if d.Skip != "" {
		fmt.Fprintf(&b, " (%s)", d.Skip)
	}
	return b.String()
}
