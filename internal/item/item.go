// Package item is the work item: one document per unit of work (ADR-002), moved through the
// state machine by the decider (ADR-006).
package item

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// State is a work item's place in the state machine.
type State string

// States, in the order an originated item usually moves through them.
const (
	Intake      State = "intake"
	Ready       State = "ready"
	Running     State = "running"
	Proposed    State = "proposed"  // pull request open, waiting on CI
	InReview    State = "in_review" // CI green, waiting on a human
	Done        State = "done"      // merged
	Closed      State = "closed"    // ticket or pull request closed without merging
	Escalated   State = "escalated" // a human must decide
	Quarantined State = "quarantined"
	Ignored     State = "ignored"
)

// Settled reports whether ynf has nothing more to do without a human or an outside change.
func (s State) Settled() bool {
	switch s {
	case InReview, Done, Closed, Escalated, Quarantined, Ignored:
		return true
	}
	return false
}

// Final reports whether the item is finished for good.
func (s State) Final() bool { return s == Done || s == Closed || s == Ignored }

// Item is the work item document.
type Item struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"` // originate or adopt
	Lane     string `json:"lane"`
	Repo     string `json:"repo"` // owner/name
	Number   int    `json:"number"`
	State    State  `json:"state"`
	Reason   string `json:"reason,omitempty"`
	Branch   string `json:"branch,omitempty"`
	PR       int    `json:"pr,omitempty"`
	PRHead   string `json:"pr_head,omitempty"`
	Attempts int    `json:"attempts"`
	// Counters are the deterministic counters decisions read (ADR-008): reaction retries and
	// per-signature failures.
	Counters map[string]int `json:"counters,omitempty"`
	LastRun  *Run           `json:"last_run,omitempty"`
	Feedback string         `json:"feedback,omitempty"` // what the next run is told, e.g. CI output
	NextDue  *time.Time     `json:"next_due,omitempty"`
	Lease    *Lease         `json:"lease,omitempty"`
	Created  time.Time      `json:"created"`
	Updated  time.Time      `json:"updated"`
}

// Run is the last run's result, as the decider needs it.
type Run struct {
	ID       string    `json:"id"`
	Outcome  string    `json:"outcome"`
	Changed  []string  `json:"changed,omitempty"`
	Detail   string    `json:"detail,omitempty"`
	Finished time.Time `json:"finished"`
}

// Lease is an exclusive claim (ADR-005).
type Lease struct {
	Owner     string    `json:"owner"`
	Epoch     int64     `json:"epoch"`
	StepID    string    `json:"step_id"`
	Acquired  time.Time `json:"acquired_at"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Held reports whether the lease is live at now.
func (l *Lease) Held(now time.Time) bool { return l != nil && now.Before(l.ExpiresAt) }

// Counter returns a counter's value.
func (it Item) Counter(name string) int { return it.Counters[name] }

// Bump increments a counter on a copy-safe map.
func (it *Item) Bump(name string) int {
	if it.Counters == nil {
		it.Counters = map[string]int{}
	}
	it.Counters[name]++
	return it.Counters[name]
}

// IssueKey is the store key for a GitHub issue's item.
func IssueKey(repo string, number int) string {
	return fmt.Sprintf("item/github/%s/issues/%d", repo, number)
}

// IssueSubject is the event subject for a GitHub issue.
func IssueSubject(repo string, number int) string {
	return fmt.Sprintf("github:issue:%s#%d", repo, number)
}

// ParseIssueSubject is the inverse of IssueSubject.
func ParseIssueSubject(s string) (repo string, number int, err error) {
	rest, ok := strings.CutPrefix(s, "github:issue:")
	if !ok {
		return "", 0, fmt.Errorf("not an issue subject: %q", s)
	}
	repo, n, ok := strings.Cut(rest, "#")
	if !ok {
		return "", 0, fmt.Errorf("not an issue subject: %q", s)
	}
	number, err = strconv.Atoi(n)
	return repo, number, err
}

// BranchFor is the deterministic branch ynf uses for an originated item (ADR-005: idempotent
// side effects).
func BranchFor(number int) string { return fmt.Sprintf("ynf/issue-%d", number) }
