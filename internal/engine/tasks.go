package engine

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/dep"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/toolrun"
)

// ErrTasksNotAllowed fails a build with a postUpgradeTasks command no --allow-tasks pattern allows.
var ErrTasksNotAllowed = errors.New("postUpgradeTasks command not allowed")

// TaskAllowlist are the --allow-tasks patterns. A pattern allows a command it matches whole,
// with * standing for any text without shell chaining or substitution (taskOperators) and runs
// of spaces and tabs compared as one space: "go generate ./...", "make *". Operators written in
// the pattern match literally ("echo * >> deps.txt"); the pattern "*" alone allows everything.
type TaskAllowlist []string

// Check returns an error wrapping ErrTasksNotAllowed for the first run whose command no
// pattern allows.
func (a TaskAllowlist) Check(runs []TaskRun) error {
	for _, r := range runs {
		cmd := normalizeCommand(r.Command)
		if !slices.ContainsFunc(a, func(p string) bool { return globMatch(normalizeCommand(p), cmd) }) {
			return fmt.Errorf("%w: %q; allow it with --allow-tasks '%s' (or DEPS_ALLOW_TASKS)", ErrTasksNotAllowed, r.Command, cmd)
		}
	}
	return nil
}

// normalizeCommand trims a command and its lines and collapses runs of spaces and tabs; line
// breaks stay, since they separate commands.
func normalizeCommand(c string) string {
	lines := strings.Split(strings.TrimSpace(c), "\n")
	for i, l := range lines {
		lines[i] = strings.Join(strings.Fields(l), " ")
	}
	return strings.Join(lines, "\n")
}

// globMatch reports whether s matches pattern whole, each * matching text without
// taskOperators; the pattern "*" matches everything.
func globMatch(pattern, s string) bool {
	if pattern == "*" {
		return true
	}
	parts := strings.Split(pattern, "*")
	return strings.HasPrefix(s, parts[0]) && globRest(parts[1:], s[len(parts[0]):])
}

// globRest reports whether s is, for each of parts, text without taskOperators followed by
// the part.
func globRest(parts []string, s string) bool {
	if len(parts) == 0 {
		return s == ""
	}
	for i := 0; i <= len(s) && !hasTaskOperator(s[:i]); i++ {
		if strings.HasPrefix(s[i:], parts[0]) && globRest(parts[1:], s[i+len(parts[0]):]) {
			return true
		}
	}
	return false
}

// hasTaskOperator reports whether s chains or substitutes commands: ; & | ` $( or a line break.
func hasTaskOperator(s string) bool {
	return strings.ContainsAny(s, ";&|`\n") || strings.Contains(s, "$(")
}

// taskTimeout bounds one postUpgradeTasks command.
const taskTimeout = 15 * time.Minute

// droppedEnv are kept from the commands' environment on top of toolrun.Secrets: the platform
// tokens deps reads, and the git variables that would point the commands' git elsewhere than
// the repository they run in.
var droppedEnv = []string{
	"GITHUB_TOKEN", "GITEA_TOKEN", "FORGEJO_TOKEN", "GITLAB_TOKEN", "CI_JOB_TOKEN",
	"GIT_DIR", "GIT_WORK_TREE", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY",
	"GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES",
}

// taskEnv is toolrun.Env without droppedEnv.
func taskEnv() []string {
	return slices.DeleteFunc(toolrun.Env(), func(kv string) bool {
		k, _, _ := strings.Cut(kv, "=")
		return slices.Contains(droppedEnv, k)
	})
}

// TaskRun is one run of a postUpgradeTasks command: for Update (executionMode update) or, when
// nil, for the branch.
type TaskRun struct {
	Command string
	Update  *dep.Update
}

// TaskRuns returns the postUpgradeTasks runs of ups: the update-mode commands of each update in
// the order of ups, then the distinct branch-mode commands in the order of first appearance.
func TaskRuns(ups []dep.Update) []TaskRun {
	var runs, branch []TaskRun
	for i := range ups {
		u := &ups[i]
		for _, c := range u.Tasks.Commands {
			switch {
			case u.Tasks.Update:
				runs = append(runs, TaskRun{c, u})
			case !slices.ContainsFunc(branch, func(r TaskRun) bool { return r.Command == c }):
				branch = append(branch, TaskRun{Command: c})
			}
		}
	}
	return append(runs, branch...)
}

// String is the command, with the dep of an update-mode run.
func (r TaskRun) String() string {
	if r.Update == nil {
		return r.Command
	}
	return r.Command + " (" + r.Update.Dep.Name + ")"
}

// env describes an update-mode run's update in DEPS_* variables.
func (r TaskRun) env() []string {
	u := r.Update
	if u == nil {
		return nil
	}
	cur := u.Dep.Version
	if u.Lockfile && u.Dep.Locked != "" {
		cur = u.Dep.Locked
	}
	return []string{
		"DEPS_MANAGER=" + u.Dep.Manager, "DEPS_PACKAGE_FILE=" + u.Dep.File, "DEPS_DEP_NAME=" + u.Dep.Name,
		"DEPS_DEP_TYPE=" + u.Dep.DepType, "DEPS_DATASOURCE=" + u.Dep.Datasource, "DEPS_UPDATE_TYPE=" + string(u.Type),
		"DEPS_CURRENT_VERSION=" + cur, "DEPS_NEW_VERSION=" + u.NewVersion, "DEPS_CURRENT_DIGEST=" + u.Dep.Digest,
		"DEPS_NEW_DIGEST=" + u.NewDigest, "DEPS_NEW_NAME=" + u.NewName,
	}
}

// runTasks performs runs one after another with sh -c in dir, each with a 15 minute timeout and
// deps' environment without droppedEnv (plus the DEPS_* variables of an update-mode run); the
// first failure stops them.
func runTasks(ctx context.Context, dir string, runs []TaskRun) error {
	env := taskEnv()
	for _, r := range runs {
		ctx, cancel := context.WithTimeout(ctx, taskTimeout)
		cmd := exec.CommandContext(ctx, "sh", "-c", r.Command)
		cmd.Dir, cmd.Env, cmd.WaitDelay = dir, append(slices.Clip(env), r.env()...), 10*time.Second
		out := &toolrun.TailBuffer{Max: 64 << 10}
		cmd.Stdout, cmd.Stderr = out, out
		err := cmd.Run()
		cancel()
		if err != nil {
			return fmt.Errorf("postUpgradeTasks: %s: %w: %s", r, err, toolrun.Tail(out.Bytes(), 20))
		}
	}
	return nil
}

// BuildWithTasks runs the postUpgradeTasks of ups on changes, the build of ups on the checkout
// root, which is commit head. The top of root's work tree is copied without .git to a
// temporary directory and made a throwaway repository of head (scratchRepo), changes are
// written into it and the runs (TaskRuns) run in root's directory there, so git works for them as in the
// checkout, but every write lands in the copy. The copy's changes against head below root
// (git add -A, so .gitignore decides) then replace changes. The checkout's repository is only
// read. Submodule pointers cannot change: a command that moves a submodule's HEAD fails the
// group, and files inside submodules never become changes; the tasks' files globs limit what
// the commands add to changes (taskFilter), and a symlink they create, change or delete there
// fails the group. A command allow does not allow fails it before anything runs; without
// tasks it returns changes.
func BuildWithTasks(ctx context.Context, root, head string, ups []dep.Update, changes []FileChange, allow TaskAllowlist) ([]FileChange, error) {
	runs := TaskRuns(ups)
	if len(runs) == 0 {
		return changes, nil
	}
	if err := allow.Check(runs); err != nil {
		return nil, err
	}
	out, err := gitOut(ctx, root, nil, "rev-parse", "--show-toplevel", "--show-prefix")
	if err != nil {
		return nil, err
	}
	realTop, prefix, _ := strings.Cut(strings.TrimSuffix(out, "\n"), "\n") // the prefix line may be empty
	tmp, err := os.MkdirTemp("", "deps-tasks-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tmp)
	top := filepath.Join(tmp, "tree")
	if err := toolrun.CopyTree(realTop, top, func(_ string, d fs.DirEntry) bool { return d.Name() == ".git" }); err != nil {
		return nil, err
	}
	links, subs, err := scratchRepo(ctx, realTop, top, head)
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(top, filepath.FromSlash(prefix))
	for _, c := range changes {
		p := filepath.Join(dir, filepath.FromSlash(c.Path))
		if c.New == nil {
			err = os.Remove(p)
		} else if err = os.MkdirAll(filepath.Dir(p), 0o755); err == nil {
			err = os.WriteFile(p, c.New, c.Mode)
		}
		if err != nil {
			return nil, err
		}
	}
	for _, r := range runs {
		if err := runTasks(ctx, dir, []TaskRun{r}); err != nil {
			return nil, err
		}
		for _, sub := range slices.Sorted(maps.Keys(subs)) {
			if h, err := gitOut(ctx, filepath.Join(top, filepath.FromSlash(sub)), nil, "rev-parse", "HEAD"); err != nil || strings.TrimSpace(h) != subs[sub] {
				return nil, fmt.Errorf("postUpgradeTasks: %s changed submodule %s; submodule updates are not supported", r, sub)
			}
		}
	}
	spec := cmp.Or(prefix, ".")
	if _, err := gitOut(ctx, top, nil, "add", "-A", "--", spec); err != nil {
		return nil, err
	}
	if out, err = gitOut(ctx, top, nil, "diff", "--cached", "--raw", "-z", "--no-renames", "--ignore-submodules=none", head, "--", spec); err != nil {
		return nil, err
	}
	after, err := copyChanges(out, realTop, top, prefix, links, taskKeep(ups))
	if err != nil {
		return nil, err
	}
	return taskFilter(ups, changes, after), nil
}

// taskFilter returns after, the changes with the tasks' output, limited to the paths the tasks'
// files globs of ups match (the union over the task entries; one without files matches all):
// elsewhere the build's changes stay as they were.
func taskFilter(ups []dep.Update, build, after []FileChange) []FileChange {
	keep := taskKeep(ups)
	if keep == nil {
		return after
	}
	out := map[string]FileChange{}
	for _, c := range build {
		out[c.Path] = c
	}
	for _, c := range after {
		if keep(c.Path) {
			out[c.Path] = c
		}
	}
	for _, c := range build {
		if keep(c.Path) && !slices.ContainsFunc(after, func(a FileChange) bool { return a.Path == c.Path }) {
			delete(out, c.Path) // a task restored the base content
		}
	}
	return slices.SortedFunc(maps.Values(out), func(a, b FileChange) int { return strings.Compare(a.Path, b.Path) })
}

// taskKeep reports which paths (relative to --dir) the tasks of ups may change: those a task
// entry's files globs match, the union over the entries; nil (all) when an entry has none.
func taskKeep(ups []dep.Update) func(string) bool {
	var lists [][]string
	for _, u := range ups {
		if len(u.Tasks.Commands) == 0 {
			continue
		}
		if len(u.Tasks.Files) == 0 {
			return nil
		}
		lists = append(lists, u.Tasks.Files)
	}
	return func(p string) bool {
		return slices.ContainsFunc(lists, func(globs []string) bool { return config.MatchGlobs(globs, p) })
	}
}

// ApplyTasks performs the postUpgradeTasks runs of ups (TaskRuns) in the checkout root, which
// holds the build's changes, for apply. With files globs (taskKeep), each file below root the
// runs changed and no task entry keeps is restored to its state before them, a symlink as a
// symlink: snapshots of root before and after the runs on a temporary index of HEAD (git add
// -A, so .gitignore decides and ignored files are left alone) tell those files apart. What the
// runs left at a restored path is removed first, so no restore writes through their symlinks.
func ApplyTasks(ctx context.Context, root string, ups []dep.Update) error {
	runs, keep := TaskRuns(ups), taskKeep(ups)
	if len(runs) == 0 {
		return nil
	}
	if keep == nil {
		return runTasks(ctx, root, runs)
	}
	tmp, err := os.MkdirTemp("", "deps-tasks-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(tmp, "index")}
	type entry struct{ mode, id string }
	snapshot := func() (map[string]entry, error) {
		if _, err := gitEnvOut(ctx, root, nil, env, "read-tree", "HEAD"); err != nil {
			return nil, err
		}
		if _, err := gitEnvOut(ctx, root, nil, env, "add", "-A", "--", "."); err != nil {
			return nil, err
		}
		out, err := gitEnvOut(ctx, root, nil, env, "diff", "--cached", "--raw", "-z", "--no-renames", "--relative", "HEAD")
		if err != nil {
			return nil, err
		}
		snap := map[string]entry{}
		fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
		for i := 0; i+1 < len(fields); i += 2 { // :<old mode> <new mode> <old id> <new id> <status>
			if m := strings.Fields(fields[i]); len(m) == 5 && m[0] != ":160000" && m[1] != "160000" {
				snap[fields[i+1]] = entry{m[1], m[3]}
			}
		}
		return snap, nil
	}
	before, err := snapshot()
	if err != nil {
		return err
	}
	if err := runTasks(ctx, root, runs); err != nil {
		return err
	}
	after, err := snapshot()
	if err != nil {
		return err
	}
	for p := range maps.Keys(after) {
		if _, ok := before[p]; !ok {
			before[p] = entry{} // unchanged from HEAD before the runs
		}
	}
	for _, p := range slices.Sorted(maps.Keys(before)) {
		prior, now := before[p], after[p]
		if prior == now || keep(p) {
			continue
		}
		file := filepath.Join(root, filepath.FromSlash(p))
		var content, mode string
		remove := false
		switch {
		case prior == entry{}: // as in HEAD, where it may not exist or be a directory
			ls, _ := gitOut(ctx, root, nil, "ls-tree", "HEAD", "--", p) // <mode> <type> <id>\t<path>
			if m := strings.Fields(ls); len(m) < 3 || m[1] != "blob" {
				remove = true // a directory's files are restored on their own
			} else {
				mode = m[0]
				content, err = gitOut(ctx, root, nil, "cat-file", "blob", m[2])
			}
		case prior.mode == "000000": // deleted
			remove = true
		default:
			mode = prior.mode
			content, err = gitOut(ctx, root, nil, "cat-file", "blob", prior.id)
		}
		if err == nil { // first, so nothing is written through a symlink the runs left at p
			if err = os.Remove(file); errors.Is(err, fs.ErrNotExist) {
				err = nil
			}
		}
		if err == nil && !remove {
			if err = os.MkdirAll(filepath.Dir(file), 0o755); err == nil {
				switch mode {
				case "120000":
					err = os.Symlink(content, file)
				case "100755":
					err = os.WriteFile(file, []byte(content), 0o755)
				default:
					err = os.WriteFile(file, []byte(content), 0o644)
				}
			}
		}
		if err != nil {
			return fmt.Errorf("postUpgradeTasks: restore %s: %w", p, err)
		}
	}
	return nil
}

// scratchRepo makes the copy dst of the work tree src, a checkout of commit head, a
// repository of its own: git init with src's object format, src's objects as alternates (read
// only: new objects go to dst), its shallow file, tags and origin URL (without credentials),
// HEAD detached at head and the index read from it. Each submodule src has checked out gets
// the same in dst (recursively); uninitialized ones stay empty directories. It returns the
// gitlink paths of head and the HEADs of the initialized submodules below dst, as slash paths.
func scratchRepo(ctx context.Context, src, dst, head string) (links []string, subs map[string]string, err error) {
	out, err := gitOut(ctx, src, nil, "rev-parse", "--show-object-format", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return nil, nil, err
	}
	format, common, _ := strings.Cut(strings.TrimSpace(out), "\n")
	// A copied .git file (a submodule's, a linked worktree's) points at src's repository.
	if err := os.RemoveAll(filepath.Join(dst, ".git")); err != nil {
		return nil, nil, err
	}
	if _, err := gitOut(ctx, dst, nil, "init", "-q", "--object-format="+format); err != nil {
		return nil, nil, err
	}
	gitDir := filepath.Join(dst, ".git")
	if err := os.WriteFile(filepath.Join(gitDir, "objects", "info", "alternates"), []byte(filepath.Join(common, "objects")+"\n"), 0o644); err != nil {
		return nil, nil, err
	}
	if b, err := os.ReadFile(filepath.Join(common, "shallow")); err == nil {
		if err := os.WriteFile(filepath.Join(gitDir, "shallow"), b, 0o644); err != nil {
			return nil, nil, err
		}
	}
	tags, err := gitOut(ctx, src, nil, "for-each-ref", "--format=create %(refname) %(objectname)", "refs/tags")
	if err != nil {
		return nil, nil, err
	}
	steps := [][]string{{"update-ref", "--no-deref", "HEAD", head}, {"read-tree", head}}
	if tags != "" {
		steps = append(steps, []string{"update-ref", "--stdin"})
	}
	if u, err := gitOut(ctx, src, nil, "config", "--get", "remote.origin.url"); err == nil && !hasPassword(strings.TrimSpace(u)) {
		steps = append(steps, []string{"remote", "add", "origin", strings.TrimSpace(u)})
	}
	for _, args := range steps {
		var stdin []byte
		if args[0] == "update-ref" && args[1] == "--stdin" {
			stdin = []byte(tags)
		}
		if _, err := gitOut(ctx, dst, stdin, args...); err != nil {
			return nil, nil, err
		}
	}
	subs = map[string]string{}
	var inited []string
	if _, err := os.Stat(filepath.Join(dst, ".gitmodules")); err == nil {
		out, err := gitOut(ctx, dst, nil, "ls-files", "-s", "-z")
		if err != nil {
			return nil, nil, err
		}
		for e := range strings.SplitSeq(out, "\x00") {
			meta, p, ok := strings.Cut(e, "\t")
			if !ok || !strings.HasPrefix(meta, "160000 ") {
				continue
			}
			links = append(links, p)
			s := filepath.Join(src, filepath.FromSlash(p))
			if !isRepoTop(ctx, s) {
				continue // not initialized
			}
			h, err := gitOut(ctx, s, nil, "rev-parse", "HEAD")
			if err != nil {
				return nil, nil, err
			}
			h = strings.TrimSpace(h)
			_, nested, err := scratchRepo(ctx, s, filepath.Join(dst, filepath.FromSlash(p)), h)
			if err != nil {
				return nil, nil, fmt.Errorf("submodule %s: %w", p, err)
			}
			subs[p] = h
			inited = append(inited, p)
			for k, v := range nested {
				subs[p+"/"+k] = v
			}
		}
	}
	if inited != nil {
		// Registers them as initialized (url from .gitmodules), for git submodule; offline.
		// Best effort: only what git submodule reports depends on it.
		_, _ = gitOut(ctx, dst, nil, append([]string{"submodule", "init", "-q", "--"}, inited...)...)
	}
	// Fresh stat data, so that git status shows only real changes; it fails when files differ.
	_, _ = gitOut(ctx, dst, nil, "update-index", "-q", "--refresh")
	return links, subs, nil
}

// isRepoTop reports whether dir is the top of a work tree of its own (an initialized
// submodule rather than an empty directory of its superproject).
func isRepoTop(ctx context.Context, dir string) bool {
	out, err := gitOut(ctx, dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return false
	}
	a, errA := os.Stat(strings.TrimSpace(out))
	b, errB := os.Stat(dir)
	return errA == nil && errB == nil && os.SameFile(a, b)
}

// hasPassword reports whether a remote URL carries a password or token.
func hasPassword(u string) bool {
	p, err := url.Parse(u)
	if err != nil || p.User == nil {
		return false
	}
	_, set := p.User.Password()
	return set
}

// copyChanges returns the changes git diff --raw -z (out, paths relative to the top of the
// work tree) lists below prefix: Old from the checkout realTop, New from the copy top.
// Submodule pointers and paths inside submodules (links) are left out. A symlink on either
// side fails where keep (nil: everywhere) keeps the tasks' changes, and is left out elsewhere:
// FileChange and the commit paths carry regular files and gitlinks only.
func copyChanges(out, realTop, top, prefix string, links []string, keep func(string) bool) ([]FileChange, error) {
	fields := strings.Split(strings.TrimSuffix(out, "\x00"), "\x00")
	var changes []FileChange
	for i := 0; i+1 < len(fields); i += 2 {
		// :<old mode> <new mode> <old id> <new id> <status>; with --no-renames one path each
		meta, name := strings.Fields(fields[i]), fields[i+1]
		if len(meta) != 5 || meta[0] == ":160000" || meta[1] == "160000" || slices.ContainsFunc(links, func(l string) bool {
			return name == l || strings.HasPrefix(name, l+"/")
		}) {
			continue
		}
		c := FileChange{Path: strings.TrimPrefix(name, prefix), Mode: 0o644}
		if meta[0] == ":120000" || meta[1] == "120000" {
			if keep != nil && !keep(c.Path) {
				continue // taskFilter keeps the build's change here
			}
			return nil, fmt.Errorf("postUpgradeTasks: symlink %s changed; symlinks are not supported", c.Path)
		}
		if meta[1] == "100755" {
			c.Mode = 0o755
		}
		var err error
		if meta[4] != "A" {
			if c.Old, err = os.ReadFile(filepath.Join(realTop, filepath.FromSlash(name))); err != nil {
				return nil, err
			}
		}
		if meta[4] != "D" {
			if c.New, err = os.ReadFile(filepath.Join(top, filepath.FromSlash(name))); err != nil {
				return nil, err
			}
		}
		changes = append(changes, c)
	}
	return changes, nil
}

// gitOut runs git in dir, with stdin and deps' environment without droppedEnv, and returns
// its stdout.
func gitOut(ctx context.Context, dir string, stdin []byte, args ...string) (string, error) {
	return gitEnvOut(ctx, dir, stdin, nil, args...)
}

// gitEnvOut is gitOut with env added to the environment.
func gitEnvOut(ctx context.Context, dir string, stdin []byte, env []string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	cmd.Env = append(append(taskEnv(), "GIT_TERMINAL_PROMPT=0"), env...)
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
