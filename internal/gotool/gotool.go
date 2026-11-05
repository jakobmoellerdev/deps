// Package gotool runs the go command where deps cannot avoid it: go env, go list, go get and
// go mod tidy.
package gotool

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"golang.org/x/mod/modfile"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// DefaultProxy is the GOPROXY used when the go command is unavailable and GOPROXY is unset.
const DefaultProxy = "https://proxy.golang.org,direct"

// GoEnv is the module proxy configuration of the go command.
type GoEnv struct {
	Proxy    string // GOPROXY
	NoProxy  string // GONOPROXY, or GOPRIVATE if GONOPROXY is empty
	ModCache string // GOMODCACHE; "" when unknown
}

// Env reads GOPROXY, GONOPROXY, GOPRIVATE and GOMODCACHE via `go env -json`.
// If go is not in PATH it falls back to the process environment (GOMODCACHE, else
// $GOPATH/pkg/mod, else ~/go/pkg/mod, as the go command defaults).
func Env(ctx context.Context) (GoEnv, error) {
	var v struct{ GOPROXY, GONOPROXY, GOPRIVATE, GOMODCACHE string }
	out, err := run(ctx, "", nil, "env", "-json", "GOPROXY", "GONOPROXY", "GOPRIVATE", "GOMODCACHE")
	switch {
	case errors.Is(err, exec.ErrNotFound):
		v.GOPROXY, v.GONOPROXY, v.GOPRIVATE = os.Getenv("GOPROXY"), os.Getenv("GONOPROXY"), os.Getenv("GOPRIVATE")
		v.GOMODCACHE = defaultModCache()
	case err != nil:
		return GoEnv{}, err
	default:
		if err := json.Unmarshal(out, &v); err != nil {
			return GoEnv{}, fmt.Errorf("go env: %w", err)
		}
	}
	e := GoEnv{Proxy: v.GOPROXY, NoProxy: v.GONOPROXY, ModCache: v.GOMODCACHE}
	if e.Proxy == "" {
		e.Proxy = DefaultProxy
	}
	if e.NoProxy == "" {
		e.NoProxy = v.GOPRIVATE
	}
	return e, nil
}

// defaultModCache mirrors the go command's GOMODCACHE default without running it.
func defaultModCache() string {
	if v := os.Getenv("GOMODCACHE"); v != "" {
		return v
	}
	gopath := os.Getenv("GOPATH")
	if gopath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		gopath = filepath.Join(home, "go")
	}
	// GOPATH may be a list; the module cache lives in the first entry.
	if first, _, _ := strings.Cut(gopath, string(os.PathListSeparator)); first != "" {
		return filepath.Join(first, "pkg", "mod")
	}
	return ""
}

// ListVersions returns the known versions of module path via `go list -m -versions -json`, run in dir.
func ListVersions(ctx context.Context, dir, path string) ([]string, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-versions", "-json", path)
	if err != nil {
		return nil, err
	}
	var m struct{ Versions []string }
	if err := json.Unmarshal(out, &m); err != nil {
		return nil, fmt.Errorf("go list -m -versions %s: %w", path, err)
	}
	return m.Versions, nil
}

// ModuleTime returns the publication time of module path at version via
// `go list -m -json path@version`, run in dir; zero when the origin reports none.
func ModuleTime(ctx context.Context, dir, path, version string) (time.Time, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-json", path+"@"+version)
	if err != nil {
		return time.Time{}, err
	}
	var m struct{ Time *time.Time }
	if err := json.Unmarshal(out, &m); err != nil {
		return time.Time{}, fmt.Errorf("go list -m %s@%s: %w", path, version, err)
	}
	if m.Time == nil {
		return time.Time{}, nil
	}
	return *m.Time, nil
}

// Module is the `go list -m -json` (and proxy `@latest`) view of a module version.
type Module struct {
	Version string
	Time    *time.Time
	Origin  *struct{ Hash string }
}

// Hash returns the commit hash of the module's origin, or "".
func (m Module) Hash() string {
	if m.Origin == nil {
		return ""
	}
	return m.Origin.Hash
}

// ModuleLatest resolves module path@latest via `go list -m -json path@latest`, run in dir.
func ModuleLatest(ctx context.Context, dir, path string) (Module, error) {
	out, err := run(ctx, dir, []string{"GOFLAGS=-mod=mod", "GOWORK=off"}, "list", "-m", "-json", path+"@latest")
	if err != nil {
		return Module{}, err
	}
	var m Module
	if err := json.Unmarshal(out, &m); err != nil {
		return Module{}, fmt.Errorf("go list -m %s@latest: %w", path, err)
	}
	return m, nil
}

// Tidy runs `go mod tidy` in dir against copies of mod and sum (sum may be nil) and returns
// the tidied contents. overlay holds patched source files by slash path relative to dir
// (import paths rewritten by a major module path update); tidy reads them through
// `-overlay`. The real files in dir are never touched. A failure because a required module
// version declares another module path is a *dep.SkipError (see pathChanged).
func Tidy(ctx context.Context, dir string, mod, sum []byte, overlay map[string][]byte) (newMod, newSum []byte, err error) {
	tmp, err := os.MkdirTemp("", "deps-tidy-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(tmp)
	modPath, sumPath := filepath.Join(tmp, "go.mod"), filepath.Join(tmp, "go.sum")
	if err := os.WriteFile(modPath, mod, 0o644); err != nil {
		return nil, nil, err
	}
	if sum != nil {
		if err := os.WriteFile(sumPath, sum, 0o644); err != nil {
			return nil, nil, err
		}
	}
	args := []string{"mod", "tidy", "-modfile=" + modPath}
	if len(overlay) > 0 {
		arg, err := writeOverlay(tmp, dir, overlay)
		if err != nil {
			return nil, nil, err
		}
		args = append(args, arg)
	}
	if _, err := run(ctx, dir, []string{"GOWORK=off", "GOFLAGS="}, args...); err != nil {
		if skip := pathChanged(err); skip != nil {
			slog.InfoContext(ctx, "go mod tidy: a required module declares another path", "dir", dir, "reason", skip.Reason)
			return nil, nil, skip
		}
		return nil, nil, inDir("go mod tidy", dir, err)
	}
	if newMod, err = os.ReadFile(modPath); err != nil {
		return nil, nil, err
	}
	if newSum, err = os.ReadFile(sumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return newMod, newSum, nil
}

// pathMismatch is the go command's refusal of a module version whose go.mod declares another
// module path: `<path>@<version>: parsing go.mod:` followed by `module declares its path as:
// <declared>` and `but was required as: <path>`.
var pathMismatch = regexp.MustCompile(`(\S+)@([^\s:]+): parsing go\.mod:\s+module declares its path as: (\S+)\s+but was required as: (\S+)`)

// pathChanged returns the skip of a go command failure caused by a required module version
// that declares another module path (the module was renamed upstream, or a fork kept its
// origin's path): no tidy of this go.mod can succeed until it requires the declared path, so
// it is the update's skip, not a failure. nil for any other failure.
func pathChanged(err error) *dep.SkipError {
	var ce *cmdError
	if !errors.As(err, &ce) {
		return nil
	}
	m := pathMismatch.FindStringSubmatch(ce.stderr)
	if len(m) != 5 || m[1] != m[4] {
		return nil
	}
	return &dep.SkipError{Reason: fmt.Sprintf("module path changed to %s (%s@%s)", m[3], m[1], m[2])}
}

// writeOverlay writes the overlay files and their `-overlay` JSON (absolute paths of dir's
// files to the copies) into tmp and returns the flag.
func writeOverlay(tmp, dir string, files map[string][]byte) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	replace := make(map[string]string, len(files))
	n := 0
	for rel, b := range files {
		n++
		p := filepath.Join(tmp, fmt.Sprintf("overlay-%d.go", n))
		if err := os.WriteFile(p, b, 0o644); err != nil {
			return "", err
		}
		replace[filepath.Join(abs, filepath.FromSlash(rel))] = p
	}
	b, err := json.Marshal(struct{ Replace map[string]string }{replace})
	if err != nil {
		return "", err
	}
	p := filepath.Join(tmp, "overlay.json")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return "", err
	}
	return "-overlay=" + p, nil
}

// Tidier is the gomod post-processor (engine.PostProcessor): Tidy on copies of go.mod and
// go.sum, with patched .go files (import path rewrites) as an overlay.
type Tidier struct{}

// Scope is the module directory: every go.mod is post-processed on its own, together with
// the patched .go files of its module (the nearest go.mod at or above their directory).
func (Tidier) Scope(root, manifest string) (string, bool) {
	dir, name, ok := strings.CutLast(manifest, "/")
	if !ok {
		dir, name = ".", manifest
	}
	if name == "go.mod" {
		return dir, true
	}
	for {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(dir), "go.mod")); err == nil {
			return dir, true
		}
		if dir == "." {
			return "", false
		}
		if dir, _, ok = strings.CutLast(dir, "/"); !ok {
			dir = "."
		}
	}
}

// Siblings lists go.sum.
func (Tidier) Siblings() []string { return []string{"go.sum"} }

// ScopeInputs implements engine.ScopeInputLister: tidy also reads the module directories that
// dir's go.mod replaces modules with (`replace m => ../m`), as slash paths relative to root.
// An absolute replacement path cannot be named relative to root and is an error.
func (Tidier) ScopeInputs(root, dir string) ([]string, error) {
	file := filepath.Join(root, filepath.FromSlash(dir), "go.mod")
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(file, b, nil) // ParseLax drops replace directives
	if err != nil {
		return nil, err
	}
	var out []string
	for _, r := range f.Replace {
		if r.New.Version != "" || !modfile.IsDirectoryPath(r.New.Path) {
			continue
		}
		if filepath.IsAbs(r.New.Path) {
			return nil, fmt.Errorf("replace %s => %s: absolute path", r.Old.Path, r.New.Path)
		}
		out = append(out, filepath.ToSlash(filepath.Join(filepath.FromSlash(dir), r.New.Path)))
	}
	return out, nil
}

// Process tidies the patched go.mod and returns the tidied go.mod and go.sum (nil when tidy
// wrote none). Other manifests are patched .go files of the module, passed as the overlay.
func (Tidier) Process(ctx context.Context, dir string, manifests, files map[string][]byte) (map[string][]byte, error) {
	mod, ok := manifests["go.mod"]
	if !ok {
		return nil, errors.New("go.mod of the module was not patched")
	}
	var overlay map[string][]byte
	if len(manifests) > 1 {
		overlay = make(map[string][]byte, len(manifests)-1)
		for rel, b := range manifests {
			if rel != "go.mod" {
				overlay[rel] = b
			}
		}
	}
	newMod, newSum, err := Tidy(ctx, dir, mod, files["go.sum"], overlay)
	if err != nil {
		return nil, err
	}
	return map[string][]byte{"go.mod": newMod, "go.sum": newSum}, nil
}

// Maintain implements engine.LockMaintainer (lockFileMaintenance): every `// indirect`
// requirement of dir's go.mod without a replace directive moves to its newest release
// (`go get -modfile=<tmp>/go.mod <module>@upgrade ...`, then Tidy), on temporary copies of
// go.mod and go.sum with the environment of Tidy. The go and toolchain directives and the
// direct requirements stay: an attempt that moves them, or that go rejects as a version
// conflict, is split in halves and retried on the result of the first half, so only a module
// whose upgrade conflicts on its own keeps its version. It returns go.mod and go.sum when
// they changed; without go.sum or indirect requirements it does nothing.
func (Tidier) Maintain(ctx context.Context, dir string, files map[string][]byte) (map[string][]byte, error) {
	sum, ok := files["go.sum"]
	if !ok {
		return nil, nil
	}
	mod, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return nil, err
	}
	f, err := modfile.Parse(filepath.Join(dir, "go.mod"), mod, nil)
	if err != nil {
		return nil, err
	}
	var mods []string
	for _, r := range f.Require {
		if r.Indirect && !slices.ContainsFunc(f.Replace, func(rp *modfile.Replace) bool { return rp.Old.Path == r.Mod.Path }) {
			mods = append(mods, r.Mod.Path)
		}
	}
	if len(mods) == 0 {
		return nil, nil
	}
	tmp, err := os.MkdirTemp("", "deps-maintain-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	u := upgrader{dir: dir, mod: filepath.Join(tmp, "go.mod"), sum: filepath.Join(tmp, "go.sum"), fixed: fixed(f)}
	if err := u.write(mod, sum); err != nil {
		return nil, err
	}
	if err := u.upgrade(ctx, mods); err != nil {
		return nil, err
	}
	upMod, upSum, err := u.read()
	if err != nil || bytes.Equal(upMod, mod) {
		return nil, err
	}
	newMod, newSum, err := Tidy(ctx, dir, upMod, upSum, nil)
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	if !bytes.Equal(newMod, mod) {
		out["go.mod"] = newMod
	}
	if !bytes.Equal(newSum, sum) {
		out["go.sum"] = newSum
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// conflictRe matches the go get errors of a version conflict between requested modules
// (`go: a@upgrade (v1.1.0) requires b@v1.5.0, not b@upgrade (v1.4.0)`) and of a requested
// version needing a newer go than the running one (`go: a@upgrade: a@v1.1.0 requires go >= 1.30`).
var conflictRe = regexp.MustCompile(`(?m)^go: \S+@.* requires (\S+, not |go >= )`)

// upgrader runs the go get attempts of Maintain on the go.mod and go.sum copies mod and sum.
type upgrader struct {
	dir, mod, sum string
	fixed         string // fixed of the original go.mod
}

// fixed renders what lock file maintenance must not move: the go and toolchain directives
// and the direct requirements.
func fixed(f *modfile.File) string {
	var lines []string
	if f.Go != nil {
		lines = append(lines, "go "+f.Go.Version)
	}
	if f.Toolchain != nil {
		lines = append(lines, "toolchain "+f.Toolchain.Name)
	}
	for _, r := range f.Require {
		if !r.Indirect {
			lines = append(lines, r.Mod.String())
		}
	}
	slices.Sort(lines)
	return strings.Join(lines, "\n")
}

// upgrade moves mods to @upgrade: all at once or, on a conflict, each half in turn. A single
// conflicting module keeps its version.
func (u *upgrader) upgrade(ctx context.Context, mods []string) error {
	ok, err := u.try(ctx, mods)
	if err != nil || ok {
		return err
	}
	if len(mods) == 1 {
		slog.DebugContext(ctx, "lock file maintenance keeps a conflicting module", "dir", u.dir, "module", mods[0])
		return nil
	}
	h := len(mods) / 2
	if err := u.upgrade(ctx, mods[:h]); err != nil {
		return err
	}
	return u.upgrade(ctx, mods[h:])
}

// try runs `go get <mods>@upgrade` on the copies and reports whether it kept the fixed part of
// go.mod; a conflict restores the copies. Other go failures are errors.
func (u *upgrader) try(ctx context.Context, mods []string) (bool, error) {
	mod, sum, err := u.read()
	if err != nil {
		return false, err
	}
	args := make([]string, 0, len(mods)+2)
	args = append(args, "get", "-modfile="+u.mod)
	for _, m := range mods {
		args = append(args, m+"@upgrade")
	}
	var ok bool
	if _, err := run(ctx, u.dir, []string{"GOWORK=off", "GOFLAGS="}, args...); err != nil {
		var ce *cmdError
		if !errors.As(err, &ce) || !conflictRe.MatchString(ce.stderr) {
			return false, inDir("go get", u.dir, err)
		}
	} else {
		b, err := os.ReadFile(u.mod)
		if err != nil {
			return false, err
		}
		f, err := modfile.Parse(u.mod, b, nil)
		if err != nil {
			return false, err
		}
		ok = fixed(f) == u.fixed
	}
	if !ok {
		return false, u.write(mod, sum)
	}
	return true, nil
}

func (u *upgrader) read() (mod, sum []byte, err error) {
	if mod, err = os.ReadFile(u.mod); err != nil {
		return nil, nil, err
	}
	if sum, err = os.ReadFile(u.sum); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return mod, sum, nil
}

func (u *upgrader) write(mod, sum []byte) error {
	if err := os.WriteFile(u.mod, mod, 0o644); err != nil {
		return err
	}
	return os.WriteFile(u.sum, sum, 0o644)
}

// cmdError is a failed go command with the tail of its stderr.
type cmdError struct {
	args   string
	stderr string
	err    error
}

func (e *cmdError) Error() string {
	if e.stderr == "" {
		return fmt.Sprintf("go %s: %v", e.args, e.err)
	}
	return fmt.Sprintf("go %s: %s", e.args, e.stderr)
}

func (e *cmdError) Unwrap() error { return e.err }

// inDir wraps err of the go command what run in dir, with go's stderr when it wrote any.
func inDir(what, dir string, err error) error {
	var ce *cmdError
	if errors.As(err, &ce) && ce.stderr != "" {
		return fmt.Errorf("%s in %s: %s", what, dir, ce.stderr)
	}
	return fmt.Errorf("%s in %s: %w", what, dir, err)
}

// run executes go with args in dir, with env appended to the process environment, and returns stdout.
func run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout bytes.Buffer
	stderr := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = &stdout, stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "go command", "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, err
		}
		return nil, &cmdError{args: strings.Join(args, " "), stderr: toolrun.Tail(stderr.Bytes(), 20), err: err}
	}
	return stdout.Bytes(), nil
}
