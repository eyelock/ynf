// Package forge is ynf's port to where code lives (ADR-003, ADR-007): pull requests, their checks
// and reviews, branches and files, and the writes only ynf makes. GitHub is the first provider.
// Where work items live is the tracker port; a forge that also tracks issues, as GitHub does,
// serves it through IssueTracker.
package forge

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/tracker"
)

// ErrNotFound means the forge has no such object.
var ErrNotFound = errors.New("forge: not found")

// Hit is one search result.
type Hit struct {
	Repo   string
	Number int
	IsPR   bool
}

// Text is a ticket's free text (tracker.Text).
type Text = tracker.Text

// NewPR is a pull request to open.
type NewPR struct {
	Head, Base, Title, Body string
	Draft                   bool
}

// Forge is the port the engine uses.
type Forge interface {
	Search(ctx context.Context, query string) ([]Hit, error)
	PullRequest(ctx context.Context, repo string, number int) (*facts.PR, error)
	// FindPR returns the open pull request whose head is branch, or 0.
	FindPR(ctx context.Context, repo, branch string) (int, error)
	OpenPR(ctx context.Context, repo string, pr NewPR) (int, error)
	// Comment posts body on a pull request unless a comment containing marker exists: side
	// effects are idempotent per step (ADR-005).
	Comment(ctx context.Context, repo string, number int, marker, body string) error
	DefaultBranch(ctx context.Context, repo string) (string, error)
	// Head is the commit a branch points at, so policy is read at a resolved commit (ADR-006).
	Head(ctx context.Context, repo, branch string) (string, error)
	// File reads a file at ref; ErrNotFound if it does not exist.
	File(ctx context.Context, repo, ref, path string) ([]byte, error)
}

// Issues is what a forge that also tracks issues offers, keyed by repository and number.
type Issues interface {
	Ticket(ctx context.Context, repo string, number int) (facts.Ticket, Text, error)
	Comment(ctx context.Context, repo string, number int, marker, body string) error
	SetLabels(ctx context.Context, repo string, number int, add, remove []string) error
}

// IssueTracker serves a forge's issues through the tracker port. Keys are owner/name#number;
// a pull request is an issue here too, so an adopted pull request is its own ticket.
func IssueTracker(i Issues) tracker.Tracker { return issueTracker{i} }

type issueTracker struct{ i Issues }

func (t issueTracker) Get(ctx context.Context, key string) (facts.Ticket, Text, error) {
	repo, n, err := ParseIssueKey(key)
	if err != nil {
		return facts.Ticket{}, Text{}, err
	}
	ft, text, err := t.i.Ticket(ctx, repo, n)
	if errors.Is(err, ErrNotFound) {
		err = tracker.ErrNotFound
	}
	ft.Key = key
	return ft, text, err
}

func (t issueTracker) Comment(ctx context.Context, key, marker, body string) error {
	repo, n, err := ParseIssueKey(key)
	if err != nil {
		return err
	}
	return t.i.Comment(ctx, repo, n, marker, body)
}

func (t issueTracker) Label(ctx context.Context, key string, add, remove []string) error {
	repo, n, err := ParseIssueKey(key)
	if err != nil {
		return err
	}
	return t.i.SetLabels(ctx, repo, n, add, remove)
}

// IssueKey is an issue's tracker key: owner/name#number.
func IssueKey(repo string, number int) string { return fmt.Sprintf("%s#%d", repo, number) }

// ParseIssueKey is the inverse of IssueKey.
func ParseIssueKey(key string) (repo string, number int, err error) {
	repo, num, ok := strings.Cut(key, "#")
	if ok && strings.Count(repo, "/") == 1 {
		if number, err = strconv.Atoi(num); err == nil && number > 0 {
			return repo, number, nil
		}
	}
	return "", 0, fmt.Errorf("%q is not an issue key (owner/name#number)", key)
}
