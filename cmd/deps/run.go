package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/releasenotes"
)

type runOpts struct {
	platform, apiURL, repo, token, base string
	authorName, authorEmail             string
	maxNewPRs                           int
	dryRun                              bool
}

func newRun(o *rootOpts) *cobra.Command {
	r := &runOpts{}
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Push one branch and open or update one pull request per dependency",
		Args:  noArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return runBot(cmd, o, r)
		},
	}
	f := cmd.Flags()
	f.StringVar(&r.platform, "platform", "auto", "forge type: auto|github|gitea|forgejo")
	f.StringVar(&r.apiURL, "api-url", "", "API base URL (default: detected)")
	f.StringVar(&r.repo, "repo", "", "repository owner/name (default: detected)")
	f.StringVar(&r.token, "token", "", "API and git token (default: $DEPS_TOKEN, $GITHUB_TOKEN, $GITEA_TOKEN, $FORGEJO_TOKEN, git credential helper)")
	f.StringVar(&r.base, "base", "", "base branch (default: the repository's default branch)")
	f.StringVar(&r.authorName, "author-name", defaultAuthorName, "commit author name (default: $DEPS_GIT_AUTHOR, then gitAuthor of the config)")
	f.StringVar(&r.authorEmail, "author-email", defaultAuthorEmail, "commit author email (default: $DEPS_GIT_AUTHOR, then gitAuthor of the config)")
	f.IntVar(&r.maxNewPRs, "max-new-prs", 10, "maximum number of pull requests to create per run (0 = unlimited)")
	f.BoolVar(&r.dryRun, "dry-run", false, "report what would change without pushing or calling write APIs")
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

func runBot(cmd *cobra.Command, o *rootOpts, r *runOpts) error {
	ctx := cmd.Context()
	stderr := cmd.ErrOrStderr()
	switch r.platform {
	case "auto", "github", "gitea", "forgejo":
	default:
		return &usageError{fmt.Errorf("--platform: invalid value %q", r.platform)}
	}
	ov := platform.Overrides{APIURL: r.apiURL, Repo: r.repo}
	if r.platform != "auto" {
		ov.Kind = r.platform
	}
	dir, err := filepath.Abs(o.dir)
	if err != nil {
		return err
	}
	repo := gitx.Repo{Dir: dir}
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

	s, err := newSession(o, info, token, client, stderr)
	if err != nil {
		return err
	}
	name, email, err := commitAuthor(cmd, r, os.Getenv("DEPS_GIT_AUTHOR"), s.cfg.GitAuthor)
	if err != nil {
		return err
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
		IgnoredAuthors:     s.cfg.GitIgnoredAuthors,
		ConcurrentLimit:    s.cfg.PRConcurrentLimit,
		HourlyLimit:        s.cfg.PRHourlyLimit,
		DryRun:             r.dryRun,
		RebaseWhen:         s.cfg.RebaseWhen,
		UpdateNotScheduled: s.cfg.UpdateNotScheduledEnabled(),
		MaxNewPRs:          r.maxNewPRs,
		Managers:           s.managers,
		BranchPrefix:       s.cfg.Prefix(),
		Notes:              releasenotes.New(client, s.sem, s.auth, s.gh, s.sources),
	}
	actions, botErrs := bot.Run(ctx, bo, s.root, res, postProcessors(), platform.NewClient(info, token, client), repo)

	var created, updated, closed, upToDate, skipped, would int
	for _, a := range actions {
		fmt.Fprintf(stderr, "pr      %s  %s  %s  %s\n", a.Status, a.Branch, a.Title, a.URL)
		switch {
		case a.Status == bot.StatusCreated:
			created++
		case a.Status == bot.StatusUpdated || a.Status == bot.StatusRebased:
			updated++
		case a.Status == bot.StatusClosed:
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
	}
	nErr := len(res.Errors) + len(botErrs)
	fmt.Fprintf(stderr, "%d created, %d updated, %d closed, %d up-to-date, %d skipped, %d errors", created, updated, closed, upToDate, skipped, nErr)
	if r.dryRun {
		fmt.Fprintf(stderr, " (dry run: %d would change)", would)
	}
	fmt.Fprintln(stderr)
	writeRunSummary(actions)
	if nErr > 0 {
		return errReported
	}
	return nil
}
