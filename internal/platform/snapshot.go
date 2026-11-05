package platform

import (
	"context"
	"errors"
	"net/http"
)

// ErrNoDependencyGraph means the forge has no dependency graph (Gitea, Forgejo).
var ErrNoDependencyGraph = errors.New("the forge has no dependency graph")

// SubmitSnapshot posts a dependency graph snapshot (POST /repos/{repo}/dependency-graph/
// snapshots, GitHub's dependency submission API; the token needs contents: write) and
// returns GitHub's result: "SUCCESS", "ACCEPTED" (queued) or "INVALID".
func SubmitSnapshot(ctx context.Context, info Info, token string, c *http.Client, snapshot any) (string, error) {
	if info.Kind != GitHub {
		return "", ErrNoDependencyGraph
	}
	g := newGitHub(info, token, c)
	var out struct {
		Result  string `json:"result"`
		Message string `json:"message"`
	}
	if err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/dependency-graph/snapshots", snapshot, &out, g.headers); err != nil {
		return "", err
	}
	if out.Result == "INVALID" {
		return out.Result, errors.New("snapshot rejected: " + out.Message)
	}
	return out.Result, nil
}
