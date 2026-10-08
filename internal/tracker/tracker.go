// Package tracker is ynf's port to where work items live (ADR-003): GitHub Issues, JIRA and
// others. It is separate from the forge, where code lives, even when one system is both.
package tracker

import (
	"context"
	"errors"

	"github.com/eyelock/ynf/internal/facts"
)

// ErrNotFound means the tracker has no such item.
var ErrNotFound = errors.New("tracker: not found")

// Ref names a ticket: the tracker instance's host and the tracker's own key, such as
// {github.com, eyelock/ynh#77} or {example.atlassian.net, PLAT-881} (ADR-002).
type Ref struct {
	Host string `json:"host"`
	Key  string `json:"key"`
}

// String is the reference people and event subjects use: github.com/eyelock/ynh#77.
func (r Ref) String() string { return r.Host + "/" + r.Key }

// Text is a ticket's free text. It reaches the agent as quoted data and nothing else (NFR-5).
type Text struct {
	Title string
	Body  string
	URL   string
}

// Tracker is the port. Keys are the tracker's own, without the host.
type Tracker interface {
	// Get reads the ticket: its structured facts, and its text for the agent.
	Get(ctx context.Context, key string) (facts.Ticket, Text, error)
	// Comment posts body unless a comment containing marker exists: side effects are
	// idempotent per step (ADR-005).
	Comment(ctx context.Context, key, marker, body string) error
	// Label adds and removes labels; adding one present or removing one absent is not an error.
	Label(ctx context.Context, key string, add, remove []string) error
}
