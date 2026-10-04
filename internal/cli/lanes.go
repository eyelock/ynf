package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/user"
	"slices"
	"strings"
	"text/tabwriter"
)

// pause and resume a lane (ADR-010). Both need a reason and record who gave it.
func (a *app) pause(ctx context.Context, cmd string, args []string) error {
	fs := a.flags(cmd)
	reason := fs.String("reason", "", "")
	repo := fs.String("repo", "", "")
	if len(args) == 0 || strings.HasPrefix(args[0], "-") {
		return withCode(ExitUsage, fmt.Errorf("%s needs a lane", cmd))
	}
	lane := args[0]
	if err := fs.Parse(args[1:]); err != nil {
		return withCode(ExitUsage, err)
	}
	if strings.TrimSpace(*reason) == "" {
		return withCode(ExitUsage, fmt.Errorf("%s needs --reason: stop conditions are changed deliberately", cmd))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	if *repo == "" {
		only, err := onlyRepo(ctx, e)
		if err != nil {
			return err
		}
		if only == "" {
			return withCode(ExitUsage, errors.New("--repo is needed with more than one enrolled repository"))
		}
		*repo = only
	}
	rp, err := e.Policy(ctx, *repo)
	if err != nil {
		return withCode(ExitPolicy, err)
	}
	if _, ok := rp.File.Lanes[lane]; !ok {
		return withCode(ExitPolicy, fmt.Errorf("%s has no lane %q", *repo, lane))
	}
	if err := e.SetPaused(ctx, *repo, lane, cmd == "pause", *reason, who()); err != nil {
		return err
	}
	state, _, err := e.LaneState(ctx, *repo, lane)
	if err != nil {
		return err
	}
	return a.out(state, fmt.Sprintf("%s/%s: %sd (%s)", *repo, lane, cmd, *reason))
}

func who() string {
	if u, err := user.Current(); err == nil && u.Username != "" {
		return u.Username
	}
	return os.Getenv("USER")
}

// stats prints each lane's factory numbers (ADR-011).
func (a *app) stats(ctx context.Context, args []string) error {
	fs := a.flags("stats")
	fs.Var(&a.lanes, "lane", "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	stats, err := e.Stats(ctx)
	if err != nil {
		return err
	}
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "REPO\tLANE\tITEMS\tPROPOSED\tMERGED\tREJECTED\tYIELD\tSTATUS")
	for _, s := range stats {
		total := 0
		for _, n := range s.States {
			total += n
		}
		yield := "-"
		if s.Yield >= 0 {
			yield = fmt.Sprintf("%.2f", s.Yield)
		}
		status := "running"
		if s.Paused {
			status = "paused: " + s.Reason
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%d\t%d\t%d\t%d\t%s\t%s\n", s.Repo, s.Lane, total, s.Proposed, s.Merged, s.Rejected, yield, status)
	}
	_ = w.Flush()
	for _, s := range stats {
		if len(s.Signatures) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s %s, top failure signatures:\n", s.Repo, s.Lane)
		sigs := slices.Collect(maps.Keys(s.Signatures))
		slices.SortFunc(sigs, func(x, y string) int {
			if d := s.Signatures[y] - s.Signatures[x]; d != 0 {
				return d
			}
			return strings.Compare(x, y)
		})
		for i, sig := range sigs {
			if i == 10 {
				break
			}
			fmt.Fprintf(&b, "  %4d  %s\n", s.Signatures[sig], sig)
		}
	}
	for _, s := range stats {
		if len(s.Models) == 0 {
			continue
		}
		fmt.Fprintf(&b, "\n%s %s, by model and effort:\n", s.Repo, s.Lane)
		mw := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
		_, _ = fmt.Fprintln(mw, "  MODEL\tEFFORT\tRUNS\tCONVERGED\tTURNS/RUN\tTOKENS/RUN\tCOST\tPROPOSED\tMERGED\tREJECTED")
		for _, m := range s.Models {
			effort, cost := m.Effort, "-"
			if effort == "" {
				effort = "-"
			}
			if m.CostUSD > 0 {
				cost = fmt.Sprintf("$%.2f", m.CostUSD)
			}
			_, _ = fmt.Fprintf(mw, "  %s\t%s\t%d\t%d\t%.1f\t%d\t%s\t%d\t%d\t%d\n", m.Model, effort, m.Runs, m.Converged,
				float64(m.Turns)/float64(m.Runs), m.Tokens/m.Runs, cost, m.Proposed, m.Merged, m.Rejected)
		}
		_ = mw.Flush()
	}
	return a.out(map[string]any{"lanes": stats}, b.String())
}
