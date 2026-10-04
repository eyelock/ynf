// Package decide is ynf's decider (ADR-006): a pure function from (lane, item, facts, event) to
// (next item, actions). No I/O, no clock (time comes from the event), no model calls. Given the
// same inputs it returns the same decision, which is what makes `ynf replay` possible.
package decide

import (
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/eyelock/ynf/internal/event"
	"github.com/eyelock/ynf/internal/facts"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/policy"
)

// Action kinds the engine carries out. Each is idempotent per step (ADR-005).
const (
	Run        = "run"         // start the lane's runner on a fresh worktree
	OpenPR     = "open_pr"     // diff gate, commit with trailers, push, open or update the pull request
	PushCommit = "push_commit" // adopted items: diff gate, commit, push to the pull request's branch, never forced
	Escalate   = "escalate"    // tell a human, on the ticket
	Quarantine = "quarantine"  // tell a human the item was taken out of rotation
	Comment    = "comment"
	Close      = "close"
	// Label is the lane's labels for a state the item enters; the engine writes them as the item
	// moves, so they follow the decision without being one.
	Label = "label"
)

// Poll is how often waiting states are re-probed. It is an input, recorded with the decision.
type Poll struct {
	CI     time.Duration `json:"ci"`
	Review time.Duration `json:"review"`
}

// Input is everything a decision reads.
type Input struct {
	Lane  policy.Lane `json:"lane"`
	Item  item.Item   `json:"item"`
	Facts facts.Facts `json:"facts"`
	Event event.Event `json:"event"`
	Poll  Poll        `json:"poll"`
}

// Action is one thing for the engine to do.
type Action struct {
	Kind     string `json:"kind"`
	Feedback string `json:"feedback,omitempty"` // for run: what the runner is told
	Reason   string `json:"reason,omitempty"`   // for escalate, quarantine, comment
}

// Decision is the decider's output.
type Decision struct {
	Item    item.Item `json:"item"`
	Actions []Action  `json:"actions,omitempty"`
	Reason  string    `json:"reason"`
}

// Decide is the decider.
func Decide(in Input) Decision {
	d := &decider{in: in, it: clone(in.Item), now: in.Event.Time}
	d.decide()
	return Decision{Item: d.it, Actions: d.actions, Reason: d.reason}
}

type decider struct {
	in      Input
	it      item.Item
	now     time.Time
	actions []Action
	reason  string
}

func (d *decider) decide() {
	it, lane, f, ev := &d.it, d.in.Lane, d.in.Facts, d.in.Event

	if !lane.On() && !it.State.Final() {
		d.to(item.Ignored, "lane %s is switched off", lane.Name)
		return
	}
	if t := f.Ticket; t != nil && t.State == "closed" && it.PR == 0 && !it.State.Final() {
		d.to(item.Closed, "ticket closed")
		return
	}

	switch it.State {
	case "", item.Intake:
		if it.Kind == "adopt" {
			d.intakeAdopted()
			return
		}
		ok, err := policy.Guard(lane.Guards.Eligible, f.CEL(), d.itemCEL())
		switch {
		case err != nil:
			d.escalate("eligibility guard failed: %v", err)
		case !ok:
			d.to(item.Ignored, "not eligible for lane %s", lane.Name)
		default:
			d.to(item.Ready, "eligible for lane %s", lane.Name)
			d.wake(0)
		}

	case item.Ready:
		if l := f.Lane; l != nil && l.Paused {
			d.reason = fmt.Sprintf("lane %s is paused (%s); waiting", lane.Name, l.PausedReason)
			d.wake(d.in.Poll.Review)
			return
		}
		if max := lane.Stop.MaxOpenProposals; max > 0 && f.Lane != nil && f.Lane.OpenProposals >= max {
			d.reason = fmt.Sprintf("lane %s has %d proposals awaiting review (max %d); waiting", lane.Name, f.Lane.OpenProposals, max)
			d.wake(d.in.Poll.Review)
			return
		}
		d.startRun(it.Feedback, "ready")

	case item.Running:
		d.running()

	case item.Proposed, item.InReview:
		d.proposed()

	case item.Escalated, item.Quarantined, item.Done, item.Closed, item.Ignored:
		d.reason = fmt.Sprintf("%s: nothing to do without a human (%s)", it.State, ev.Type)
		it.NextDue = nil
	}
}

// intakeAdopted decides whether to take on someone else's pull request (ADR-002, ADR-007). A pull
// request that is not eligible yet (say its checks are still running) is looked at again later
// rather than ignored; a fork or a draft is never adopted.
func (d *decider) intakeAdopted() {
	it, lane, pr := &d.it, d.in.Lane, d.in.Facts.PR
	switch {
	case pr == nil:
		d.escalate("pull request #%d is gone", it.PR)
		return
	case pr.Merged || pr.State == "closed":
		d.to(item.Closed, "#%d is no longer open", pr.Number)
		return
	case pr.Fork:
		d.to(item.Ignored, "#%d comes from a fork; ynf does not push to forks", pr.Number)
		return
	case pr.Draft:
		it.State = item.Intake
		d.reason = fmt.Sprintf("#%d is a draft; waiting", pr.Number)
		d.wake(d.in.Poll.Review)
		return
	}
	ok, err := policy.Guard(lane.Guards.Eligible, d.in.Facts.CEL(), d.itemCEL())
	switch {
	case err != nil:
		d.escalate("eligibility guard failed: %v", err)
	case !ok:
		it.State = item.Intake
		d.reason = fmt.Sprintf("#%d is not eligible for lane %s yet", pr.Number, lane.Name)
		d.wake(d.in.Poll.Review)
	default:
		it.Branch, it.PRHead = pr.HeadRef, pr.HeadSHA
		feedback := ""
		if failed := pr.Failed(); len(failed) > 0 {
			feedback = "These checks are failing on the pull request: " + strings.Join(failed, ", ")
		}
		it.Feedback = feedback
		d.to(item.Ready, "adopting #%d (%s at %.7s)", pr.Number, pr.HeadRef, pr.HeadSHA)
		d.wake(0)
	}
}

func (d *decider) running() {
	it, lane, ev := &d.it, d.in.Lane, d.in.Event
	switch ev.Type {
	case event.RunFinished:
		it.Attempts = 0
		it.LastRun = &item.Run{
			ID: ev.Str("run_id"), Outcome: ev.Str("outcome"), Detail: ev.Str("detail"),
			Changed: stringList(ev.Data["changed"]), Finished: d.now,
		}
		for _, host := range stringList(ev.Data["denied"]) {
			it.Bump("sig/egress/denied/" + host)
		}
		outcome := it.LastRun.Outcome
		if outcome == "converged" {
			if len(it.LastRun.Changed) == 0 {
				d.escalate("the run converged without changing anything")
				return
			}
			d.react("converged", OpenPR, "")
			return
		}
		d.bumpRunSignatures(outcome)
		d.react("outcome."+outcome, Escalate, fmt.Sprintf("The previous run ended %s: %s", outcome, it.LastRun.Detail))

	case event.ActionDone:
		action := ev.Str("action")
		if action != OpenPR && action != PushCommit {
			d.reason = "action " + action + " done"
			return
		}
		if !ev.Bool("ok") {
			if ev.Bool("head_moved") {
				// The author pushed while ynf worked: start again from their new head.
				if n := it.Bump("head_moved"); n > lane.Attempts {
					d.escalate("the pull request's head kept moving (%d times)", n)
					return
				}
				d.to(item.Ready, "the head moved while ynf worked; starting again from the new head")
				d.wake(0)
				return
			}
			d.escalate("could not propose the change: %s", ev.Str("reason"))
			return
		}
		if action == OpenPR {
			it.PR = int(num(ev.Data["pr"]))
			it.Branch = ev.Str("branch")
		}
		it.PRHead = ev.Str("head")
		it.Feedback = ""
		d.to(item.Proposed, "proposed as #%d", it.PR)
		d.wake(d.in.Poll.CI)

	case event.TimerDue:
		// A run was started and never reported: the holder died. Restart, never resume (ADR-005).
		if it.Attempts >= lane.Attempts {
			d.quarantine("%d attempts without a finished run", it.Attempts)
			return
		}
		d.startRun(it.Feedback, "the last attempt did not finish")

	default:
		d.reason = "running; " + ev.Type + " changes nothing"
	}
}

func (d *decider) proposed() {
	it, f := &d.it, d.in.Facts
	pr := f.PR
	switch {
	case pr == nil:
		d.escalate("pull request #%d is gone", it.PR)
		return
	case pr.Merged:
		d.to(item.Done, "#%d merged", pr.Number)
		return
	case pr.State == "closed":
		d.to(item.Closed, "#%d closed without merging", pr.Number)
		return
	case pr.ChangesRequested && it.State == item.InReview:
		d.react("changes_requested", Escalate, "A reviewer requested changes on the pull request.")
		return
	}
	it.PRHead = pr.HeadSHA
	switch pr.CIState() {
	case "pending":
		d.reason = fmt.Sprintf("#%d: CI pending", pr.Number)
		d.wake(d.in.Poll.CI)
	case "success":
		if it.State != item.InReview {
			d.to(item.InReview, "#%d: CI green", pr.Number)
		} else {
			d.reason = fmt.Sprintf("#%d: in review", pr.Number)
		}
		d.wake(d.in.Poll.Review)
	case "failure":
		// CI failed on a change whose last run converged: the harness's sensors passed and the real
		// gate did not. Only ynf sees that, so it is its own signature, per check. A pull request
		// ynf's run never converged on (an adopted one) fails plainly.
		kind := "ci"
		if it.LastRun != nil && it.LastRun.Outcome == "converged" {
			kind = "ci-diverges"
		}
		for _, check := range sortedUnique(pr.Failed()) {
			it.Bump(signature(kind, check))
		}
		d.react("ci_failed", Escalate, "CI failed on the pull request: "+strings.Join(pr.Failed(), ", "))
	}
}

// bumpRunSignatures counts why a run that did not converge failed. Each fact the runner reported
// is its own signature: the budget that bound the run, with the harness it ran, and every sensor
// still failing. A run with neither falls back to sig/outcome/<outcome>, so nothing goes uncounted,
// and only then: a failure is never counted under both a specific signature and the fallback.
// Events recorded before these facts existed carry none of them, and so fall back as they always did.
func (d *decider) bumpRunSignatures(outcome string) {
	it, ev := &d.it, d.in.Event
	specific := false
	if bound := ev.Str("bound_by"); bound != "" {
		if h := ev.Str("harness"); h != "" {
			it.Bump(signature("budget", bound, "harness:"+h))
		} else {
			it.Bump(signature("budget", bound))
		}
		specific = true
	}
	for _, name := range sortedUnique(stringList(ev.Data["failed_sensors"])) {
		it.Bump(signature("stuck", "sensor:"+name))
		specific = true
	}
	if !specific {
		it.Bump(signature("outcome", outcome))
	}
}

// react applies the lane's reaction to key, or def if the lane has none.
func (d *decider) react(key, def, feedback string) {
	r, ok := d.in.Lane.When[key]
	if !ok {
		r = policy.Reaction{Action: def}
	}
	switch {
	case r.ResumeWith != "":
		if n := d.it.Bump("resume/" + key); r.Max > 0 && n > r.Max {
			d.act(orDefault(r.Then, Escalate), "%s %d times", key, n)
			return
		}
		d.startRun(feedback, key+": resuming with "+r.ResumeWith)
	case r.Retry > 0:
		if n := d.it.Bump("retry/" + key); n <= r.Retry {
			d.startRun(feedback, fmt.Sprintf("%s: retry %d of %d", key, n, r.Retry))
			return
		}
		d.act(orDefault(r.Then, Escalate), "%s after %d retries", key, r.Retry)
	default:
		d.act(r.Action, "%s", key)
	}
}

func (d *decider) act(action, format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	switch action {
	case OpenPR, PushCommit:
		if (action == PushCommit) != (d.it.Kind == "adopt") {
			d.escalate("%s: %s is for %s items, and this one is %s", reason, action, map[string]string{OpenPR: "originated", PushCommit: "adopted"}[action], orDefault(d.it.Kind, "originate"))
			return
		}
		d.reason = reason + ": proposing the change"
		d.actions = append(d.actions, Action{Kind: action})
		d.it.NextDue = nil
	case Quarantine:
		d.quarantine("%s", reason)
	case Close:
		d.to(item.Closed, "%s", reason)
		d.actions = append(d.actions, Action{Kind: Close, Reason: reason})
	case Comment:
		d.reason = reason
		d.actions = append(d.actions, Action{Kind: Comment, Reason: reason})
	default: // escalate, and anything not carried out yet (request_review)
		if action != Escalate {
			reason = fmt.Sprintf("%s (%s is not supported yet)", reason, action)
		}
		d.escalate("%s", reason)
	}
}

func (d *decider) startRun(feedback, why string) {
	d.it.Attempts++
	d.it.Feedback = feedback
	d.to(item.Running, "%s: starting run (attempt %d)", why, d.it.Attempts)
	d.actions = append(d.actions, Action{Kind: Run, Feedback: feedback})
	// If no run.finished arrives by then, the holder died and the next sweep restarts the run.
	d.wake(2 * time.Hour)
}

func (d *decider) escalate(format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	d.to(item.Escalated, "%s", reason)
	d.actions = append(d.actions, Action{Kind: Escalate, Reason: reason})
	d.it.NextDue = nil
}

func (d *decider) quarantine(format string, args ...any) {
	reason := fmt.Sprintf(format, args...)
	d.to(item.Quarantined, "%s", reason)
	d.actions = append(d.actions, Action{Kind: Quarantine, Reason: reason})
	d.it.NextDue = nil
}

func (d *decider) to(s item.State, format string, args ...any) {
	d.it.State = s
	d.it.Reason = fmt.Sprintf(format, args...)
	d.reason = d.it.Reason
	if s.Settled() && s != item.InReview {
		d.it.NextDue = nil
	}
}

func (d *decider) wake(after time.Duration) {
	t := d.now.Add(after)
	d.it.NextDue = &t
}

func (d *decider) itemCEL() map[string]any {
	c := map[string]any{}
	for k, v := range d.it.Counters {
		c[k] = v
	}
	return map[string]any{"state": string(d.it.State), "attempts": d.it.Attempts, "lane": d.it.Lane, "counters": c}
}

func clone(it item.Item) item.Item {
	it.Counters = maps.Clone(it.Counters)
	if it.NextDue != nil {
		t := *it.NextDue
		it.NextDue = &t
	}
	if it.LastRun != nil {
		r := *it.LastRun
		it.LastRun = &r
	}
	return it
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func num(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func stringList(v any) []string {
	switch l := v.(type) {
	case []string:
		return l
	case []any:
		out := make([]string, 0, len(l))
		for _, s := range l {
			if str, ok := s.(string); ok {
				out = append(out, str)
			}
		}
		return out
	}
	return nil
}
