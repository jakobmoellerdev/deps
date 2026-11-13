package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"

	"github.com/urfave/cli/v3"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/config/extendref"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/gitx"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// repoSpec is one --repositories entry, written <repo>[//<path>][#<ref>]: the repository
// (owner/name, owner/<glob>, a GitLab project path, or a git URL), the directory of the
// clone to scan and the ref to check out.
type repoSpec struct {
	Repo string
	Path string // clean relative slash path to scan in the clone; "" = its root
	Ref  string // branch, tag or commit; "" = the repository's default branch
	URL  bool   // Repo is a git URL, not owner/name
}

// parseRepoSpec parses a --repositories entry with the grammar of extends entries
// (extendref.Parse), where //<path> is a directory of the repository instead of a file and
// defaults to its root, and the repository may be a glob.
func parseRepoSpec(e string) (repoSpec, error) {
	s := repoSpec{Repo: strings.TrimSpace(e)}
	if i := strings.LastIndexByte(s.Repo, '#'); i >= 0 {
		s.Repo, s.Ref = s.Repo[:i], s.Repo[i+1:]
		if !extendref.ValidRef(s.Ref) {
			return repoSpec{}, fmt.Errorf("invalid ref %q after '#'", s.Ref)
		}
	}
	start := 0
	if i := strings.Index(s.Repo, "://"); i >= 0 {
		start = i + len("://")
		s.URL = true
	}
	if i := strings.Index(s.Repo[start:], "//"); i >= 0 {
		s.Repo, s.Path = s.Repo[:start+i], s.Repo[start+i+2:]
		if !extendref.ValidPath(s.Path) {
			return repoSpec{}, fmt.Errorf("invalid path %q after '//': must be a clean relative slash path", s.Path)
		}
	}
	switch {
	case s.URL:
		u, err := url.Parse(s.Repo)
		if err != nil {
			return repoSpec{}, fmt.Errorf("invalid repository URL: %w", err)
		}
		switch u.Scheme {
		case "https", "http", "ssh", "git", "file":
		default:
			return repoSpec{}, fmt.Errorf("repository URL %q: scheme must be https, http, ssh, git or file", s.Repo)
		}
		if u.Scheme != "file" && u.Host == "" {
			return repoSpec{}, fmt.Errorf("repository URL %q has no host", s.Repo)
		}
		if strings.Trim(u.Path, "/") == "" {
			return repoSpec{}, fmt.Errorf("repository URL %q has no path", s.Repo)
		}
		if _, hasPass := u.User.Password(); u.User != nil && hasPass {
			return repoSpec{}, fmt.Errorf("repository URL %q must not contain a password; the platform token or git credentials authenticate", u.Redacted())
		}
	default:
		owner, pattern := splitRepository(s.Repo)
		if owner == "" || pattern == "" || strings.Contains(s.Repo, "//") || strings.HasPrefix(s.Repo, "/") || strings.HasSuffix(s.Repo, "/") {
			return repoSpec{}, fmt.Errorf("%q must be owner/name, owner/<glob> or a git URL", e)
		}
		if err := validateGlob(pattern); err != nil {
			return repoSpec{}, fmt.Errorf("%q: %w", e, err)
		}
		if s.Ref != "" && isGlob(pattern) {
			return repoSpec{}, fmt.Errorf("%q: a '#<ref>' needs one repository, not a glob", e)
		}
	}
	return s, nil
}

// parseRepoSpecs parses the --repositories entries, reporting problems as usage errors.
func parseRepoSpecs(entries []string) ([]repoSpec, error) {
	specs := make([]repoSpec, 0, len(entries))
	for _, e := range entries {
		s, err := parseRepoSpec(e)
		if err != nil {
			return nil, &usageError{fmt.Errorf("--repositories: %w", err)}
		}
		specs = append(specs, s)
	}
	return specs, nil
}

// repositoryEntries returns the --repositories entries of cmd: the flag, else its variable
// (whitespace- or comma-separated; applyEnv leaves string slice flags to their command).
func repositoryEntries(cmd *cli.Command, flagValue []string) []string {
	if cmd.IsSet("repositories") {
		return flagValue
	}
	return strings.FieldsFunc(os.Getenv(flagEnv("repositories")), func(c rune) bool { return c == ',' || unicode.IsSpace(c) })
}

// repositoriesFlag is --repositories, the remote repositories a command reads instead of the
// checkout in --dir. what names what the command does with each of them.
func repositoriesFlag(dest *[]string, what string) cli.Flag {
	return &cli.StringSliceFlag{Name: "repositories", Destination: dest, Usage: what +
		" these repositories one after another instead of the checkout in --dir, each cloned into a temporary directory with the token: owner/name, a git URL, or owner/<glob> (e.g. 'my-org/*', 'my-org/svc-*') for the repositories of an organization or user the token can list (GitLab: group/sub/name, a glob covers subgroups, ** any number of path segments), without archived ones and mirrors; an entry may name a directory to scan and the ref to read, <repo>//<dir>#<ref> (repeatable, comma-separated)"}
}

// repoTarget is one repository to clone: its name on the forge (owner/name, a project path,
// or the path of its URL), the URL to clone, the credential scope of that URL, the directory
// of the clone to scan and the ref to check out.
type repoTarget struct {
	Name string
	URL  string
	Path string
	Ref  string
	Info platform.Info
}

// remoteSource is the repository source of a command: the parsed --repositories entries with
// the forge they are on and the credentials to read them.
type remoteSource struct {
	specs  []repoSpec
	info   platform.Info
	token  string
	client *http.Client
	// tokenHost is the forge host token authenticates against; the token is only ever sent
	// to that host, so a target on another forge is cloned without it.
	tokenHost string
	// into is where kept clones are made (<into>/<owner>/<name>, apply --into); "" makes a
	// temporary clone that is removed again after the repository.
	into string
}

// newRemoteSource parses entries and detects the forge of the owner/name entries among them:
// the CI environment, the overrides ov, else the origin remote of --dir. Entries that are git
// URLs carry their own forge and need none of them.
func newRemoteSource(ctx context.Context, o *rootOpts, entries []string, token string, client *http.Client) (*remoteSource, error) {
	specs, err := parseRepoSpecs(entries)
	if err != nil {
		return nil, err
	}
	src := &remoteSource{specs: specs, token: token, client: client}
	named := false
	for _, s := range specs {
		named = named || !s.URL
	}
	if !named {
		// With URL entries alone the forge of the token is not needed to resolve them, but
		// it still decides which of them may see the token; it can only come from the CI
		// environment here, and a missing one simply leaves every clone anonymous.
		if info, detectErr := platform.Detect(ctx, os.Getenv, "", platform.Overrides{}, client); detectErr == nil {
			src.tokenHost = info.Host
		}
		return src, nil
	}
	dir, err := filepath.Abs(o.dir)
	if err != nil {
		return nil, err
	}
	remote, remoteErr := gitx.Repo{Dir: dir}.RemoteURL(ctx)
	if remoteErr != nil {
		remote = "" // the forge must come from the CI environment or the overrides
	}
	if src.info, err = platform.Detect(ctx, os.Getenv, remote, platform.Overrides{}, client); err != nil {
		if remote == "" {
			return nil, fmt.Errorf("--repositories: no forge in the CI environment and no origin remote in %s to detect it from (give a git URL entry instead): %w", dir, err)
		}
		return nil, err
	}
	src.tokenHost = src.info.Host
	if src.info.Kind != platform.GitLab {
		for _, s := range specs {
			if owner, _ := splitRepository(s.Repo); !s.URL && strings.Contains(owner, "/") {
				return nil, &usageError{fmt.Errorf("--repositories: %q: %s repositories are owner/name; nested group paths exist only on GitLab", s.Repo, src.info.Kind)}
			}
		}
	}
	return src, nil
}

// targets resolves the entries to the repositories to clone, expanding globs with one list
// request per owner (expandRepositories). Each repository is listed once, where it first
// matches; a URL entry keeps its own forge, detected from the URL.
func (src *remoteSource) targets(ctx context.Context) ([]repoTarget, error) {
	var out []repoTarget
	seen := map[string]bool{}
	for _, s := range src.specs {
		if s.URL {
			// A file:// repository is on no forge; its deps are looked up as a plain checkout's.
			var info platform.Info
			if !strings.HasPrefix(s.Repo, "file://") {
				var err error
				if info, err = platform.Detect(ctx, func(string) string { return "" }, s.Repo, platform.Overrides{}, src.client); err != nil {
					return nil, fmt.Errorf("--repositories: %q: %w", s.Repo, err)
				}
			}
			t := repoTarget{Name: info.Repo, URL: s.Repo, Path: s.Path, Ref: s.Ref, Info: info}
			if t.Name == "" {
				t.Name = strings.TrimSuffix(strings.Trim(s.Repo[strings.Index(s.Repo, "://")+3:], "/"), ".git")
			}
			if !seen[strings.ToLower(t.URL)] {
				seen[strings.ToLower(t.URL)] = true
				out = append(out, t)
			}
			continue
		}
		names, err := expandRepositories(ctx, src.info, src.token, src.client, []string{s.Repo})
		if err != nil {
			return nil, err
		}
		scope := strings.TrimSuffix(src.info.ServerURL, "/") + "/"
		for _, name := range names {
			if seen[strings.ToLower(name)] {
				continue
			}
			seen[strings.ToLower(name)] = true
			info := src.info
			info.Repo = name
			out = append(out, repoTarget{Name: name, URL: scope + name + ".git", Path: s.Path, Ref: s.Ref, Info: info})
		}
	}
	return out, nil
}

// eachRepository clones every repository of the source in turn and calls fn with the
// directory to scan. Without an --into directory the clone is temporary and removed right
// after the repository; with one it is kept at <into>/<owner>/<name> and stays after the
// command, so its changes can be inspected. A failing repository is reported and recorded in
// multi (when not nil) and does not stop the others; a canceled context does. It returns the
// repositories handled and how many of them failed.
func (src *remoteSource) eachRepository(ctx context.Context, o *rootOpts, stderr io.Writer, multi *multiRun, fn func(dir, name string, info platform.Info) error) (total, failed int, err error) {
	targets, err := src.targets(ctx)
	if err != nil {
		return 0, 0, err
	}
	if len(targets) == 0 {
		names := make([]string, len(src.specs))
		for i, s := range src.specs {
			names[i] = s.Repo
		}
		return 0, 0, fmt.Errorf("--repositories: no repository matches %s", strings.Join(names, ", "))
	}
	work := src.into
	if work == "" {
		if work, err = os.MkdirTemp("", "deps-repositories-*"); err != nil {
			return 0, 0, err
		}
		defer os.RemoveAll(work)
	}
	defer actionsLog.setRepo("")
	for i, t := range targets {
		fmt.Fprintf(stderr, "repository %s\n", t.Name)
		actionsLog.setRepo(t.Name)
		dir := filepath.Join(work, strconv.Itoa(i))
		keep := src.into != ""
		if keep {
			dir = filepath.Join(work, filepath.FromSlash(t.Name))
		}
		err := func() error {
			if !keep {
				defer os.RemoveAll(dir)
			}
			if err := cloneRepository(ctx, t, src.authHeader(t), dir); err != nil {
				return err
			}
			if keep {
				fmt.Fprintf(stderr, "checkout %s\n", dir)
			}
			return fn(filepath.Join(dir, filepath.FromSlash(t.Path)), t.Name, t.Info)
		}()
		if err != nil {
			failed++
			if !errors.Is(err, errReported) {
				reportError(stderr, o.term, fmt.Errorf("%s: %w", t.Name, err))
				if multi != nil {
					multi.fail(t.Name, err)
				}
			}
			if ctx.Err() != nil {
				break
			}
		}
	}
	return len(targets), failed, nil
}

// repoSessions opens the session of each clone: the first one of the command loads the
// config and builds the datasources, later ones share them (session.forCheckout), so a
// release list fetched for one repository is reused for the next. The token a session gets
// is the one of its own forge (remoteSource.tokenFor): the session's auth registers it for
// the forge of the clone, so a clone of a foreign forge must be opened without it.
type repoSessions struct {
	base *session
}

func (p *repoSessions) open(ctx context.Context, o *rootOpts, dir string, info platform.Info, token string, client *http.Client, stderr io.Writer) (*session, error) {
	if p.base == nil {
		ro := *o
		ro.dir = dir
		s, err := newSession(ctx, &ro, info, token, client, stderr)
		if err != nil {
			return nil, err
		}
		p.base = s
		return s, nil
	}
	s, err := p.base.forCheckout(ctx, dir, o.config, info)
	if err != nil {
		return nil, err
	}
	printLint(stderr, s.cfg)
	return s, nil
}

// close releases what the first session holds; the later ones share it.
func (p *repoSessions) close() {
	if p.base != nil {
		p.base.close()
	}
}

// tokenFor is the forge token a target on info's forge may see: the source's token when
// info names the forge the token belongs to, "" for every other forge. A --repositories
// entry given as a git URL carries the forge of that URL, which may be a host the token
// does not belong to, and handing it the forge credentials would leak them to a foreign
// server.
func (src *remoteSource) tokenFor(info platform.Info) string {
	if src.tokenHost == "" || !strings.EqualFold(info.Host, src.tokenHost) {
		return ""
	}
	return src.token
}

// authHeader is the git auth header the clone of t gets: the header is written into the
// clone's config, so only a target on the token's own forge is authenticated and every
// other one is cloned anonymously.
func (src *remoteSource) authHeader(t repoTarget) string {
	return gitAuthHeader(src.tokenFor(t.Info))
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

// cloneRepository clones the target's ref (default: the default branch) into dir with the git
// auth header for its forge, which the clone keeps for the pushes of a run.
func cloneRepository(ctx context.Context, t repoTarget, header, dir string) error {
	scope := strings.TrimSuffix(t.Info.ServerURL, "/") + "/"
	if t.Info.ServerURL == "" {
		scope = ""
	}
	if err := gitx.Clone(ctx, t.URL, dir, t.Ref, scope, header); err != nil {
		return fmt.Errorf("clone: %w", err)
	}
	return nil
}
