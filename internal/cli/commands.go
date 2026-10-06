package cli

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/lease"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/telemetry"
	"github.com/eyelock/ynf/internal/tracker"
	"gopkg.in/yaml.v3"
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
	unreadable := map[string]bool{}
	add := func(name string, ok bool, detail string) { checks = append(checks, check{name, ok, detail}) }
	// Docker is needed only when a lane runs in it; inside the factory image every run is inline.
	// Until the lanes are read, assume it is.
	needDocker, lanesRead := false, false

	if err := a.loadConfig(); err != nil {
		add("config", false, err.Error())
	} else {
		detail := a.cfg.Path
		if len(a.cfg.Shadowed) > 0 {
			detail += " (shadows " + strings.Join(a.cfg.Shadowed, ", ") + ")"
		}
		add("config", true, detail)
		if ok, detail := a.spoolCheck(); detail != "" {
			add("telemetry", ok, detail)
		}
		if _, err := a.engine(); err != nil {
			add("engine", false, err.Error())
		} else {
			add("store", true, a.cfg.Store)
			enrolled, err := a.eng.Enrolled(ctx)
			if fp, ferr := a.eng.Factory(ctx); ferr == nil && fp != nil {
				add("factory", true, fmt.Sprintf("%s at %.7s (%s)", fp.Repo, fp.SHA, fp.Dir))
			}
			if err != nil {
				add("repos", false, err.Error())
			} else {
				add("repos", true, strings.Join(enrolled, ", "))
			}
			for _, r := range enrolled {
				rp, err := a.eng.Policy(ctx, r)
				if err != nil {
					add("lanes "+r, false, err.Error())
					continue
				}
				lanesRead = true
				for _, l := range rp.File.Lanes {
					if ex, err := a.eng.Executor(l.Run.Executor); l.On() && (err != nil || ex.Name() == "docker") {
						needDocker = true
					}
				}
				dir := rp.Dir
				if dir == "" {
					dir = "the configuration repository's lanes only"
				}
				detail := fmt.Sprintf("%s on %s at %.7s: %s", dir, rp.Base, rp.SHA, strings.Join(rp.File.Names(), ", "))
				if len(rp.Shadowed) > 0 {
					detail += "; shadows " + strings.Join(rp.Shadowed, ", ")
				}
				add("lanes "+r, true, detail)
			}
			// Which checks gate a pull request comes from branch protection and the rulesets. When
			// neither can be read, every check gates instead; ynf still works, so it is not a failure.
			if reps, err := a.eng.RequiredChecks(ctx); err != nil {
				add("required checks", false, err.Error())
			} else {
				for _, rp := range reps {
					name := "required checks " + rp.Repo
					switch {
					case rp.Known:
						add(name, true, fmt.Sprintf("%s: %s", rp.Branch, strings.Join(rp.Checks, ", ")))
					default:
						add(name, false, fmt.Sprintf("not readable on %s, so every check gates a pull request: %s", rp.Branch, rp.Detail))
						unreadable[name] = true
					}
				}
			}
			if conns, err := a.eng.Connections(ctx); err != nil {
				add("connections", false, err.Error())
			} else {
				for _, c := range conns {
					if c.Kind == "tracker" && c.Detail == "its forge's issues" {
						continue // the forge's own line says it
					}
					add(c.Kind+" "+c.Name, c.OK, c.Host+": "+c.Detail)
				}
			}
		}
	}
	optional := map[string]bool{"ynh": true, "ynm": true, "ynr": true, "memory queue": true} // ADR-012
	maps.Copy(optional, unreadable)
	if a.eng != nil {
		// Writes ynm could not take wait in ynf's store: worth saying, not a failure.
		if n, since, err := a.eng.MemoryQueued(ctx); err == nil && n > 0 {
			add("memory queue", false, fmt.Sprintf("%d memory writes queued since %s", n, since.Local().Format(time.RFC3339)))
		}
	}
	for _, tool := range []struct{ name, args string }{{"git", "--version"}, {"docker", "version --format {{.Server.Version}}"}, {"ynh", "version"}, {"ynm", "--version"}, {"ynr", ""}} {
		if tool.name == "ynh" {
			// The same detection a lane with no runner uses (ADR-012), so doctor and a run agree.
			d := runner.DetectedYnh(ctx)
			detail := "not found or not working (" + d.Detail + ")"
			switch {
			case d.Found:
				detail = fmt.Sprintf("%s, capabilities %s: detected, used by a lane with no runner and a ynh block", d.Version, d.Capabilities)
			case d.Capabilities != "":
				detail = "found but not supported (" + d.Detail + ")"
			}
			add("ynh", d.Found, detail)
			continue
		}
		if tool.name == "ynr" {
			// Detected like ynh, and unlike it nothing uses it by being found (ADR-012).
			ok, detail := ynrCheck(ctx, a.cfg)
			add("ynr", ok, detail)
			continue
		}
		c := exec.CommandContext(ctx, tool.name, strings.Fields(tool.args)...)
		telemetry.Command(ctx, c)
		out, err := c.Output()
		detail := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
		if err != nil {
			detail = "not found or not working (" + err.Error() + ")"
			if tool.name == "docker" && lanesRead && !needDocker {
				detail = "not needed: no lane runs in docker here"
				optional[tool.name] = true
			}
		}
		add(tool.name, err == nil, detail)
	}
	var b strings.Builder
	ok := true
	for _, c := range checks {
		mark := "ok  "
		if !c.OK {
			mark = "FAIL"
			if optional[c.Name] {
				mark = "--  "
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
		repo := fs.String("repo", "", "")
		if err := fs.Parse(args[1:]); err != nil {
			return withCode(ExitUsage, err)
		}
		doc, err := os.ReadFile(*file)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		if *repo != "" {
			return a.validateMerged(ctx, *repo, *file, doc)
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
		if *repo == "" {
			if *repo, err = onlyRepo(ctx, e); err != nil {
				return err
			}
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
		shown := map[string]any{"repo": rp.Repo, "dir": rp.Dir, "ref": rp.Base, "sha": rp.SHA, "lanes": lanes}
		// What a lane that names no runner resolves to on this host (ADR-012), which no lane says.
		resolves := map[string]string{}
		for name, l := range lanes {
			if l.Run.Runner != "" {
				continue
			}
			if res, err := runner.Resolve(l, e.HostYnh(ctx)); err != nil {
				resolves[name] = "none: " + err.Error()
			} else {
				resolves[name] = res.Note
			}
		}
		if len(resolves) > 0 {
			shown["resolves"] = resolves
		}
		if rp.Config != nil {
			// Which layer set each value (ADR-006): the configuration repository or the repository.
			shown["config"] = map[string]string{"repo": rp.Config.Repo, "sha": rp.Config.SHA}
			sources := map[string]map[string]string{}
			for name := range lanes {
				src, err := policy.LaneSources(rp.Config.Lanes, rp.Own, name)
				if err != nil {
					return err
				}
				for path, layer := range src {
					if sources[name] == nil {
						sources[name] = map[string]string{}
					}
					if layer == "repo" {
						sources[name][path] = "repo@" + short(rp.SHA)
					} else {
						sources[name][path] = "config@" + short(rp.Config.SHA)
					}
				}
			}
			shown["sources"] = sources
		}
		// Text is YAML, the language lanes are written in; --format json gets the same data as JSON.
		// It goes through JSON so both show the same keys and leave out the same empty values.
		j, err := json.Marshal(shown)
		if err != nil {
			return err
		}
		var plain any
		if err := yaml.Unmarshal(j, &plain); err != nil {
			return err
		}
		var y strings.Builder
		enc := yaml.NewEncoder(&y)
		enc.SetIndent(2)
		if err := enc.Encode(plain); err != nil {
			return err
		}
		return a.out(shown, y.String())
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
	listen := fs.String("listen", "", "")
	secretEnv := fs.String("webhook-secret-env", "YNF_WEBHOOK_SECRET", "")
	startTokenEnv := fs.String("start-token-env", "YNF_START_TOKEN", "")
	fs.Var(&a.lanes, "lane", "")
	if err := fs.Parse(args); err != nil {
		return withCode(ExitUsage, err)
	}
	e, err := a.engine()
	if err != nil {
		return err
	}
	if *listen != "" {
		if !forever {
			return withCode(ExitUsage, errors.New("--listen is for serve"))
		}
		addr, err := a.listen(ctx, e, *listen, *secretEnv, *startTokenEnv)
		if err != nil {
			return err
		}
		e.Log.Info("receiving GitHub webhooks", "addr", addr, "path", "/webhook/github")
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
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", it.Ref(), it.Lane, it.State, pr, it.Reason)
	}
	_ = w.Flush()
	return b.String()
}

// key accepts an item key, or a reference (ADR-002): host/owner/name#number, owner/name#number
// on the configured forge (defaultHost), or host/KEY for a ticket on another tracker.
func key(s, defaultHost string, resolve func(name string) (string, error)) (string, error) {
	if strings.HasPrefix(s, "item/") {
		return s, nil
	}
	ref, err := parseRef(s, defaultHost, resolve)
	if err != nil {
		return "", withCode(ExitUsage, err)
	}
	return item.Key(ref), nil
}

// parseRef resolves a reference to a tracker host and the tracker's own key.
func parseRef(s, defaultHost string, resolve func(name string) (string, error)) (tracker.Ref, error) {
	if repoPart, num, ok := strings.Cut(s, "#"); ok {
		n, err := strconv.Atoi(num)
		parts := strings.Split(repoPart, "/")
		if err == nil && n > 0 && (len(parts) == 2 || len(parts) == 3) && !slices.Contains(parts, "") {
			host := defaultHost
			if len(parts) == 3 {
				host, parts = parts[0], parts[1:]
			}
			return tracker.Ref{Host: host, Key: forge.IssueKey(parts[0]+"/"+parts[1], n)}, nil
		}
	} else if host, k, ok := strings.Cut(s, "/"); ok && host != "" && k != "" && !strings.Contains(k, "/") {
		if !strings.Contains(host, ".") {
			// A configured tracker's name, such as jira: stored by its host.
			if resolve == nil {
				return tracker.Ref{}, fmt.Errorf("%q: %q is not a host or a configured tracker", s, host)
			}
			h, err := resolve(host)
			if err != nil {
				return tracker.Ref{}, fmt.Errorf("%q: %w", s, err)
			}
			host = h
		}
		return tracker.Ref{Host: host, Key: k}, nil
	}
	return tracker.Ref{}, fmt.Errorf("%q is not a reference (owner/name#number, host/owner/name#number, host/KEY) or an item key", s)
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
	k, err := key(args[1], e.ForgeHost, trackerNames(ctx, e))
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
		if it.State != item.Escalated && it.State != item.Quarantined {
			return withCode(ExitUsage, fmt.Errorf("%s is %s; retry is only for escalated or quarantined items", k, it.State))
		}
		if it.Lease.Held(e.Now()) {
			return fmt.Errorf("%s is being worked on by %s; release it first if that is stale", k, it.Lease.Owner)
		}
		it.State, it.Reason, it.Attempts, it.Counters, it.Feedback = item.Ready, "retried by a human", 0, nil, ""
		now := e.Now()
		it.NextDue = &now
	}
	if what == "release" && !it.State.Settled() {
		// The timer still stands at the dead holder's lease expiry, so clearing the lease alone
		// would restart the work no sooner than waiting. Make it due now. A settled item has no
		// work to restart, and its timer is a wake-up to look again, so it is left as it is.
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
			via := ""
			if r.RunnerDetected {
				via = " (detected"
				if r.RunnerVersion != "" {
					via += " " + r.RunnerVersion
				}
				via += ")"
			}
			if r.Executor != "" {
				via += " via " + r.Executor
			}
			return fmt.Sprintf("%s %s%s: %s, %d changed (%s)", r.RunID, r.Runner, via, strings.TrimSpace(r.Outcome+" "+r.Detail), len(r.Changed), r.Duration)
		}
	case "action":
		var r engine.ActionRecord
		if json.Unmarshal(en.Body, &r) == nil {
			return fmt.Sprintf("%s ok=%v %s", r.Action, r.OK, r.Detail)
		}
	}
	return string(en.Body)
}

// validateMerged checks a repository's lanes.yaml as ynf would use it: laid over the configuration
// repository's lanes, which a layer alone may depend on, and validated as a whole.
func (a *app) validateMerged(ctx context.Context, repo, file string, doc []byte) error {
	e, err := a.engine()
	if err != nil {
		return err
	}
	rp, err := e.PolicyWithLayer(ctx, repo, doc)
	if err != nil {
		return withCode(ExitPolicy, err)
	}
	names := rp.File.Names()
	out := map[string]any{"valid": true, "file": file, "repo": repo, "lanes": names}
	text := fmt.Sprintf("%s: valid, %d lanes (%s)", file, len(names), strings.Join(names, ", "))
	if rp.Config != nil {
		out["config"] = map[string]string{"repo": rp.Config.Repo, "sha": rp.Config.SHA}
		text += fmt.Sprintf("\nmerged over config@%s of %s", short(rp.Config.SHA), rp.Config.Repo)
	}
	return a.out(out, text)
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
	e, err := a.engine()
	if err != nil {
		return err
	}
	k, err := key(args[0], e.ForgeHost, trackerNames(ctx, e))
	if err != nil {
		return err
	}
	var override *policy.File
	if *pol != "" {
		doc, err := os.ReadFile(*pol)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		if override, err = replayPolicy(ctx, e, k, doc); err != nil {
			return withCode(ExitPolicy, err)
		}
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

// replayPolicy loads the --policy file. One that fails alone may be a repository's layer, so when
// there is a configuration repository it is laid over that for the item's repository, as ynf
// would; the file's own error is reported when the item cannot say which repository that is.
func replayPolicy(ctx context.Context, e *engine.Engine, k string, doc []byte) (*policy.File, error) {
	f, err := policy.Load(doc)
	if err == nil || e.ConfigRepo == "" {
		return f, err
	}
	stored, _, gerr := e.Store.Get(ctx, k)
	if gerr != nil {
		return nil, err
	}
	var it item.Item
	if json.Unmarshal(stored, &it) != nil {
		return nil, err
	}
	rp, merr := e.PolicyWithLayer(ctx, e.ItemRepo(it), doc)
	if merr != nil {
		return nil, merr
	}
	return rp.File, nil
}

// onlyRepo is the enrolled repository when there is exactly one, so commands can default to it.
func onlyRepo(ctx context.Context, e *engine.Engine) (string, error) {
	enrolled, err := e.Enrolled(ctx)
	if err != nil {
		return "", err
	}
	if len(enrolled) == 1 {
		return enrolled[0], nil
	}
	return "", nil
}

func short(sha string) string { return sha[:min(7, len(sha))] }

// trackerNames resolves a configured tracker's name to its host, for references.
func trackerNames(ctx context.Context, e *engine.Engine) func(string) (string, error) {
	return func(name string) (string, error) { return e.TrackerHost(ctx, name) }
}
