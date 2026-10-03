// Package forge is ynf's view of the forge (ADR-003, ADR-007): searching for work, probing the
// facts a decision needs, and the writes only ynf makes. GitHub is the first provider.
package forge

import (
	"context"
	"errors"

	"github.com/eyelock/ynf/internal/facts"
)

// ErrNotFound means the forge has no such object.
var ErrNotFound = errors.New("forge: not found")

// Hit is one search result.
type Hit struct {
	Repo   string
	Number int
	IsPR   bool
}

// Text is a ticket's free text. It reaches the agent as quoted data and nothing else (NFR-5).
type Text struct {
	Title string
	Body  string
	URL   string
}

// NewPR is a pull request to open.
type NewPR struct {
	Head, Base, Title, Body string
	Draft                   bool
}

// Forge is the port the engine uses.
type Forge interface {
	Search(ctx context.Context, query string) ([]Hit, error)
	Ticket(ctx context.Context, repo string, number int) (facts.Ticket, Text, error)
	PullRequest(ctx context.Context, repo string, number int) (*facts.PR, error)
	// FindPR returns the open pull request whose head is branch, or 0.
	FindPR(ctx context.Context, repo, branch string) (int, error)
	OpenPR(ctx context.Context, repo string, pr NewPR) (int, error)
	// Comment posts body on an issue or pull request unless a comment containing marker exists:
	// side effects are idempotent per step (ADR-005).
	Comment(ctx context.Context, repo string, number int, marker, body string) error
	DefaultBranch(ctx context.Context, repo string) (string, error)
	// File reads a file at ref; ErrNotFound if it does not exist.
	File(ctx context.Context, repo, ref, path string) ([]byte, error)
}
