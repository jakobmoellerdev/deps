package cargo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// The toolchain cargo runs with is the pinned rust of a mise config at or above the workspace
// root, else the nearest rust-toolchain file's version, read as the build patched them.
func TestRustupToolchain(t *testing.T) {
	for _, tc := range []struct {
		name    string
		files   map[string]string
		patched map[string]string
		want    string
	}{
		{"none", nil, nil, ""},
		{"top toolchain file", map[string]string{"rust-toolchain.toml": "[toolchain]\nchannel = \"1.82.0\"\n"}, nil, "1.82.0"},
		{"legacy file first", map[string]string{"ws/rust-toolchain": "1.80\n", "ws/rust-toolchain.toml": "[toolchain]\nchannel = \"1.82.0\"\n"}, nil, "1.80"},
		{"nearest wins", map[string]string{"rust-toolchain": "1.80.0\n", "ws/rust-toolchain.toml": "[toolchain]\nchannel = \"nightly-2025-10-10\"\n"}, nil, "nightly-2025-10-10"},
		{"channel hides parent", map[string]string{"rust-toolchain": "1.80.0\n", "ws/rust-toolchain": "stable\n"}, nil, ""},
		{"above the repository top", map[string]string{"../rust-toolchain": "1.80.0\n"}, nil, ""},
		{"patched by the branch", map[string]string{"rust-toolchain": "1.80.0\n"}, map[string]string{"rust-toolchain": "1.99.1\n"}, "1.99.1"},
		{"mise first", map[string]string{"ws/rust-toolchain": "1.80.0\n", ".config/mise.toml": "[tools]\nrust = \"1.82\"\n"}, nil, "1.82"},
		{"mise channel", map[string]string{"ws/rust-toolchain": "1.80.0\n", "mise.toml": "[tools]\nrust = \"stable\"\nnode = \"22\"\n"}, nil, "1.80.0"},
		{"broken mise config", map[string]string{"ws/rust-toolchain": "1.80.0\n", "mise.toml": "[tools\n"}, nil, "1.80.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parent := t.TempDir()
			repo := filepath.Join(parent, "repo")
			write(t, repo, map[string]string{".git/HEAD": "", "ws/Cargo.toml": ""})
			write(t, repo, tc.files)
			ctx := t.Context()
			if tc.patched != nil {
				files := map[string][]byte{}
				for p, c := range tc.patched {
					files[filepath.Join(repo, p)] = []byte(c)
				}
				ctx = toolrun.WithPatched(ctx, files)
			}
			if got := rustupToolchain(ctx, filepath.Join(repo, "ws")); got != tc.want {
				t.Errorf("toolchain = %q, want %q", got, tc.want)
			}
		})
	}
	if dirs := repoDirs("/"); len(dirs) != 1 || dirs[0] != "/" {
		t.Errorf("repoDirs(/) = %v", dirs)
	}
}

// cargo runs with the toolchain: a grouped toolchain update moves the one it resolves with.
func TestProcessToolchain(t *testing.T) {
	log := stub(t)
	repo := t.TempDir()
	manifest := "[dependencies]\nanyhow = \"1.0.80\"\n"
	write(t, repo, map[string]string{".git/HEAD": "", "Cargo.toml": manifest, "rust-toolchain.toml": "[toolchain]\nchannel = \"1.82.0\"\n"})
	ctx := toolrun.WithPatched(t.Context(), map[string][]byte{filepath.Join(repo, "rust-toolchain.toml"): []byte("[toolchain]\nchannel = \"1.99.1\"\n")})
	lock := map[string][]byte{"Cargo.lock": []byte(lockOf("anyhow@1.0.80"))}
	if _, err := (Locker{}).Process(ctx, repo, map[string][]byte{"Cargo.toml": []byte(manifest)}, lock); err != nil {
		t.Fatal(err)
	}
	if _, err := (Locker{}).Maintain(t.Context(), repo, lock); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(b), "toolchain 1.99.1\n"); got != 1 || strings.Count(string(b), "toolchain 1.82.0\n") != 1 {
		t.Errorf("log:\n%s", b)
	}
}
