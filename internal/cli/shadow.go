package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/shadow"
)

// shadowCmd is shadow mode (FR-26, ADR-011): measure a lane's yield against closed tickets whose
// answer is known, proposing nothing.
func (a *app) shadowCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return withCode(ExitUsage, errors.New("shadow run|ls|grade|report"))
	}
	switch args[0] {
	case "run":
		return a.shadowRun(ctx, args[1:])
	case "ls":
		return a.shadowLs(ctx, args[1:])
	case "grade":
		return a.shadowGrade(ctx, args[1:])
	case "report":
		return a.shadowReport(ctx, args[1:])
	}
	return withCode(ExitUsage, fmt.Errorf("shadow %q: want run, ls, grade or report", args[0]))
}

// positional splits a leading argument that is not a flag from the flags after it, so a command
// reads `grade <id> --attempt x` and `grade --attempt x <id>` alike.
func positional(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func (a *app) shadowRun(ctx context.Context, args []string) error {
	lane, args := positional(args)
	fs := a.flags("shadow run")
	var repos, tickets multi
	fs.Var(&repos, "repo", "")
	fs.Var(&tickets, "ticket", "")
	sinceArg := fs.String("since", "90d", "")
	limit := fs.Int("limit", 20, "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	if lane == "" && fs.NArg() == 1 {
		lane = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return withCode(ExitUsage, fmt.Errorf("shadow run takes one lane, not %v", fs.Args()))
	}
	if lane == "" {
		return withCode(ExitUsage, errors.New("shadow run needs a lane"))
	}
	since, err := policy.ParseDuration(*sinceArg)
	if err != nil || since <= 0 {
		return withCode(ExitUsage, fmt.Errorf("--since %q: want a duration such as 90d or 720h", *sinceArg))
	}
	if *limit <= 0 {
		return withCode(ExitUsage, fmt.Errorf("--limit %d: want at least 1", *limit))
	}
	a.interactive = true // the process executor is allowed for shadow mode (ADR-007)
	e, err := a.engine()
	if err != nil {
		return err
	}
	req := engine.ShadowRequest{Lane: lane, Since: since, Limit: *limit}
	for _, r := range repos {
		repo, err := forgeRepo(r, e.ForgeHost)
		if err != nil || repo == "" {
			return withCode(ExitUsage, fmt.Errorf("--repo %q is not owner/name", r))
		}
		req.Repos = append(req.Repos, repo)
	}
	for _, t := range tickets {
		ref, err := parseRef(t, e.ForgeHost, trackerNames(ctx, e))
		if err != nil {
			return withCode(ExitUsage, err)
		}
		req.Tickets = append(req.Tickets, ref)
	}
	run, err := e.ShadowRun(ctx, req)
	var refused *engine.RefusedError
	if errors.As(err, &refused) {
		return withCode(ExitUsage, err)
	}
	if err != nil && run.ID == "" {
		return err
	}
	// The attempts are named, so a script can grade them.
	type attemptRow struct {
		ID      string `json:"id"`
		Ticket  string `json:"ticket"`
		FixPR   int    `json:"fix_pr"`
		Outcome string `json:"outcome"`
	}
	done := struct {
		shadow.Run
		Attempts []attemptRow `json:"attempts"`
	}{Run: run, Attempts: []attemptRow{}}
	as, aerr := shadow.Attempts(ctx, e.Store, run.ID)
	if aerr != nil {
		return aerr
	}
	var b strings.Builder
	fmt.Fprintf(&b, "shadow run %s: lane %s, %d candidate(s), %d attempted, %d skipped\n", run.ID, run.Lane, run.Candidates, run.Attempted, len(run.Skipped))
	for _, at := range as {
		done.Attempts = append(done.Attempts, attemptRow{at.ID, at.Ticket, at.FixPR, at.Outcome})
		fmt.Fprintf(&b, "  attempt %s: %s against the fix in #%d, %s\n", at.ID, at.Ticket, at.FixPR, at.Outcome)
	}
	for _, s := range run.Skipped {
		fmt.Fprintf(&b, "  skipped %s: %s\n", s.Ticket, s.Reason)
	}
	if run.Stopped != "" {
		fmt.Fprintf(&b, "stopped: %s\n", run.Stopped)
	}
	if run.Attempted > 0 {
		fmt.Fprintf(&b, "grade the attempts blind with: ynf shadow grade %s\n", run.ID)
	}
	if oerr := a.out(done, b.String()); oerr != nil {
		return oerr
	}
	return err
}

func (a *app) shadowLs(ctx context.Context, args []string) error {
	if len(args) > 0 {
		return withCode(ExitUsage, errors.New("shadow ls takes no arguments"))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	runs, err := shadow.Runs(ctx, e.Store)
	if err != nil {
		return err
	}
	type row struct {
		ID         string    `json:"id"`
		Lane       string    `json:"lane"`
		Created    time.Time `json:"created"`
		Candidates int       `json:"candidates"`
		Attempted  int       `json:"attempted"`
		Graded     int       `json:"graded"`
	}
	rows := []row{}
	var b strings.Builder
	tw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "ID\tLANE\tWHEN\tCANDIDATES\tATTEMPTED\tGRADED")
	for _, r := range runs {
		n, err := shadow.Graded(ctx, e.Store, r.ID)
		if err != nil {
			return err
		}
		rows = append(rows, row{r.ID, r.Lane, r.Created, r.Candidates, r.Attempted, n})
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\t%d\n", r.ID, r.Lane, r.Created.Format("2006-01-02 15:04"), r.Candidates, r.Attempted, n)
	}
	_ = tw.Flush()
	if len(runs) == 0 {
		b.Reset()
		b.WriteString("no shadow runs")
	}
	return a.out(rows, b.String())
}

// pickRun is the run named, or the latest one.
func pickRun(ctx context.Context, e *engine.Engine, id string) (shadow.Run, error) {
	if id != "" {
		r, err := shadow.LoadRun(ctx, e.Store, id)
		if err != nil {
			return r, withCode(ExitUsage, err)
		}
		return r, nil
	}
	runs, err := shadow.Runs(ctx, e.Store)
	if err != nil {
		return shadow.Run{}, err
	}
	if len(runs) == 0 {
		return shadow.Run{}, withCode(ExitUsage, errors.New("no shadow runs; start one with ynf shadow run <lane>"))
	}
	return runs[len(runs)-1], nil
}

func (a *app) shadowGrade(ctx context.Context, args []string) error {
	id, args := positional(args)
	fs := a.flags("shadow grade")
	attempt := fs.String("attempt", "", "")
	gradeA := fs.String("a", "", "")
	gradeB := fs.String("b", "", "")
	regrade := fs.Bool("regrade", false, "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return withCode(ExitUsage, fmt.Errorf("shadow grade takes one shadow run id, not %v", fs.Args()))
	}
	scripted := *attempt != ""
	if scripted != (*gradeA != "" && *gradeB != "") || (!scripted && (*gradeA != "" || *gradeB != "")) {
		return withCode(ExitUsage, errors.New("the scripted form is --attempt <id> --a <grade> --b <grade>, all three"))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	run, err := pickRun(ctx, e, id)
	if err != nil {
		return err
	}
	attempts, err := shadow.Attempts(ctx, e.Store, run.ID)
	if err != nil {
		return err
	}
	g := grader{ctx: ctx, e: e, run: run, regrade: *regrade, out: a.stdout, by: os.Getenv("USER")}
	if err := g.autoGrade(attempts); err != nil {
		return err
	}
	if scripted {
		i := slices.IndexFunc(attempts, func(x shadow.Attempt) bool { return x.ID == *attempt })
		if i < 0 {
			return withCode(ExitUsage, fmt.Errorf("shadow run %s has no attempt %s", run.ID, *attempt))
		}
		done, err := g.record(attempts[i], *gradeA, *gradeB)
		if err != nil {
			return withCode(ExitUsage, err)
		}
		return a.out(done, fmt.Sprintf("%s graded: the agent's patch %s, the human's %s", attempts[i].Ticket, done.Agent, gradeOrNone(done.Human)))
	}
	if a.stdin == nil || !isTerminal(a.stdin) {
		return withCode(ExitUsage, errors.New("grading is by a person at a terminal; scripts use --attempt <id> --a <grade> --b <grade>"))
	}
	return g.interactive(a.stdin, attempts)
}

// grader grades attempts of one shadow run.
type grader struct {
	ctx     context.Context
	e       *engine.Engine
	run     shadow.Run
	regrade bool
	out     io.Writer
	by      string
}

func gradeOrNone(s string) string {
	if s == "" {
		return "not graded"
	}
	return s
}

// autoGrade grades an ungraded attempt that left no patch as wrong, without asking anyone: there
// is nothing to show a grader, and an agent that changed nothing did not fix the ticket.
func (g grader) autoGrade(attempts []shadow.Attempt) error {
	for _, at := range attempts {
		if strings.TrimSpace(at.AgentPatch) != "" {
			continue
		}
		if _, ok, err := shadow.LoadGrade(g.ctx, g.e.Store, g.run.ID, at.ID); err != nil || ok {
			if err != nil {
				return err
			}
			continue
		}
		if err := shadow.SaveGrade(g.ctx, g.e.Store, g.run.ID, shadow.AutoGrade(at.ID, at.AgentIsA, g.e.Now()), false); err != nil {
			return err
		}
		say(g.out, "%s: no patch, graded wrong automatically\n", at.Ticket)
	}
	return nil
}

// record stores a grader's grades for patches A and B, in the attempt's recorded order.
func (g grader) record(at shadow.Attempt, a, b string) (shadow.Grade, error) {
	gr, err := shadow.NewGrade(at.ID, at.AgentIsA, a, b, g.e.Now())
	if err != nil {
		return gr, err
	}
	gr.By = g.by
	if strings.TrimSpace(at.AgentPatch) == "" {
		return gr, fmt.Errorf("%s left no patch and was graded wrong automatically; there is nothing to grade", at.Ticket)
	}
	return gr, shadow.SaveGrade(g.ctx, g.e.Store, g.run.ID, gr, g.regrade)
}

// interactive walks the attempts still to grade: the ticket, then the two patches as A and B in
// their recorded order, each graded before anything says which was the agent's. What the run did
// is shown only after both grades are in.
func (g grader) interactive(in io.Reader, attempts []shadow.Attempt) error {
	var todo []shadow.Attempt
	for _, at := range attempts {
		if strings.TrimSpace(at.AgentPatch) == "" {
			continue
		}
		if _, ok, err := shadow.LoadGrade(g.ctx, g.e.Store, g.run.ID, at.ID); err != nil {
			return err
		} else if ok && !g.regrade {
			continue
		}
		todo = append(todo, at)
	}
	if len(todo) == 0 {
		say(g.out, "nothing to grade\n")
		return nil
	}
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	ask := func(which string) (string, bool) {
		for {
			say(g.out, "grade %s [1 %s, 2 %s, 3 %s, 4 %s, 5 %s, q to stop]: ", which, shadow.Equivalent, shadow.DifferentOK, shadow.Superficial, shadow.Wrong, shadow.DoesNotBuild)
			if !sc.Scan() {
				return "", false
			}
			s := strings.TrimSpace(sc.Text())
			if s == "q" || s == "quit" {
				return "", false
			}
			if n, err := strconv.Atoi(s); err == nil && n >= 1 && n <= len(shadow.AllGrades) {
				return shadow.AllGrades[n-1], true
			}
			if shadow.ValidGrade(s) {
				return s, true
			}
			say(g.out, "%q is not a grade\n", s)
		}
	}
	for i, at := range todo {
		pa, pb := at.Blind()
		say(g.out, "\n=== attempt %d of %d: %s ===\n\n%s\n\n%s\n", i+1, len(todo), at.Ticket, strings.TrimSpace(at.Title), strings.TrimSpace(at.Body))
		say(g.out, "\n--- patch A ---\n%s\n--- patch B ---\n%s\n", strings.TrimRight(pa, "\n"), strings.TrimRight(pb, "\n"))
		ga, ok := ask("A")
		if !ok {
			return nil
		}
		gb, ok := ask("B")
		if !ok {
			return nil
		}
		gr, err := g.record(at, ga, gb)
		if err != nil {
			return err
		}
		agent, human := "A", "B"
		if !at.AgentIsA {
			agent, human = "B", "A"
		}
		say(g.out, "\n%s was the agent's patch (%s), %s the human's (fix pull request #%d). Graded: agent %s, human %s.\n", agent, at.Outcome, human, at.FixPR, gr.Agent, gr.Human)
		if at.Outcome != "converged" {
			say(g.out, "The run did not converge: %s\n", oneLineText(at.Detail))
		}
	}
	return nil
}

func oneLineText(s string) string { return strings.Join(strings.Fields(s), " ") }

// isTerminal reports whether r is a terminal, where a person can grade.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	fi, err := f.Stat()
	if err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	// /dev/null is a character device too, and nobody is there to grade.
	null, err := os.Stat(os.DevNull)
	return err != nil || !os.SameFile(fi, null)
}

func (a *app) shadowReport(ctx context.Context, args []string) error {
	id, args := positional(args)
	fs := a.flags("shadow report")
	lane := fs.String("lane", "", "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	if id == "" && fs.NArg() == 1 {
		id = fs.Arg(0)
	} else if fs.NArg() > 0 {
		return withCode(ExitUsage, fmt.Errorf("shadow report takes one shadow run id, not %v", fs.Args()))
	}
	if id != "" && *lane != "" {
		return withCode(ExitUsage, errors.New("shadow report takes a shadow run id or --lane, not both"))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	var runs []shadow.Run
	if *lane != "" {
		all, err := shadow.Runs(ctx, e.Store)
		if err != nil {
			return err
		}
		for _, r := range all {
			if r.Lane == *lane {
				runs = append(runs, r)
			}
		}
		if len(runs) == 0 {
			return withCode(ExitUsage, fmt.Errorf("no shadow runs of lane %s", *lane))
		}
	} else {
		r, err := pickRun(ctx, e, id)
		if err != nil {
			return err
		}
		runs, *lane = []shadow.Run{r}, r.Lane
	}
	var attempts []shadow.Attempt
	grades := map[string]shadow.Grade{}
	for _, r := range runs {
		as, err := shadow.Attempts(ctx, e.Store, r.ID)
		if err != nil {
			return err
		}
		for _, at := range as {
			if g, ok, err := shadow.LoadGrade(ctx, e.Store, r.ID, at.ID); err != nil {
				return err
			} else if ok {
				grades[at.ID] = g
			}
		}
		attempts = append(attempts, as...)
	}
	rep := shadow.BuildReport(*lane, runs, attempts, grades)
	return a.out(rep, rep.Text())
}

// say writes to the person grading; a terminal that cannot be written to has nobody to tell.
func say(w io.Writer, format string, args ...any) { _, _ = fmt.Fprintf(w, format, args...) }
