package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"

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
		groups = append(groups, engine.GroupUpdates(b.Res.Updates, prefixOf(b))...)
	}
	return res, groups
}
