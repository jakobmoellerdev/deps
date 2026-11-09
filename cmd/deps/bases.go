package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"go.yaml.in/yaml/v4"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// runBase is a base branch of run and the checkout of its tip.
type runBase struct {
	name string
	repo gitx.Repo // the checkout: --dir, or the --dir of a linked work tree of the tip
	tree string    // the linked work tree, "" = the checkout of --dir
}

// runBases returns the base branches of run: --base, else the baseBranches of the config
// unfolded against the default branch and the branches on origin (config.ExpandBaseBranches;
// names origin does not have are skipped with a warning), else the default branch. multi
// reports whether their update branches carry the base (config.MultiBase; never with
// --base). --base and the default branch use the checkout, which must be at their tip (bot.Run
// checks); a configured base whose tip is not the checkout's HEAD gets a linked work tree of
// its fetched tip. closeBases removes those.
func runBases(ctx context.Context, flag string, cfg *config.Config, repo gitx.Repo, pc platform.Client) (bases []runBase, multi bool, err error) {
	if flag != "" {
		return []runBase{{name: flag, repo: repo}}, false, nil
	}
	def, err := pc.DefaultBranch(ctx)
	if err != nil {
		return nil, false, fmt.Errorf("default branch: %w", err)
	}
	if len(cfg.BaseBranches) == 0 {
		return []runBase{{name: def, repo: repo}}, false, nil
	}
	refs, err := repo.LsRemote(ctx, "refs/heads/*")
	if err != nil {
		return nil, false, err
	}
	remote := make([]string, 0, len(refs))
	for ref := range refs {
		remote = append(remote, strings.TrimPrefix(ref, "refs/heads/"))
	}
	slices.Sort(remote)
	head, err := repo.RevParse(ctx, "HEAD")
	if err != nil {
		return nil, false, err
	}
	prefix, err := repo.Prefix(ctx)
	if err != nil {
		return nil, false, err
	}
	defer func() {
		if err != nil {
			closeBases(ctx, repo, bases)
			bases = nil
		}
	}()
	for _, name := range cfg.ExpandBaseBranches(def, remote) {
		tip, ok := refs["refs/heads/"+name]
		switch {
		case !ok:
			slog.WarnContext(ctx, "base branch not found on origin, skipping it", "branch", name)
			continue
		case tip == head:
			bases = append(bases, runBase{name: name, repo: repo})
			continue
		}
		b, err := checkoutBase(ctx, repo, name, prefix)
		if err != nil {
			return bases, false, fmt.Errorf("base branch %s: %w", name, err)
		}
		bases = append(bases, b)
	}
	if len(bases) == 0 {
		return nil, false, &config.Error{Err: fmt.Errorf("baseBranches: no branch on origin matches %q", cfg.BaseBranches)}
	}
	return bases, cfg.MultiBase(), nil
}

// checkoutBase fetches the tip of base and checks it out into a new linked work tree; prefix
// is the path of repo's directory in its work tree (gitx.Repo.Prefix).
func checkoutBase(ctx context.Context, repo gitx.Repo, base, prefix string) (runBase, error) {
	sha, err := repo.FetchBranch(ctx, base)
	if err != nil {
		return runBase{}, err
	}
	tree, err := os.MkdirTemp("", "deps-base-")
	if err != nil {
		return runBase{}, err
	}
	if err := repo.AddWorktree(ctx, tree, sha); err != nil {
		return runBase{}, errors.Join(err, os.RemoveAll(tree))
	}
	slog.InfoContext(ctx, "base branch checked out", "branch", base, "commit", sha[:min(7, len(sha))], "dir", tree)
	return runBase{name: base, repo: gitx.Repo{Dir: filepath.Join(tree, filepath.FromSlash(prefix)), Signer: repo.Signer}, tree: tree}, nil
}

// closeBases removes the linked work trees of bases; failures only leave temp space behind, so
// they are logged.
func closeBases(ctx context.Context, repo gitx.Repo, bases []runBase) {
	ctx = context.WithoutCancel(ctx)
	for _, b := range bases {
		if b.tree == "" {
			continue
		}
		if err := errors.Join(repo.RemoveWorktree(ctx, b.tree), os.RemoveAll(b.tree)); err != nil {
			slog.WarnContext(ctx, "removing the base branch work tree", "branch", b.name, "dir", b.tree, "err", err)
		}
	}
}

// openBase returns the checkout of base branch name (explain --base): the checkout of repo
// when it is at the tip of name on origin, else a linked work tree of the fetched tip, which
// closeBases removes.
func openBase(ctx context.Context, repo gitx.Repo, name string) (runBase, error) {
	refs, err := repo.LsRemote(ctx, "refs/heads/"+name)
	if err != nil {
		return runBase{}, err
	}
	tip, ok := refs["refs/heads/"+name]
	if !ok {
		return runBase{}, &usageError{fmt.Errorf("base branch %s not found on origin", name)}
	}
	head, err := repo.RevParse(ctx, "HEAD")
	if err != nil {
		return runBase{}, err
	}
	if tip == head {
		return runBase{name: name, repo: repo}, nil
	}
	prefix, err := repo.Prefix(ctx)
	if err != nil {
		return runBase{}, err
	}
	b, err := checkoutBase(ctx, repo, name, prefix)
	if err != nil {
		return runBase{}, fmt.Errorf("base branch %s: %w", name, err)
	}
	return b, nil
}

// runWideKeys are the top-level config keys that span all bases of a run (what bot.Options
// and the branch names take from the config, and the base branches themselves): the config of
// a base branch (useBaseBranchConfig) does not change them.
var runWideKeys = []string{
	"baseBranches", "useBaseBranchConfig", "branchPrefix", "gitAuthor", "platformCommit", "gitIgnoredAuthors",
	"prConcurrentLimit", "prHourlyLimit", "rebaseWhen", "rebaseLabel", "stopUpdatingLabel", "updateNotScheduled",
	"dependencyDashboard", "dependencyDashboardTitle", "dependencyDashboardLabels",
}

// baseSession is the session resolving base b, its rules with baseBranches matching b. The base
// the checkout serves uses s's config; a base checked out into its own work tree gets the config
// of s's useBaseBranchConfig with its config file there (mergeBaseConfig).
func (s *session) baseSession(ctx context.Context, b runBase) (*session, error) {
	if b.tree == "" {
		return s.forBase(b.repo.Dir, b.name), nil
	}
	cfg, err := s.mergeBaseConfig(ctx, s.cfg, b.name, b.repo.Dir)
	if err != nil {
		return nil, fmt.Errorf("base branch %s: %w", b.name, err)
	}
	if cfg == s.cfg {
		return s.forBase(b.repo.Dir, b.name), nil
	}
	bs, err := s.withConfig(ctx, b.repo.Dir, cfg, s.info)
	if err != nil {
		return nil, fmt.Errorf("base branch %s: %w", b.name, err)
	}
	return bs.forBase(b.repo.Dir, b.name), nil
}

// configName returns the path of the session's config file relative to its root, with
// slashes; "" when it has none there (the built-in defaults, or --config outside --dir).
func (s *session) configName() string {
	if s.cfg.Path() == "" {
		return ""
	}
	p, err := filepath.Abs(s.cfg.Path())
	if err != nil {
		return ""
	}
	rel, err := filepath.Rel(s.root, p)
	if err != nil || !filepath.IsLocal(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// mergeBaseConfig returns the config of base branch base under over's useBaseBranchConfig:
// with merge or merge-if-present, the file of the name of s's config file in dir, the checkout
// of the base's tip, read with s.readConfig (translated with --renovate), merged over over
// (config.MergeBase; named <base>:<name> in explain and rule sources); with none, over. A base
// without the file is a config error with merge and gets over with merge-if-present
// (Renovate's modes). A base's other value of a key in runWideKeys is ignored with a warning.
func (s *session) mergeBaseConfig(ctx context.Context, over *config.Config, base, dir string) (*config.Config, error) {
	mode := over.UseBaseBranchConfig
	if mode == "" || mode == config.BaseConfigNone {
		return over, nil
	}
	name := s.configName()
	if name == "" {
		if mode == config.BaseConfigMergeIfPresent {
			return over, nil
		}
		return nil, &config.Error{Err: errors.New("useBaseBranchConfig: merge: no config file to read on the base branch (the config must be a file inside --dir; merge-if-present uses the checkout's config then)")}
	}
	path := filepath.Join(dir, filepath.FromSlash(name))
	read := s.readConfig
	if read == nil {
		read = func(_ context.Context, _, p string) ([]byte, error) { return os.ReadFile(p) }
	}
	src, err := read(ctx, dir, path)
	switch {
	case errors.Is(err, fs.ErrNotExist) && mode == config.BaseConfigMergeIfPresent:
		slog.InfoContext(ctx, "base branch has no config file, using the checkout's", "branch", base, "file", name)
		return over, nil
	case errors.Is(err, fs.ErrNotExist):
		return nil, &config.Error{Err: fmt.Errorf("useBaseBranchConfig: merge: %s not found on the base branch (merge-if-present uses the checkout's config then)", name)}
	case err != nil:
		if _, ok := errors.AsType[*config.Error](err); ok {
			return nil, err
		}
		return nil, &config.Error{Err: err}
	}
	display := base + ":" + name
	cfg, err := over.MergeBase(ctx, src, display, display, s.fetcher)
	if err != nil {
		return nil, err
	}
	for _, k := range runWideKeys {
		if cfg.KeyFile(k) == display && keyValue(cfg, k) != keyValue(over, k) {
			slog.WarnContext(ctx, "the base branch's config sets a key that spans the run; the checkout's value applies", "branch", base, "key", k)
		}
	}
	return cfg, nil
}

// keyValue is the YAML of the value of top-level key in cfg as written, "" when unset.
func keyValue(cfg *config.Config, key string) string {
	doc := cfg.Document()
	if doc == nil {
		return ""
	}
	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value == key {
			out, err := yaml.Marshal(doc.Content[i+1])
			if err != nil {
				return ""
			}
			return string(out)
		}
	}
	return ""
}

// baseLine is the log line naming base and, when its config differs from the checkout's, the
// config files applied to it ("base    release/1.x  config .deps.yaml + release/1.x:.deps.yaml").
func baseLine(base string, cfg *config.Config) string {
	files := cfg.Files()
	if len(files) < 2 {
		return "base    " + base
	}
	return "base    " + base + "  config " + strings.Join(files, " + ")
}

// runResult is what the summary of run covers: the results of every base side by side (the
// only one as is) with resErrs, their resolution errors as reported, and the groups bot.Run
// made of them under prefix, with multi under each base's bot.BasePrefix.
func runResult(bases []bot.Base, prefix string, multi bool, resErrs []error) (*engine.Result, []engine.Group) {
	prefixOf := func(b bot.Base) string {
		if multi {
			return bot.BasePrefix(prefix, b.Name)
		}
		return prefix
	}
	if len(bases) == 1 {
		res := bases[0].Res
		res.Errors = resErrs
		return res, engine.GroupUpdates(res.Updates, prefixOf(bases[0]))
	}
	res := &engine.Result{Errors: resErrs}
	var groups []engine.Group
	for _, b := range bases {
		res.Updates = append(res.Updates, b.Res.Updates...)
		res.Skipped = append(res.Skipped, b.Res.Skipped...)
		res.Deps = append(res.Deps, b.Res.Deps...)
		res.Pending = append(res.Pending, b.Res.Pending...)
		res.Warnings = append(res.Warnings, b.Res.Warnings...)
		groups = append(groups, engine.GroupUpdates(b.Res.Updates, prefixOf(b))...)
	}
	return res, groups
}
