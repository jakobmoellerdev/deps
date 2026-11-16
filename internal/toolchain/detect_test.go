package toolchain

import (
	"maps"
	"os"
	"path/filepath"
	"testing"
)

func repo(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestDetect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files map[string]string
		want  map[Tool]string
	}{
		{name: "empty", want: map[Tool]string{}},
		{
			name:  "go.mod toolchain wins over the language version",
			files: map[string]string{"go.mod": "module m\n\ngo 1.24.0\n\ntoolchain go1.27.4\n"},
			want:  map[Tool]string{Go: "1.27.4"},
		},
		{
			name:  "go.mod without a toolchain line",
			files: map[string]string{"go.mod": "module m\n\ngo 1.24\n"},
			want:  map[Tool]string{Go: "1.24"},
		},
		{
			name:  "node from .nvmrc",
			files: map[string]string{".nvmrc": "v24.11.0\n"},
			want:  map[Tool]string{Node: "24.11.0"},
		},
		{
			name:  "node lts line stays for the resolver",
			files: map[string]string{".nvmrc": "lts/*\n"},
			want:  map[Tool]string{Node: "lts/*"},
		},
		{
			name:  "engines.node range is no version",
			files: map[string]string{"package.json": `{"engines":{"node":">=20"}}`},
			want:  map[Tool]string{},
		},
		{
			name:  "packageManager pins bun",
			files: map[string]string{"package.json": `{"packageManager":"bun@1.1.38"}`},
			want:  map[Tool]string{Bun: "1.1.38"},
		},
		{
			name:  "packageManager pnpm is installed through node, not on its own",
			files: map[string]string{"package.json": `{"packageManager":"pnpm@9.12.0"}`},
			want:  map[Tool]string{},
		},
		{
			name:  ".tool-versions covers every ecosystem",
			files: map[string]string{".tool-versions": "# comment\ngolang 1.27.4\nnodejs 24.11.0\njava temurin-21.0.5+11\nterraform 1.9.8\n"},
			want:  map[Tool]string{Go: "1.27.4", Node: "24.11.0", Java: "21.0.5+11", Terraform: "1.9.8"},
		},
		{
			name:  ".tool-versions wins over the ecosystem file",
			files: map[string]string{".tool-versions": "nodejs 22.11.0\n", ".nvmrc": "24.11.0\n"},
			want:  map[Tool]string{Node: "22.11.0"},
		},
		{
			name:  "rust-toolchain.toml channel",
			files: map[string]string{"rust-toolchain.toml": "[toolchain]\nchannel = \"1.83.0\"\ncomponents = [\"rustfmt\"]\n"},
			want:  map[Tool]string{Rust: "1.83.0"},
		},
		{
			name:  "rust stable channel names no release",
			files: map[string]string{"rust-toolchain.toml": "[toolchain]\nchannel = \"stable\"\n"},
			want:  map[Tool]string{},
		},
		{
			name:  "plain rust-toolchain file",
			files: map[string]string{"rust-toolchain": "1.82.0\n"},
			want:  map[Tool]string{Rust: "1.82.0"},
		},
		{
			name:  "global.json pins the .NET SDK",
			files: map[string]string{"global.json": `{"sdk":{"version":"9.0.101","rollForward":"latestFeature"}}`},
			want:  map[Tool]string{DotNet: "9.0.101"},
		},
		{
			name:  "system means the machine's own",
			files: map[string]string{".java-version": "system\n"},
			want:  map[Tool]string{},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Detect(repo(t, tc.files))
			if !maps.Equal(got, tc.want) {
				t.Errorf("Detect = %v, want %v", got, tc.want)
			}
		})
	}
}
