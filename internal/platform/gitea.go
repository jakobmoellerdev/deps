package platform

import (
	"context"
	"net/http"
	"strconv"
)

// giteaClient serves Gitea and Forgejo (same API surface).
type giteaClient struct{ rest }

func (g *giteaClient) headers(h http.Header) {
	if g.token != "" {
		h.Set("Authorization", "token "+g.token)
	}
	h.Set("Accept", "application/json")
}

func (g *giteaClient) toPR(p *apiPR) PR {
	pr := p.pr()
	pr.Merged = p.Merged
	m := p.Mergeable != nil && *p.Mergeable
	pr.Mergeable = &m
	return pr
}

func (g *giteaClient) DefaultBranch(ctx context.Context) (string, error) {
	return defaultBranch(ctx, &g.rest, g.headers)
}

func (g *giteaClient) ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) {
	return listPRs(ctx, &g.rest, g.headers, g.toPR, 50, maxPages, func(page int) string {
		return "/repos/" + g.repo + "/pulls?state=" + state + "&sort=recentupdate&limit=50&page=" + strconv.Itoa(page)
	})
}

func (g *giteaClient) GetPR(ctx context.Context, n int) (PR, error) {
	var p apiPR
	if err := g.do(ctx, http.MethodGet, prPath(g.repo, n), nil, &p, g.headers); err != nil {
		return PR{}, err
	}
	return g.toPR(&p), nil
}

func (g *giteaClient) CreatePR(ctx context.Context, head, base, title, body string) (PR, error) {
	var p apiPR
	in := prCreate{Title: title, Head: head, Base: base, Body: body}
	if err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/pulls", in, &p, g.headers); err != nil {
		return PR{}, err
	}
	return g.toPR(&p), nil
}

func (g *giteaClient) UpdatePR(ctx context.Context, n int, title, body string) error {
	return g.do(ctx, http.MethodPatch, prPath(g.repo, n), prEdit{Title: title, Body: body}, nil, g.headers)
}

func (g *giteaClient) ClosePR(ctx context.Context, n int) error {
	return g.do(ctx, http.MethodPatch, prPath(g.repo, n), prEdit{State: "closed"}, nil, g.headers)
}
