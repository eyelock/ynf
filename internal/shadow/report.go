package shadow

import (
	"fmt"
	"maps"
	"math"
	"slices"
	"strings"
)

// z95 is the normal quantile for a two-sided 95% interval.
const z95 = 1.959964

// Wilson is the 95% Wilson score interval for k successes in n trials. It is the interval to use
// at the sample sizes shadow mode works with: unlike the plain normal approximation it stays
// inside [0, 1] and is honest at 0 or n successes. With no trials it is the whole of [0, 1].
func Wilson(k, n int) (low, high float64) {
	if n <= 0 {
		return 0, 1
	}
	p, nn := float64(k)/float64(n), float64(n)
	z2 := z95 * z95
	denom := 1 + z2/nn
	centre := (p + z2/(2*nn)) / denom
	margin := z95 * math.Sqrt(p*(1-p)/nn+z2/(4*nn*nn)) / denom
	return math.Max(0, centre-margin), math.Min(1, centre+margin)
}

// Rate is a proportion with its 95% interval.
type Rate struct {
	Successes int     `json:"successes"`
	N         int     `json:"n"`
	Value     float64 `json:"value"`
	Low       float64 `json:"low"`
	High      float64 `json:"high"`
}

// NewRate is k out of n with its Wilson interval; nil when there is nothing to divide by.
func NewRate(k, n int) *Rate {
	if n == 0 {
		return nil
	}
	lo, hi := Wilson(k, n)
	return &Rate{Successes: k, N: n, Value: float64(k) / float64(n), Low: lo, High: hi}
}

func (r *Rate) String() string {
	if r == nil {
		return "n/a"
	}
	return fmt.Sprintf("%.2f (%d/%d, 95%% interval %.2f to %.2f)", r.Value, r.Successes, r.N, r.Low, r.High)
}

// Group is the numbers for one repository, or all of them pooled.
type Group struct {
	Repo      string         `json:"repo"` // empty for the pooled group
	Attempted int            `json:"attempted"`
	Graded    int            `json:"graded"`
	Grades    map[string]int `json:"grades,omitempty"` // grades of the agent's patches
	// Yield is (equivalent + different-valid) ÷ graded agent attempts; nil until one is graded.
	Yield *Rate `json:"yield,omitempty"`
	// UpperBound is converged-and-gate-accepted ÷ attempted: what yield could be at most, with
	// nothing graded. It is not a measured yield.
	UpperBound  *Rate `json:"upper_bound"`
	Superficial int   `json:"superficial"`
}

// GraderCheck is how the grader did on the human patches, which are known to have been merged:
// a grader who calls many of them wrong is a grader to doubt.
type GraderCheck struct {
	Graded int            `json:"graded"`
	Grades map[string]int `json:"grades,omitempty"`
	Passed *Rate          `json:"passed,omitempty"` // equivalent or different-valid
}

// Report is what `ynf shadow report` says.
type Report struct {
	Lane      string   `json:"lane"`
	Runs      []string `json:"runs"`
	Attempted int      `json:"attempted"`
	Graded    int      `json:"graded"`
	Pooled    Group    `json:"pooled"`
	Repos     []Group  `json:"repos,omitempty"`
	// Outcomes counts every attempt's run outcome.
	Outcomes map[string]int `json:"outcomes"`
	// CostUSD is the total the runner reported; CostReported how many attempts reported one.
	CostUSD      float64     `json:"cost_usd"`
	CostReported int         `json:"cost_reported"`
	CostPerRun   float64     `json:"cost_per_attempt,omitempty"`
	Pins         []PinRecord `json:"pins"`
	Grader       GraderCheck `json:"grader_check"`
	// BreakEven is y* = r ÷ h, when the lane declares the human time h and the review time r;
	// lanes declare neither yet, so it is never set and yield is not compared with it.
	BreakEven *float64 `json:"break_even,omitempty"`
	Note      string   `json:"note,omitempty"`
}

// PinRecord is one repository's pins in one shadow run.
type PinRecord struct {
	Run  string `json:"run"`
	Repo string `json:"repo"`
	Pins
}

// BuildReport computes the report for attempts (all of one lane), their grades by attempt id, and
// the runs they came from.
func BuildReport(lane string, runs []Run, attempts []Attempt, grades map[string]Grade) Report {
	rep := Report{Lane: lane, Outcomes: map[string]int{}, Attempted: len(attempts)}
	for _, r := range runs {
		rep.Runs = append(rep.Runs, r.ID)
		for _, repo := range slices.Sorted(maps.Keys(r.Pins)) {
			rep.Pins = append(rep.Pins, PinRecord{Run: r.ID, Repo: repo, Pins: r.Pins[repo]})
		}
	}
	repos := map[string][]Attempt{}
	for _, a := range attempts {
		repos[a.Repo] = append(repos[a.Repo], a)
		rep.Outcomes[a.Outcome]++
		if a.CostUSD > 0 {
			rep.CostUSD += a.CostUSD
			rep.CostReported++
		}
	}
	if rep.CostReported > 0 {
		rep.CostPerRun = rep.CostUSD / float64(rep.CostReported)
	}
	rep.Pooled = group("", attempts, grades)
	rep.Graded = rep.Pooled.Graded
	for _, repo := range slices.Sorted(maps.Keys(repos)) {
		rep.Repos = append(rep.Repos, group(repo, repos[repo], grades))
	}
	rep.Grader = GraderCheck{Grades: map[string]int{}}
	ok := 0
	for _, a := range attempts {
		g, graded := grades[a.ID]
		if !graded || g.Human == "" {
			continue
		}
		rep.Grader.Graded++
		rep.Grader.Grades[g.Human]++
		if Success(g.Human) {
			ok++
		}
	}
	rep.Grader.Passed = NewRate(ok, rep.Grader.Graded)
	return rep
}

func group(repo string, attempts []Attempt, grades map[string]Grade) Group {
	g := Group{Repo: repo, Attempted: len(attempts), Grades: map[string]int{}}
	up, ok := 0, 0
	for _, a := range attempts {
		if a.Converged() && a.GateAccepted {
			up++
		}
		gr, graded := grades[a.ID]
		if !graded {
			continue
		}
		g.Graded++
		g.Grades[gr.Agent]++
		if Success(gr.Agent) {
			ok++
		}
	}
	g.Superficial = g.Grades[Superficial]
	g.Yield = NewRate(ok, g.Graded)
	g.UpperBound = NewRate(up, g.Attempted)
	return g
}

// Text renders the report for a terminal.
func (r Report) Text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "lane %s: %d attempt(s) in %d shadow run(s), %d graded\n\n", r.Lane, r.Attempted, len(r.Runs), r.Graded)
	yield := func(g Group) {
		if g.Yield != nil {
			fmt.Fprintf(&b, "  yield          %s\n", g.Yield)
			if g.Graded < g.Attempted {
				fmt.Fprintf(&b, "                 over the %d graded of %d attempted\n", g.Graded, g.Attempted)
			}
		} else {
			fmt.Fprintf(&b, "  yield          not graded yet\n")
		}
		fmt.Fprintf(&b, "  upper bound    %s  (converged and the diff gate accepted; upper bound, not graded)\n", g.UpperBound)
	}
	b.WriteString("pooled\n")
	yield(r.Pooled)
	if len(r.Repos) > 1 || (len(r.Repos) == 1 && r.Repos[0].Repo != "") {
		for _, g := range r.Repos {
			fmt.Fprintf(&b, "\n%s\n", g.Repo)
			yield(g)
		}
	}
	b.WriteString("\ngrades of the agent's patches\n")
	if r.Pooled.Graded == 0 {
		b.WriteString("  none yet\n")
	}
	for _, g := range AllGrades {
		if n := r.Pooled.Grades[g]; n > 0 {
			fmt.Fprintf(&b, "  %-16s %d\n", g, n)
		}
	}
	if r.Pooled.Superficial > 0 {
		fmt.Fprintf(&b, "  superficial: %d of %d graded changes silence a sensor without fixing the defect\n", r.Pooled.Superficial, r.Pooled.Graded)
	}
	b.WriteString("\nrun outcomes\n")
	for _, o := range slices.Sorted(maps.Keys(r.Outcomes)) {
		fmt.Fprintf(&b, "  %-16s %d\n", o, r.Outcomes[o])
	}
	if r.CostReported > 0 {
		fmt.Fprintf(&b, "\ncost  $%.4f in all, $%.4f per attempt (%d of %d attempts reported one)\n", r.CostUSD, r.CostPerRun, r.CostReported, r.Attempted)
	}
	b.WriteString("\ngrader check: the human patches, which were merged\n")
	if r.Grader.Graded == 0 {
		b.WriteString("  none graded yet\n")
	} else {
		fmt.Fprintf(&b, "  %d of %d graded equivalent or different-valid", r.Grader.Passed.Successes, r.Grader.Graded)
		for _, g := range AllGrades {
			if n := r.Grader.Grades[g]; n > 0 {
				fmt.Fprintf(&b, "  %s %d", g, n)
			}
		}
		b.WriteString("\n")
	}
	b.WriteString("\npins\n")
	for _, p := range r.Pins {
		fmt.Fprintf(&b, "  run %s, %s\n", p.Run, p.Repo)
		fmt.Fprintf(&b, "    lane %s, policy %.12s", p.Lane, p.PolicyHash)
		if p.PolicySHA != "" {
			fmt.Fprintf(&b, " (lanes read at %.7s)", p.PolicySHA)
		}
		fmt.Fprintf(&b, "\n    runner %s, executor %s", p.Runner, p.Executor)
		if p.Image != "" {
			fmt.Fprintf(&b, ", image %s", p.Image)
		}
		b.WriteString("\n")
		o := p.Observed
		fmt.Fprintf(&b, "    harness %s (sha %.7s), ynh %s, model %s, effort %s\n", dash(o.Harness), dash(o.HarnessSHA), dash(o.Ynh), dash(firstOf(o.Model, p.Model)), dash(firstOf(o.Effort, p.Effort)))
		if len(p.Varied) > 0 {
			fmt.Fprintf(&b, "    NOT PINNED: %s differed between attempts, so this sample is more than one experiment\n", strings.Join(p.Varied, ", "))
		}
	}
	if r.BreakEven != nil {
		fmt.Fprintf(&b, "\nbreak-even y* = %.2f\n", *r.BreakEven)
	}
	b.WriteString("\nA shadow yield is an upper bound on what the lane will do on open work: the tickets that were fixed are the tractable ones.\n")
	return b.String()
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
