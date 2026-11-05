package helm

import (
	"bytes"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// repoYAML returns the slash path and content of every YAML file of this repository.
func repoYAML(tb testing.TB) (paths []string, srcs [][]byte) {
	tb.Helper()
	root := filepath.Join("..", "..", "..")
	if err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == ".git" {
			return filepath.SkipDir
		}
		if !d.Type().IsRegular() || !strings.HasSuffix(p, ".yaml") && !strings.HasSuffix(p, ".yml") {
			return nil
		}
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, p)
		paths, srcs = append(paths, filepath.ToSlash(rel)), append(srcs, src)
		return nil
	}); err != nil {
		tb.Fatal(err)
	}
	return paths, srcs
}

// BenchmarkManifests runs the custom resource extraction over every YAML file of this
// repository; most of them have none of the API groups.
func BenchmarkManifests(b *testing.B) {
	paths, srcs := repoYAML(b)
	for b.Loop() {
		m := &helmManager{}
		for i, p := range paths {
			m.manifests(p, srcs[i])
		}
	}
}

// crdMarkers and markerScan are the prefilter hasCRDGroup replaces: one scan per marker.
var crdMarkers = [][]byte{[]byte("k0sproject.io/"), []byte("fluxcd.io/"), []byte("argoproj.io/")}

func markerScan(src []byte) bool {
	return slices.ContainsFunc(crdMarkers, func(b []byte) bool { return bytes.Contains(src, b) })
}

var crdGroupSeeds = []string{
	"", ".io/", "k0sproject.io/", "apiVersion: helm.toolkit.fluxcd.io/v2", "argoproj.io", "argoproj.io/",
	"a.io/fluxcd.io/", "fluxcd.io.io/", "k0sproject.ioo/", "app.kubernetes.io/name: x\nargoproj.io/v1",
}

func TestHasCRDGroupParity(t *testing.T) {
	paths, srcs := repoYAML(t)
	for _, s := range crdGroupSeeds {
		paths, srcs = append(paths, s), append(srcs, []byte(s))
	}
	for i, src := range srcs {
		if got, want := hasCRDGroup(src), markerScan(src); got != want {
			t.Errorf("%s: hasCRDGroup = %v, marker scan says %v", paths[i], got, want)
		}
	}
}

func FuzzHasCRDGroup(f *testing.F) {
	for _, s := range crdGroupSeeds {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, src []byte) {
		if got, want := hasCRDGroup(src), markerScan(src); got != want {
			t.Errorf("hasCRDGroup(%q) = %v, marker scan says %v", src, got, want)
		}
	})
}
