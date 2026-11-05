package gitx

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
)

func benchWriteTreeGit(b *testing.B, dir string, args ...string) string {
	b.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		b.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func BenchmarkWriteTreeHashStdinPaths(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git not in PATH")
	}
	b.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	b.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	b.Setenv("GIT_AUTHOR_NAME", "Tester")
	b.Setenv("GIT_AUTHOR_EMAIL", "tester@example.com")
	b.Setenv("GIT_COMMITTER_NAME", "Tester")
	b.Setenv("GIT_COMMITTER_EMAIL", "tester@example.com")
	dir := b.TempDir()
	benchWriteTreeGit(b, dir, "init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "README"), []byte("readme\n"), 0o644); err != nil {
		b.Fatal(err)
	}
	benchWriteTreeGit(b, dir, "add", ".")
	benchWriteTreeGit(b, dir, "commit", "-q", "-m", "init")
	parent := benchWriteTreeGit(b, dir, "rev-parse", "HEAD")
	r := Repo{Dir: dir}
	for _, n := range []int{1, 3, 8} {
		changes := make([]engine.FileChange, n)
		for i := range changes {
			changes[i] = engine.FileChange{Path: fmt.Sprintf("f%d.txt", i), New: fmt.Appendf(nil, "content %d\n", i), Mode: 0o644}
		}
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			for b.Loop() {
				if _, err := r.WriteTree(b.Context(), parent, changes); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
