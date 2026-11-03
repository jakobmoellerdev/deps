package releasenotes

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// release is what deps keeps of a published release; body is cut to maxBody.
type release struct {
	tag, url, body string
	cut            bool // body was longer than maxBody
	draft, pre     bool
}

// apiBase returns the REST base of r.
func (n *Notes) apiBase(r repoRef) string {
	switch {
	case r.kind == github && r.host == "github.com":
		return n.githubAPI
	case r.kind == github:
		return "https://" + r.host + "/api/v3"
	}
	return "https://" + r.host + "/api/v1"
}

// releases lists the newest releases of r (one page) once per repository per run.
func (n *Notes) releases(ctx context.Context, r repoRef) ([]release, error) {
	key := strings.ToLower(n.apiBase(r) + "/repos/" + r.owner + "/" + r.name)
	n.mu.Lock()
	e := n.lists[key]
	if e == nil {
		e = &listEntry{}
		n.lists[key] = e
	}
	n.mu.Unlock()
	e.once.Do(func() {
		start := time.Now()
		e.rels, e.err = n.fetch(ctx, r)
		slog.DebugContext(ctx, "release notes listed", "repo", key, "releases", len(e.rels), "duration", time.Since(start), "err", e.err)
	})
	return e.rels, e.err
}

func (n *Notes) fetch(ctx context.Context, r repoRef) ([]release, error) {
	target := n.apiBase(r) + "/repos/" + r.owner + "/" + r.name + "/releases?"
	host := r.host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	_, token, authed := n.auth.Static(host)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	if r.kind == github {
		req.URL.RawQuery = "per_page=100"
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if authed {
			req.Header.Set("Authorization", "Bearer "+token)
		}
	} else {
		req.URL.RawQuery = "limit=50" // Gitea's default maximum page size
		req.Header.Set("Accept", "application/json")
		if authed {
			req.Header.Set("Authorization", "token "+token)
		}
	}
	if err := n.acquire(ctx); err != nil {
		return nil, err
	}
	defer n.release()
	resp, err := n.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		_ = resp.Body.Close()
	}()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: HTTP %d", req.URL.Redacted(), resp.StatusCode)
	}
	dec := json.NewDecoder(resp.Body)
	if tok, err := dec.Token(); err != nil || tok != json.Delim('[') {
		return nil, fmt.Errorf("GET %s: not a release list", req.URL.Redacted())
	}
	var rels []release
	for dec.More() {
		var raw struct {
			TagName    string `json:"tag_name"`
			HTMLURL    string `json:"html_url"`
			Body       string `json:"body"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := dec.Decode(&raw); err != nil {
			return nil, fmt.Errorf("GET %s: %w", req.URL.Redacted(), err)
		}
		if raw.TagName == "" {
			continue
		}
		body, cut := truncate(strings.TrimSpace(strings.ReplaceAll(raw.Body, "\r\n", "\n")), maxBody)
		rels = append(rels, release{tag: raw.TagName, url: raw.HTMLURL, body: body, cut: cut, draft: raw.Draft, pre: raw.Prerelease})
	}
	return rels, nil
}

func (n *Notes) acquire(ctx context.Context) error {
	if n.sem == nil {
		return ctx.Err()
	}
	select {
	case n.sem <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (n *Notes) release() {
	if n.sem != nil {
		<-n.sem
	}
}
