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
	"time"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/releasenotes"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

type runOpts struct {
	platform, apiURL, repo, token, base string
	triggerSchedule                     string
	authorName, authorEmail             string
	signingKey                          string
	maxNewPRs                           int
	dryRun                              bool
}

func newRun(o *rootOpts) *cobra.Command {
	r := &runOpts{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Push one branch and open or update one pull request per dependency or group",
		Long:  helpText("run"),
		Example: "  # what would happen, without a token and without writes\n" +
			"  deps run --dry-run -v\n" +
			"  # what the run triggered by the Sunday cron would do\n" +
			"  deps run --dry-run --trigger-schedule '0 0 * * 0'\n" +
			"  # in CI (the action runs this with DEPS_TOKEN set)\n" +
			"  DEPS_TOKEN=$TOKEN deps run -v --log-level info",
		Args: noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBot(cmd, o, r)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.platform, "platform", "auto", "forge type: auto|github|gitea|forgejo (auto: from the CI environment, else the origin remote)")
	f.StringVar(&r.apiURL, "api-url", "", "API base URL (default: detected)")
	f.StringVar(&r.repo, "repo", "", "repository owner/name (default: detected)")
	f.StringVar(&r.token, "token", "", "platform API token; git pushes use the checkout's credentials (default: $DEPS_TOKEN, $GITHUB_TOKEN, $GITEA_TOKEN, $FORGEJO_TOKEN, git credential helper)")
	f.StringVar(&r.base, "base", "", "base branch (default: the repository's default branch)")
	f.StringVar(&r.authorName, "author-name", defaultAuthorName, "commit author name (when not set: $DEPS_GIT_AUTHOR, then gitAuthor of the config, then the default)")
	f.StringVar(&r.authorEmail, "author-email", defaultAuthorEmail, "commit author email (when not set: $DEPS_GIT_AUTHOR, then gitAuthor of the config, then the default)")
	f.IntVar(&r.maxNewPRs, "max-new-prs", 10, "maximum number of pull requests to create per run (0 = unlimited)")
	f.BoolVar(&r.dryRun, "dry-run", false, "report what would change without pushing or calling write APIs (no PR, merge or issue writes); works without a token")
	f.StringVar(&r.triggerSchedule, "trigger-schedule", "", "act as if the run was triggered by this workflow cron, overriding $GITHUB_EVENT_NAME/$GITHUB_EVENT_PATH (default: $DEPS_TRIGGER_SCHEDULE)")
	f.StringVar(&r.signingKey, "signing-key", "", "sign deps' commits with this passphrase-less private key (OpenSSH or armored OpenPGP), given as the key itself or a file path (default: $DEPS_SIGNING_KEY)")
	return cmd
}

const (
	defaultAuthorName  = "deps[bot]"
	defaultAuthorEmail = "deps-bot@noreply.invalid"
)

// commitAuthor resolves the commit author field by field: an explicitly set flag, then
// env ($DEPS_GIT_AUTHOR), then cfg (gitAuthor), then the defaults. env and cfg are
// 'Name <email>'.
func commitAuthor(cmd *cobra.Command, r *runOpts, env, cfg string) (name, email string, err error) {
	name, email = defaultAuthorName, defaultAuthorEmail
	if cfg != "" {
		a, err := config.ParseAuthor(cfg) // validated by config.Load
		if err != nil {
			return "", "", &usageError{fmt.Errorf("gitAuthor: %w", err)}
		}
		name, email = a.Name, a.Email
	}
	if env != "" {
		a, err := config.ParseAuthor(env)
		if err != nil {
			return "", "", &usageError{fmt.Errorf("DEPS_GIT_AUTHOR: %w", err)}
		}
		name, email = a.Name, a.Email
	}
	if cmd.Flags().Changed("author-name") {
		name = r.authorName
	}
	if cmd.Flags().Changed("author-email") {
		email = r.authorEmail
	}
	return name, email, nil
}

// detectTrigger resolves what started the run: --trigger-schedule, then
// $DEPS_TRIGGER_SCHEDULE, then the CI event. A schedule event whose cron cannot be read
// matches every schedule, with a warning.
func detectTrigger(ctx context.Context, flag string, getenv func(string) string) (schedule.Trigger, error) {
	override, src := flag, "--trigger-schedule"
	if override == "" {
		override, src = getenv("DEPS_TRIGGER_SCHEDULE"), "DEPS_TRIGGER_SCHEDULE"
	}
	if override != "" {
		if err := schedule.Validate(override); err != nil {
			return schedule.Trigger{}, &usageError{fmt.Errorf("%s: %q: %w", src, override, err)}
		}
	}
	t, err := schedule.Detect(getenv, override)
	if err != nil {
		slog.WarnContext(ctx, "cannot read the triggering cron; every schedule matches", "err", err)
	} else if t.Event == schedule.EventSchedule && t.Cron == "" {
		slog.WarnContext(ctx, "schedule event without a cron in the payload; every schedule matches")
	}
	slog.InfoContext(ctx, "trigger", "event", t.Event, "cron", t.Cron)
	return t, nil
}

// newSigner prepares commit signing from --signing-key, then $DEPS_SIGNING_KEY; nil (unsigned)
// when both are empty. An unrecognized key is a usage error.
func newSigner(ctx context.Context, flag, env string) (*gitx.Signer, error) {
	key, src := flag, "--signing-key"
	if key == "" {
		key, src = env, "DEPS_SIGNING_KEY"
	}
	if key == "" {
		return nil, nil
	}
	s, err := gitx.NewSigner(ctx, key)
	if errors.Is(err, gitx.ErrKeyFormat) {
		return nil, &usageError{fmt.Errorf("%s: %w", src, err)}
	}
	if err != nil {
		return nil, fmt.Errorf("%s: %w", src, err)
	}
	slog.InfoContext(ctx, "commit signing", "format", s.Format, "key", s.Fingerprint)
	return s, nil
}

// platformCommits resolves platformCommit ($DEPS_PLATFORM_COMMIT, then the config, then
// auto) to whether commits go through the GitHub API. auto enables them on GitHub for a
// GitHub App installation token when no signing key is set. enabled on another forge or
// together with a signing key is a usage error; without a token (a dry run) it falls back to
// git with a warning.
func platformCommits(ctx context.Context, env, cfg string, kind platform.Kind, token string, signed bool) (bool, error) {
	v, src := env, "DEPS_PLATFORM_COMMIT"
	if v == "" {
		v, src = cfg, "platformCommit"
	}
	switch v {
	case "", config.PlatformCommitAuto:
		return kind == platform.GitHub && platform.IsAppToken(token) && !signed, nil
	case config.PlatformCommitDisabled:
		return false, nil
	case config.PlatformCommitEnabled:
		switch {
		case kind != platform.GitHub:
			return false, &usageError{fmt.Errorf("%s: enabled is only supported on GitHub, not %s", src, kind)}
		case signed:
			return false, &usageError{fmt.Errorf("%s: enabled conflicts with the signing key: GitHub signs platform commits itself", src)}
		case token == "":
			slog.WarnContext(ctx, "no token: platform commits disabled for this dry run", "source", src)
			return false, nil
		}
		return true, nil
	}
	return false, &usageError{fmt.Errorf("%s: must be one of %s, got %q", src, strings.Join(config.PlatformCommits, ", "), v)}
}

// apiGit is a gitx.Repo whose commits, branch updates and branch tips go through the GitHub
// API (platformCommit); remote listing, branch deletion and WriteTree stay with git. Git
// object ids are content hashes, so the tree WriteTree computes locally is the tree of the
// API commit made from the same changes.
type apiGit struct {
	gitx.Repo
	api *platform.GitHubCommits
}

// Commit ignores name and email: GitHub authors API commits as the token's account.
func (g apiGit) Commit(ctx context.Context, parent string, changes []engine.FileChange, msg, _, _ string) (string, error) {
	files := make([]platform.CommitFile, len(changes))
	for i, c := range changes {
		files[i] = platform.CommitFile{Path: c.Path, Executable: c.Mode&0o111 != 0, Content: c.New}
	}
	return g.api.Commit(ctx, parent, files, msg)
}

// Push maps refused updates to the errors of gitx.Repo.Push.
func (g apiGit) Push(ctx context.Context, commit, branch, expected string) error {
	err := g.api.Push(ctx, commit, branch, expected)
	switch {
	case errors.Is(err, platform.ErrRefStale):
		return fmt.Errorf("%w: %w", gitx.ErrStale, err)
	case errors.Is(err, platform.ErrRefRejected):
		return fmt.Errorf("%w: %w", gitx.ErrRejected, err)
	}
	return err
}

func (g apiGit) BranchTip(ctx context.Context, branch string) (gitx.Tip, error) {
	t, err := g.api.BranchTip(ctx, branch)
	return gitx.Tip{Author: t.Author, Parents: t.Parents, Tree: t.Tree}, err
}

func runBot(cmd *cobra.Command, o *rootOpts, r *runOpts) error {
	start := time.Now()
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	switch r.platform {
	case "auto", "github", "gitea", "forgejo":
	default:
		return &usageError{fmt.Errorf("--platform: invalid value %q", r.platform)}
	}
	trigger, err := detectTrigger(ctx, r.triggerSchedule, os.Getenv)
	if err != nil {
		return err
	}
	signer, err := newSigner(ctx, r.signingKey, os.Getenv("DEPS_SIGNING_KEY"))
	if err != nil {
		return err
	}
	defer func() {
		if err := signer.Close(); err != nil {
			slog.WarnContext(ctx, "removing commit signing key material", "err", err)
		}
	}()
	ov := platform.Overrides{APIURL: r.apiURL, Repo: r.repo}
	if r.platform != "auto" {
		ov.Kind = r.platform
	}
	dir, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	repo := gitx.Repo{Dir: dir, Signer: signer}
	remote, err := repo.RemoteURL(ctx)
	if err != nil {
		return err
	}
	client := newHTTPClient()
	info, err := platform.Detect(ctx, os.Getenv, remote, ov, client)
	if err != nil {
		return err
	}

	token := r.token
	if token == "" {
		token = envToken()
	}
	if token == "" {
		if _, pass, ok := httpx.NewAuth(info, "", os.Getenv).Helper(ctx, info.Host); ok {
			token = pass
		}
	}
	if token == "" && !r.dryRun {
		return fmt.Errorf("no token for %s", info.Host)
	}

	s, err := newSession(o, info, token, client)
	if err != nil {
		return err
	}
	defer s.close()
	name, email, err := commitAuthor(cmd, r, os.Getenv("DEPS_GIT_AUTHOR"), s.cfg.GitAuthor)
	if err != nil {
		return err
	}
	usePlatform, err := platformCommits(ctx, os.Getenv("DEPS_PLATFORM_COMMIT"), s.cfg.PlatformCommit, info.Kind, token, signer != nil)
	if err != nil {
		return err
	}
	var g bot.Git = repo
	ignored := s.cfg.GitIgnoredAuthors
	if usePlatform {
		prefix, err := repo.Prefix(ctx)
		if err != nil {
			return err
		}
		commits, err := platform.NewGitHubCommits(ctx, info, token, client, prefix)
		if err != nil {
			return fmt.Errorf("platform commits: %w", err)
		}
		// Branches deps committed with git under the configured author stay its own.
		ignored = append(slices.Clone(ignored), email)
		name, email = commits.Author()
		g = apiGit{Repo: repo, api: commits}
		slog.InfoContext(ctx, "platform commits", "author", name+" <"+email+">")
		if !platform.IsAppToken(token) {
			slog.WarnContext(ctx, "platform commits with a token that is not a GitHub App installation token: GitHub leaves them unsigned")
		}
	}
	res, err := s.resolve(ctx)
	if err != nil {
		return err
	}
	report(stderr, o.verbose, res, res.Errors, nil)
	bo := bot.Options{
		Base:               r.base,
		AuthorName:         name,
		AuthorEmail:        email,
		IgnoredAuthors:     ignored,
		ConcurrentLimit:    s.cfg.PRConcurrentLimit,
		HourlyLimit:        s.cfg.PRHourlyLimit,
		DryRun:             r.dryRun,
		RebaseWhen:         s.cfg.RebaseWhen,
		UpdateNotScheduled: s.cfg.UpdateNotScheduledEnabled(),
		Trigger:            trigger,
		MaxNewPRs:          r.maxNewPRs,
		Managers:           s.managers,
		BranchPrefix:       s.cfg.Prefix(),
		Notes:              releasenotes.New(client, s.sem, s.auth, s.gh, s.sources),
	}
	if s.cfg.DependencyDashboard {
		bo.Dashboard = &bot.DashboardOptions{Title: s.cfg.DashboardTitle(), Labels: s.cfg.DependencyDashboardLabels}
	}
	actions, botErrs := bot.Run(ctx, bo, s.root, res, postProcessors(s), platform.NewClient(info, token, client), g)

	var created, updated, closed, upToDate, skipped, would, automerged, wouldAutomerge int
	for _, a := range actions {
		if a.Dashboard {
			fmt.Fprintf(stderr, "dashboard %s  %s  %s\n", a.Status, a.Title, a.URL)
			continue
		}
		fmt.Fprintf(stderr, "pr      %s  %s  %s  %s\n", a.Display(), a.Branch, a.Title, a.URL)
		switch a.Automerge {
		case bot.AutomergeRequested, bot.AutomergeMerged:
			automerged++
		case bot.AutomergeWould:
			wouldAutomerge++
		}
		switch {
		case a.Status == bot.StatusCreated:
			created++
		case a.Status == bot.StatusUpdated || a.Status == bot.StatusRebased:
			updated++
		case a.Status == bot.StatusClosed || a.Status == bot.StatusDeleted:
			closed++
		case a.Status == bot.StatusUpToDate:
			upToDate++
		case strings.HasPrefix(a.Status, "skipped"):
			skipped++
		default:
			would++
		}
	}
	for _, e := range botErrs {
		fmt.Fprintf(stderr, "error   %v\n", e)
		actionsLog.error(e)
	}
	nErr := len(res.Errors) + len(botErrs)
	sum := summary{Command: "run", Run: true, Res: res, Groups: engine.GroupUpdates(res.Updates, s.cfg.Prefix()), Actions: actions,
		Errors: append(append([]error(nil), res.Errors...), botErrs...), Elapsed: time.Since(start), Verbose: o.verbose}
	if r.dryRun {
		sum.Command = "run --dry-run"
	}
	sum.write(stderr)
	fmt.Fprintf(stderr, "%d created, %d updated, %d closed, %d up-to-date, %d skipped, %d automerge, %d errors", created, updated, closed, upToDate, skipped, automerged, nErr)
	if r.dryRun {
		fmt.Fprintf(stderr, " (dry run: %d would change, %d would automerge)", would, wouldAutomerge)
	}
	fmt.Fprintln(stderr)
	if nErr > 0 {
		return errReported
	}
	return nil
}
