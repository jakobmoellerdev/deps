package platform

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
)

// NewClient returns the pull request client for info.Kind (gitea and forgejo share one).
func NewClient(info Info, token string, c *http.Client) Client {
	r := rest{api: info.APIURL, repo: info.Repo, token: token, c: c}
	if info.Kind == GitHub {
		return &githubClient{r}
	}
	return &giteaClient{r}
}

// rest is the shared JSON-over-HTTP core of both clients.
type rest struct {
	api, repo, token string
	c                *http.Client
}

func (r *rest) do(ctx context.Context, method, path string, in, out any, setHeaders func(http.Header)) error {
	var body io.Reader
	if in != nil {
		b, err := json.Marshal(in)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, r.api+path, body)
	if err != nil {
		return err
	}
	if in != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	setHeaders(req.Header)
	resp, err := r.c.Do(req)
	if err != nil {
		return fmt.Errorf("%s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return fmt.Errorf("%s %s: %s: %s", method, path, resp.Status, b)
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("%s %s: decode: %w", method, path, err)
	}
	return nil
}

// apiPR covers the PR JSON of GitHub and Gitea/Forgejo.
type apiPR struct {
	Number    int     `json:"number"`
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	State     string  `json:"state"`
	HTMLURL   string  `json:"html_url"`
	Merged    bool    `json:"merged"`
	MergedAt  *string `json:"merged_at"`
	Mergeable *bool   `json:"mergeable"`
	Head      struct {
		Ref string `json:"ref"`
	} `json:"head"`
}

func (p *apiPR) pr() PR {
	return PR{Number: p.Number, Title: p.Title, Body: p.Body, Head: p.Head.Ref, State: p.State, URL: p.HTMLURL}
}

type prEdit struct {
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	State string `json:"state,omitempty"`
}

type prCreate struct {
	Title string `json:"title"`
	Head  string `json:"head"`
	Base  string `json:"base"`
	Body  string `json:"body"`
}

type githubClient struct{ rest }

func (g *githubClient) headers(h http.Header) {
	if g.token != "" {
		h.Set("Authorization", "Bearer "+g.token)
	}
	h.Set("Accept", "application/vnd.github+json")
	h.Set("X-GitHub-Api-Version", "2022-11-28")
}

func (g *githubClient) toPR(p *apiPR) PR {
	pr := p.pr()
	pr.Merged = p.MergedAt != nil
	pr.Mergeable = p.Mergeable
	return pr
}

func (g *githubClient) DefaultBranch(ctx context.Context) (string, error) {
	return defaultBranch(ctx, &g.rest, g.headers)
}

func (g *githubClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	const per = 100
	return listPRs(ctx, &g.rest, g.headers, g.toPR, per, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/pulls?state=" + state + "&sort=updated&direction=desc&per_page=100&page=" + strconv.Itoa(page)
	})
}

func (g *githubClient) GetPR(ctx context.Context, n int) (PR, error) {
	var p apiPR
	if err := g.do(ctx, http.MethodGet, prPath(g.repo, n), nil, &p, g.headers); err != nil {
		return PR{}, err
	}
	return g.toPR(&p), nil
}

func (g *githubClient) CreatePR(ctx context.Context, head, base, title, body string) (PR, error) {
	var p apiPR
	in := prCreate{Title: title, Head: head, Base: base, Body: body}
	if err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/pulls", in, &p, g.headers); err != nil {
		return PR{}, err
	}
	return g.toPR(&p), nil
}

func (g *githubClient) UpdatePR(ctx context.Context, n int, title, body string) error {
	return g.do(ctx, http.MethodPatch, prPath(g.repo, n), prEdit{Title: title, Body: body}, nil, g.headers)
}

func (g *githubClient) ClosePR(ctx context.Context, n int) error {
	return g.do(ctx, http.MethodPatch, prPath(g.repo, n), prEdit{State: "closed"}, nil, g.headers)
}

func prPath(repo string, n int) string { return "/repos/" + repo + "/pulls/" + strconv.Itoa(n) }

func defaultBranch(ctx context.Context, r *rest, h func(http.Header)) (string, error) {
	var v struct {
		DefaultBranch string `json:"default_branch"`
	}
	if err := r.do(ctx, http.MethodGet, "/repos/"+r.repo, nil, &v, h); err != nil {
		return "", err
	}
	if v.DefaultBranch == "" {
		return "", fmt.Errorf("GET /repos/%s: empty default_branch", r.repo)
	}
	return v.DefaultBranch, nil
}

func listPRs(ctx context.Context, r *rest, h func(http.Header), conv func(*apiPR) PR, per, maxPages int, path func(int) string) ([]PR, error) {
	var out []PR
	for page := 1; page <= maxPages; page++ {
		var ps []apiPR
		if err := r.do(ctx, http.MethodGet, path(page), nil, &ps, h); err != nil {
			return nil, err
		}
		for i := range ps {
			out = append(out, conv(&ps[i]))
		}
		if len(ps) < per {
			break
		}
	}
	return out, nil
}
