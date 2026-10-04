// Package item is the work item: one document per unit of work (ADR-002), moved through the
// state machine by the decider (ADR-006).
package item

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/tracker"
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

// Item is the work item document. It has two references (ADR-002): its ticket, what the work is,
// and its code, where the change goes: a forge host, a repository, and its branch and pull request.
type Item struct {
	Key    string      `json:"key"`
	Kind   string      `json:"kind"` // originate or adopt
	Lane   string      `json:"lane"`
	Ticket tracker.Ref `json:"ticket"`
	Forge  string      `json:"forge"` // the code's forge host: github.com
	Repo   string      `json:"repo"`  // owner/name on Forge
	// Number is the GitHub issue or pull request number when the ticket is one; 0 otherwise.
	Number   int    `json:"number,omitempty"`
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

// Key is the store key for an item: item/<host>/<the tracker's own key>, with a GitHub key as a
// path, so identity is the system's own and never a configured name (ADR-002).
func Key(t tracker.Ref) string {
	if repo, n, ok := githubIssue(t.Key); ok {
		return fmt.Sprintf("item/%s/%s/issues/%d", t.Host, repo, n)
	}
	return "item/" + t.Host + "/" + t.Key
}

// PRKey is the store key for an adopted pull request's item: the pull request is the work.
func PRKey(host, repo string, number int) string {
	return fmt.Sprintf("item/%s/%s/pulls/%d", host, repo, number)
}

// Subject is the event subject for an item: its ticket's reference.
func (it Item) Subject() string { return it.Ticket.String() }

// Ref is how people name the item: github.com/eyelock/ynh#77.
func (it Item) Ref() string { return it.Ticket.String() }

// BranchName is the deterministic branch ynf uses for an originated item (ADR-005: idempotent side
// effects): ynf/issue-77 for a GitHub issue, ynf/plat-881 for a key from elsewhere.
func (it Item) BranchName() string {
	if _, n, ok := githubIssue(it.Ticket.Key); ok {
		return fmt.Sprintf("ynf/issue-%d", n)
	}
	var b strings.Builder
	for _, r := range strings.ToLower(it.Ticket.Key) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return "ynf/" + strings.Trim(b.String(), "-.")
}

// githubIssue reads an owner/name#number key.
func githubIssue(key string) (repo string, number int, ok bool) {
	repo, num, found := strings.Cut(key, "#")
	if !found || strings.Count(repo, "/") != 1 {
		return "", 0, false
	}
	n, err := strconv.Atoi(num)
	return repo, n, err == nil && n > 0
}

// IssueKey is the store key for a GitHub issue's item on the forge at host.
func IssueKey(host, repo string, number int) string {
	return Key(tracker.Ref{Host: host, Key: fmt.Sprintf("%s#%d", repo, number)})
}
