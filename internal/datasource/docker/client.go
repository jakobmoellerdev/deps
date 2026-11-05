package dockerds

import (
	"bytes"
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
	"net/netip"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
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
	used := c.cached(r, key)
	if used != nil {
		req.Header.Set("Authorization", used.auth)
	}
	resp, err := c.http.Do(req)
	if err == nil && used != nil && strings.HasPrefix(used.auth, "Bearer ") && invalidToken(resp) {
		// The cached token was rejected without a challenge: ask for one.
		resp.Body.Close()
		anon := req.Clone(req.Context())
		anon.Header.Del("Authorization")
		resp, err = c.http.Do(anon)
	}
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
		e = c.fetch(ctx, c.basicKey(r), used, func() (string, error) { return c.basic(ctx, r) })
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

// invalidToken reports whether resp is a 400 with a DENIED error, which registries such as
// Amazon ECR Public send instead of a 401 challenge for an expired or invalid bearer token (as
// oras-go handles it). A HEAD response has no body to tell, so any 400 counts: asking once more
// without the token costs a request, while trusting it would fail the lookup. The body read is
// put back for the caller.
func invalidToken(resp *http.Response) bool {
	if resp.StatusCode != http.StatusBadRequest {
		return false
	}
	if resp.Request != nil && resp.Request.Method == http.MethodHead {
		return true
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxErrorBytes))
	resp.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(b), resp.Body), resp.Body}
	if err != nil {
		return false
	}
	var body struct {
		Errors errorDetails `json:"errors"`
	}
	if json.Unmarshal(b, &body) != nil {
		return false
	}
	return slices.ContainsFunc(body.Errors, func(e errorDetail) bool { return e.Code == "DENIED" })
}

// basicKey is the cache key of r's Basic credential: per repository, since namespaced
// credentials (`auths` key ghcr.io/org) may differ between repositories of a registry.
func (c *client) basicKey(r *repository) string { return r.registry + " basic " + r.path }

// cached returns the fetched credential of r that the registry's last challenge scheme asks
// for; nil when there is none yet.
func (c *client) cached(r *repository, bearerKey string) *tokenEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	var e *tokenEntry
	switch c.schemes[r.registry] {
	case schemeBasic:
		e = c.tokens[c.basicKey(r)]
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

// basic returns the Basic Authorization value of r's credential.
func (c *client) basic(ctx context.Context, r *repository) (string, error) {
	cred, err := c.cred(ctx, r.registry, r.path)
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
	if err := validateRealm(ctx, realm, reqURL); err != nil {
		return "", err
	}
	scopes := []string{scope}
	for s := range strings.FieldsSeq(params["scope"]) {
		if !slices.Contains(scopes, s) {
			scopes = append(scopes, s)
		}
	}
	slices.Sort(scopes)
	cred, err := c.cred(ctx, r.registry, r.path)
	if err != nil {
		return "", err
	}
	var token string
	switch {
	case cred.AccessToken != "":
		token = cred.AccessToken
	case cred.RefreshToken != "":
		var oauthErr error
		if token, oauthErr = c.oauth2Token(ctx, reqURL, realm, service, scopes, cred); oauthErr != nil {
			if !oauth2Unsupported(oauthErr) {
				return "", oauthErr
			}
			if token, err = c.distributionToken(ctx, reqURL, realm, service, scopes, cred); err != nil {
				return "", errors.Join(err, oauthErr)
			}
		}
	default:
		if token, err = c.distributionToken(ctx, reqURL, realm, service, scopes, cred); err != nil {
			return "", err
		}
	}
	return "Bearer " + token, nil
}

// validateRealm refuses a realm the registry's credentials must not go to, as oras-go does: one
// that is not http(s), plain http when the registry is reached over https, or, on another host
// than the registry's, a host that is or resolves to an address of blockedRealmPrefixes
// (IPv4-mapped, NAT64 and 6to4 addresses unwrapped). A failed lookup allows the realm: behind a
// proxy the name may not resolve here, and a bogus one fails when dialed. DNS rebinding (a name
// resolving differently when dialed) is not defended against. Token requests check every
// redirect hop the same way (tokenRequest).
func validateRealm(ctx context.Context, realm string, registry *url.URL) error {
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
	host := u.Hostname()
	if strings.EqualFold(host, registry.Hostname()) {
		return nil
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if blockedRealmAddr(addr) {
			return fmt.Errorf("bearer realm %q: address %s not allowed", realm, unwrapAddr(addr))
		}
		return nil
	}
	addrs, _ := lookupIPAddr(ctx, host) // a failed lookup allows the realm, see above
	for _, a := range addrs {
		if addr, ok := netip.AddrFromSlice(a.IP); ok && blockedRealmAddr(addr) {
			return fmt.Errorf("bearer realm %q: host %s resolves to %s, not allowed", realm, host, unwrapAddr(addr))
		}
	}
	return nil
}

// lookupIPAddr resolves realm hosts; tests replace it.
var lookupIPAddr = net.DefaultResolver.LookupIPAddr

// blockedRealmPrefixes are oras-go's default realm denylist: loopback, link-local (including
// the cloud metadata address 169.254.169.254), link-local multicast, unspecified, and the
// metadata addresses of Alibaba, AWS (IPv6), GCP (IPv6) and Azure. Private networks are allowed:
// self-hosted registries put their token service there.
var blockedRealmPrefixes = []netip.Prefix{
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("224.0.0.0/24"),
	netip.MustParsePrefix("ff02::/16"),
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("100.100.100.200/32"),
	netip.MustParsePrefix("fd00:ec2::254/128"),
	netip.MustParsePrefix("fd00:ec2::23/128"),
	netip.MustParsePrefix("fd20:ce::254/128"),
	netip.MustParsePrefix("168.63.129.16/32"),
}

var (
	nat64Prefix  = netip.MustParsePrefix("64:ff9b::/96")
	sixTo4Prefix = netip.MustParsePrefix("2002::/16")
)

// blockedRealmAddr reports whether addr, as is or unwrapped, is in blockedRealmPrefixes.
func blockedRealmAddr(addr netip.Addr) bool {
	unwrapped := unwrapAddr(addr)
	for _, p := range blockedRealmPrefixes {
		if p.Contains(unwrapped) || p.Contains(addr) {
			return true
		}
	}
	return false
}

// unwrapAddr returns the IPv4 address an IPv4-mapped, NAT64 or 6to4 address embeds, else addr
// without its zone.
func unwrapAddr(addr netip.Addr) netip.Addr {
	addr = addr.Unmap().WithZone("")
	if !addr.Is6() {
		return addr
	}
	b := addr.As16()
	switch {
	case nat64Prefix.Contains(addr):
		return netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]})
	case sixTo4Prefix.Contains(addr):
		return netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]})
	}
	return addr
}

// distributionToken GETs a token from realm, with basic credentials when cred has a username or
// password, anonymously otherwise.
func (c *client) distributionToken(ctx context.Context, registry *url.URL, realm, service string, scopes []string, cred credential) (string, error) {
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
	return c.tokenRequest(req, registry)
}

// oauth2Token POSTs an OAuth2 refresh-token grant to realm.
func (c *client) oauth2Token(ctx context.Context, registry *url.URL, realm, service string, scopes []string, cred credential) (string, error) {
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
	return c.tokenRequest(req, registry)
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

// maxTokenRedirects bounds the redirects of a token request, as net/http does by default.
const maxTokenRedirects = 10

// tokenRequest sends a token request and decodes the token (`access_token`, else `token`).
// Every redirect hop must pass validateRealm against registry, so a validated realm cannot send
// the credentials on to a blocked address or from https to http. A realm on another host than
// the registry's, reached without a proxy, is also checked when connecting (see dialGuard).
func (c *client) tokenRequest(req *http.Request, registry *url.URL) (string, error) {
	maps.Copy(req.Header, c.header)
	req = dialGuard(req, registry)
	hc := *c.http
	hc.CheckRedirect = func(next *http.Request, via []*http.Request) error {
		if err := validateRealm(next.Context(), next.URL.String(), registry); err != nil {
			return fmt.Errorf("token request redirect rejected: %w", err)
		}
		if len(via) >= maxTokenRedirects {
			return fmt.Errorf("stopped after %d redirects", maxTokenRedirects)
		}
		return nil
	}
	resp, err := hc.Do(req)
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

// dialGuard returns req with a dial check (httpx.WithDialCheck) refusing connections to
// blockedRealmPrefixes, when req goes to another host than registry's and not through a proxy.
// validateRealm resolves the realm host beforehand; this check sees the address actually
// connected to, which a name that resolves differently the second time (DNS rebinding) cannot
// evade. Through a proxy the connection is the proxy's, which is not checked, as in oras-go.
func dialGuard(req *http.Request, registry *url.URL) *http.Request {
	if strings.EqualFold(req.URL.Hostname(), registry.Hostname()) {
		return req
	}
	if proxy, err := http.ProxyFromEnvironment(req); err != nil || proxy != nil {
		return req
	}
	host := req.URL.Host
	return req.WithContext(httpx.WithDialCheck(req.Context(), func(addr netip.Addr) error {
		if blockedRealmAddr(addr) {
			return fmt.Errorf("token request to %s: address %s not allowed", host, unwrapAddr(addr))
		}
		return nil
	}))
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
