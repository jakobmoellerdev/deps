// Package platform detects the code forge (GitHub, Gitea, Forgejo) and talks to its pull request API.
package platform

import (
	"context"
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
	State     string
	URL       string
	Merged    bool
	Mergeable *bool
	CreatedAt time.Time // zero when the API omits created_at
}

// Client is the pull request API of a forge.
type Client interface {
	DefaultBranch(ctx context.Context) (string, error)
	ListPRs(ctx context.Context, state string, maxPages int) ([]PR, error) // "open" | "closed"
	GetPR(ctx context.Context, number int) (PR, error)
	CreatePR(ctx context.Context, head, base, title, body string) (PR, error)
	UpdatePR(ctx context.Context, number int, title, body string) error
	ClosePR(ctx context.Context, number int) error
}

// Overrides replace detected Info fields; "" means auto.
type Overrides struct{ Kind, APIURL, Repo string }
