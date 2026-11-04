// Package gotool runs the go command where deps cannot avoid it: go env, go list and go mod tidy.
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
	"strings"
	"time"
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
// `-overlay`. The real files in dir are never touched.
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
		var ce *cmdError
		if errors.As(err, &ce) && ce.stderr != "" {
			return nil, nil, fmt.Errorf("go mod tidy in %s: %s", dir, ce.stderr)
		}
		return nil, nil, fmt.Errorf("go mod tidy in %s: %w", dir, err)
	}
	if newMod, err = os.ReadFile(modPath); err != nil {
		return nil, nil, err
	}
	if newSum, err = os.ReadFile(sumPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, nil, err
	}
	return newMod, newSum, nil
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

// run executes go with args in dir, with env appended to the process environment, and returns stdout.
func run(ctx context.Context, dir string, env []string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "go command", "args", args, "dir", dir, "duration", time.Since(start), "err", err)
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return nil, err
		}
		return nil, &cmdError{args: strings.Join(args, " "), stderr: tail(stderr.Bytes(), 20), err: err}
	}
	return stdout.Bytes(), nil
}

// tail returns the last n lines of b, trimmed.
func tail(b []byte, n int) string {
	b = bytes.TrimSpace(b)
	i := len(b)
	for ; n > 0 && i > 0; n-- {
		i = bytes.LastIndexByte(b[:i], '\n')
		if i < 0 {
			i = 0
			break
		}
	}
	if i > 0 {
		i++ // skip the newline
	}
	return string(b[i:])
}
