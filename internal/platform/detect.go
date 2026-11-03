package platform

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
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

// apiURL derives the API base URL for a kind.
func apiURL(k Kind, serverURL, host string) string {
	switch k {
	case GitHub:
		if host == "github.com" {
			return "https://api.github.com"
		}
		return serverURL + "/api/v3"
	default:
		return serverURL + "/api/v1"
	}
}

// parseRemote splits a git remote URL into host (with port for http(s)), hostname and owner/repo.
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
	if hostname == "" || strings.Count(p, "/") != 1 || strings.HasPrefix(p, "/") || strings.HasSuffix(p, "/") {
		return "", "", "", fmt.Errorf("unsupported remote %q", remote)
	}
	return hostport, hostname, p, nil
}

// Detect determines the platform from CI env, else from the remote URL plus API probes.
// Non-empty Overrides fields replace detected ones.
func Detect(ctx context.Context, getenv func(string) string, remoteURL string, o Overrides, c *http.Client) (Info, error) {
	info, ok := FromEnv(getenv)
	if !ok {
		hostport, hostname, repo, err := parseRemote(remoteURL)
		if err != nil {
			return Info{}, err
		}
		info = Info{ServerURL: "https://" + hostport, Host: hostname, Repo: repo}
		switch {
		case o.Kind != "":
			// Kind given: skip probing.
		case hostname == "github.com":
			info.Kind = GitHub
		default:
			k, err := probe(ctx, c, hostport)
			if err != nil {
				return Info{}, err
			}
			info.Kind = k
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
	return info, nil
}

func probe(ctx context.Context, c *http.Client, hostport string) (Kind, error) {
	base := probeScheme + "://" + hostport
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
// With signInJSON, a 401/403 JSON {"message": ...} also counts: Gitea/Forgejo instances
// with REQUIRE_SIGNIN_VIEW reject anonymous API calls that way, while unknown routes give 404.
func probeOK(ctx context.Context, c *http.Client, u string, needVersion, signInJSON bool) bool {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return false
	}
	resp, err := c.Do(req)
	if err != nil {
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
		return json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v) == nil && v.Version != nil
	case http.StatusUnauthorized, http.StatusForbidden:
		return signInJSON && json.NewDecoder(io.LimitReader(resp.Body, 1<<16)).Decode(&v) == nil && v.Message != nil
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
