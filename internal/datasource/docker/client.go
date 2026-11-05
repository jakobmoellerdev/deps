package dockerds

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
)

// client sends registry requests with the authentication the registry asks for, as the
// distribution spec's token flow and the Docker CLI do: a request is sent with the credential
// cached for its registry and scope, and on a 401 the WWW-Authenticate challenge says how to get
// one. Basic challenges get the Docker config's username and password; Bearer challenges a token
// from the realm: the config's registry token as is, an OAuth2 refresh-token grant (POST) for an
// identity token, falling back to the token GET when the realm has no OAuth2 endpoint, and
// otherwise the token GET, with the username and password or anonymously.
//
// Tokens are cached per registry and scope for the whole run, so one exchange serves every
// request of a repository and concurrent callers share it. A cached token the registry rejects
// (expired) is fetched anew once.
type client struct {
	http     *http.Client
	header   http.Header // added to every request (User-Agent)
	cred     credentialFunc
	clientID string // OAuth2 client_id

	mu      sync.Mutex
	schemes map[string]authScheme // registry → scheme of its last challenge
	tokens  map[string]*tokenEntry
}

type authScheme int

const (
	schemeNone authScheme = iota
	schemeBasic
	schemeBearer
)

// tokenEntry is a cached Authorization value; done closes once it is fetched.
type tokenEntry struct {
	done chan struct{}
	auth string // "Basic …" or "Bearer …"
	err  error
}

// errBasicCredentialNotFound is the error of a Basic challenge without a credential.
var errBasicCredentialNotFound = errors.New("basic credential not found")

// maxTokenBytes bounds a token response.
const maxTokenBytes = 128 << 10

func newClient(hc *http.Client, header http.Header, cred credentialFunc, clientID string) *client {
	return &client{
		http: hc, header: header, cred: cred, clientID: clientID,
		schemes: map[string]authScheme{},
		tokens:  map[string]*tokenEntry{},
	}
}

// do sends req (GET or HEAD, no body) for repository r with pull access.
func (c *client) do(req *http.Request, r *repository) (*http.Response, error) {
	maps.Copy(req.Header, c.header)
	scope := "repository:" + r.path + ":pull"
	key := c.tokenKey(r.registry, scope)
	used := c.cached(key)
	if used != nil {
		req.Header.Set("Authorization", used.auth)
	}
	resp, err := c.http.Do(req)
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		return resp, err
	}
	if resp.Request != nil && resp.Request.URL.Host != req.URL.Host {
		return resp, nil // redirected elsewhere: not the registry's challenge
	}
	scheme, params := parseChallenge(resp.Header.Get("Www-Authenticate"))
	if scheme == schemeNone {
		return resp, nil
	}
	resp.Body.Close()
	c.mu.Lock()
	c.schemes[r.registry] = scheme
	c.mu.Unlock()
	ctx := req.Context()
	var e *tokenEntry
	if scheme == schemeBasic {
		e = c.fetch(ctx, r.registry+" basic", used, func() (string, error) { return c.basic(ctx, r.registry) })
	} else {
		e = c.fetch(ctx, key, used, func() (string, error) { return c.bearer(ctx, r, req.URL, params, scope) })
	}
	if e.err != nil {
		return nil, fmt.Errorf("%s %q: %w", req.Method, req.URL, e.err)
	}
	retry := req.Clone(ctx)
	retry.Header.Set("Authorization", e.auth)
	return c.http.Do(retry)
}

// tokenKey is the cache key of registry's bearer token for scope.
func (c *client) tokenKey(registry, scope string) string { return registry + " bearer " + scope }

// cached returns the fetched credential the registry's last challenge scheme asks for; nil
// when there is none yet.
func (c *client) cached(bearerKey string) *tokenEntry {
	registry, _, _ := strings.Cut(bearerKey, " ")
	c.mu.Lock()
	defer c.mu.Unlock()
	var e *tokenEntry
	switch c.schemes[registry] {
	case schemeBasic:
		e = c.tokens[registry+" basic"]
	case schemeBearer:
		e = c.tokens[bearerKey]
	}
	if e == nil {
		return nil
	}
	select {
	case <-e.done:
		if e.err == nil {
			return e
		}
	default:
	}
	return nil
}

// fetch returns the credential of key, fetching it with get unless another caller does or has.
// A cached entry that is stale (the one the rejected request carried) is replaced; a failed
// fetch is not cached, so a later caller tries again.
func (c *client) fetch(ctx context.Context, key string, stale *tokenEntry, get func() (string, error)) *tokenEntry {
	c.mu.Lock()
	e := c.tokens[key]
	if e == nil || e == stale {
		e = &tokenEntry{done: make(chan struct{})}
		c.tokens[key] = e
		c.mu.Unlock()
		e.auth, e.err = get()
		if e.err != nil {
			c.mu.Lock()
			if c.tokens[key] == e {
				delete(c.tokens, key)
			}
			c.mu.Unlock()
		}
		close(e.done)
		return e
	}
	c.mu.Unlock()
	select {
	case <-e.done:
		return e
	case <-ctx.Done():
		return &tokenEntry{err: ctx.Err()}
	}
}

// basic returns the Basic Authorization value of registry's credential.
func (c *client) basic(ctx context.Context, registry string) (string, error) {
	cred, err := c.cred(ctx, registry)
	if err != nil {
		return "", fmt.Errorf("resolve credential: %w", err)
	}
	if cred == (credential{}) {
		return "", errBasicCredentialNotFound
	}
	if cred.Username == "" || cred.Password == "" {
		return "", errors.New("missing username or password for basic auth")
	}
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(cred.Username+":"+cred.Password)), nil
}

// bearer returns the Bearer Authorization value for scope and the scopes the challenge adds
// (sorted, as the Docker CLI and oras-go send them) from the challenge's realm.
func (c *client) bearer(ctx context.Context, r *repository, reqURL *url.URL, params map[string]string, scope string) (string, error) {
	realm, service := params["realm"], params["service"]
	if err := validateRealm(realm, reqURL); err != nil {
		return "", err
	}
	scopes := []string{scope}
	for s := range strings.FieldsSeq(params["scope"]) {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	slices.Sort(scopes)
	cred, err := c.cred(ctx, r.registry)
	if err != nil {
		return "", err
	}
	var token string
	switch {
	case cred.AccessToken != "":
		token = cred.AccessToken
	case cred.RefreshToken != "":
		var oauthErr error
		if token, oauthErr = c.oauth2Token(ctx, realm, service, scopes, cred); oauthErr != nil {
			if !oauth2Unsupported(oauthErr) {
				return "", oauthErr
			}
			if token, err = c.distributionToken(ctx, realm, service, scopes, cred); err != nil {
				return "", errors.Join(err, oauthErr)
			}
		}
	default:
		if token, err = c.distributionToken(ctx, realm, service, scopes, cred); err != nil {
			return "", err
		}
	}
	return "Bearer " + token, nil
}

// validateRealm refuses a realm the registry's credentials must not go to: one that is not
// http(s), plain http when the registry is reached over https, or a loopback, link-local or
// unspecified address on another host than the registry's.
func validateRealm(realm string, registry *url.URL) error {
	u, err := url.Parse(realm)
	if err != nil || realm == "" {
		return fmt.Errorf("invalid bearer realm %q", realm)
	}
	switch {
	case u.Scheme == "https":
	case u.Scheme == "http" && registry.Scheme == "http":
	default:
		return fmt.Errorf("bearer realm %q: scheme %q not allowed for a registry reached over %s", realm, u.Scheme, registry.Scheme)
	}
	if strings.EqualFold(u.Hostname(), registry.Hostname()) {
		return nil
	}
	if ip := net.ParseIP(u.Hostname()); ip != nil && (ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()) {
		return fmt.Errorf("bearer realm %q: address %s not allowed", realm, ip)
	}
	return nil
}

// distributionToken GETs a token from realm, with basic credentials when cred has a username or
// password, anonymously otherwise.
func (c *client) distributionToken(ctx context.Context, realm, service string, scopes []string, cred credential) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, realm, http.NoBody)
	if err != nil {
		return "", err
	}
	if cred.Username != "" || cred.Password != "" {
		req.SetBasicAuth(cred.Username, cred.Password)
	}
	q := req.URL.Query()
	if service != "" {
		q.Set("service", service)
	}
	for _, s := range scopes {
		q.Add("scope", s)
	}
	req.URL.RawQuery = q.Encode()
	return c.tokenRequest(req)
}

// oauth2Token POSTs an OAuth2 refresh-token grant to realm.
func (c *client) oauth2Token(ctx context.Context, realm, service string, scopes []string, cred credential) (string, error) {
	form := url.Values{
		"grant_type":    {"refresh_token"},
		"refresh_token": {cred.RefreshToken},
		"service":       {service},
		"client_id":     {c.clientID},
		"scope":         {strings.Join(scopes, " ")},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, realm, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	return c.tokenRequest(req)
}

// oauth2Unsupported reports whether err is a token endpoint's refusal of an OAuth2 POST.
func oauth2Unsupported(err error) bool {
	var resp *errorResponse
	if !errors.As(err, &resp) {
		return false
	}
	switch resp.StatusCode {
	case http.StatusBadRequest, http.StatusUnauthorized, http.StatusNotFound, http.StatusMethodNotAllowed:
		return true
	}
	return false
}

// tokenRequest sends a token request and decodes the token (`access_token`, else `token`).
func (c *client) tokenRequest(req *http.Request) (string, error) {
	maps.Copy(req.Header, c.header)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", responseError(resp)
	}
	var body struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := json.UnmarshalRead(io.LimitReader(resp.Body, maxTokenBytes), &body); err != nil {
		return "", fmt.Errorf("%s %q: decode token: %w", req.Method, req.URL.Redacted(), err)
	}
	if token := cmp.Or(body.AccessToken, body.Token); token != "" {
		return token, nil
	}
	return "", fmt.Errorf("%s %q: empty token", req.Method, req.URL.Redacted())
}

// parseChallenge parses a WWW-Authenticate header: its scheme and, for Bearer, its
// parameters (realm, service, scope).
func parseChallenge(header string) (authScheme, map[string]string) {
	name, rest := parseToken(header)
	var scheme authScheme
	switch {
	case strings.EqualFold(name, "basic"):
		return schemeBasic, nil
	case strings.EqualFold(name, "bearer"):
		scheme = schemeBearer
	default:
		return schemeNone, nil
	}
	params := map[string]string{}
	for {
		var key, value string
		if key, rest = parseToken(skipSpace(rest)); key == "" {
			return scheme, params
		}
		if rest = skipSpace(rest); rest == "" || rest[0] != '=' {
			return scheme, params
		}
		if rest = skipSpace(rest[1:]); rest == "" {
			return scheme, params
		}
		if rest[0] == '"' {
			prefix, err := strconv.QuotedPrefix(rest)
			if err != nil {
				return scheme, params
			}
			if value, err = strconv.Unquote(prefix); err != nil {
				return scheme, params
			}
			rest = rest[len(prefix):]
		} else if value, rest = parseToken(rest); value == "" {
			return scheme, params
		}
		params[strings.ToLower(key)] = value
		if rest = skipSpace(rest); rest == "" || rest[0] != ',' {
			return scheme, params
		}
		rest = rest[1:]
	}
}

// parseToken splits s after its leading RFC 7230 token.
func parseToken(s string) (token, rest string) {
	i := strings.IndexFunc(s, func(r rune) bool {
		return (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') && !strings.ContainsRune("!#$%&'*+-.^_`|~", r)
	})
	if i < 0 {
		return s, ""
	}
	return s[:i], s[i:]
}

func skipSpace(s string) string { return strings.TrimLeft(s, " \t") }
