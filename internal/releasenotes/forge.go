package releasenotes

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Page sizes of the one release page listed per repository: GitHub allows 100, Gitea caps at
// MAX_RESPONSE_ITEMS (default 50).
const (
	githubNotes = 100
	giteaNotes  = 50
)

// release is what deps keeps of a published release; body is cut to maxBody.
type release struct {
	tag, url, body string
	cut            bool // body was longer than maxBody
	draft, pre     bool
}

func newRelease(tag, url, body string, draft, pre bool) release {
	body, cut := truncate(strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n")), maxBody)
	return release{tag: tag, url: url, body: body, cut: cut, draft: draft, pre: pre}
}

// releases lists the newest releases of r (one page) once per repository per run.
func (n *Notes) releases(ctx context.Context, r repoRef) ([]release, error) {
	key := strconv.Itoa(int(r.kind)) + " " + strings.ToLower(r.web())
	n.mu.Lock()
	e := n.lists[key]
	if e == nil {
		e = &listEntry{}
		n.lists[key] = e
	}
	n.mu.Unlock()
	e.once.Do(func() {
		start := time.Now()
		if r.kind == github {
			e.rels, e.err = n.fetchGitHub(ctx, r)
		} else {
			e.rels, e.err = n.fetchGitea(ctx, r)
		}
		slog.DebugContext(ctx, "release notes listed", "repo", r.web(), "releases", len(e.rels), "duration", time.Since(start), "err", e.err)
	})
	return e.rels, e.err
}

// fetchGitHub lists the releases of a GitHub repository (GraphQL when authenticated).
func (n *Notes) fetchGitHub(ctx context.Context, r repoRef) ([]release, error) {
	var rels []release
	err := n.gh.Notes(ctx, githubapi.Repo{Scheme: "https", Host: r.host, Owner: r.owner, Name: r.name}, githubNotes, func(note githubapi.Note) {
		if note.Tag != "" {
			rels = append(rels, newRelease(note.Tag, note.URL, note.Body, note.Draft, note.Prerelease))
		}
	})
	return rels, err
}

// fetchGitea lists the releases of a Gitea/Forgejo repository (`<host>/api/v1`).
func (n *Notes) fetchGitea(ctx context.Context, r repoRef) ([]release, error) {
	target := "https://" + r.host + "/api/v1/repos/" + r.owner + "/" + r.name + "/releases?limit=" + strconv.Itoa(giteaNotes)
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
	req.Header.Set("Accept", "application/json")
	if authed {
		req.Header.Set("Authorization", "token "+token)
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
	dec := jsontext.NewDecoder(resp.Body, jsontext.AllowDuplicateNames(true)) // no per-object name sets
	if tok, err := dec.ReadToken(); err != nil || tok.Kind() != '[' {
		return nil, fmt.Errorf("GET %s: not a release list", req.URL.Redacted())
	}
	var rels []release
	for dec.PeekKind() != ']' {
		var raw struct {
			TagName    string `json:"tag_name"`
			HTMLURL    string `json:"html_url"`
			Body       string `json:"body"`
			Draft      bool   `json:"draft"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.UnmarshalDecode(dec, &raw); err != nil {
			return nil, fmt.Errorf("GET %s: %w", req.URL.Redacted(), err)
		}
		if raw.TagName != "" {
			rels = append(rels, newRelease(raw.TagName, raw.HTMLURL, raw.Body, raw.Draft, raw.Prerelease))
		}
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
