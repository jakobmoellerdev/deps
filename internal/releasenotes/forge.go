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
	"net/url"
	"strconv"
	"strings"
	"time"

	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/githubapi"
	"git.jakob-moeller.cloud/jakobmoellerdev/deps/internal/httpx"
)

// Page sizes of the one release page listed per repository: GitHub and GitLab allow 100,
// Gitea caps at MAX_RESPONSE_ITEMS (default 50).
const (
	githubNotes = 100
	gitlabNotes = 100
	giteaNotes  = 50
)

// release is what deps keeps of a published release; body is cut to maxBody.
type release struct {
	tag, url, body string
	cut            bool // body was longer than maxBody
	draft, pre     bool
}

func newRelease(tag, link, body string, draft, pre bool) release {
	body, cut := truncate(strings.TrimSpace(strings.ReplaceAll(body, "\r\n", "\n")), maxBody)
	return release{tag: tag, url: link, body: body, cut: cut, draft: draft, pre: pre}
}

// releases lists the newest releases of r (one page) once per repository per run.
func (n *Notes) releases(ctx context.Context, r repoRef) ([]release, error) {
	key := strconv.Itoa(int(r.kind)) + " " + strings.ToLower(r.web())
	return n.lists.Do(ctx, key, func() ([]release, error) {
		start := time.Now()
		var rels []release
		var err error
		switch r.kind {
		case github:
			rels, err = n.fetchGitHub(ctx, r)
		case gitlab:
			rels, err = n.fetchGitLab(ctx, r)
		default:
			rels, err = n.fetchGitea(ctx, r)
		}
		slog.DebugContext(ctx, "release notes listed", "repo", r.web(), "releases", len(rels), "duration", time.Since(start), "err", err)
		return rels, err
	})
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
	target := r.base() + "/api/v1/repos/" + r.owner + "/" + r.name + "/releases?limit=" + strconv.Itoa(giteaNotes)
	_, token, authed := n.auth.Static(hostname(r.host))
	return fetchList(ctx, n, target, func(h http.Header) {
		if authed {
			h.Set("Authorization", "token "+token)
		}
	}, func(raw struct {
		TagName    string `json:"tag_name"`
		HTMLURL    string `json:"html_url"`
		Body       string `json:"body"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	},
	) (release, bool) {
		return newRelease(raw.TagName, raw.HTMLURL, raw.Body, raw.Draft, raw.Prerelease), raw.TagName != ""
	})
}

// fetchGitLab lists the releases of a GitLab project (`<host>[<root>]/api/v4`), newest
// first. The token of the host goes as `Authorization: Bearer`, the CI job token as
// `JOB-TOKEN`. An upcoming release (released_at in the future) counts as a draft; GitLab has
// no pre-releases.
func (n *Notes) fetchGitLab(ctx context.Context, r repoRef) ([]release, error) {
	target := r.base() + "/api/v4/projects/" + url.PathEscape(r.owner+"/"+r.name) + "/releases?per_page=" + strconv.Itoa(gitlabNotes)
	user, token, authed := n.auth.Static(hostname(r.host))
	return fetchList(ctx, n, target, func(h http.Header) {
		switch {
		case !authed:
		case user == "gitlab-ci-token":
			h.Set("JOB-TOKEN", token)
		default:
			h.Set("Authorization", "Bearer "+token)
		}
	}, func(raw struct {
		TagName     string `json:"tag_name"`
		Description string `json:"description"`
		Upcoming    bool   `json:"upcoming_release"`
		Links       struct {
			Self string `json:"self"`
		} `json:"_links"`
	},
	) (release, bool) {
		return newRelease(raw.TagName, raw.Links.Self, raw.Description, raw.Upcoming, false), raw.TagName != ""
	})
}

// hostname strips the port from host.
func hostname(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// fetchList GETs the release list at target with header's credentials and keeps the
// releases conv accepts, decoding one element at a time.
func fetchList[T any](ctx context.Context, n *Notes, target string, header func(http.Header), conv func(T) (release, bool)) ([]release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, http.NoBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", httpx.UserAgent())
	req.Header.Set("Accept", "application/json")
	header(req.Header)
	done, err := httpx.Acquire(ctx, n.sem)
	if err != nil {
		return nil, err
	}
	defer done()
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
		var raw T
		if err := json.UnmarshalDecode(dec, &raw); err != nil {
			return nil, fmt.Errorf("GET %s: %w", req.URL.Redacted(), err)
		}
		if rel, ok := conv(raw); ok {
			rels = append(rels, rel)
		}
	}
	return rels, nil
}
