package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"slices"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// staticUser is the Basic auth user sent with platform tokens; GitHub, Gitea and Forgejo accept it.
const staticUser = "x-access-token"

// gitlabUser is the Basic auth user GitLab documents for access tokens.
const gitlabUser = "oauth2"

// HelperFunc returns credentials for host, or ok=false if there are none.
type HelperFunc func(ctx context.Context, host string) (user, pass string, ok bool)

// AuthOption configures an Auth.
type AuthOption func(*Auth)

// WithHelper replaces the `git credential fill` lookup used by Helper.
func WithHelper(fn HelperFunc) AuthOption { return func(a *Auth) { a.fill = fn } }

// WithSecretHook calls fn with every password the git credential helper returns, once per
// host and before Helper hands it out, e.g. to mask it in a CI log.
func WithSecretHook(fn func(secret string)) AuthOption { return func(a *Auth) { a.onSecret = fn } }

// WithToken registers Basic credentials (user, token) for host (without port) unless host
// already has a token: the platform token and DEPS_GITHUB_COM_TOKEN registered by NewAuth win,
// then earlier WithToken options. An empty host or token registers nothing. source names
// where the token comes from (an environment variable) in the credentials log note.
func WithToken(host, user, token, source string) AuthOption {
	return func(a *Auth) { a.add(host, user, token, source) }
}

// WithGHToken makes the GitHub CLI's login (`gh auth token --hostname github.com`) the
// github.com token when neither the platform token nor DEPS_GITHUB_COM_TOKEN is one. gh runs
// once, at the first github.com lookup that needs a token; without gh on PATH or a login
// there is none.
func WithGHToken() AuthOption { return func(a *Auth) { a.gh = ghAuthToken } }

// ghAuthToken runs `gh auth token --hostname github.com` without prompting; "" when gh is
// missing, not logged in or slow.
func ghAuthToken() string {
	if _, err := exec.LookPath("gh"); err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "gh", "auth", "token", "--hostname", "github.com")
	cmd.Env = append(os.Environ(), "GH_PROMPT_DISABLED=1")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Auth holds static per-host tokens and a cache of git credential helper results.
// A nil *Auth has no credentials.
type Auth struct {
	tokens   map[string]static
	fill     HelperFunc
	onSecret func(string) // WithSecretHook; nil = none

	mu     sync.Mutex
	helper map[string]*cred

	gh      func() string // WithGHToken: the GitHub CLI's github.com token, "" = none
	ghOnce  sync.Once
	ghToken string
}

type static struct{ user, token, source string }

// cred is the git credential helper result for one host; mu serializes its lookups and done
// marks user, pass and ok final.
type cred struct {
	mu         sync.Mutex
	done       bool
	user, pass string
	ok         bool
}

// NewAuth registers platformToken for info.Host (user oauth2 on GitLab, else x-access-token)
// and DEPS_GITHUB_COM_TOKEN for github.com (when the platform host is not github.com itself),
// then applies opts.
func NewAuth(info platform.Info, platformToken string, getenv func(string) string, opts ...AuthOption) *Auth {
	a := &Auth{tokens: make(map[string]static, 2), fill: gitCredentialFill, helper: make(map[string]*cred)}
	user := staticUser
	if info.Kind == platform.GitLab {
		user = gitlabUser
	}
	a.add(info.Host, user, platformToken, "platform token")
	if info.Host != "github.com" {
		a.add("github.com", staticUser, getenv("DEPS_GITHUB_COM_TOKEN"), "DEPS_GITHUB_COM_TOKEN")
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// add registers (user, token) from source for host unless host is empty, token is empty or
// host already has a token.
func (a *Auth) add(host, user, token, source string) {
	if host == "" || token == "" {
		return
	}
	if _, ok := a.tokens[host]; !ok {
		a.tokens[host] = static{user, token, source}
	}
}

// Static returns the configured Basic credentials for host: (x-access-token or oauth2, token)
// for the platform token, (x-access-token, token) for github.com, the registered user for
// WithToken ones. The first use of a host's credentials is noted (NoteSource) with their
// source.
func (a *Auth) Static(host string) (user, pass string, ok bool) {
	if a == nil {
		return "", "", false
	}
	if s, ok := a.tokens[host]; ok {
		NoteSource("credentials", "host", host, "source", s.source)
		return s.user, s.token, true
	}
	if host == "github.com" && a.gh != nil {
		a.ghOnce.Do(func() {
			a.ghToken = a.gh()
			slog.Debug("github.com token from gh auth token", "found", a.ghToken != "")
			if a.ghToken != "" && a.onSecret != nil {
				a.onSecret(a.ghToken)
			}
		})
		if a.ghToken != "" {
			NoteSource("credentials", "host", host, "source", "gh auth token")
			return staticUser, a.ghToken, true
		}
	}
	return "", "", false
}

// Helper asks the git credential helper for host once; success and failure are both cached,
// except a failure that ended after ctx did (a canceled run or caller): the next caller asks
// again with its own context, as datasource.Once does. host may include a port, as in the git
// credential protocol.
func (a *Auth) Helper(ctx context.Context, host string) (user, pass string, ok bool) {
	if a == nil || a.fill == nil {
		return "", "", false
	}
	a.mu.Lock()
	c := a.helper[host]
	if c == nil {
		c = &cred{}
		a.helper[host] = c
	}
	a.mu.Unlock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return c.user, c.pass, c.ok
	}
	user, pass, ok = a.fill(ctx, host)
	slog.DebugContext(ctx, "git credential helper", "host", host, "found", ok)
	if !ok && ctx.Err() != nil {
		return "", "", false
	}
	if ok {
		NoteSource("credentials", "host", host, "source", "git credential fill")
		if a.onSecret != nil {
			a.onSecret(pass)
		}
	}
	c.user, c.pass, c.ok, c.done = user, pass, ok, true
	return user, pass, ok
}

// gitCredentialFill runs `git credential fill` without any interactive prompt.
func gitCredentialFill(ctx context.Context, host string) (user, pass string, ok bool) {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", "credential", "fill")
	cmd.Stdin = strings.NewReader("protocol=https\nhost=" + host + "\n\n")
	// GIT_ASKPASS="" disables every askpass program (GIT_ASKPASS, core.askPass, SSH_ASKPASS),
	// so together with GIT_TERMINAL_PROMPT=0 git fails instead of prompting.
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=")
	out, err := cmd.Output()
	if err != nil {
		return "", "", false
	}
	return parseCredential(out)
}

// parseCredential reads username= and password= from git credential output.
func parseCredential(out []byte) (user, pass string, ok bool) {
	for len(out) > 0 {
		var line []byte
		line, out, _ = bytes.Cut(out, []byte{'\n'})
		line = bytes.TrimSuffix(line, []byte{'\r'})
		k, v, found := bytes.Cut(line, []byte{'='})
		if !found {
			continue
		}
		switch string(k) {
		case "username":
			user = string(v)
		case "password":
			pass = string(v)
		}
	}
	return user, pass, pass != ""
}

// IsJWT reports whether token is a JSON Web Token: three dot-separated parts whose first is a
// base64url-encoded JSON header ("eyJ"). Forgejo's Authorized Integrations hand such a token to
// an Actions job instead of a static access token.
func IsJWT(token string) bool {
	if !strings.HasPrefix(token, "eyJ") || strings.Count(token, ".") != 2 {
		return false
	}
	return !slices.Contains(strings.Split(token, "."), "")
}

// SetAuth puts (user, token) on req: Basic, except for a JWT, which goes in as
// "Authorization: Bearer <token>". Forgejo authenticates a JWT from an Authorized Integration
// only as a bearer token; its Basic auth checks passwords and access tokens and answers 401 for
// a JWT. The user is then unused, as the JWT names its own account.
func SetAuth(req *http.Request, user, token string) {
	if IsJWT(token) {
		req.Header.Set("Authorization", "Bearer "+token)
		return
	}
	req.SetBasicAuth(user, token)
}
