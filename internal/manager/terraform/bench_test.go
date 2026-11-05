package terraform

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// BenchmarkExtract extracts every fixture its manager matches, per syntax: Terraform HCL
// (*.tf), Terraform JSON (*.tf.json) and the other HCL files (terragrunt, tflint).
func BenchmarkExtract(b *testing.B) {
	type file struct {
		path string
		src  []byte
		mgr  manager.Manager
	}
	groups := map[string][]file{}
	mgrs := []manager.Manager{New(), NewTerragrunt(), NewVersion(), NewTFLint()}
	err := filepath.WalkDir("testdata", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel := filepath.ToSlash(p)
		for _, m := range mgrs {
			if !m.Match(rel) {
				continue
			}
			src, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			group := "other"
			switch {
			case strings.HasSuffix(rel, ".tf.json"):
				group = "json"
			case strings.HasSuffix(rel, ".tf"):
				group = "hcl"
			}
			groups[group] = append(groups[group], file{rel, src, m})
		}
		return nil
	})
	if err != nil {
		b.Fatal(err)
	}
	for _, group := range []string{"hcl", "json", "other"} {
		files := groups[group]
		if len(files) == 0 {
			b.Fatalf("no %s fixtures", group)
		}
		b.Run(group, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				for _, f := range files {
					if _, err := f.mgr.Extract(f.path, f.src, manager.Env{}); err != nil {
						b.Fatal(f.path, err)
					}
				}
			}
		})
	}
}
