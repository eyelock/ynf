package cli

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/eyelock/ynf/internal/engine"
)

// connections lists the forges or trackers the factory works with, each checked (ADR-011).
func (a *app) connections(ctx context.Context, kind string, args []string) error {
	if len(args) > 0 {
		return withCode(ExitUsage, fmt.Errorf("%ss takes no arguments", kind))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	all, err := e.Connections(ctx)
	if err != nil {
		return err
	}
	var list []engine.Connection
	var b strings.Builder
	ok := true
	for _, c := range all {
		if c.Kind != kind {
			continue
		}
		list = append(list, c)
		ok = ok && c.OK
		fmt.Fprintf(&b, "%s  %-12s %-8s %-32s %s\n", mark(c.OK), c.Name, c.Provider, c.Host, c.Detail)
	}
	if err := a.out(list, b.String()); err != nil {
		return err
	}
	if !ok {
		return withCode(ExitAdapter, fmt.Errorf("a %s cannot be reached", kind))
	}
	return nil
}

// ticket reads a ticket through its tracker without starting anything: whether ynf can see it,
// and what a run would be given.
func (a *app) ticket(ctx context.Context, args []string) error {
	if len(args) != 1 {
		return withCode(ExitUsage, errors.New("ticket <ref>: owner/repo#n, host/owner/repo#n, or <tracker>/<key>"))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	ref, err := parseRef(args[0], e.ForgeHost, trackerNames(ctx, e))
	if err != nil {
		return withCode(ExitUsage, err)
	}
	t, text, err := e.Ticket(ctx, ref)
	if err != nil {
		return fmt.Errorf("%s: %w", ref, err)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s  %s\n", ref, text.Title)
	fmt.Fprintf(&b, "state   %s\n", t.State)
	if len(t.Labels) > 0 {
		fmt.Fprintf(&b, "labels  %s\n", strings.Join(t.Labels, ", "))
	}
	if t.Repo != "" {
		fmt.Fprintf(&b, "repo    %s\n", t.Repo)
	}
	if text.Body != "" {
		fmt.Fprintf(&b, "\n%s\n", text.Body)
	}
	return a.out(map[string]any{"ref": ref, "ticket": t, "title": text.Title, "body": text.Body}, b.String())
}

// harness shows how each of a repository's lanes runs, and the harness it is held to (ADR-006).
func (a *app) harness(ctx context.Context, args []string) error {
	e, err := a.engine()
	if err != nil {
		return err
	}
	repos := args
	if len(repos) == 0 {
		if repos, err = e.Enrolled(ctx); err != nil {
			return err
		}
	}
	all := map[string][]engine.LaneRun{}
	var b strings.Builder
	ok := true
	for _, arg := range repos {
		repo, err := forgeRepo(arg, e.ForgeHost)
		if err != nil {
			return withCode(ExitUsage, err)
		}
		runs, err := e.LaneRuns(ctx, repo)
		if err != nil {
			return fmt.Errorf("%s: %w", repo, err)
		}
		all[repo] = runs
		fmt.Fprintf(&b, "%s\n", repo)
		for _, r := range runs {
			ok = ok && r.Problem == ""
			state := ""
			if !r.On {
				state = " (off)"
			}
			fmt.Fprintf(&b, "  %s  %s%s: %s lane, %s on %s\n", mark(r.Problem == ""), r.Lane, state, r.Kind, r.Runner, r.Executor)
			if r.Harness != "" {
				fmt.Fprintf(&b, "        harness %s, %s\n", r.Harness, r.Where)
			} else {
				fmt.Fprintf(&b, "        %s\n", r.Where)
			}
			if h := r.Read; h != nil {
				if h.ID != "" {
					fmt.Fprintf(&b, "        installed as %s\n", h.ID)
				}
				fmt.Fprintf(&b, "        budgets: %d turns, %d tokens, %s wall\n", h.Agent.MaxTurns, h.Agent.MaxTokens, orNone(h.Agent.MaxWall))
				fmt.Fprintf(&b, "        sensors: %s\n", orNone(strings.Join(slices.Sorted(maps.Keys(h.Sensors)), ", ")))
				fmt.Fprintf(&b, "        focuses: %s\n", orNone(strings.Join(slices.Sorted(maps.Keys(h.Focuses)), ", ")))
			}
			if r.Problem != "" {
				fmt.Fprintf(&b, "        problem: %s\n", r.Problem)
			}
		}
	}
	if err := a.out(all, b.String()); err != nil {
		return err
	}
	if !ok {
		return withCode(ExitPolicy, errors.New("a lane's harness cannot be read or does not fit it"))
	}
	return nil
}

func mark(ok bool) string {
	if ok {
		return "ok  "
	}
	return "FAIL"
}

func orNone(s string) string {
	if s == "" || s == "0" {
		return "none"
	}
	return s
}
