package cargo

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/tomlspan"
)

// ScopeInputs implements engine.ScopeInputLister. cargo update runs in a mirror of the
// workspace root dir that copies its Cargo.toml files and symlinks everything else (mirror),
// so it reads:
//   - every Cargo.toml below dir, as the mirror walks it (.git, target and node_modules left
//     out), with the directory of each path dependency outside dir, by its tree;
//   - .cargo/config, .cargo/config.toml, rust-toolchain and rust-toolchain.toml in dir and its
//     ancestors up to root, where cargo and rustup look for them;
//   - of the other files below dir only their paths, as one digest: cargo needs their
//     existence (auto-discovered targets, `path` and `build` entries) but not their content,
//     so a source edit keeps the inputs and an added, removed or renamed file changes them.
func (Locker) ScopeInputs(_ context.Context, root, dir string, _ []string) (paths, digests []string, err error) {
	abs := filepath.Join(root, filepath.FromSlash(dir))
	h := sha256.New()
	err = filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(abs, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		switch {
		case d.IsDir() && rel != "." && (d.Name() == ".git" || d.Name() == "target" || d.Name() == "node_modules"):
			return filepath.SkipDir
		case d.IsDir(), rel == lockFile:
			return nil
		case d.Name() != manifestFile:
			h.Write([]byte(rel + "\n"))
			return nil
		}
		paths = append(paths, path.Join(dir, rel))
		src, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		for _, dep := range pathDeps(src) {
			if path.IsAbs(dep) || filepath.IsAbs(dep) {
				return fmt.Errorf("%s: absolute path dependency %s", rel, dep)
			}
			if target := path.Join(path.Dir(rel), dep); target == ".." || strings.HasPrefix(target, "../") {
				paths = append(paths, path.Join(dir, target))
			}
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	for d := dir; ; d = path.Dir(d) {
		for _, n := range []string{".cargo/config", ".cargo/config.toml", "rust-toolchain", "rust-toolchain.toml"} {
			p := path.Join(d, n)
			if st, err := os.Stat(filepath.Join(root, filepath.FromSlash(p))); err == nil && !st.IsDir() {
				paths = append(paths, p)
			}
		}
		if d == "." {
			break
		}
	}
	return paths, []string{"cargo files " + dir + " " + hex.EncodeToString(h.Sum(nil))}, nil
}

// pathDeps returns the `path` values of the dependency entries of the Cargo.toml src (the
// tables Extract reads); a manifest that does not parse has none, cargo reports it.
func pathDeps(src []byte) []string {
	doc, err := tomlspan.Parse(src)
	if err != nil {
		return nil
	}
	var out []string
	for _, t := range depTables(doc) {
		for _, k := range t.Keys {
			if v := t.Fields[k]; v.Kind == tomlspan.Table {
				if p := v.Fields["path"]; p != nil && p.Kind == tomlspan.String {
					out = append(out, p.Str)
				}
			}
		}
	}
	return out
}
