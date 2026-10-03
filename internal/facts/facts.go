// Package facts is the structured snapshot of the world a decision reads (ADR-003: probed fresh
// before every decision, never trusted from an event). It holds labels, states, counts and check
// conclusions, never free text, so a ticket body cannot steer a decision (NFR-5).
package facts

// Facts is one consistent snapshot, recorded with the decision so it can be replayed.
type Facts struct {
	Ticket *Ticket `json:"ticket,omitempty"`
	PR     *PR     `json:"pr,omitempty"`
}

// Ticket is an issue's structured state.
type Ticket struct {
	Number int      `json:"number"`
	State  string   `json:"state"` // open, closed
	Labels []string `json:"labels"`
}

// PR is a pull request's structured state.
type PR struct {
	Number           int     `json:"number"`
	State            string  `json:"state"` // open, closed
	Merged           bool    `json:"merged"`
	Draft            bool    `json:"draft"`
	Fork             bool    `json:"fork"`
	HeadSHA          string  `json:"head_sha"`
	Checks           []Check `json:"checks"`
	ChangesRequested bool    `json:"changes_requested"`
	Approved         bool    `json:"approved"`
}

// Check is one check run or commit status on the pull request's head.
type Check struct {
	Name       string `json:"name"`
	Status     string `json:"status"`     // queued, in_progress, completed
	Conclusion string `json:"conclusion"` // success, failure, neutral, cancelled, skipped, timed_out, action_required, ""
	Required   bool   `json:"required"`
}

// CIState summarises the checks that gate the pull request: required ones if any are marked,
// otherwise all of them.
func (p PR) CIState() string {
	gating := make([]Check, 0, len(p.Checks))
	for _, c := range p.Checks {
		if c.Required {
			gating = append(gating, c)
		}
	}
	if len(gating) == 0 {
		gating = p.Checks
	}
	if len(gating) == 0 {
		return "pending" // nothing has reported yet
	}
	state := "success"
	for _, c := range gating {
		switch {
		case c.Status != "completed":
			state = "pending"
		case c.Conclusion == "success" || c.Conclusion == "neutral" || c.Conclusion == "skipped":
		default:
			return "failure"
		}
	}
	return state
}

// Failed lists the gating checks that failed.
func (p PR) Failed() []string {
	var out []string
	for _, c := range p.Checks {
		if c.Status == "completed" && c.Conclusion != "success" && c.Conclusion != "neutral" && c.Conclusion != "skipped" {
			out = append(out, c.Name)
		}
	}
	return out
}

// CEL is the facts as the guard environment sees them.
func (f Facts) CEL() map[string]any {
	m := map[string]any{}
	if t := f.Ticket; t != nil {
		m["ticket"] = map[string]any{"number": t.Number, "state": t.State, "labels": strs(t.Labels)}
	}
	if p := f.PR; p != nil {
		checks := make([]any, len(p.Checks))
		for i, c := range p.Checks {
			checks[i] = map[string]any{"name": c.Name, "status": c.Status, "conclusion": c.Conclusion, "required": c.Required}
		}
		m["pr"] = map[string]any{
			"number": p.Number, "state": p.State, "merged": p.Merged, "draft": p.Draft, "fork": p.Fork,
			"checks": checks, "changes_requested": p.ChangesRequested, "approved": p.Approved, "ci": p.CIState(),
		}
	}
	return m
}

func strs(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}
