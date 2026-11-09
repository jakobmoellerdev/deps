package copier

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"maps"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/yamlspan"
)

// UpdateArgs are the arguments of `copier update` before `--answers-file` and `--vcs-ref`, as
// Renovate passes them: questions answered before keep their answers, new ones take their
// defaults. Like Renovate without allowScripts, deps never passes `--trust`.
var UpdateArgs = []string{"update", "--skip-answered", "--defaults"}

// Updater is the copier post-processor (engine.PostProcessor and engine.TreeRegenerator):
// after deps wrote the new `_commit` it runs `copier update` on a temporary git repository
// holding a copy of the project and returns every file the template update changed,
// including the answers file Copier rewrites.
type Updater struct{}

// Scope is the answers file's directory, the project Copier updates.
func (Updater) Scope(_ context.Context, _, manifest string) (string, bool) {
	return path.Dir(manifest), fileRe.MatchString(manifest)
}

// Siblings lists nothing: every file Copier changes is below the tree ".".
func (Updater) Siblings() []string { return nil }

// Trees implements engine.TreeRegenerator: a template update may change any file of the
// project.
func (Updater) Trees(string) []string { return []string{"."} }

// Process copies the project at dir (the files git does not ignore) into a temporary git
// repository with the answers files as they are in dir, commits it, and runs
// `copier update --skip-answered --defaults --answers-file <file> --vcs-ref <_commit>` for
// each patched answers file in manifests, with the `_commit` deps wrote. It returns the
// files that differ from the copy afterwards (nil for deleted ones). An update that leaves
// merge conflicts fails, naming the files.
func (Updater) Process(ctx context.Context, dir string, manifests, _ map[string][]byte) (map[string][]byte, error) {
	argv, err := copierArgv()
	if err != nil {
		return nil, err
	}
	tmp, err := os.MkdirTemp("", "deps-copier-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	if err := copyProject(ctx, dir, tmp); err != nil {
		return nil, err
	}
	g := gitRunner{dir: tmp, scope: dir}
	if _, err := g.run(ctx, "init", "--quiet", "--template="); err != nil {
		return nil, err
	}
	if err := g.commit(ctx, "deps: project"); err != nil {
		return nil, err
	}
	base, err := g.run(ctx, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	for _, rel := range slices.Sorted(maps.Keys(manifests)) {
		ref, err := commitOf(manifests[rel])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", rel, err)
		}
		args := slices.Concat(argv[1:], UpdateArgs, []string{"--answers-file", rel, "--vcs-ref", ref})
		if err := runCopier(ctx, tmp, dir, argv[0], args); err != nil {
			return nil, err
		}
		conflicts, err := g.run(ctx, "diff", "--name-only", "--diff-filter=U", "-z")
		if err != nil {
			return nil, err
		}
		if c := splitZ(conflicts); len(c) > 0 {
			return nil, fmt.Errorf("copier update of %s to %s in %s left merge conflicts in %s", rel, ref, dir, strings.Join(c, ", "))
		}
		if err := g.commit(ctx, "deps: "+rel); err != nil {
			return nil, err
		}
	}
	diff, err := g.run(ctx, "diff", "--name-only", "--no-renames", "-z", strings.TrimSpace(base), "HEAD")
	if err != nil {
		return nil, err
	}
	out := map[string][]byte{}
	for _, p := range splitZ(diff) {
		b, err := result(tmp, p)
		if err != nil {
			return nil, err
		}
		out[p] = b
	}
	for rel := range manifests {
		if _, ok := out[rel]; !ok {
			return nil, fmt.Errorf("copier update left %s in %s unchanged", rel, dir)
		}
	}
	return out, nil
}

// commitOf returns the `_commit` of an answers file.
func commitOf(src []byte) (string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(src, &root); err != nil {
		return "", err
	}
	if len(root.Content) > 0 {
		if v := yamlspan.Value(yamlspan.Get(root.Content[0], "_commit")); v != "" {
			return v, nil
		}
	}
	return "", errors.New("no _commit")
}

// result returns the content of the file p (slash path) of the copy at tmp: nil when it was
// deleted.
func result(tmp, p string) ([]byte, error) {
	f := filepath.Join(tmp, filepath.FromSlash(p))
	st, err := os.Lstat(f)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil, nil
	case err != nil:
		return nil, err
	case !st.Mode().IsRegular():
		return nil, fmt.Errorf("copier update made %s no regular file, which deps cannot write", p)
	}
	return os.ReadFile(f)
}

// copierArgv returns the command running copier: copier from PATH, else `uvx --from copier
// copier`.
func copierArgv() ([]string, error) {
	if p, err := exec.LookPath("copier"); err == nil {
		return []string{p}, nil
	}
	if uvx, err := exec.LookPath("uvx"); err == nil {
		return []string{uvx, "--from", "copier", "copier"}, nil
	}
	return nil, errors.New("copier not found (needed to update from Copier templates)")
}

// env is the environment of copier and git: no terminal prompts for template credentials.
func env() []string { return toolrun.Env("GIT_TERMINAL_PROMPT=0") }

// runCopier runs bin args in tmp; a failure carries the tail of copier's output with tmp shown
// as scope.
func runCopier(ctx context.Context, tmp, scope, bin string, args []string) error {
	cmd, cancel := toolrun.Command(ctx, bin, args...)
	defer cancel()
	cmd.Dir = tmp
	cmd.Env = env()
	out := &toolrun.TailBuffer{Max: 32 << 10}
	cmd.Stdout, cmd.Stderr = out, out
	start := time.Now()
	err := cmd.Run()
	slog.DebugContext(ctx, "copier command", "args", args, "dir", scope, "duration", time.Since(start), "err", err)
	if err == nil {
		return nil
	}
	msg := cmp.Or(strings.ReplaceAll(toolrun.Tail(out.Bytes(), 20), tmp, scope), err.Error())
	return fmt.Errorf("copier update in %s: %s", scope, msg)
}

// gitRunner runs git in the temporary repository dir, the copy of scope.
type gitRunner struct{ dir, scope string }

func (g gitRunner) run(ctx context.Context, args ...string) (string, error) {
	cmd, cancel := toolrun.Command(ctx, "git", args...)
	defer cancel()
	cmd.Dir = g.dir
	cmd.Env = env()
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s for copier in %s: %s", args[0], g.scope, cmp.Or(strings.TrimSpace(stderr.String()), err.Error()))
	}
	return string(out), nil
}

// commit commits every file of the repository, as Copier refuses to update a dirty one;
// hooks and signing of the user's git configuration are off.
func (g gitRunner) commit(ctx context.Context, msg string) error {
	if _, err := g.run(ctx, "add", "--all"); err != nil {
		return err
	}
	_, err := g.run(ctx, "-c", "user.name=deps", "-c", "user.email=deps@localhost", "-c", "commit.gpgsign=false",
		"commit", "--quiet", "--no-verify", "--allow-empty", "-m", msg)
	return err
}

// copyProject copies the project at dir to dst: the files git lists as tracked or untracked
// and not ignored (`git ls-files`), so build output and installed packages stay behind;
// outside a git repository every file but .git. Modes and symlinks are kept.
func copyProject(ctx context.Context, dir, dst string) error {
	cmd, cancel := toolrun.Command(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	defer cancel()
	cmd.Dir = dir
	cmd.Env = env()
	list, err := cmd.Output()
	if err != nil {
		return toolrun.CopyTree(dir, dst, func(_ string, d fs.DirEntry) bool { return d.Name() == ".git" })
	}
	for _, p := range splitZ(string(list)) {
		if err := copyEntry(filepath.Join(dir, filepath.FromSlash(p)), filepath.Join(dst, filepath.FromSlash(p))); err != nil {
			return err
		}
	}
	return nil
}

// copyEntry copies the regular file or symlink src to dst; anything else (a submodule's
// directory, a file deleted from the work tree) is left out.
func copyEntry(src, dst string) error {
	st, err := os.Lstat(src)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() && st.Mode()&fs.ModeSymlink == 0 {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	if st.Mode()&fs.ModeSymlink != 0 {
		target, err := os.Readlink(src)
		if err != nil {
			return err
		}
		return os.Symlink(target, dst)
	}
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, st.Mode().Perm())
}

// splitZ splits NUL-terminated git output.
func splitZ(s string) []string {
	var out []string
	for f := range strings.SplitSeq(s, "\x00") {
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}
