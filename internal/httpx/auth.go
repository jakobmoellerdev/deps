package httpx

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/platform"
)

// staticUser is the Basic auth user sent with static tokens; GitHub, Gitea and Forgejo accept it.
const staticUser = "x-access-token"

// HelperFunc returns credentials for host, or ok=false if there are none.
type HelperFunc func(ctx context.Context, host string) (user, pass string, ok bool)

// AuthOption configures an Auth.
type AuthOption func(*Auth)

// WithHelper replaces the `git credential fill` lookup used by Helper.
func WithHelper(fn HelperFunc) AuthOption { return func(a *Auth) { a.fill = fn } }

// Auth holds static per-host tokens and a cache of git credential helper results.
// A nil *Auth has no credentials.
type Auth struct {
	tokens map[string]string
	fill   HelperFunc

	mu     sync.Mutex
	helper map[string]*cred
}

type cred struct {
	once       sync.Once
	user, pass string
	ok         bool
}

// NewAuth registers platformToken for info.Host and DEPS_GITHUB_COM_TOKEN for github.com
// (when the platform host is not github.com itself).
func NewAuth(info platform.Info, platformToken string, getenv func(string) string, opts ...AuthOption) *Auth {
	a := &Auth{tokens: make(map[string]string, 2), fill: gitCredentialFill, helper: make(map[string]*cred)}
	if platformToken != "" && info.Host != "" {
		a.tokens[info.Host] = platformToken
	}
	if t := getenv("DEPS_GITHUB_COM_TOKEN"); t != "" && info.Host != "github.com" {
		a.tokens["github.com"] = t
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// Static returns the configured token for host as Basic credentials ("x-access-token", token).
func (a *Auth) Static(host string) (user, pass string, ok bool) {
	if a == nil {
		return "", "", false
	}
	t, ok := a.tokens[host]
	if !ok {
		return "", "", false
	}
	return staticUser, t, true
}

// Helper asks the git credential helper for host once; success and failure are both cached.
// host may include a port, as in the git credential protocol.
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
	c.once.Do(func() {
		c.user, c.pass, c.ok = a.fill(ctx, host)
		slog.DebugContext(ctx, "git credential helper", "host", host, "found", c.ok)
	})
	return c.user, c.pass, c.ok
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
