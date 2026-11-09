// Package platform detects the code forge (GitHub, Gitea, Forgejo, GitLab) and talks to its
// pull (merge) request API.
package platform

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"
)

// Kind is the forge type.
type Kind string

const (
	GitHub  Kind = "github"
	Gitea   Kind = "gitea"
	Forgejo Kind = "forgejo"
	GitLab  Kind = "gitlab"
)

// Info describes the repository's forge.
type Info struct {
	Kind      Kind
	ServerURL string
	APIURL    string
	Host      string
	Repo      string // "owner/name"; on GitLab the project path, which may be "group/subgroup/name"
}

// PR is the subset of pull request fields deps uses.
type PR struct {
	Number    int
	Title     string
	Body      string
	Head      string
	HeadSHA   string // head.sha
	Base      string // base (target) branch; "" when the forge does not report it
	State     string
	URL       string
	Merged    bool
	Mergeable *bool
	CreatedAt time.Time // zero when the API omits created_at
	NodeID    string    // GitHub GraphQL node id (node_id); "" on Gitea and Forgejo
	// AutoMerge reports auto-merge as already enabled (GitHub auto_merge non-null, GitLab
	// merge_when_pipeline_succeeds). Gitea and Forgejo do not report it, so it is always false
	// there and requests are idempotent.
	AutoMerge bool
	// PushCancelsAutoMerge reports that a push to the PR's branch cancels auto-merge (GitLab),
	// so AutoMerge no longer holds once the branch is pushed.
	PushCancelsAutoMerge bool
	Labels               []string // label names; nil when it has none
	// Draft reports a draft PR: GitHub's, Gitea's and GitLab's draft field, on Gitea and
	// Forgejo also a title with a work-in-progress prefix (WIP: or [WIP]).
	Draft bool
}

// HasLabel reports whether p has the label name (case-insensitive, as forges compare label
// names).
func (p PR) HasLabel(name string) bool {
	return slices.ContainsFunc(p.Labels, func(l string) bool { return strings.EqualFold(l, name) })
}

// Issue is the subset of issue fields deps uses.
type Issue struct {
	Number int
	Title  string
	Body   string
	URL    string
}

// PRMetadata is what a new update PR gets besides its title and body.
type PRMetadata struct{ Labels, Reviewers, Assignees []string }

// Empty reports whether m sets nothing.
func (m PRMetadata) Empty() bool {
	return len(m.Labels) == 0 && len(m.Reviewers) == 0 && len(m.Assignees) == 0
}

// splitReviewers separates team:<slug> reviewers (config.TeamPrefix) from user names.
func splitReviewers(rs []string) (users, teams []string) {
	for _, r := range rs {
		if t, ok := strings.CutPrefix(r, "team:"); ok {
			teams = append(teams, t)
		} else {
			users = append(users, r)
		}
	}
	return users, teams
}

// CheckState is the combined state of the commit statuses and check runs of a commit.
type CheckState string

const (
	ChecksNone    CheckState = "none"    // no commit status and no check run
	ChecksPending CheckState = "pending" // some have not finished, none failed
	ChecksSuccess CheckState = "success" // at least one, and all succeeded
	ChecksFailure CheckState = "failure" // at least one failed
)

// Errors the caller handles without reporting an error.
var (
	// ErrAutoMergePending means the forge cannot schedule the merge yet (Gitea/Forgejo: the
	// mergeability check of a fresh push is still running); the next run asks again.
	ErrAutoMergePending = errors.New("pull request not ready for auto-merge yet")
	// ErrAutoMergeUnavailable means the forge will not auto-merge this PR (GitHub: auto-merge
	// is disallowed in the repository, or the PR is already mergeable, "clean status"); the
	// caller may merge it directly once its checks passed.
	ErrAutoMergeUnavailable = errors.New("auto-merge not available")
	// ErrIssuesDisabled means ListIssues found no issue tracker: issues are disabled, or
	// Gitea/Forgejo use an external tracker.
	ErrIssuesDisabled = errors.New("issues are disabled in the repository")
)

// Client is the pull request and issue API of a forge.
type Client interface {
	DefaultBranch(ctx context.Context) (string, error)
	ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) // "open" | "closed"
	GetPR(ctx context.Context, number int) (PR, error)
	// CreatePR opens a PR; draft opens it as a draft (GitHub: the draft flag; Gitea and
	// Forgejo: the title gets the "WIP: " prefix; GitLab: the "Draft: " prefix).
	CreatePR(ctx context.Context, head, base, title, body string, draft bool) (PR, error)
	// UpdatePR sets the title and body of PR number; draft is whether it is a draft now, so
	// Gitea, Forgejo and GitLab keep its title prefix (GitHub keeps the draft state anyway).
	UpdatePR(ctx context.Context, number int, title, body string, draft bool) error
	ClosePR(ctx context.Context, number int) error
	// SetPRMetadata adds labels (by name), requests reviewers (users, or team:<slug> teams)
	// and adds assignees on PR number; empty lists are skipped. Each part is attempted; the
	// failures are joined.
	SetPRMetadata(ctx context.Context, number int, m PRMetadata) error
	// RemovePRLabel removes the label named label (case-insensitive on Gitea and Forgejo)
	// from PR number; a label the PR does not have is no error.
	RemovePRLabel(ctx context.Context, number int, label string) error
	// EnableAutoMerge asks the forge to merge pr with strategy (config.AutomergeStrategies;
	// auto = the repository default) once its required checks pass. It returns
	// ErrAutoMergePending or ErrAutoMergeUnavailable (wrapped) when that is not possible now;
	// a merge that is already scheduled counts as success.
	EnableAutoMerge(ctx context.Context, pr PR, strategy string) error
	// Checks combines the commit statuses and check runs of commit sha.
	Checks(ctx context.Context, sha string) (CheckState, error)
	// MergePR merges pr now with strategy, provided its head is still pr.HeadSHA.
	MergePR(ctx context.Context, pr PR, strategy string) error
	ListIssues(ctx context.Context, maxPages int) ([]Issue, error) // open issues, PRs excluded; ErrIssuesDisabled
	CreateIssue(ctx context.Context, title, body string, labels []string) (Issue, error)
	UpdateIssue(ctx context.Context, number int, body string) error
}

// Overrides replace detected Info fields; "" means auto.
type Overrides struct{ Kind, APIURL, Repo string }
