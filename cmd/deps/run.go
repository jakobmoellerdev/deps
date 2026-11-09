package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/bot"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/docgen"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/engine"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/releasenotes"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/schedule"
)

type runOpts struct {
	platform, apiURL, repo, token, base string
	repositories                        []string
	triggerSchedule, scheduleTime       string
	authorName, authorEmail             string
	signingKey                          string
	maxNewPRs                           int
	dryRun                              bool
}

func newRun(o *rootOpts) *cli.Command {
	r := &runOpts{}
	return &cli.Command{
		Name:  "run",
		Usage: "Push one branch and open or update one pull request per dependency or group",
		Metadata: map[string]any{docgen.MetaExample: "  # what would happen, without a token and without writes\n" +
			"  deps run --dry-run --show-skipped\n" +
			"  # what the run triggered by the Sunday cron would do\n" +
			"  deps run --dry-run --trigger-schedule '0 0 * * 0'\n" +
			"  # what a run on Monday 05:30 UTC would do with schedule time windows\n" +
			"  deps run --dry-run --schedule-time 2026-11-09T05:30:00Z\n" +
			"  # in CI (the action runs this with DEPS_TOKEN set)\n" +
			"  DEPS_TOKEN=$TOKEN deps run --show-skipped --log-level info"},
		ArgValidator: noArgs,
		Action: func(ctx context.Context, cmd *cli.Command) error {
			return runBot(ctx, cmd, o, r)
		},
		Flags: append(scanFlags(o, scanResults),
			&cli.StringFlag{Name: "platform", Value: "auto", Destination: &r.platform, Usage: "forge type: auto|github|gitea|forgejo|gitlab (auto: from the CI environment, else the origin remote)"},
			&cli.StringFlag{Name: "api-url", Destination: &r.apiURL, Usage: "API base URL (default: detected)"},
			&cli.StringFlag{Name: "repo", Destination: &r.repo, Usage: "repository owner/name, or the GitLab project path (default: detected)"},
			&cli.StringSliceFlag{Name: "repositories", Destination: &r.repositories, Usage: "run on these repositories one after another instead of the checkout in --dir, each cloned into a temporary directory with the token: owner/name, or owner/<glob> (e.g. 'my-org/*', 'my-org/svc-*') for the repositories of an organization or user the token can list (GitLab: group/sub/name, a glob covers subgroups, ** any number of path segments), without archived ones and mirrors (repeatable, comma-separated)"},
			&cli.StringFlag{Name: "token", Destination: &r.token, Usage: "platform API token; git pushes use the checkout's credentials (when not set: $GITHUB_TOKEN, $GITEA_TOKEN, $FORGEJO_TOKEN, $GITLAB_TOKEN, then the git credential helper)"},
			&cli.StringFlag{Name: "base", Destination: &r.base, Usage: "base branch, overriding baseBranches of the config (default: baseBranches, else the repository's default branch)"},
			&cli.StringFlag{Name: "author-name", Destination: &r.authorName, Usage: "commit author name (when not set: $DEPS_GIT_AUTHOR, then gitAuthor of the config, then " + defaultAuthorName + ")"},
			&cli.StringFlag{Name: "author-email", Destination: &r.authorEmail, Usage: "commit author email (when not set: $DEPS_GIT_AUTHOR, then gitAuthor of the config, then " + defaultAuthorEmail + ")"},
			&cli.IntFlag{Name: "max-new-prs", Value: 10, Destination: &r.maxNewPRs, Usage: "maximum number of pull requests to create per run (0 = unlimited)"},
			&cli.BoolFlag{Name: "dry-run", Destination: &r.dryRun, Usage: "report what would change without pushing or calling write APIs (no PR, merge or issue writes); works without a token"},
			&cli.StringFlag{Name: "trigger-schedule", Destination: &r.triggerSchedule, Usage: "act as if the run was triggered by this workflow cron, overriding $GITHUB_EVENT_NAME/$GITHUB_EVENT_PATH"},
			&cli.StringFlag{Name: "schedule-time", Destination: &r.scheduleTime, Usage: "evaluate schedule time windows at this RFC 3339 time instead of now, e.g. 2026-11-09T05:30:00+01:00"},
			&cli.StringFlag{Name: "signing-key", Destination: &r.signingKey, Usage: "sign deps' commits with this passphrase-less private key (OpenSSH or armored OpenPGP), given as the key itself or a file path (when not set: unsigned)"},
			allowTasksFlag(o),
		),
	}
}

const (
	defaultAuthorName  = "deps[bot]"
	defaultAuthorEmail = "deps-bot@noreply.invalid"
)

// commitAuthor resolves the commit author field by field: an explicitly set flag, then
// env ($DEPS_GIT_AUTHOR), then cfg (gitAuthor), then the defaults. env and cfg are
// 'Name <email>'.
func commitAuthor(cmd *cli.Command, r *runOpts, env, cfg string) (name, email string, err error) {
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
	if cmd.IsSet("author-name") {
		name = r.authorName
	}
	if cmd.IsSet("author-email") {
		email = r.authorEmail
	}
	return name, email, nil
}

// detectTrigger resolves what started the run: the --trigger-schedule value override (from
// src, the flag or its variable), then the CI event. A schedule event whose cron cannot be
// read matches every cron trigger, with a warning. The run time of schedule windows is at
// (--schedule-time, from atSrc), else now.
func detectTrigger(ctx context.Context, override, src, at, atSrc string, getenv func(string) string) (schedule.Trigger, error) {
	if override != "" {
		if err := schedule.ValidateCron(override); err != nil {
			return schedule.Trigger{}, &usageError{fmt.Errorf("%s: %q: %w", src, override, err)}
		}
	}
	now := time.Now()
	if at != "" {
		var err error
		if now, err = time.Parse(time.RFC3339, at); err != nil {
			return schedule.Trigger{}, &usageError{fmt.Errorf("%s: %q: want an RFC 3339 time like 2026-11-09T05:30:00Z", atSrc, at)}
		}
	}
	t, err := schedule.Detect(getenv, override)
	if err != nil {
		slog.WarnContext(ctx, "cannot read the triggering cron; every cron trigger matches", "err", err)
	} else if t.Event == schedule.EventSchedule && t.Cron == "" {
		slog.WarnContext(ctx, "schedule event without a cron in the payload; every cron trigger matches")
	}
	t.Time = now
	slog.InfoContext(ctx, "trigger", "event", t.Event, "cron", t.Cron, "time", now.Format(time.RFC3339))
	return t, nil
}

// newSigner prepares commit signing from the --signing-key value key (from src, the flag or
// its variable); nil (unsigned) when empty. An unrecognized key is a usage error.
func newSigner(ctx context.Context, key, src string) (*gitx.Signer, error) {
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

// platformCommitEnv overrides the config key platformCommit.
const platformCommitEnv = "DEPS_PLATFORM_COMMIT"

// platformCommitSetting resolves platformCommit: env ($DEPS_PLATFORM_COMMIT), then cfg, then
// auto. It returns the value and where it came from (the variable, the config key, or "" for
// the default); a value that is none of config.PlatformCommits is a usage error.
func platformCommitSetting(env, cfg string) (v, src string, err error) {
	v, src = env, platformCommitEnv
	if v == "" {
		v, src = cfg, "platformCommit"
	}
	if v == "" {
		return config.PlatformCommitAuto, "", nil
	}
	if !slices.Contains(config.PlatformCommits, v) {
		return "", "", &usageError{fmt.Errorf("%s: must be one of %s, got %q", src, strings.Join(config.PlatformCommits, ", "), v)}
	}
	return v, src, nil
}

// platformCommits resolves platformCommit (platformCommitSetting) to whether commits go
// through the GitHub API. auto enables them on GitHub for a GitHub App installation token
// when no signing key is set. enabled on another forge or together with a signing key is a
// usage error; without a token (a dry run) it falls back to git with a warning.
func platformCommits(ctx context.Context, env, cfg string, kind platform.Kind, token string, signed bool) (bool, error) {
	v, src, err := platformCommitSetting(env, cfg)
	if err != nil {
		return false, err
	}
	switch v {
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
	return kind == platform.GitHub && platform.IsAppToken(token) && !signed, nil
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
		files[i] = platform.CommitFile{Path: c.Path, Executable: c.Mode&0o111 != 0, Content: c.New, Gitlink: c.Gitlink}
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

func (g apiGit) BranchTip(ctx context.Context, branch string) (platform.Tip, error) {
	return g.api.BranchTip(ctx, branch)
}

// runShared is what the repositories of one run share.
type runShared struct {
	trigger    schedule.Trigger
	allowTasks engine.TaskAllowlist
	signer     *gitx.Signer
	client     *http.Client
	token      string
}

func runBot(ctx context.Context, cmd *cli.Command, o *rootOpts, r *runOpts) error {
	start := time.Now()
	switch r.platform {
	case "auto", "github", "gitea", "forgejo", "gitlab":
	default:
		return &usageError{fmt.Errorf("--platform: invalid value %q", r.platform)}
	}
	repos := r.repositories
	if !cmd.IsSet("repositories") {
		repos = strings.FieldsFunc(os.Getenv(flagEnv("repositories")), func(c rune) bool { return c == ',' || unicode.IsSpace(c) })
	}
	if err := validateRepositories(repos); err != nil {
		return &usageError{err}
	}
	if len(repos) > 0 && r.repo != "" {
		return &usageError{errors.New("--repo and --repositories are mutually exclusive")}
	}
	trigger, err := detectTrigger(ctx, r.triggerSchedule, flagSource(cmd, "trigger-schedule"), r.scheduleTime, flagSource(cmd, "schedule-time"), os.Getenv)
	if err != nil {
		return err
	}
	sh := &runShared{trigger: trigger, allowTasks: taskAllowlist(o), client: newHTTPClient()}
	if sh.signer, err = newSigner(ctx, r.signingKey, flagSource(cmd, "signing-key")); err != nil {
		return err
	}
	defer func() {
		if err := sh.signer.Close(); err != nil {
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
	remote, err := gitx.Repo{Dir: dir}.RemoteURL(ctx)
	if err != nil {
		if len(repos) == 0 {
			return err
		}
		remote = "" // the forge must come from the CI environment
	}
	info, err := platform.Detect(ctx, os.Getenv, remote, ov, sh.client)
	if err != nil {
		if len(repos) > 0 && remote == "" {
			return fmt.Errorf("--repositories: no forge in the CI environment and no origin remote in %s to detect it from: %w", dir, err)
		}
		return err
	}
	if info.Kind != platform.GitLab {
		for _, e := range repos {
			if owner, _ := splitRepository(e); strings.Contains(owner, "/") {
				return &usageError{fmt.Errorf("--repositories: %q: %s repositories are owner/name; nested group paths exist only on GitLab", e, info.Kind)}
			}
		}
	}

	sh.token = r.token
	if sh.token == "" {
		sh.token = envToken()
	}
	if sh.token == "" {
		if _, pass, ok := httpx.NewAuth(info, "", os.Getenv, httpx.WithSecretHook(actionsLog.mask)).Helper(ctx, info.Host); ok {
			sh.token = pass
		}
	}
	if sh.token == "" && !r.dryRun {
		return fmt.Errorf("no token for %s", info.Host)
	}
	if len(repos) > 0 {
		return runRepositories(ctx, cmd, o, r, sh, info, repos)
	}

	s, err := newSession(ctx, o, info, sh.token, sh.client, cmd.Root().ErrWriter)
	if err != nil {
		return err
	}
	defer s.close()
	return runRepo(ctx, cmd, o, r, sh, s, start, nil)
}

// runRepo runs the bot on the checkout of session s. In a multi-repository run (multi
// non-nil) its summary names the repository and its result is added to multi.
func runRepo(ctx context.Context, cmd *cli.Command, o *rootOpts, r *runOpts, sh *runShared, s *session, start time.Time, multi *multiRun) error {
	stderr := cmd.Root().ErrWriter
	info, token, client := s.info, sh.token, sh.client
	repo := gitx.Repo{Dir: s.root, Signer: sh.signer}
	name, email, err := commitAuthor(cmd, r, os.Getenv("DEPS_GIT_AUTHOR"), s.cfg.GitAuthor)
	if err != nil {
		return err
	}
	usePlatform, err := platformCommits(ctx, os.Getenv(platformCommitEnv), s.cfg.PlatformCommit, info.Kind, token, sh.signer != nil)
	if err != nil {
		return err
	}
	gitOf := func(repo gitx.Repo) bot.Git { return repo }
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
		gitOf = func(repo gitx.Repo) bot.Git { return apiGit{Repo: repo, api: commits} }
		slog.InfoContext(ctx, "platform commits", "author", name+" <"+email+">")
		if !platform.IsAppToken(token) {
			slog.WarnContext(ctx, "platform commits with a token that is not a GitHub App installation token: GitHub leaves them unsigned")
		}
	}
	pc := platform.NewClient(info, token, client)
	bases, multiBase, err := runBases(ctx, r.base, s.cfg, repo, pc)
	if err != nil {
		return err
	}
	defer closeBases(ctx, repo, bases)
	// Every base is resolved with the checkout's config, its rules matching that base.
	botBases := make([]bot.Base, len(bases))
	var resErrs []error
	var unused []int
	for i, b := range bases {
		bs := s.forBase(b.repo.Dir, b.name)
		res, err := bs.resolve(ctx)
		if err != nil {
			return err
		}
		errs := res.Errors
		if multiBase {
			fmt.Fprintf(stderr, "base    %s\n", b.name)
			errs = make([]error, len(res.Errors))
			for j, e := range res.Errors {
				errs[j] = fmt.Errorf("%s: %w", b.name, e)
			}
		}
		report(stderr, o.term, o.showSkipped, res, errs, nil)
		resErrs = append(resErrs, errs...)
		// A rule is unused when it matched no dependency on any base.
		u := bs.cfg.UnusedRules(res.Deps)
		if i > 0 {
			u = slices.DeleteFunc(u, func(r int) bool { return !slices.Contains(unused, r) })
		}
		unused = u
		botBases[i] = bot.Base{Name: b.name, Root: bs.root, Res: res, Git: gitOf(b.repo)}
	}
	bo := bot.Options{
		AuthorName:         name,
		AuthorEmail:        email,
		IgnoredAuthors:     ignored,
		ConcurrentLimit:    s.cfg.PRConcurrentLimit,
		HourlyLimit:        s.cfg.PRHourlyLimit,
		NotPendingHours:    s.cfg.NotPendingHours(),
		DryRun:             r.dryRun,
		RebaseWhen:         s.cfg.RebaseWhen,
		RebaseLabel:        s.cfg.RebaseLabelName(),
		StopUpdatingLabel:  s.cfg.StopUpdatingLabelName(),
		UpdateNotScheduled: s.cfg.UpdateNotScheduledEnabled(),
		Trigger:            sh.trigger,
		MaxNewPRs:          r.maxNewPRs,
		Managers:           s.managers,
		BranchPrefix:       s.cfg.Prefix(),
		MultiBase:          multiBase,
		AllowTasks:         sh.allowTasks,
		Notes:              releasenotes.New(client, s.sem, s.auth, s.gh, s.sources),
		Progress:           s.progress,
		PRLog:              prLog(stderr),
	}
	if s.cfg.DependencyDashboard {
		bo.Dashboard = &bot.DashboardOptions{Title: s.cfg.DashboardTitle(), Labels: s.cfg.DependencyDashboardLabels}
	}
	actions, botErrs := bot.Run(ctx, bo, botBases, postProcessors(s), pc)

	for _, a := range actions {
		if a.Dashboard {
			fmt.Fprintf(stderr, "dashboard %s  %s  %s\n", a.Status, a.Title, a.URL)
			continue
		}
		fmt.Fprintf(stderr, "pr      %s  %s  %s  %s\n", a.Display(), a.Branch, a.Title, a.URL)
	}
	n := countActions(actions)
	for _, e := range botErrs {
		reportError(stderr, o.term, e)
	}
	res, groups := runResult(botBases, s.cfg.Prefix(), multiBase, resErrs)
	nErr := len(res.Errors) + len(botErrs)
	sum := summary{Command: "run", Run: true, Res: res, Groups: groups, Actions: actions,
		Errors: append(append([]error(nil), res.Errors...), botErrs...), Elapsed: time.Since(start), ShowSkipped: o.showSkipped, Unused: unused, Config: s.cfg, Report: o.report, Style: o.term.style}
	if r.dryRun {
		sum.Command = "run --dry-run"
	}
	if multi != nil {
		sum.Repository = info.Repo
		multi.add(&sum)
	}
	sum.write(stderr)
	var reportErr error
	if multi == nil {
		reportErr = sum.writeMachine(stderr, sum.Report)
	}
	fmt.Fprintf(stderr, "%d created, %d updated, %d closed, %d up-to-date, %d skipped, %d automerge, %d errors", n.created, n.updated, n.closed, n.upToDate, n.skipped, n.automerged, nErr)
	if r.dryRun {
		fmt.Fprintf(stderr, " (dry run: %d would change, %d would automerge)", n.would, n.wouldAutomerge)
	}
	fmt.Fprintln(stderr)
	if nErr > 0 {
		return errReported
	}
	return reportErr
}

// validateRepositories checks the --repositories entries: owner/name, or owner/<glob> with
// path.Match globs in the path after the owner, where ** matches any number of path
// segments. GitLab's owner can be a group path (group/sub/name, group/sub/*, group/**).
func validateRepositories(repos []string) error {
	for _, e := range repos {
		owner, pattern := splitRepository(e)
		if owner == "" || pattern == "" || slices.Contains(strings.Split(e, "/"), "") {
			return fmt.Errorf("--repositories: %q must be owner/name or owner/<glob>", e)
		}
		for seg := range strings.SplitSeq(pattern, "/") {
			if _, err := path.Match(seg, ""); seg != "**" && err != nil {
				return fmt.Errorf("--repositories: %q: %w", e, err)
			}
		}
	}
	return nil
}

// splitRepository splits a --repositories entry into its owner, the path segments before
// the first one with a glob (or before the last), and the name pattern after it.
func splitRepository(e string) (owner, pattern string) {
	segs := strings.Split(e, "/")
	i := slices.IndexFunc(segs, isGlob)
	if i < 0 {
		i = len(segs) - 1
	}
	return strings.Join(segs[:i], "/"), strings.Join(segs[i:], "/")
}

func isGlob(s string) bool { return strings.ContainsAny(s, "*?[\\") }

// matchRepository reports whether the path segments name match those of pattern: each a
// path.Match pattern, or ** for any number of segments.
func matchRepository(pattern, name []string) bool {
	if len(pattern) == 0 {
		return len(name) == 0
	}
	if pattern[0] == "**" {
		for i := range len(name) + 1 {
			if matchRepository(pattern[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	if m, _ := path.Match(pattern[0], name[0]); !m {
		return false
	}
	return matchRepository(pattern[1:], name[1:])
}

// expandRepositories resolves the --repositories entries to owner/name (GitLab project
// paths): a glob entry to the matching repositories of its owner (platform.ListRepos), in the
// forge's order. Each repository is listed once, where it first matches.
func expandRepositories(ctx context.Context, info platform.Info, token string, client *http.Client, entries []string) ([]string, error) {
	var out []string
	seen := map[string]bool{} // lower-cased names in out; forges match them case-insensitively
	add := func(r string) {
		if k := strings.ToLower(r); !seen[k] {
			seen[k] = true
			out = append(out, r)
		}
	}
	listed := map[string][]string{}
	for _, e := range entries {
		owner, pattern := splitRepository(e)
		if !isGlob(pattern) {
			add(e)
			continue
		}
		all, ok := listed[owner]
		if !ok {
			var err error
			if all, err = platform.ListRepos(ctx, info, token, client, owner); err != nil {
				return nil, fmt.Errorf("--repositories: list the repositories of %s: %w", owner, err)
			}
			listed[owner] = all
		}
		segs := strings.Split(pattern, "/")
		matched := 0
		for _, full := range all {
			if len(full) <= len(owner) || full[len(owner)] != '/' || !strings.EqualFold(full[:len(owner)], owner) ||
				!matchRepository(segs, strings.Split(full[len(owner)+1:], "/")) {
				continue
			}
			matched++
			add(full)
		}
		if matched == 0 {
			slog.WarnContext(ctx, "no repository matches", "pattern", e)
		}
	}
	return out, nil
}

// runRepositories runs the bot on each repository of --repositories in turn: a shallow clone
// of its base branch in a temporary directory, authenticated with the token, and the config
// found there (or --config). The datasources are shared, so a release list is fetched once
// for all of them. A failing repository does not stop the others.
func runRepositories(ctx context.Context, cmd *cli.Command, o *rootOpts, r *runOpts, sh *runShared, info platform.Info, entries []string) error {
	start := time.Now()
	stderr := cmd.Root().ErrWriter
	names, err := expandRepositories(ctx, info, sh.token, sh.client, entries)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		return fmt.Errorf("--repositories: no repository matches %s", strings.Join(entries, ", "))
	}
	work, err := os.MkdirTemp("", "deps-repositories-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	command := "run"
	if r.dryRun {
		command = "run --dry-run"
	}
	multi := newMultiRun(command)
	header := gitAuthHeader(sh.token)
	var base *session // the first session; later repositories share its datasources
	defer func() {
		if base != nil {
			base.close()
		}
		actionsLog.setRepo("")
	}()
	failed := 0
	for i, name := range names {
		fmt.Fprintf(stderr, "repository %s\n", name)
		actionsLog.setRepo(name)
		ri := info
		ri.Repo = name
		dir := filepath.Join(work, strconv.Itoa(i))
		err := func() error {
			defer os.RemoveAll(dir)
			if err := cloneRepository(ctx, ri, header, r.base, dir); err != nil {
				return err
			}
			var s *session
			var err error
			if base == nil {
				ro := *o
				ro.dir = dir
				if s, err = newSession(ctx, &ro, ri, sh.token, sh.client, stderr); err == nil {
					base = s
				}
			} else if s, err = base.forCheckout(ctx, dir, o.config, ri); err == nil {
				printLint(stderr, s.cfg)
			}
			if err != nil {
				return err
			}
			return runRepo(ctx, cmd, o, r, sh, s, time.Now(), multi)
		}()
		if err == nil {
			continue
		}
		failed++
		if !errors.Is(err, errReported) {
			reportError(stderr, o.term, fmt.Errorf("%s: %w", name, err))
			multi.fail(name, err)
		}
		if ctx.Err() != nil {
			break
		}
	}
	actionsLog.setRepo("")
	reportErr := multi.write(stderr, o.report, time.Since(start))
	fmt.Fprintf(stderr, "%d repositories, %d failed\n", len(names), failed)
	if failed > 0 {
		return errReported
	}
	return reportErr
}

// gitAuthHeader is the http.extraheader that authenticates git with token as Basic
// credentials, as actions/checkout sets it; "" without a token. The token and the encoded
// credentials are masked in the CI log.
func gitAuthHeader(token string) string {
	if token == "" {
		return ""
	}
	creds := base64.StdEncoding.EncodeToString([]byte("x-access-token:" + token))
	actionsLog.mask(token)
	actionsLog.mask(creds)
	return "AUTHORIZATION: basic " + creds
}

// cloneRepository clones the base branch (default: the default branch) of the repository
// info.Repo into dir with the git auth header for the forge, which the clone keeps for the
// pushes of the run.
func cloneRepository(ctx context.Context, info platform.Info, header, base, dir string) error {
	scope := strings.TrimSuffix(info.ServerURL, "/") + "/"
	if err := gitx.Clone(ctx, scope+info.Repo+".git", dir, base, scope, header); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	return nil
}
