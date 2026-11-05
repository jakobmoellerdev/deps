package platform

import (
	"context"
	"errors"
	"net/http"
)

// CheckRun is a completed GitHub check run with a markdown report.
type CheckRun struct {
	Name, HeadSHA string
	Conclusion    string // success, neutral, failure, …
	Title         string // output title, one line
	Summary       string // output summary, markdown; at most CheckRunSummaryLimit bytes
}

// CheckRunSummaryLimit is GitHub's maximum length of a check run's output summary.
const CheckRunSummaryLimit = 65535

// ErrNoCheckRuns means the forge or the token cannot create check runs: Gitea and Forgejo
// have none, and on GitHub only App installation tokens (GITHUB_TOKEN included) with
// checks: write can create them; PATs cannot.
var ErrNoCheckRuns = errors.New("check runs are not available")

// CreateCheckRun creates run on GitHub (POST /repos/{repo}/check-runs) and returns its URL.
// It returns ErrNoCheckRuns (wrapped) for other forges and when GitHub refuses the token
// (403, or 404 for a token without access).
func CreateCheckRun(ctx context.Context, info Info, token string, c *http.Client, run CheckRun) (string, error) {
	if info.Kind != GitHub {
		return "", ErrNoCheckRuns
	}
	g := newGitHub(info, token, c)
	in := map[string]any{
		"name":       run.Name,
		"head_sha":   run.HeadSHA,
		"status":     "completed",
		"conclusion": run.Conclusion,
		"output":     map[string]string{"title": run.Title, "summary": run.Summary},
	}
	var out struct {
		HTMLURL string `json:"html_url"`
	}
	err := g.do(ctx, http.MethodPost, "/repos/"+g.repo+"/check-runs", in, &out, g.headers)
	if hasStatus(err, http.StatusForbidden, "") || hasStatus(err, http.StatusNotFound, "") {
		return "", errors.Join(ErrNoCheckRuns, err)
	}
	return out.HTMLURL, err
}
