package cargo

import (
	"context"
	"os"
	"path/filepath"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/rusttoolchain"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager/toolversions"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// toolchainFiles are the toolchain files rustup reads in a directory, in its order.
var toolchainFiles = []string{"rust-toolchain", "rust-toolchain.toml"}

// rustupToolchain returns the toolchain cargo runs with (RUSTUP_TOOLCHAIN) for the workspace
// root dir, Renovate's `rust` constraint of Cargo.lock updates: the `rust` a mise config at or
// above dir pins (mise exports it as RUSTUP_TOOLCHAIN, which rustup prefers to toolchain
// files), else the version the nearest rust-toolchain file pins, looked up as rustup does from
// dir upwards. Both stop at the repository top (the nearest directory holding .git). Files are
// read as the build patched them (toolrun.ReadFile), so crate updates grouped with a toolchain
// update resolve with the new toolchain. "" when nothing pins one: the nearest toolchain file
// names a channel (`stable`), a path or a custom toolchain, which hides those above it, or
// there is none; rustup then picks the toolchain itself.
func rustupToolchain(ctx context.Context, dir string) string {
	dirs := repoDirs(dir)
	for _, d := range dirs {
		for _, n := range toolversions.MiseConfigs {
			src, err := toolrun.ReadFile(ctx, filepath.Join(d, filepath.FromSlash(n)))
			if err != nil {
				continue
			}
			if v, ok := toolversions.MiseTool(src, rusttoolchain.DepName); ok {
				return v
			}
		}
	}
	for _, d := range dirs {
		for _, n := range toolchainFiles {
			if src, err := toolrun.ReadFile(ctx, filepath.Join(d, n)); err == nil {
				v, _ := rusttoolchain.Toolchain(n, src)
				return v
			}
		}
	}
	return ""
}

// repoDirs returns dir and its ancestors up to the repository top (the nearest one holding
// .git), nearest first; up to the filesystem root when dir is in no repository.
func repoDirs(dir string) []string {
	var dirs []string
	for d := dir; ; d = filepath.Dir(d) {
		dirs = append(dirs, d)
		if _, err := os.Lstat(filepath.Join(d, ".git")); err == nil || filepath.Dir(d) == d {
			return dirs
		}
	}
}
