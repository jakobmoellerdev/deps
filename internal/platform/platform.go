// Package platform detects the code forge (GitHub, Gitea, Forgejo) and talks to its pull request API.
package platform

import (
	"context"
	"errors"
	"time"
)

// Kind is the forge type.
type Kind string

const (
	GitHub  Kind = "github"
	Gitea   Kind = "gitea"
	Forgejo Kind = "forgejo"
)

// Info describes the repository's forge.
type Info struct {
	Kind      Kind
	ServerURL string
	APIURL    string
	Host      string
	Repo      string // "owner/name"
}

// PR is the subset of pull request fields deps uses.
type PR struct {
	Number    int
	Title     string
	Body      string
	Head      string
	HeadSHA   string // head.sha
	State     string
	URL       string
	Merged    bool
	Mergeable *bool
	CreatedAt time.Time // zero when the API omits created_at
	NodeID    string    // GitHub GraphQL node id (node_id); "" on Gitea and Forgejo
	// AutoMerge reports auto-merge as already enabled (GitHub auto_merge non-null). Gitea and
	// Forgejo do not report it, so it is always false there and requests are idempotent.
	AutoMerge bool
}

// Issue is the subset of issue fields deps uses.
type Issue struct {
	Number int
	Title  string
	Body   string
	URL    string
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
	CreatePR(ctx context.Context, head, base, title, body string) (PR, error)
	UpdatePR(ctx context.Context, number int, title, body string) error
	ClosePR(ctx context.Context, number int) error
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
