// Package githubapi reads releases and files of GitHub and GitHub Enterprise Server repositories.
//
// With a token for the host it uses GraphQL, which returns only the requested fields: a page
// of 100 releases is a few KiB, where the REST list always carries every release body and
// asset (megabytes for large repositories). Without a token it uses REST, because GraphQL
// requires authentication. Release lists are cached per repository and run and shared by all
// callers (the github-releases datasource and git-tags release times).
package githubapi

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// ErrNotFound is wrapped by errors for a repository (or release) that does not exist or is
// not visible with the credentials used: REST 404 or GraphQL NOT_FOUND.
var ErrNotFound = errors.New("not found")

// ErrForbidden is wrapped by errors for a repository that exists but whose data the
// credentials used may not read, and that another token would not fix: a REST 403 or a
// GraphQL FORBIDDEN that is no rate limit. A GitHub organization with an IP allow list
// refuses every credentialed request from an address that is not on the list this way, for
// its public repositories as well (anonymous requests for them still succeed).
var ErrForbidden = errors.New("forbidden")

// Unreadable reports whether err means the API does not show a repository's data with the
// credentials used (ErrNotFound) or refuses to (ErrForbidden). Callers whose data is optional
// — release times, whether a repository is archived — then leave it unknown instead of
// failing the run; a caller that needs the data (the versions of github-releases) still
// fails, so a pin that stopped moving stays visible.
func Unreadable(err error) bool {
	return errors.Is(err, ErrNotFound) || errors.Is(err, ErrForbidden)
}

// Repo is a GitHub repository. Host is "github.com" for the public instance, else a GitHub
// Enterprise Server host (with an optional port) whose API is served by the host itself.
type Repo struct {
	Scheme, Host, Owner, Name string
}

// Path returns "owner/name".
func (r Repo) Path() string { return r.Owner + "/" + r.Name }

func (r Repo) public() bool { return strings.EqualFold(r.Host, "github.com") }

// hostname is Host without the port, the key of credentials.
func (r Repo) hostname() string {
	if h, _, err := net.SplitHostPort(r.Host); err == nil {
		return h
	}
	return r.Host
}

// ParseRepo accepts `owner/repo` (github.com) or `http(s)://host/owner/repo[.git]`.
func ParseRepo(name string) (Repo, error) {
	r := Repo{Scheme: "https", Host: "github.com"}
	p := name
	if strings.Contains(name, "://") {
		u, err := url.Parse(name)
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			return Repo{}, fmt.Errorf("invalid github repository %q", name)
		}
		r.Scheme, r.Host, p = u.Scheme, u.Host, strings.TrimSuffix(strings.Trim(u.Path, "/"), ".git")
		if r.public() {
			r.Scheme, r.Host = "https", "github.com"
		}
	}
	owner, rest, ok := strings.Cut(p, "/")
	if !ok || owner == "" || rest == "" || strings.Contains(rest, "/") {
		return Repo{}, fmt.Errorf("invalid github repository %q", name)
	}
	r.Owner, r.Name = owner, rest
	return r, nil
}

// Client sends GitHub API requests on the shared HTTP client and caches release lists.
type Client struct {
	client *http.Client
	auth   *httpx.Auth
	sem    chan struct{}
	// github.com endpoints; GitHub Enterprise Server uses <host>/api/v3 and <host>/api/graphql.
	restAPI, graphqlAPI string

	mu     sync.Mutex
	lists  map[string]*List
	assets map[string]*assetEntry
}

// Option configures a Client.
type Option func(*Client)

// WithAPI replaces the github.com REST base and GraphQL endpoint (tests use a local server).
func WithAPI(rest, graphql string) Option {
	return func(c *Client) { c.restAPI, c.graphqlAPI = rest, graphql }
}

// New returns a Client. sem bounds concurrent requests and is shared with other datasources;
// auth may be nil for anonymous access.
func New(client *http.Client, auth *httpx.Auth, sem chan struct{}, opts ...Option) *Client {
	c := &Client{
		client: client, auth: auth, sem: sem,
		restAPI: "https://api.github.com", graphqlAPI: "https://api.github.com/graphql",
		lists: make(map[string]*List), assets: make(map[string]*assetEntry),
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

func (c *Client) rest(r Repo) string {
	if r.public() {
		return c.restAPI
	}
	return r.Scheme + "://" + r.Host + "/api/v3"
}

func (c *Client) graphqlURL(r Repo) string {
	if r.public() {
		return c.graphqlAPI
	}
	return r.Scheme + "://" + r.Host + "/api/graphql"
}

// token returns the static token for r's host.
func (c *Client) token(r Repo) (string, bool) {
	_, t, ok := c.auth.Static(r.hostname())
	return t, ok
}

// GraphQL reports whether requests for r use GraphQL (a token for its host is configured).
func (c *Client) GraphQL(r Repo) bool {
	_, ok := c.token(r)
	return ok
}

// restGet sends an authenticated (when a token is known) REST GET of path below r's API.
// The caller closes the response body; non-2xx responses are returned as they are.
func (c *Client) restGet(ctx context.Context, r Repo, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.rest(r)+path, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", httpx.UserAgent())
	if t, ok := c.token(r); ok {
		req.Header.Set("Authorization", "Bearer "+t)
	}
	return c.client.Do(req)
}

// restDo holds a request slot for a REST GET of path below r's API, turns a non-2xx response
// into a statusError and otherwise hands the body to decode. logRate logs the response's rate
// limit headers before the status check.
func (c *Client) restDo(ctx context.Context, r Repo, path string, logRate bool, decode func(io.Reader) error) error {
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	resp, err := c.restGet(ctx, r, path)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	if logRate {
		logRateLimit(ctx, r, resp, false)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp)
	}
	return decode(resp.Body)
}

// statusError describes a non-2xx REST or GraphQL response: a 404 wraps ErrNotFound, a rate
// limit (a 429, or a 403 with an exhausted quota or Retry-After) wraps httpx.ErrRateLimited,
// naming its host and reset time, and any other 403 wraps ErrForbidden.
func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, ErrNotFound)
	case resp.StatusCode == http.StatusForbidden && httpx.LimitError(resp) == nil:
		return fmt.Errorf("HTTP %d: %w", resp.StatusCode, ErrForbidden)
	}
	return httpx.ResponseError(resp)
}

// gqlError is one entry of a GraphQL `errors` array.
type gqlError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// graphql runs query with vars against r's GraphQL endpoint and decodes `data` into out.
// GitHub answers 200 with an `errors` array for failed queries: NOT_FOUND wraps ErrNotFound,
// RATE_LIMITED wraps httpx.ErrRateLimited. The query only reads, so it is retried like a GET.
func graphql[T any](ctx context.Context, c *Client, r Repo, query string, vars map[string]any, out *T) error {
	token, ok := c.token(r)
	if !ok {
		return errors.New("graphql requires a token")
	}
	body, err := json.Marshal(map[string]any{"query": query, "variables": vars})
	if err != nil {
		return err
	}
	if err := c.acquire(ctx); err != nil {
		return err
	}
	defer c.release()
	req, err := http.NewRequestWithContext(httpx.Idempotent(ctx), http.MethodPost, c.graphqlURL(r), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer closeBody(resp)
	logRateLimit(ctx, r, resp, true)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return statusError(resp)
	}
	var res struct {
		Data   *T         `json:"data"`
		Errors []gqlError `json:"errors"`
	}
	res.Data = out
	if err := json.UnmarshalRead(resp.Body, &res); err != nil {
		return fmt.Errorf("malformed response: %w", err)
	}
	if len(res.Errors) > 0 {
		return gqlErr(resp, r, res.Errors)
	}
	return nil
}

// gqlErr classifies the `errors` array of a 200 answer: NOT_FOUND wraps ErrNotFound,
// RATE_LIMITED wraps httpx.ErrRateLimited and FORBIDDEN wraps ErrForbidden. An organization
// IP allow list refuses a query in this array rather than with a status, and its message is
// matched as well because GitHub does not always type that error.
func gqlErr(resp *http.Response, r Repo, errs []gqlError) error {
	for _, e := range errs {
		switch {
		case e.Type == "NOT_FOUND":
			return fmt.Errorf("%s: %w", e.Message, ErrNotFound)
		case e.Type == "RATE_LIMITED":
			return fmt.Errorf("%w by %s until %s: %s", httpx.ErrRateLimited, r.hostname(), resetTime(resp.Header.Get("X-RateLimit-Reset")), e.Message)
		case e.Type == "FORBIDDEN" || strings.Contains(e.Message, "IP allow list"):
			return fmt.Errorf("%s: %w", e.Message, ErrForbidden)
		}
	}
	msgs := make([]string, len(errs))
	for i, e := range errs {
		msgs[i] = e.Message
	}
	return fmt.Errorf("graphql: %s", strings.Join(msgs, "; "))
}

func logRateLimit(ctx context.Context, r Repo, resp *http.Response, graphql bool) {
	h := resp.Header
	slog.DebugContext(ctx, "github rate limit", "repo", r.Path(), "graphql", graphql,
		"limit", h.Get("X-RateLimit-Limit"), "remaining", h.Get("X-RateLimit-Remaining"), "reset", h.Get("X-RateLimit-Reset"))
}

// resetTime renders the X-RateLimit-Reset epoch seconds as RFC 3339, or the raw value.
func resetTime(v string) string {
	sec, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return v
	}
	return time.Unix(sec, 0).UTC().Format(time.RFC3339)
}

func (c *Client) acquire(ctx context.Context) error {
	if c.sem == nil {
		return ctx.Err()
	}
	select {
	case c.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) release() {
	if c.sem != nil {
		<-c.sem
	}
}

// closeBody drains a little of the body so the connection can be reused, then closes it.
func closeBody(resp *http.Response) {
	httpx.DrainBody(resp)
	_ = resp.Body.Close()
}
