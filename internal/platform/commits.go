package platform

import (
	"cmp"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strings"
)

// IsAppToken reports whether token is a GitHub App installation token, as GITHUB_TOKEN in
// GitHub Actions is: such tokens start with "ghs_" (Renovate's GitHub App detection).
func IsAppToken(token string) bool { return strings.HasPrefix(token, "ghs_") }

// Errors of GitHubCommits.Push.
var (
	// ErrRefStale means the branch no longer points at the expected commit.
	ErrRefStale = errors.New("stale info")
	// ErrRefRejected means GitHub declined the update, e.g. branch protection or a ruleset.
	ErrRefRejected = errors.New("rejected")
)

// Tip is the tip commit of a branch.
type Tip struct {
	Author  string   // author email; Author's email for commits of the token's account
	Parents []string // parent ids
	Tree    string   // tree id
}

// GitHubCommits creates commits and moves branches through the GitHub API instead of git
// (Renovate's platformCommit). GitHub signs the commits it creates for a GitHub App
// installation token, so they show as verified, authored by the App's bot account; for other
// tokens they are unsigned. The author is always the token's account.
//
// Commit uploads the changed files as blobs and creates a tree on top of the parent's tree
// and a commit; nothing references the commit until Push moves the branch with the GraphQL
// updateRefs mutation, which compares the branch tip and sets it in one step. A failed
// Commit or Push therefore leaves the branch as it was.
type GitHubCommits struct {
	gh          *githubClient
	prefix      string // path of the change paths' directory in the repository: "" or ending in '/'
	name, email string // the token's account (Author)
	id          int64  // its user id
}

// NewGitHubCommits returns the GitHub API committer for info and looks up the token's account:
// GraphQL viewer for a GitHub App installation token (the App's bot account), GET /user for
// other tokens. Change paths are relative to prefix, the repository path of the directory
// deps runs in (gitx.Repo.Prefix).
func NewGitHubCommits(ctx context.Context, info Info, token string, c *http.Client, prefix string) (*GitHubCommits, error) {
	g := &GitHubCommits{gh: &githubClient{api: info.APIURL, repo: info.Repo, token: token, c: c}, prefix: prefix}
	var login string
	if IsAppToken(token) {
		var v struct {
			Viewer struct {
				Login      string `json:"login"`
				DatabaseID int64  `json:"databaseId"`
			} `json:"viewer"`
		}
		msg, err := g.gh.graphql(ctx, "viewer", `query { viewer { login databaseId } }`, map[string]any{}, &v)
		if err != nil {
			return nil, err
		}
		if msg != "" {
			return nil, fmt.Errorf("POST /graphql viewer: %s", msg)
		}
		login, g.id, g.name = v.Viewer.Login, v.Viewer.DatabaseID, v.Viewer.Login
	} else {
		var u struct {
			Login string `json:"login"`
			ID    int64  `json:"id"`
			Name  string `json:"name"`
		}
		if err := g.gh.do(ctx, http.MethodGet, "/user", nil, &u, g.gh.headers); err != nil {
			return nil, err
		}
		login, g.id, g.name = u.Login, u.ID, cmp.Or(u.Name, u.Login)
	}
	if login == "" || g.id == 0 {
		return nil, errors.New("GitHub reports no account for the token")
	}
	// GitHub's noreply address of the account, as in the commits it creates for an App.
	noreply := "users.noreply." + info.Host
	if strings.HasSuffix(info.Host, ".ghe.com") {
		noreply = "users.noreply.ghe.com"
	}
	g.email = fmt.Sprintf("%d+%s@%s", g.id, login, noreply)
	return g, nil
}

// Author returns the name and the noreply email of the token's account. BranchTip reports
// this email for every commit GitHub links to the account, whatever email the commit carries.
func (g *GitHubCommits) Author() (name, email string) { return g.name, g.email }

// BranchTip returns the tip of branch (GET /repos/{repo}/commits/refs/heads/{branch}). A tip
// whose author GitHub links to the token's account reports Author's email, so commits GitHub
// created for the token, and commits of the same user under any of their verified emails,
// count as deps' own.
func (g *GitHubCommits) BranchTip(ctx context.Context, branch string) (Tip, error) {
	var c struct {
		Commit struct {
			Author struct {
				Email string `json:"email"`
			} `json:"author"`
			Tree shaJSON `json:"tree"`
		} `json:"commit"`
		Author *struct {
			ID int64 `json:"id"`
		} `json:"author"` // the linked GitHub account; null when the email is unknown to GitHub
		Parents []shaJSON `json:"parents"`
	}
	if err := g.gh.do(ctx, http.MethodGet, "/repos/"+g.gh.repo+"/commits/refs/heads/"+EscapePath(branch), nil, &c, g.gh.headers); err != nil {
		return Tip{}, err
	}
	t := Tip{Author: c.Commit.Author.Email, Tree: c.Commit.Tree.SHA}
	if c.Author != nil && c.Author.ID == g.id {
		t.Author = g.email
	}
	for _, p := range c.Parents {
		t.Parents = append(t.Parents, p.SHA)
	}
	return t, nil
}

// treeEntry is an entry of POST /git/trees; a nil SHA deletes the path from the base tree.
type treeEntry struct {
	Path string  `json:"path"`
	Mode string  `json:"mode"`
	Type string  `json:"type"`
	SHA  *string `json:"sha"`
}

type shaJSON struct {
	SHA string `json:"sha"`
}

// CommitFile is one file Commit writes or deletes.
type CommitFile struct {
	Path       string // slash path relative to the committer's prefix
	Executable bool   // mode 100755 instead of 100644
	Content    []byte // nil deletes the file
}

// Commit creates a commit with message msg on top of parent with files applied and returns
// its id. GitHub sets the author to the token's account (Author) and, for App tokens, the
// committer to GitHub. The commit is on no branch until Push.
func (g *GitHubCommits) Commit(ctx context.Context, parent string, files []CommitFile, msg string) (string, error) {
	repo := "/repos/" + g.gh.repo
	var pc struct {
		Tree shaJSON `json:"tree"`
	}
	if err := g.gh.do(ctx, http.MethodGet, repo+"/git/commits/"+parent, nil, &pc, g.gh.headers); err != nil {
		return "", err
	}
	entries := make([]treeEntry, 0, len(files))
	for _, f := range files {
		e := treeEntry{Path: path.Join(g.prefix, f.Path), Mode: "100644", Type: "blob"}
		if f.Executable {
			e.Mode = "100755"
		}
		if f.Content != nil {
			// Blobs carry the exact bytes, binary files included (inline tree content must be UTF-8).
			var b shaJSON
			in := map[string]string{"content": base64.StdEncoding.EncodeToString(f.Content), "encoding": "base64"}
			if err := g.gh.do(ctx, http.MethodPost, repo+"/git/blobs", in, &b, g.gh.headers); err != nil {
				return "", fmt.Errorf("%s: %w", f.Path, err)
			}
			e.SHA = &b.SHA
		}
		entries = append(entries, e)
	}
	var tree shaJSON
	if err := g.gh.do(ctx, http.MethodPost, repo+"/git/trees", map[string]any{"base_tree": pc.Tree.SHA, "tree": entries}, &tree, g.gh.headers); err != nil {
		return "", err
	}
	var commit shaJSON
	in := map[string]any{"message": msg, "tree": tree.SHA, "parents": []string{parent}}
	if err := g.gh.do(ctx, http.MethodPost, repo+"/git/commits", in, &commit, g.gh.headers); err != nil {
		return "", err
	}
	return commit.SHA, nil
}

const updateRefsMutation = `mutation($repo: ID!, $ref: GitRefname!, $before: GitObjectID!, $after: GitObjectID!) {
  updateRefs(input: {repositoryId: $repo, refUpdates: [{name: $ref, beforeOid: $before, afterOid: $after, force: true}]}) { clientMutationId }
}`

// zeroOID as beforeOid of updateRefs means the ref must not exist yet.
const zeroOID = "0000000000000000000000000000000000000000"

// Push sets refs/heads/<branch> to commit if it currently points at expected ("" = must not
// exist), like git push --force-with-lease: GraphQL updateRefs with beforeOid and force
// compares and sets the ref atomically. A refused update wraps ErrRefStale when the branch
// moved, else ErrRefRejected.
func (g *GitHubCommits) Push(ctx context.Context, commit, branch, expected string) error {
	repo, err := g.gh.repository(ctx, g.gh.headers)
	if err != nil {
		return err
	}
	ref := "refs/heads/" + branch
	vars := map[string]string{"repo": repo.NodeID, "ref": ref, "before": cmp.Or(expected, zeroOID), "after": commit}
	msg, err := g.gh.graphql(ctx, "updateRefs", updateRefsMutation, vars, nil)
	if err != nil || msg == "" {
		return err
	}
	// GitHub rejects a stale beforeOid with a generic error; name the cause when the branch moved.
	var cur struct {
		Object shaJSON `json:"object"`
	}
	switch err := g.gh.do(ctx, http.MethodGet, "/repos/"+g.gh.repo+"/git/ref/heads/"+EscapePath(branch), nil, &cur, g.gh.headers); {
	case err == nil && cur.Object.SHA != expected:
		return fmt.Errorf("update %s: %w: the branch is at %s, expected %s", ref, ErrRefStale, short(cur.Object.SHA), cmp.Or(short(expected), "no branch"))
	case expected != "" && hasStatus(err, http.StatusNotFound, ""):
		return fmt.Errorf("update %s: %w: the branch was deleted, expected %s", ref, ErrRefStale, short(expected))
	}
	return fmt.Errorf("update %s: %w: %s", ref, ErrRefRejected, msg)
}

func short(s string) string { return s[:min(7, len(s))] }

// EscapePath escapes each '/'-separated segment of p for a URL path, keeping
// its slashes, so branch and tag names with '/' address the right resource.
func EscapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}
