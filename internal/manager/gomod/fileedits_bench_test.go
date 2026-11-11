package gomod

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// moduleTree writes a module of dirs packages with files .go files each, one of which imports
// the module whose path is updated.
func moduleTree(tb testing.TB, dirs, files int) string {
	tb.Helper()
	root := tb.TempDir()
	body := "package p\n\nimport (\n\t\"fmt\"\n\t\"strings\"\n)\n\n" +
		strings.Repeat("// A line of a file that does not mention the updated module at all.\n", 30) +
		"func F() string { return fmt.Sprint(strings.TrimSpace(\" x \")) }\n"
	write := func(p, content string) {
		abs := filepath.Join(root, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			tb.Fatal(err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			tb.Fatal(err)
		}
	}
	write("mod/go.mod", "module example.com/x\n\nrequire github.com/google/go-github/v92 v92.0.0\n")
	for d := range dirs {
		for f := range files {
			write(fmt.Sprintf("mod/pkg%d/f%d.go", d, f), body)
		}
	}
	write("mod/pkg0/uses.go", "package p\n\nimport gh \"github.com/google/go-github/v92/github\"\n\nvar _ = gh.Client{}\n")
	return root
}

// BenchmarkFileEdits rewrites the import paths of a module in a tree of 200 packages, as a
// group with several major path updates or replacements of the same module does.
func BenchmarkFileEdits(b *testing.B) {
	root := moduleTree(b, 200, 8)
	fe := NewCached(NewScanCache()).(manager.FileEditor)
	u := dep.Update{
		Dep:        dep.Dep{Manager: Name, File: "mod/go.mod", Name: "github.com/google/go-github/v92", Version: "v92.0.0"},
		Type:       dep.Major,
		NewVersion: "v93.0.0",
		NewName:    "github.com/google/go-github/v93",
	}
	b.ReportAllocs()
	for b.Loop() {
		out, err := fe.FileEdits(root, u)
		if err != nil || len(out) != 1 {
			b.Fatalf("FileEdits = %v, %v", out, err)
		}
	}
}
