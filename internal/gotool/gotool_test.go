package gotool

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTail(t *testing.T) {
	tests := []struct {
		in   string
		n    int
		want string
	}{
		{"a\nb\nc\n", 2, "b\nc"},
		{"a\nb\nc", 5, "a\nb\nc"},
		{"one", 1, "one"},
		{"", 3, ""},
	}
	for _, tt := range tests {
		if got := tail([]byte(tt.in), tt.n); got != tt.want {
			t.Errorf("tail(%q, %d) = %q, want %q", tt.in, tt.n, got, tt.want)
		}
	}
}

// TestTidyOffline tidies a module without requirements, which needs no network.
func TestTidyOffline(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go not in PATH")
	}
	t.Setenv("GOPROXY", "off")
	t.Setenv("GOTOOLCHAIN", "local")
	dir := t.TempDir()
	orig := []byte("module example.com/x\n\ngo 1.21\n")
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), orig, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	mod, sum, err := Tidy(context.Background(), dir, []byte("module   example.com/x\n\n\n\ngo 1.21\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(mod), "module example.com/x\n") {
		t.Errorf("tidied go.mod not reformatted:\n%s", mod)
	}
	if len(sum) != 0 {
		t.Errorf("go.sum = %q, want empty", sum)
	}
	got, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, orig) {
		t.Errorf("real go.mod changed:\n%s", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "go.sum")); !os.IsNotExist(err) {
		t.Errorf("real go.sum created: %v", err)
	}

	_, _, err = Tidy(context.Background(), dir, []byte("this is not a go.mod\n"), nil)
	if err == nil || !strings.HasPrefix(err.Error(), "go mod tidy in "+dir+": ") {
		t.Fatalf("err = %v, want go mod tidy in %s: ...", err, dir)
	}
}
