package terraform

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/manager"
)

// TerragruntLocker is the terragrunt post-processor (engine.PostProcessor and
// LockMaintainer) for the .terraform.lock.hcl Terragrunt copies next to a terragrunt.hcl.
// The providers it locks are the module's, which deps does not see, so terragrunt.hcl
// updates (module sources, engines) leave the lock alone, as with Renovate; lock file
// maintenance refreshes it from the lock itself.
type TerragruntLocker struct{}

// Scope returns the directory of a terragrunt.hcl that has a lock file next to it.
func (TerragruntLocker) Scope(ctx context.Context, root, manifest string) (string, bool) {
	return Locker{}.Scope(ctx, root, manifest)
}

// Siblings lists the lock file.
func (TerragruntLocker) Siblings() []string { return []string{LockFile} }

// Process returns nothing: no terragrunt.hcl dep is locked.
func (TerragruntLocker) Process(context.Context, string, map[string][]byte, map[string][]byte) (map[string][]byte, error) {
	return nil, nil
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): every locked provider
// moves to the newest version the `constraints` its lock entry records admit, with fresh
// hashes, as Renovate's maintenance of a terragrunt lock does. `terraform init -upgrade
// -backend=false` (`tofu` when the lock names registry.opentofu.org) runs in an empty
// temporary directory holding the lock and a configuration requiring exactly the locked
// providers under those constraints; the module is not downloaded. It returns the lock when
// it changed; without a lock file it does nothing.
func (TerragruntLocker) Maintain(ctx context.Context, _ string, files map[string][]byte) (map[string][]byte, error) {
	lock, ok := files[LockFile]
	if !ok {
		return nil, nil
	}
	cfg, tofu, err := lockConfig(lock)
	if err != nil || cfg == nil {
		return nil, err
	}
	tool, bin, err := lockTool(tofu)
	if err != nil {
		return nil, err
	}
	work, err := os.MkdirTemp("", "deps-terragrunt-lock-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	if err := errors.Join(
		os.WriteFile(filepath.Join(work, "providers.tf"), cfg, 0o644),
		os.WriteFile(filepath.Join(work, LockFile), lock, 0o644),
	); err != nil {
		return nil, err
	}
	if err := runInit(ctx, bin, work, "-upgrade"); err != nil {
		return nil, fmt.Errorf("%s init for %s: %w", tool, LockFile, err)
	}
	out, err := os.ReadFile(filepath.Join(work, LockFile))
	if err != nil || bytes.Equal(out, lock) {
		return nil, err
	}
	return map[string][]byte{LockFile: out}, nil
}

// lockConfig returns a configuration requiring each provider of lock under the constraints
// its entry records (nil when it locks none), and whether one is an OpenTofu provider.
func lockConfig(lock []byte) ([]byte, bool, error) {
	b, err := parseHCL(lock)
	if err != nil {
		return nil, false, fmt.Errorf("%s: %w", LockFile, err)
	}
	var cfg bytes.Buffer
	tofu := false
	n := 0
	for _, blk := range b.blocks {
		if blk.typ != "provider" || len(blk.labels) != 1 {
			continue
		}
		addr := blk.labels[0]
		tofu = tofu || strings.HasPrefix(strings.ToLower(addr), OpenTofuRegistry+"/")
		if n == 0 {
			cfg.WriteString("terraform {\n  required_providers {\n")
		}
		fmt.Fprintf(&cfg, "    p%d = {\n      source = %s\n", n, strconv.Quote(addr))
		for _, a := range blk.body.attrs {
			if a.name == "constraints" && a.val.kind == kindString {
				fmt.Fprintf(&cfg, "      version = %s\n", strconv.Quote(a.val.str))
			}
		}
		cfg.WriteString("    }\n")
		n++
	}
	if n == 0 {
		return nil, false, nil
	}
	cfg.WriteString("  }\n}\n")
	return cfg.Bytes(), tofu, nil
}

// LockRoots implements manager.LockMaintainer: the .terraform.lock.hcl next to each
// terragrunt.hcl, unless the directory has Terraform configuration files, whose lock the
// terraform manager maintains.
func (terragruntManager) LockRoots(root string, files []string) []manager.LockRoot {
	var out []manager.LockRoot
	for _, f := range files {
		dir, ok := Locker{}.Scope(context.Background(), root, f)
		if !ok || hasConfig(filepath.Join(root, filepath.FromSlash(dir))) {
			continue
		}
		out = append(out, manager.LockRoot{Manifest: f, Lockfile: path.Join(dir, LockFile)})
	}
	slices.SortFunc(out, func(a, b manager.LockRoot) int { return strings.Compare(a.Lockfile, b.Lockfile) })
	return slices.CompactFunc(out, func(a, b manager.LockRoot) bool { return a.Lockfile == b.Lockfile })
}

// hasConfig reports whether dir holds a Terraform or OpenTofu configuration file.
func hasConfig(dir string) bool {
	entries, _ := os.ReadDir(dir)
	return slices.ContainsFunc(entries, func(e os.DirEntry) bool { return !e.IsDir() && isConfigFile(e.Name()) })
}
