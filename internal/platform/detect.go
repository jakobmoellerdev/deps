package platform

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// probeScheme is the scheme used for platform probes; tests point it at TLS test servers.
var probeScheme = "https"

// FromEnv detects the platform from CI environment variables without network access.
func FromEnv(getenv func(string) string) (Info, bool) {
	var info Info
	switch {
	case getenv("FORGEJO_ACTIONS") == "true":
		pick := func(a, b string) string {
			if v := getenv(a); v != "" {
				return v
			}
			return getenv(b)
		}
		info = Info{
			Kind:      Forgejo,
			ServerURL: pick("FORGEJO_SERVER_URL", "GITHUB_SERVER_URL"),
			APIURL:    pick("FORGEJO_API_URL", "GITHUB_API_URL"),
			Repo:      pick("FORGEJO_REPOSITORY", "GITHUB_REPOSITORY"),
		}
	case getenv("GITEA_ACTIONS") == "true":
		info = envInfo(Gitea, getenv)
	case getenv("GITHUB_ACTIONS") == "true":
		info = envInfo(GitHub, getenv)
	case getenv("GITLAB_CI") == "true":
		info = Info{
			Kind:      GitLab,
			ServerURL: getenv("CI_SERVER_URL"),
			APIURL:    getenv("CI_API_V4_URL"),
			Repo:      getenv("CI_PROJECT_PATH"),
		}
	default:
		return Info{}, false
	}
	info.ServerURL = strings.TrimSuffix(info.ServerURL, "/")
	info.APIURL = strings.TrimSuffix(info.APIURL, "/")
	if u, err := url.Parse(info.ServerURL); err == nil {
		info.Host = u.Hostname()
	}
	if info.APIURL == "" {
		info.APIURL = apiURL(info.Kind, info.ServerURL, info.Host)
	}
	return info, true
}

func envInfo(k Kind, getenv func(string) string) Info {
	return Info{
		Kind:      k,
		ServerURL: getenv("GITHUB_SERVER_URL"),
		APIURL:    getenv("GITHUB_API_URL"),
		Repo:      getenv("GITHUB_REPOSITORY"),
	}
}

// WorkflowRun is the Actions workflow run deps runs in.
type WorkflowRun struct {
	Workflow string // GITHUB_WORKFLOW, the workflow's name; "" = unknown
	Number   string // GITHUB_RUN_NUMBER; "" = unknown
	URL      string // the run's web page
}

// RunFromEnv returns the GitHub, Gitea or Forgejo Actions workflow run deps runs in, from
// the variables FromEnv reads plus GITHUB_RUN_ID: <server>/<repository>/actions/runs/<id>
// (Gitea and Forgejo address runs by id like GitHub), with /attempts/<n> on GitHub for a
// re-run (GITHUB_RUN_ATTEMPT > 1; Gitea and Forgejo have no such page). Elsewhere, also on
// GitLab CI, it is the zero WorkflowRun.
func RunFromEnv(getenv func(string) string) WorkflowRun {
	info, ok := FromEnv(getenv)
	id := getenv("GITHUB_RUN_ID")
	if !ok || info.Kind == GitLab || info.ServerURL == "" || info.Repo == "" || id == "" {
		return WorkflowRun{}
	}
	run := WorkflowRun{
		Workflow: getenv("GITHUB_WORKFLOW"),
		Number:   getenv("GITHUB_RUN_NUMBER"),
		URL:      info.ServerURL + "/" + info.Repo + "/actions/runs/" + url.PathEscape(id),
	}
	if n, err := strconv.Atoi(getenv("GITHUB_RUN_ATTEMPT")); info.Kind == GitHub && err == nil && n > 1 {
		run.URL += "/attempts/" + strconv.Itoa(n)
	}
	return run
}

// apiURL derives the API base URL for a kind.
func apiURL(k Kind, serverURL, host string) string {
	switch k {
	case GitHub:
		if host == "github.com" {
			return "https://api.github.com"
		}
		return serverURL + "/api/v3"
	case GitLab:
		return serverURL + "/api/v4"
	default:
		return serverURL + "/api/v1"
	}
}

// parseRemote splits a git remote URL into host (with port for http(s)), hostname and the
// repository path: owner/repo, or group/subgroup/repo (GitLab).
func parseRemote(remote string) (hostport, hostname, repo string, err error) {
	remote = strings.TrimSpace(remote)
	var p string
	switch {
	case strings.Contains(remote, "://"):
		u, perr := url.Parse(remote)
		if perr != nil {
			return "", "", "", fmt.Errorf("parse remote %q: %w", remote, perr)
		}
		hostname = u.Hostname()
		hostport = hostname
		if u.Scheme == "https" || u.Scheme == "http" {
			hostport = u.Host
		} else if u.Scheme != "ssh" {
			return "", "", "", fmt.Errorf("unsupported remote %q", remote)
		}
		p = u.Path
	default:
		at := strings.IndexByte(remote, '@')
		colon := strings.IndexByte(remote, ':')
		if at < 0 || colon < at {
			return "", "", "", fmt.Errorf("unsupported remote %q", remote)
		}
		hostname = remote[at+1 : colon]
		hostport = hostname
		p = remote[colon+1:]
	}
	p = strings.TrimSuffix(strings.Trim(p, "/"), ".git")
	if hostname == "" || !strings.Contains(p, "/") || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") || strings.Contains(p, "//") {
		return "", "", "", fmt.Errorf("unsupported remote %q", remote)
	}
	return hostport, hostname, p, nil
}

// RemoteServerURL returns the https base URL of the forge of a git remote URL
// (https://host[:port]), as Detect derives it without CI environment.
func RemoteServerURL(remote string) (string, error) {
	hostport, _, _, err := parseRemote(remote)
	if err != nil {
		return "", err
	}
	return "https://" + hostport, nil
}

// Detect determines the platform from CI env, else from the remote URL plus API probes.
// Non-empty Overrides fields replace detected ones.
func Detect(ctx context.Context, getenv func(string) string, remoteURL string, o Overrides, c *http.Client) (Info, error) {
	info, ok := FromEnv(getenv)
	via := "ci environment"
	if !ok {
		hostport, hostname, repo, err := parseRemote(remoteURL)
		if err != nil {
			return Info{}, err
		}
		info = Info{ServerURL: "https://" + hostport, Host: hostname, Repo: repo}
		switch {
		case o.Kind != "":
			via = "--platform"
		case hostname == "github.com":
			info.Kind, via = GitHub, "remote host"
		case hostname == "gitlab.com":
			info.Kind, via = GitLab, "remote host"
		default:
			k, err := probe(ctx, c, hostport)
			if err != nil {
				return Info{}, err
			}
			info.Kind, via = k, "api probe"
		}
		if info.Kind != "" {
			info.APIURL = apiURL(info.Kind, info.ServerURL, info.Host)
		}
	}
	if o.Kind != "" {
		info.Kind = Kind(o.Kind)
		if o.APIURL == "" {
			info.APIURL = apiURL(info.Kind, info.ServerURL, info.Host)
		}
	}
	if o.APIURL != "" {
		info.APIURL = strings.TrimSuffix(o.APIURL, "/")
	}
	if o.Repo != "" {
		info.Repo = o.Repo
	}
	if info.Kind != GitLab && info.Kind != "" && strings.Count(info.Repo, "/") > 1 {
		return Info{}, fmt.Errorf("repository %q: %s repositories are owner/name; nested group paths exist only on GitLab", info.Repo, info.Kind)
	}
	slog.InfoContext(ctx, "platform detected", "kind", info.Kind, "via", via, "api", info.APIURL, "repo", info.Repo)
	return info, nil
}

// probe asks the host's version endpoints. GitLab goes first: GitLab redirects unknown paths
// to its sign-in page, which would pass the Forgejo probe after the redirect, while Gitea,
// Forgejo and GitHub have no /api/v4.
func probe(ctx context.Context, c *http.Client, hostport string) (Kind, error) {
	base := probeScheme + "://" + hostport
	// GitLab answers /api/v4/version 401 {"message":"401 Unauthorized"} without a token.
	if probeOK(ctx, c, base+"/api/v4/version", true, true) {
		return GitLab, nil
	}
	if probeOK(ctx, c, base+"/api/forgejo/v1/version", false, true) {
		return Forgejo, nil
	}
	if probeOK(ctx, c, base+"/api/v1/version", true, true) {
		return Gitea, nil
	}
	if probeOK(ctx, c, base+"/api/v3/meta", false, false) {
		return GitHub, nil
	}
	return "", fmt.Errorf("cannot detect platform for %s; pass --platform and --api-url", hostport)
}

// probeOK reports whether u answers 200 (with a JSON "version" field if needVersion).
// With signInJSON, a 401/403 JSON {"message": ...} also counts: GitLab without a token and
// Gitea/Forgejo instances with REQUIRE_SIGNIN_VIEW reject anonymous API calls that way, while
// unknown routes give 404.
func probeOK(ctx context.Context, c *http.Client, u string, needVersion, signInJSON bool) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, http.NoBody)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
		slog.DebugContext(ctx, "platform probe failed", "url", u, "err", err)
		return false
	}
	defer resp.Body.Close()
	var v struct {
		Version *string `json:"version"`
		Message *string `json:"message"`
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if !needVersion {
			return true
		}
		return json.UnmarshalRead(io.LimitReader(resp.Body, 1<<16), &v) == nil && v.Version != nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return signInJSON && json.UnmarshalRead(io.LimitReader(resp.Body, 1<<16), &v) == nil && v.Message != nil
	}
	return false
}

// DefaultActionsHost returns the host used for `owner/repo@ref` action references and where it came from.
func DefaultActionsHost(info Info, getenv func(string) string) (host, source string) {
	if v := getenv("DEPS_DEFAULT_ACTIONS_URL"); v != "" {
		if u, err := url.Parse(v); err == nil && u.Hostname() != "" {
			return u.Hostname(), "env"
		}
		return strings.TrimSuffix(v, "/"), "env"
	}
	if info.Kind == Forgejo {
		return "data.forgejo.org", "platform default"
	}
	return "github.com", "platform default"
}

// DefaultGitLabURL is the GitLab instance used when neither config, CI env nor the platform
// name one.
const DefaultGitLabURL = "https://gitlab.com"

// GitLabURL returns the base URL of the GitLab instance the gitlabci manager resolves against
// and where it came from: configured ("config"), else CI_SERVER_URL as set by GitLab CI
// ("env"), else the server of a GitLab platform ("platform"), else https://gitlab.com
// ("default"). The result has no trailing slash.
func GitLabURL(configured string, info Info, getenv func(string) string) (u, source string) {
	if configured != "" {
		return strings.TrimRight(configured, "/"), "config"
	}
	if v := getenv("CI_SERVER_URL"); v != "" {
		return strings.TrimRight(v, "/"), "env"
	}
	if info.Kind == GitLab && info.ServerURL != "" {
		return strings.TrimRight(info.ServerURL, "/"), "platform"
	}
	return DefaultGitLabURL, "default"
}
