package dockerds

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// credential is what the Docker config holds for a registry: a username and password, an
// identity token (an OAuth2 refresh token, `identitytoken`) or a registry token (a bearer token
// used as is, `registrytoken`). The zero value is anonymous access.
type credential struct {
	Username, Password string
	RefreshToken       string
	AccessToken        string
}

// credentialFunc returns the credential of repository path (library/alpine) on registry
// (host[:port], docker.io for Docker Hub).
type credentialFunc func(ctx context.Context, registry, path string) (credential, error)

// hubServerAddress is the key of Docker Hub credentials in the Docker config and credential
// helpers, as the Docker CLI stores them.
const hubServerAddress = "https://index.docker.io/v1/"

// dockerConfig is the part of the Docker config deps reads, and the credential helper results
// looked up from it.
type dockerConfig struct {
	Auths       map[string]authConfig `json:"auths"`
	CredsStore  string                `json:"credsStore"`
	CredHelpers map[string]string     `json:"credHelpers"`

	mu      sync.Mutex
	helpers map[helperKey]*helperCall
}

// helperKey is a credential helper and the server address it is asked for.
type helperKey struct{ helper, addr string }

// helperCall is a credential helper lookup: done closes when cred and err are set.
type helperCall struct {
	done chan struct{}
	cred credential
	err  error
}

type authConfig struct {
	Auth          string `json:"auth"`
	Username      string `json:"username"`
	Password      string `json:"password"`
	IdentityToken string `json:"identitytoken"`
	RegistryToken string `json:"registrytoken"`
}

// dockerCredentials reads credentials from the Docker config ($DOCKER_CONFIG or ~/.docker) and
// its credential helpers, as the Docker CLI does: a credential helper named for the registry in
// `credHelpers`, else the `credsStore` helper when the config names one, else `auths`. Keys
// naming a namespace of the registry (`ghcr.io/org`) take precedence, most specific first, as
// in oras-go. Credential helper results are kept for the life of the returned func
// (helperCredential). An unreadable config fails every authenticated lookup with its error.
func dockerCredentials() credentialFunc {
	cfg, err := loadDockerConfig()
	if err != nil {
		return func(context.Context, string, string) (credential, error) {
			return credential{}, fmt.Errorf("docker config: %w", err)
		}
	}
	return cfg.credential
}

// loadDockerConfig reads the Docker config; a missing or empty one has no credentials.
func loadDockerConfig() (*dockerConfig, error) {
	dir := os.Getenv("DOCKER_CONFIG")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home directory: %w", err)
		}
		dir = filepath.Join(home, ".docker")
	}
	path := filepath.Join(dir, "config.json")
	b, err := os.ReadFile(path)
	cfg := &dockerConfig{}
	if errors.Is(err, os.ErrNotExist) || (err == nil && len(bytes.TrimSpace(b)) == 0) {
		return cfg, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, cfg); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// credential looks up repository path on registry: first the `credHelpers` and `auths` keys of
// its namespaces, most specific first (ghcr.io/org/app, then ghcr.io/org), then the registry
// itself. Docker Hub (docker.io) is looked up under its server address
// https://index.docker.io/v1/ (or, failing that, one of its hosts) and has no namespaces, as in
// oras-go. The credsStore helper is only asked for the registry, so a lookup runs it at most
// once, and each helper runs once per server address (helperCredential). Keys match regardless
// of an http(s):// prefix, a trailing slash and the host's case.
func (c *dockerConfig) credential(ctx context.Context, registry, path string) (credential, error) {
	addr, keys := registry, []string{normalizeServerAddress(registry)}
	if isHub(registry) {
		addr, keys = hubServerAddress, hubKeys
	} else {
		for ns := path; ns != ""; {
			key := keys[0] + "/" + ns
			if helper, ok := lookup(c.CredHelpers, key); ok && helper != "" {
				if cred, err := c.helperCredential(ctx, helper, registry+"/"+ns); err != nil || cred != (credential{}) {
					return cred, err
				}
			}
			if a, ok := lookup(c.Auths, key); ok {
				return a.credential()
			}
			i := strings.LastIndexByte(ns, '/')
			if i < 0 {
				break
			}
			ns = ns[:i]
		}
	}
	for _, k := range keys {
		if helper, ok := lookup(c.CredHelpers, k); ok && helper != "" {
			return c.helperCredential(ctx, helper, addr)
		}
	}
	if c.CredsStore != "" {
		return c.helperCredential(ctx, c.CredsStore, addr)
	}
	if a, ok := c.Auths[addr]; ok {
		return a.credential()
	}
	for _, k := range keys {
		if a, ok := lookup(c.Auths, k); ok {
			return a.credential()
		}
	}
	return credential{}, nil
}

// hubKeys are the normalized config keys of Docker Hub, the Docker CLI's first.
var hubKeys = []string{"index.docker.io/v1", hubRegistry, "registry-1.docker.io", "index.docker.io"}

// isHub reports whether registry is one of Docker Hub's hosts.
func isHub(registry string) bool {
	return slices.Contains(hubKeys[1:], strings.ToLower(registry))
}

// normalizeServerAddress strips a server address of its scheme and trailing slash and lower-cases
// its host: https://Index.Docker.io/v1/ → index.docker.io/v1.
func normalizeServerAddress(addr string) string {
	addr = strings.TrimPrefix(addr, "https://")
	addr = strings.TrimPrefix(addr, "http://")
	addr = strings.TrimSuffix(addr, "/")
	host, path, ok := strings.Cut(addr, "/")
	if host = strings.ToLower(host); ok {
		return host + "/" + path
	}
	return host
}

// lookup returns the value of the first key (in sorted order) of m whose normalized form is
// key, and whether there is one.
func lookup[V any](m map[string]V, key string) (V, bool) {
	var match string
	found := false
	for k := range m {
		if normalizeServerAddress(k) == key && (!found || k < match) {
			match, found = k, true
		}
	}
	return m[match], found
}

func (a authConfig) credential() (credential, error) {
	c := credential{Username: a.Username, Password: a.Password, RefreshToken: a.IdentityToken, AccessToken: a.RegistryToken}
	if a.Auth == "" {
		return c, nil
	}
	b, err := base64.StdEncoding.DecodeString(a.Auth)
	if err != nil {
		return credential{}, fmt.Errorf("docker config: decode auth: %w", err)
	}
	var ok bool
	if c.Username, c.Password, ok = strings.Cut(string(b), ":"); !ok {
		return credential{}, errors.New("docker config: auth is not base64(username:password)")
	}
	return c, nil
}

// helperTokenUser is the username a credential helper returns for an identity token.
const helperTokenUser = "<token>"

// errHelperNotFound is the message of a credential helper that has no credential for the server.
const errHelperNotFound = "credentials not found in native keychain"

// helperCredential returns the credential helper's credential for addr, running
// `docker-credential-<helper> get` once per helper and address for the life of the config:
// concurrent lookups wait for the running one, and credentials and "not found" are kept. A
// failed run is not, so the next lookup runs the helper again.
func (c *dockerConfig) helperCredential(ctx context.Context, helper, addr string) (credential, error) {
	key := helperKey{helper, addr}
	c.mu.Lock()
	call := c.helpers[key]
	if call == nil {
		if c.helpers == nil {
			c.helpers = make(map[helperKey]*helperCall)
		}
		call = &helperCall{done: make(chan struct{})}
		c.helpers[key] = call
		c.mu.Unlock()
		call.cred, call.err = runHelper(ctx, helper, addr)
		if call.err != nil {
			c.mu.Lock()
			delete(c.helpers, key)
			c.mu.Unlock()
		}
		close(call.done)
		return call.cred, call.err
	}
	c.mu.Unlock()
	select {
	case <-call.done:
		return call.cred, call.err
	case <-ctx.Done():
		return credential{}, ctx.Err()
	}
}

// runHelper runs `docker-credential-<helper> get` with the server address on stdin.
func runHelper(ctx context.Context, helper, addr string) (credential, error) {
	name := "docker-credential-" + helper
	cmd := exec.CommandContext(ctx, name, "get")
	cmd.Stdin = strings.NewReader(addr)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		msg := string(bytes.TrimSpace(out))
		if msg == errHelperNotFound {
			return credential{}, nil
		}
		if exitErr := (*exec.ExitError)(nil); errors.As(err, &exitErr) && msg != "" {
			err = errors.New(msg)
		}
		return credential{}, fmt.Errorf("%s get %s: %w", name, addr, err)
	}
	var resp struct {
		Username string `json:"Username"`
		Secret   string `json:"Secret"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return credential{}, fmt.Errorf("%s get %s: %w", name, addr, err)
	}
	if resp.Username == helperTokenUser {
		return credential{RefreshToken: resp.Secret}, nil
	}
	return credential{Username: resp.Username, Password: resp.Secret}, nil
}
