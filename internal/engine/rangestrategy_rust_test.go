package engine

import (
	"context"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/cargo"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/rusttoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// rustReleases is what the rust-version datasource lists: stable releases, numbered betas and
// dated nightlies in index order, then the derived release lines.
var rustReleases = fakeGo{"rust": {
	"nightly-2025-10-10", "1.82.0", "1.83.0-beta.2", "nightly-2025-12-31", "1.99.0", "1.99.1", "1.100.0-beta.10", "nightly-2026-11-05",
	"1.82", "1.99",
}}

// TestRustToolchainChannels resolves the channels of rust-toolchain files: a dated nightly
// moves to the newest nightly in one patch update (a new year is no major), `stable` and
// `nightly` are up to date unless rangeStrategy pin writes the newest release they admit.
func TestRustToolchainChannels(t *testing.T) {
	files := map[string]string{
		"stable/rust-toolchain.toml": "[toolchain]\nchannel = \"stable\"\ncomponents = [\"clippy\"]\n",
		"nightly/rust-toolchain":     "nightly-x86_64-unknown-linux-gnu\n",
		"dated/rust-toolchain":       "nightly-2025-10-10\n",
		"beta/rust-toolchain":        "beta\n",
		"pinned/rust-toolchain":      "1.82.0\n",
	}
	for _, tt := range []struct {
		name, cfg string
		want      []string
		edited    map[string]string
	}{
		{
			name: "auto",
			want: []string{"rust 1.82.0->1.99.1 minor", "rust nightly-2025-10-10->nightly-2026-11-05 patch"},
		},
		{
			name: "pin", cfg: "rules: [{managers: [rust-toolchain], rangeStrategy: pin}]\n",
			want: []string{
				"rust 1.82.0->1.99.1 minor", "rust nightly-2025-10-10->nightly-2026-11-05 patch",
				"rust nightly->nightly-2026-11-05 pin", "rust stable->1.99.1 pin",
			},
			edited: map[string]string{
				"stable/rust-toolchain.toml": "[toolchain]\nchannel = \"1.99.1\"\ncomponents = [\"clippy\"]\n",
				"nightly/rust-toolchain":     "nightly-2026-11-05-x86_64-unknown-linux-gnu\n",
			},
		},
		{
			name: "replace", cfg: "rangeStrategy: replace\n",
			want: []string{"rust 1.82.0->1.99.1 minor", "rust nightly-2025-10-10->nightly-2026-11-05 patch"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root, cfg := setup(t, tt.cfg, maps.Clone(files))
			o := Options{Managers: []manager.Manager{rusttoolchain.New()}, Datasources: map[string]Datasource{rusttoolchain.Datasource: rustReleases}}
			res, err := Resolve(t.Context(), root, cfg, env, o)
			if err != nil || len(res.Errors) > 0 {
				t.Fatal(err, res.Errors)
			}
			got := strings.Split(renderUpdates(res.Updates), "\n")
			slices.Sort(got)
			if strings.Join(got, "\n") != strings.Join(tt.want, "\n") {
				t.Errorf("updates =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
			for _, u := range res.Updates {
				want, ok := tt.edited[u.Dep.File]
				if !ok {
					continue
				}
				local, _ := Local([]dep.Update{u})
				changes, errs := Build(t.Context(), root, o.Managers, local, nil)
				if len(errs) > 0 || len(changes) != 1 {
					t.Fatal(changes, errs)
				}
				if got := string(changes[0].New); got != want {
					t.Errorf("%s =\n%s\nwant\n%s", u.Dep.File, got, want)
				}
			}
		})
	}
}

// The post-processors of a group read the group's other files as patched: cargo resolves
// Cargo.lock with the toolchain a grouped rust-toolchain update writes.
func TestBuildPatchedFiles(t *testing.T) {
	root, _ := setup(t, "", map[string]string{"rust-toolchain": "1.82.0\n", "Cargo.toml": "[dependencies]\nanyhow = \"1.0.0\"\n"})
	managers := []manager.Manager{rusttoolchain.New(), cargo.New()}
	var ups []dep.Update
	for _, f := range []struct {
		m          manager.Manager
		file, next string
	}{{managers[0], "rust-toolchain", "1.99.1"}, {managers[1], "Cargo.toml", "1.0.100"}} {
		src, err := os.ReadFile(filepath.Join(root, f.file))
		if err != nil {
			t.Fatal(err)
		}
		deps, err := f.m.Extract(f.file, src, manager.Env{})
		if err != nil || len(deps) != 1 {
			t.Fatal(deps, err)
		}
		ups = append(ups, dep.Update{Dep: deps[0], NewVersion: f.next})
	}
	var seen string
	post := map[string]PostProcessor{cargo.Name: postFunc{f: func(ctx context.Context, dir string, _, _ map[string][]byte) (map[string][]byte, error) {
		b, err := toolrun.ReadFile(ctx, filepath.Join(dir, "rust-toolchain"))
		seen = string(b)
		return nil, err
	}}}
	for _, tc := range []struct {
		ups  []dep.Update
		want string
	}{{ups, "1.99.1\n"}, {ups[1:], "1.82.0\n"}} {
		if _, errs := Build(t.Context(), root, managers, tc.ups, post); len(errs) > 0 {
			t.Fatal(errs)
		}
		if seen != tc.want {
			t.Errorf("post-processor read rust-toolchain %q, want %q", seen, tc.want)
		}
	}
}
