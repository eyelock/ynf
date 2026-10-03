package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/store"
)

type cryptoReader struct{}

func (cryptoReader) Read(p []byte) (int, error) { return rand.Read(p) }

func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(a.stderr)
	return fs
}

// doctor reports what ynf found: config, store, token, and the providers it detects (ADR-012).
func (a *app) doctor(ctx context.Context) error {
	type check struct {
		Name   string `json:"name"`
		OK     bool   `json:"ok"`
		Detail string `json:"detail"`
	}
	var checks []check
	add := func(name string, ok bool, detail string) { checks = append(checks, check{name, ok, detail}) }

	if err := a.loadConfig(); err != nil {
		add("config", false, err.Error())
	} else {
		detail := a.cfg.Path
		if len(a.cfg.Shadowed) > 0 {
			detail += " (shadows " + strings.Join(a.cfg.Shadowed, ", ") + ")"
		}
		add("config", true, detail)
		if _, err := a.engine(); err != nil {
			add("engine", false, err.Error())
		} else {
			add("store", true, a.cfg.Store)
			add("repos", true, strings.Join(a.cfg.Repos, ", "))
			for _, r := range a.cfg.Repos {
				rp, err := a.eng.Policy(ctx, r)
				if err != nil {
					add("lanes "+r, false, err.Error())
					continue
				}
				detail := fmt.Sprintf("%s on %s: %s", rp.Dir, rp.Base, strings.Join(rp.File.Names(), ", "))
				if len(rp.Shadowed) > 0 {
					detail += "; shadows " + strings.Join(rp.Shadowed, ", ")
				}
				add("lanes "+r, true, detail)
			}
		}
	}
	for _, tool := range []struct{ name, args string }{{"git", "--version"}, {"docker", "version --format {{.Server.Version}}"}, {"ynh", "version"}, {"ynm", "--version"}} {
		out, err := exec.CommandContext(ctx, tool.name, strings.Fields(tool.args)...).Output()
		detail := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if err != nil {
			detail = "not found or not working (" + err.Error() + ")"
		}
		add(tool.name, err == nil, detail)
	}
	var b strings.Builder
	ok := true
	for _, c := range checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
			if c.Name == "ynh" || c.Name == "ynm" {
				mark = "--  " // optional (ADR-012)
			} else {
				ok = false
			}
		}
		fmt.Fprintf(&b, "%s  %-24s %s\n", mark, c.Name, c.Detail)
	}
	if err := a.out(map[string]any{"ok": ok, "checks": checks}, b.String()); err != nil {
		return err
	}
	if !ok {
		return withCode(ExitPolicy, errors.New("doctor found problems"))
	}
	return nil
}

func (a *app) lanesCmd(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return withCode(ExitUsage, errors.New("lanes validate|show"))
	}
	switch args[0] {
	case "validate":
		fs := a.flags("lanes validate")
		file := fs.String("file", ".agents/factory/lanes.yaml", "")
		if err := fs.Parse(args[1:]); err != nil {
			return withCode(ExitUsage, err)
		}
		doc, err := os.ReadFile(*file)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		f, err := policy.Load(doc)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		return a.out(map[string]any{"valid": true, "file": *file, "lanes": f.Names()},
			fmt.Sprintf("%s: valid, %d lanes (%s)", *file, len(f.Lanes), strings.Join(f.Names(), ", ")))
	case "show":
		fs := a.flags("lanes show")
		repo := fs.String("repo", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return withCode(ExitUsage, err)
		}
		e, err := a.engine()
		if err != nil {
			return err
		}
		if *repo == "" && len(a.cfg.Repos) == 1 {
			*repo = a.cfg.Repos[0]
		}
		rp, err := e.Policy(ctx, *repo)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		lanes := rp.File.Lanes
		if name := fs.Arg(0); name != "" {
			l, ok := lanes[name]
			if !ok {
				return withCode(ExitPolicy, fmt.Errorf("%s has no lane %q", *repo, name))
			}
			lanes = map[string]policy.Lane{name: l}
		}
		j, _ := json.MarshalIndent(map[string]any{"repo": rp.Repo, "dir": rp.Dir, "ref": rp.Base, "lanes": lanes}, "", "  ")
		return a.out(map[string]any{"repo": rp.Repo, "dir": rp.Dir, "ref": rp.Base, "lanes": lanes}, string(j))
	}
	return withCode(ExitUsage, fmt.Errorf("unknown lanes command %q", args[0]))
}

// sweep runs searches and due items: once, until settled, or forever (serve).
func (a *app) sweep(ctx context.Context, args []string, forever bool) error {
	fs := a.flags("sweep")
	until := fs.Bool("until-settled", false, "")
	timeout := fs.Duration("timeout", 20*time.Minute, "")
	interval := fs.Duration("interval", 15*time.Second, "")
	if forever {
		*interval = time.Minute
	}
	fs.Var(&a.lanes, "lane", "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	deadline := time.Now().Add(*timeout)
	for {
		e.ResetPolicies()
		if err := e.Sweep(ctx); err != nil {
			e.Log.Error("sweep", "err", err)
		}
		for {
			n, err := e.RunDue(ctx)
			if err != nil {
				e.Log.Error("due", "err", err)
			}
			if n == 0 {
				break
			}
		}
		settled, err := e.Settled(ctx)
		if err != nil {
			return err
		}
		switch {
		case !forever && !*until:
			return a.summary(ctx)
		case !forever && settled:
			return a.summary(ctx)
		case !forever && time.Now().After(deadline):
			_ = a.summary(ctx)
			return withCode(ExitUnsettled, fmt.Errorf("not settled after %s", *timeout))
		}
		select {
		case <-ctx.Done():
			return a.summary(context.WithoutCancel(ctx))
		case <-time.After(*interval):
		}
	}
}

func (a *app) summary(ctx context.Context) error {
	items, err := a.eng.Items(ctx)
	if err != nil {
		return err
	}
	return a.out(map[string]any{"items": items}, table(items))
}

func table(items []item.Item) string {
	var b strings.Builder
	w := tabwriter.NewWriter(&b, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ITEM\tLANE\tSTATE\tPR\tREASON")
	for _, it := range items {
		pr := ""
		if it.PR > 0 {
			pr = "#" + strconv.Itoa(it.PR)
		}
		_, _ = fmt.Fprintf(w, "%s#%d\t%s\t%s\t%s\t%s\n", it.Repo, it.Number, it.Lane, it.State, pr, it.Reason)
	}
	_ = w.Flush()
	return b.String()
}

// key accepts owner/name#number or a full item key.
func key(s string) (string, error) {
	if strings.HasPrefix(s, "item/") {
		return s, nil
	}
	repo, n, ok := strings.Cut(s, "#")
	num, err := strconv.Atoi(n)
	if !ok || err != nil || !strings.Contains(repo, "/") {
		return "", withCode(ExitUsage, fmt.Errorf("%q is not owner/name#number or an item key", s))
	}
	return item.IssueKey(repo, num), nil
}

func (a *app) items(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return withCode(ExitUsage, errors.New("items ls|show|log|retry|release"))
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	if args[0] == "ls" {
		return a.summary(ctx)
	}
	if len(args) < 2 {
		return withCode(ExitUsage, fmt.Errorf("items %s needs an item", args[0]))
	}
	k, err := key(args[1])
	if err != nil {
		return err
	}
	switch args[0] {
	case "show":
		it, _, err := lease.Load(ctx, e.Store, k)
		if err != nil {
			return err
		}
		j, _ := json.MarshalIndent(it, "", "  ")
		return a.out(it, string(j))
	case "log":
		log, err := e.Store.Log(ctx, k)
		if err != nil {
			return err
		}
		var b strings.Builder
		for _, en := range log {
			fmt.Fprintf(&b, "%s  %-8s %s\n", en.Time.Format(time.RFC3339), en.Kind, summarise(en))
		}
		return a.out(log, b.String())
	case "retry", "release":
		return a.human(ctx, e, k, args[0])
	}
	return withCode(ExitUsage, fmt.Errorf("unknown items command %q", args[0]))
}

// human applies an operator's decision: retry puts an escalated or quarantined item back to ready;
// release clears a stuck lease. Both are recorded.
func (a *app) human(ctx context.Context, e *engine.Engine, k, what string) error {
	doc, v, err := e.Store.Get(ctx, k)
	if err != nil {
		return err
	}
	var it item.Item
	if err := json.Unmarshal(doc, &it); err != nil {
		return err
	}
	if what == "retry" {
		if it.Lease.Held(e.Now()) {
			return fmt.Errorf("%s is being worked on by %s; release it first if that is stale", k, it.Lease.Owner)
		}
		it.State, it.Reason, it.Attempts, it.Counters, it.Feedback = item.Ready, "retried by a human", 0, nil, ""
		now := e.Now()
		it.NextDue = &now
	}
	it.Lease = nil
	b, _ := json.Marshal(it)
	if _, err := e.Store.Put(ctx, k, b, v); err != nil {
		return err
	}
	if it.NextDue != nil {
		if err := e.Store.Schedule(ctx, k, *it.NextDue); err != nil {
			return err
		}
	}
	note, _ := json.Marshal(map[string]string{"by": "human", "action": what})
	if err := e.Store.Append(ctx, k, store.LogEntry{ID: e.NewID(), Time: e.Now(), Kind: "note", Body: note}); err != nil {
		return err
	}
	return a.out(it, fmt.Sprintf("%s: %s", k, map[string]string{"retry": "back to ready", "release": "lease cleared"}[what]))
}

func summarise(en store.LogEntry) string {
	switch en.Kind {
	case "decision":
		var r engine.DecisionRecord
		if json.Unmarshal(en.Body, &r) == nil {
			s := fmt.Sprintf("%s -> %s: %s", r.Input.Event.Type, r.Decision.Item.State, r.Decision.Reason)
			for _, act := range r.Decision.Actions {
				s += " [" + act.Kind + "]"
			}
			return s
		}
	case "run":
		var r engine.RunRecord
		if json.Unmarshal(en.Body, &r) == nil {
			return fmt.Sprintf("%s %s via %s: %s %s, %d changed (%s)", r.RunID, r.Runner, r.Executor, r.Outcome, r.Detail, len(r.Changed), r.Duration)
		}
	case "action":
		var r engine.ActionRecord
		if json.Unmarshal(en.Body, &r) == nil {
			return fmt.Sprintf("%s ok=%v %s", r.Action, r.OK, r.Detail)
		}
	}
	return string(en.Body)
}

func (a *app) replay(ctx context.Context, args []string) error {
	fs := a.flags("replay")
	pol := fs.String("policy", "", "")
	if len(args) == 0 {
		return withCode(ExitUsage, errors.New("replay needs an item"))
	}
	if err := fs.Parse(args[1:]); err != nil {
		return withCode(ExitUsage, err)
	}
	k, err := key(args[0])
	if err != nil {
		return err
	}
	var override *policy.File
	if *pol != "" {
		doc, err := os.ReadFile(*pol)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		if override, err = policy.Load(doc); err != nil {
			return withCode(ExitPolicy, err)
		}
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	log, err := e.Store.Log(ctx, k)
	if err != nil {
		return err
	}
	rs, err := engine.Replay(log, override)
	if err != nil {
		return err
	}
	var b strings.Builder
	differ := 0
	for _, r := range rs {
		mark := "same"
		if !r.Same {
			mark, differ = "DIFF", differ+1
		}
		fmt.Fprintf(&b, "%s  %-18s recorded %-11s replayed %-11s %s\n", mark, r.Event, r.Recorded.Item.State, r.Replayed.Item.State, r.Replayed.Reason)
	}
	fmt.Fprintf(&b, "%d decisions, %d differ", len(rs), differ)
	if err := a.out(map[string]any{"decisions": rs, "differ": differ}, b.String()); err != nil {
		return err
	}
	if differ > 0 {
		return withCode(ExitDifferences, fmt.Errorf("%d decisions differ", differ))
	}
	return nil
}
