package cargo

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestLockerScopeInputs(t *testing.T) {
	root := t.TempDir()
	write := func(files map[string]string) {
		t.Helper()
		for rel, content := range files {
			p := filepath.Join(root, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	write(map[string]string{
		".cargo/config.toml":           "",
		"rust-toolchain.toml":          "",
		".config/mise.toml":            "",
		"ws/Cargo.toml":                "[workspace]\nmembers = [\"crates/*\"]\n\n[workspace.dependencies]\nshared = { path = \"../shared\" }\ninner = { path = \"crates/inner\" }\n",
		"ws/Cargo.lock":                "",
		"ws/.cargo/config.toml":        "",
		"ws/crates/a/Cargo.toml":       "[package]\nname = \"a\"\n\n[target.'cfg(unix)'.dependencies]\nother = { path = \"../../../other\", version = \"1\" }\n",
		"ws/crates/a/src/lib.rs":       "",
		"ws/crates/inner/Cargo.toml":   "[package]\nname = \"inner\"\n",
		"ws/target/debug/Cargo.toml":   "",
		"ws/node_modules/x/Cargo.toml": "",
		"shared/Cargo.toml":            "[package]\nname = \"shared\"\n",
	})
	inputs := func() ([]string, []string) {
		t.Helper()
		paths, digests, err := Locker{}.ScopeInputs(t.Context(), root, "ws", nil)
		if err != nil {
			t.Fatal(err)
		}
		slices.Sort(paths)
		return paths, digests
	}
	paths, digests := inputs()
	want := []string{".cargo/config.toml", ".config/mise.toml", "other", "rust-toolchain.toml", "shared", "ws/.cargo/config.toml", "ws/Cargo.toml", "ws/crates/a/Cargo.toml", "ws/crates/inner/Cargo.toml"}
	if !slices.Equal(paths, want) {
		t.Errorf("paths = %q, want %q", paths, want)
	}
	if len(digests) != 1 || !strings.HasPrefix(digests[0], "cargo files ws ") {
		t.Fatalf("digests = %q", digests)
	}

	// Content edits of other files keep the digest, new files and renames change it.
	write(map[string]string{"ws/crates/a/src/lib.rs": "pub fn f() {}\n", "ws/target/debug/x": ""})
	if _, d := inputs(); !slices.Equal(d, digests) {
		t.Error("a source edit changed the digest")
	}
	write(map[string]string{"ws/crates/a/src/main.rs": ""})
	if _, d := inputs(); slices.Equal(d, digests) {
		t.Error("a new file kept the digest")
	}

	write(map[string]string{"ws/crates/a/Cargo.toml": "[dependencies]\nx = { path = \"/abs/x\" }\n"})
	if _, _, err := (Locker{}).ScopeInputs(t.Context(), root, "ws", nil); err == nil {
		t.Error("an absolute path dependency was accepted")
	}
}
