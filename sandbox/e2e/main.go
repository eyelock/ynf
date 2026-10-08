// Command e2e is the factory's acceptance test: it runs ynf against the live sandbox until every
// item settles, then checks each fixture's expect block — the item's state, and for a proposal
// the draft pull request, its trailers and its green CI — and replays every recorded decision.
//
// Unlike `make calibrate`, this tests ynf itself. Only fixtures in the lanes given with -lanes are
// checked. A fixture with expect.crash is the crash test: ynf is killed (SIGKILL) as that
// fixture's run starts, and a fresh ynf must restart it once its lease runs out.
//
// With the gofmt lane in -lanes, it ends with shadow mode (-shadow=false skips it): the human fix
// for the fmt-format issue is merged, which closes it, and `ynf shadow run` takes the issue on
// the commit before that fix. The patch is graded with the scripted form, the report is read, and
// nothing outward changed: the issue's comments and labels, the pull requests, the branches, the
// items and the stats are the same after as before. It merges a pull request into the sandbox's
// main, so it is the last thing e2e does, and `make reset` is what undoes it.
//
// With ynm installed, memory is part of the check: every item that ran must have its step memory
// (ynf.step.v1, for its last run) and a failure memory (ynf.failure.v1) per failure signature, in
// the sandbox's namespace. -forget-memory empties that namespace, which `make reset` does, so
// every run starts from the same memory.
//
// -memory-outage proves ADR-008's promise that a ynm outage loses nothing: ynm is made unreachable
// for the whole run (a shim first on PATH that fails while a flag file exists, in front of a
// scratch YNM_HOME), so every memory write queues in ynf's store while the factory carries on; the
// run then checks `ynf doctor` reports the queue, brings ynm back, runs one more sweep, and checks
// the queue is empty and every memory arrived. It costs no more than the run itself: the extra sweep
// finds every item settled, so no model runs.
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type fixture struct {
	ID     string   `yaml:"id"`
	Kind   string   `yaml:"kind"`
	Ticket string   `yaml:"ticket"`
	Title  string   `yaml:"title"`
	Body   string   `yaml:"body"`
	Labels []string `yaml:"labels"`
	Lane   string   `yaml:"lane"`
	Expect struct {
		Result     results  `yaml:"result"`
		Signatures []string `yaml:"signatures"`
		// Memory is text the item's failure memories must carry: what the run reported (ADR-008).
		Memory []string `yaml:"memory"`
		Detail string   `yaml:"detail"`
		Crash  bool     `yaml:"crash"`
		Runner *struct {
			Name     string `yaml:"name"`
			Detected bool   `yaml:"detected"`
		} `yaml:"runner"`
		Start  bool `yaml:"start"`
		Labels struct {
			Present []string `yaml:"present"`
			Absent  []string `yaml:"absent"`
		} `yaml:"labels"`
	} `yaml:"expect"`
}

// results is a fixture's expected ending: one state, or a list when more than one is legitimate.
type results []string

func (r *results) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.SequenceNode {
		return n.Decode((*[]string)(r))
	}
	var one string
	if err := n.Decode(&one); err != nil {
		return err
	}
	*r = results{one}
	return nil
}

func (r results) accepts(state string) bool {
	return slices.Contains(r, "any") || slices.Contains(r, state)
}

type item struct {
	Key     string `json:"key"`
	LastRun *struct {
		ID      string `json:"id"`
		Outcome string `json:"outcome"`
		Detail  string `json:"detail"`
	} `json:"last_run"`
	Counters map[string]int `json:"counters"`
	Repo     string         `json:"repo"`
	Number   int            `json:"number"`
	Lane     string         `json:"lane"`
	State    string         `json:"state"`
	Reason   string         `json:"reason"`
	PR       int            `json:"pr"`
	Branch   string         `json:"branch"`
}

func main() {
	root := flag.String("root", ".", "the sandbox/ directory")
	repo := flag.String("repo", "eyelock/ynf-sandbox", "the sandbox repository")
	factory := flag.String("factory", "eyelock/ynf-sandbox-factory", "the configuration repository, which enrols the sandbox")
	lanes := flag.String("lanes", "gofmt,deps", "lanes to run and check")
	timeout := flag.Duration("timeout", 15*time.Minute, "how long to wait for items to settle")
	forget := flag.Bool("forget-memory", false, "empty the sandbox's ynm namespace and exit")
	outage := flag.Bool("memory-outage", false, "run with ynm unreachable, then bring it back and check the queued memory writes arrive")
	hideYnh := flag.Bool("hide-ynh", false, "hide ynh from ynf (no folder on the PATH ynf is given holds one), so a lane with no runner falls back to its command")
	image := flag.String("image", "", "run ynf inside this factory-flavoured harness image, as a job runner would (ADR-009, shape B)")
	gateOn := flag.Bool("gate", true, "end by adopting the gate fixture's pull request and proving ynf keeps it proposed while a ruleset-required check has not started (needs a rebuilt sandbox: the pull request takes one commit from ynf, once)")
	shadowOn := flag.Bool("shadow", true, "with the gofmt lane, end by closing fmt-format with a merged human fix and proving shadow mode on it (merges into the sandbox's main)")
	flag.Parse()
	if *forget {
		if err := forgetMemory(*repo); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*root, *repo, *factory, *image, *outage, strings.Split(*lanes, ","), *timeout, *hideYnh, *shadowOn, *gateOn); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
}

func run(root, repo, factory, image string, outage bool, lanes []string, timeout time.Duration, hideYnh, shadowOn, gateOn bool) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return err
	}
	var ff struct {
		Fixtures []fixture `yaml:"fixtures"`
	}
	b, err := os.ReadFile(filepath.Join(root, "fixtures.yaml"))
	if err != nil {
		return err
	}
	if err := yaml.Unmarshal(b, &ff); err != nil {
		return err
	}

	tmp, err := os.MkdirTemp("", "ynf-e2e-")
	if err != nil {
		return err
	}
	// A failed acceptance run keeps its evidence: the store with every decision, and each run's
	// task, stdout, stderr and trajectory under work/steps. Only a passing run is cleaned up.
	passed := false
	defer func() {
		if passed {
			_ = os.RemoveAll(tmp)
			return
		}
		fmt.Printf("\nevidence kept in %s\n  ynf --config %s items log <owner/name#n>\n  runs: %s\n",
			tmp, filepath.Join(tmp, "config.yaml"), filepath.Join(tmp, "work", "steps"))
	}()
	if tmp, err = filepath.EvalSymlinks(tmp); err != nil {
		return err
	}

	ynf := filepath.Join(tmp, "ynf")
	if out, err := sh(filepath.Dir(root), "go", "build", "-o", ynf, "./cmd/ynf"); err != nil {
		return fmt.Errorf("build ynf: %w\n%s", err, out)
	}
	// The docker executor's egress proxy is a linux ynf, found beside ynf.
	arch, err := sh("", "docker", "version", "--format", "{{.Server.Arch}}")
	if err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	arch = strings.TrimSpace(arch)
	if out, err := shEnv(filepath.Dir(root), []string{"GOOS=linux", "GOARCH=" + arch, "CGO_ENABLED=0"}, "go", "build", "-o", ynf+"-linux-"+arch, "./cmd/ynf"); err != nil {
		return fmt.Errorf("build linux ynf: %w\n%s", err, out)
	}
	// The sandbox tracker that is not a forge: its server goes on ynf's PATH, as an operator would
	// install a tracker's MCP server, and its tickets are written fresh for every run.
	if out, err := sh(filepath.Join(root, "e2e"), "go", "build", "-o", filepath.Join(tmp, "ynf-sandbox-tracker"), "./tracker"); err != nil {
		return fmt.Errorf("build the sandbox tracker: %w\n%s", err, out)
	}
	trackerData := filepath.Join(tmp, "tracker.json")
	tickets := map[string]trackerTicket{}
	for _, f := range ff.Fixtures {
		if f.Kind != "ticket" {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, f.Body))
		if err != nil {
			return err
		}
		tickets[f.Ticket] = trackerTicket{Title: f.Title, Body: string(body), Labels: f.Labels, Status: "open", Repo: "github.com/" + repo}
	}
	tb, _ := json.MarshalIndent(tickets, "", "  ")
	if err := os.WriteFile(trackerData, tb, 0o644); err != nil {
		return err
	}
	_ = os.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))
	_ = os.Setenv("YNF_SANDBOX_TRACKER_DATA", trackerData)
	// ynf writes its OpenTelemetry to a spool of this run's own, and the run reads it back (ADR-011).
	// With ynr, ynf is configured as a factory job with the collector on: ynr serve ships what is in
	// the spool to a receiver of e2e's own, and the run reads it from there, with what ynr stamped.
	// Without ynr, those checks are skipped, and said to be. Inside an image the variable does not
	// reach ynf, so that run is not checked.
	spool := ""
	var col *collectorSetup
	if image == "" {
		spool = filepath.Join(tmp, "spool")
		if err := os.MkdirAll(spool, 0o755); err != nil {
			return err
		}
		bin, why := ynrBinary()
		if bin == "" {
			fmt.Printf("collector, manifest and quota checks skipped: %s\n  ynf writes to a spool of this run's own, and only the step and intake checks run\n", why)
			_ = os.Setenv("YNR_SPOOL", spool)
		} else {
			rcv, err := startReceiver()
			if err != nil {
				return err
			}
			defer rcv.close()
			// The spool-image lane's image, with a user of its own (uid 10042); cached after the first build.
			if slices.Contains(lanes, "spool-image") {
				if o, err := sh(filepath.Join(root, "images", "probe"), "docker", "build", "-q", "-t", "ynf-sandbox-probe:latest", "."); err != nil {
					return fmt.Errorf("build the spool-image lane's image: %w\n%s", err, o)
				}
			}
			col = &collectorSetup{spool: spool, rcv: rcv, bin: bin}
			fmt.Printf("collector on: ynf starts %s as ynr serve for the sweep, shipping to a receiver at %s\n", bin, rcv.url)
		}
	}
	if hideYnh {
		// ynf finds ynh only on PATH (ADR-012). A folder put first on the PATH ynf is given holds a
		// ynh that answers as a shell does for a missing command, so a lane with no runner runs its
		// command, as it would on a machine without ynh. Taking ynh's folder off PATH instead would
		// also take whatever else lives there, such as Homebrew's.
		hide := filepath.Join(tmp, "hide-ynh")
		if err := os.MkdirAll(hide, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(hide, "ynh"), []byte("#!/bin/sh\necho 'ynh: command not found' >&2\nexit 127\n"), 0o755); err != nil {
			return err
		}
		_ = os.Setenv("PATH", hide+string(os.PathListSeparator)+os.Getenv("PATH"))
		fmt.Println("ynh hidden from ynf: a lane with no runner falls back to its command")
	}

	cfg := filepath.Join(tmp, "config.yaml")
	// A short lease so the crash test's restart comes about half a minute after the kill.
	// Memory is explicit: ynm in the sandbox's namespace when it is installed, and checked;
	// otherwise off, and said so.
	_, ynmErr := exec.LookPath("ynm")
	memoryOn := ynmErr == nil && image == ""
	mem := "memory: {provider: none}\n"
	if memoryOn {
		mem = "memory: {provider: ynm, namespace: \"" + memoryNamespace + "\"}\n"
	} else {
		fmt.Println("memory not checked: ynm is not installed")
	}
	var down *ynmOutage
	if outage {
		if !memoryOn {
			return errors.New("-memory-outage needs ynm installed, and ynf on the host (not -image)")
		}
		if down, err = startOutage(tmp); err != nil {
			return err
		}
		fmt.Println("ynm is unreachable for this run; its writes queue in ynf's store")
	}
	// Enrolment comes from the configuration repository, as a deployed factory's does (ADR-006).
	inImage := ""
	if image != "" {
		// Shape B: every run inline beside ynf, in the image; work on a Linux volume.
		inImage = "executor: inline\nwork_dir: /work\n"
		if ynf, err = inImageWrapper(root, tmp, arch, image); err != nil {
			return err
		}
		fmt.Printf("ynf runs inside %s, as a job runner would run it\n", image)
	}
	telemetryCfg := ""
	if col != nil {
		telemetryCfg = col.config()
	}
	if err := os.WriteFile(cfg, fmt.Appendf(nil, "version: 1\nfactory: {repo: %s}\npoll: {ci: 15s, review: 1m}\nlease: {ttl: 30s, heartbeat: 10s}\n%s%s%s", factory, mem, inImage, telemetryCfg), 0o644); err != nil {
		return err
	}
	// Memories written before this run are not evidence for it (e2e-only skips the reset).
	memSince := time.Now().UTC().Add(-time.Second)
	numbers, err := issueNumbers(repo)
	if err != nil {
		return err
	}

	logPath := filepath.Join(tmp, "ynf.log")
	args := []string{"--config", cfg, "--format", "json", "--log-file", logPath, "sweep", "--until-settled", "--interval", "10s", "--timeout", timeout.String()}
	for _, l := range lanes {
		args = append(args, "--lane", l)
	}
	fmt.Printf("running ynf %s\nynf log (also below as it happens): %s\n\n", strings.Join(lanes, ", "), logPath)
	start := time.Now()
	// Fixtures nothing searches for are started, as a person or an automation would: a GitHub
	// issue with no lane label, and every ticket in the tracker that is not a forge.
	for _, f := range ff.Fixtures {
		if !f.Expect.Start && f.Kind != "ticket" && f.Kind != "prompt" || !slices.Contains(lanes, f.Lane) {
			continue
		}
		if f.Kind == "prompt" {
			if err := startPrompt(root, ynf, cfg, logPath, repo, f); err != nil {
				return err
			}
			continue
		}
		ref, _, _, err := subject(f, numbers, repo)
		if err != nil {
			return err
		}
		args := []string{"--config", cfg, "--log-file", logPath, "start", ref, "--lane", f.Lane}
		if f.Kind == "ticket" {
			args = append(args, "--repo", repo)
		}
		fmt.Printf("ynf %s (%s)\n", strings.Join(args[4:], " "), f.ID)
		if out, err := stream(ynf, args...); err != nil {
			return fmt.Errorf("ynf start %s: %w\n%s", f.ID, err, out)
		}
	}
	for _, f := range ff.Fixtures {
		if image != "" && f.Expect.Crash {
			fmt.Printf("%s: the crash test needs ynf as a process here to kill; skipped in an image\n", f.ID)
		}
		if !f.Expect.Crash || image != "" || !slices.Contains(lanes, f.Lane) {
			continue
		}
		n, ok := numbers[f.Title]
		if !ok {
			return fmt.Errorf("no issue titled %q in the sandbox", f.Title)
		}
		key := fmt.Sprintf("item=item/github.com/%s/issues/%d ", repo, n)
		killed, err := streamUntil(func(line string) bool {
			return strings.Contains(line, `msg="run started"`) && strings.Contains(line, key)
		}, ynf, args...)
		if err != nil {
			return fmt.Errorf("ynf sweep before the crash: %w", err)
		}
		if !killed {
			return fmt.Errorf("%s: its run never started, so there was nothing to kill", f.ID)
		}
		fmt.Printf("\nkilled ynf (SIGKILL) as #%d's run started (%s); a fresh ynf takes over\n\n", n, f.ID)
		if col != nil {
			// ynr serve is not ynf's child in the sense that dies with it: a job's container ends it.
			stopOrphans(spool)
		}
	}
	out, err := stream(ynf, args...)
	if err != nil {
		return fmt.Errorf("ynf sweep: %w\n%s", err, out)
	}
	var res struct {
		Items []item `json:"items"`
	}
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		return fmt.Errorf("ynf sweep output: %w\n%s", err, out)
	}
	fmt.Printf("settled in %s\n\n", time.Since(start).Round(time.Second))

	failed := 0
	if down != nil {
		if err := down.recover(repo, ynf, cfg, logPath, memSince, lanes, expectedFailures(ff.Fixtures, lanes, numbers, repo)); err != nil {
			fmt.Printf("FAIL  %-24s %s\n", "memory outage", err)
			failed++
		}
	}
	var memories []memoryRecord
	if memoryOn {
		if memories, err = listMemory(repo, memSince); err != nil {
			return err
		}
	}
	for _, f := range ff.Fixtures {
		if !slices.Contains(lanes, f.Lane) {
			continue
		}
		detail, err := check(f, numbers, res.Items, repo, ynf, cfg, trackerData)
		if err == nil && memoryOn {
			var m string
			if m, err = checkMemory(f, numbers, res.Items, repo, memories); err == nil {
				detail += m
			}
		}
		mark := "ok  "
		if err != nil {
			mark, detail = "FAIL", err.Error()
			failed++
		}
		fmt.Printf("%s  %-24s %s\n", mark, f.ID, detail)
	}
	if failed > 0 {
		return fmt.Errorf("%d fixture(s) did not end as expected", failed)
	}
	if spool != "" && slices.Contains(lanes, "gofmt") {
		if col != nil {
			if err := checkCollector(*col, filepath.Join(tmp, "work"), logPath, res.Items, lanes); err != nil {
				return err
			}
		} else if err := checkSpool(spool, gofmtKeys(res.Items), "gofmt"); err != nil {
			return err
		}
	}
	if shadowOn && slices.Contains(lanes, "gofmt") {
		if err := shadowStage(ff.Fixtures, numbers, repo, ynf, cfg); err != nil {
			return fmt.Errorf("shadow mode: %w", err)
		}
	}
	if gateOn && image == "" {
		if err := gateStage(ff.Fixtures, repo, ynf, cfg, logPath); err != nil {
			return fmt.Errorf("required checks: %w", err)
		}
	}
	passed = true
	return nil
}

// trackerHost is the sandbox tracker's host, from its site in the configuration repository.
const trackerHost = "tracker.ynf-sandbox.invalid"

// promptKeys is each prompt fixture's item key, which ynf chooses when it is started.
var promptKeys = map[string]string{}

// startPrompt starts a prompt fixture as a person would, with no ticket: its title and body are the
// prompt, and its labels are given with --label, for the lane's templates to read.
func startPrompt(root, ynf, cfg, logPath, repo string, f fixture) error {
	body, err := os.ReadFile(filepath.Join(root, f.Body))
	if err != nil {
		return err
	}
	args := []string{"--config", cfg, "--format", "json", "--log-file", logPath, "start", "--prompt", f.Title + "\n\n" + string(body), "--repo", repo, "--lane", f.Lane}
	for _, l := range f.Labels {
		args = append(args, "--label", l)
	}
	fmt.Printf("ynf start --prompt %q --label %s --repo %s --lane %s (%s)\n", f.Title, strings.Join(f.Labels, " --label "), repo, f.Lane, f.ID)
	out, err := stream(ynf, args...)
	if err != nil {
		return fmt.Errorf("ynf start %s: %w\n%s", f.ID, err, out)
	}
	var it struct {
		Key string `json:"key"`
	}
	if err := json.Unmarshal([]byte(out), &it); err != nil {
		return fmt.Errorf("ynf start %s: %w", f.ID, err)
	}
	if !strings.HasPrefix(it.Key, "item/adhoc/") {
		return fmt.Errorf("ynf start %s: no ad hoc item in %q", f.ID, out)
	}
	promptKeys[f.ID] = it.Key
	return nil
}

// subject is how ynf names a fixture's ticket (ref), its item's key, and how e2e reports it.
func subject(f fixture, numbers map[string]int, repo string) (ref, key, name string, err error) {
	if f.Kind == "prompt" {
		k, ok := promptKeys[f.ID]
		if !ok {
			return "", "", "", fmt.Errorf("%s was never started", f.ID)
		}
		return k, k, "prompt " + f.ID, nil
	}
	if f.Kind == "ticket" {
		return trackerHost + "/" + f.Ticket, "item/" + trackerHost + "/" + f.Ticket, f.Ticket, nil
	}
	n, ok := numbers[f.Title]
	if !ok {
		return "", "", "", fmt.Errorf("no issue titled %q in the sandbox", f.Title)
	}
	return fmt.Sprintf("%s#%d", repo, n), fmt.Sprintf("item/github.com/%s/issues/%d", repo, n), fmt.Sprintf("#%d", n), nil
}

func check(f fixture, numbers map[string]int, items []item, repo, ynf, cfg, trackerData string) (string, error) {
	ref, key, name, err := subject(f, numbers, repo)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(items, func(it item) bool { return it.Key == key })
	if i < 0 {
		return "", fmt.Errorf("%s was never tracked", name)
	}
	it := items[i]
	if !f.Expect.Result.accepts(it.State) {
		msg := fmt.Sprintf("%s ended %s (%s), expected %s", name, it.State, it.Reason, strings.Join(f.Expect.Result, " or "))
		if r := it.LastRun; r != nil && r.Detail != "" {
			msg += fmt.Sprintf("\n      last run %s: %s", r.ID, oneLine(r.Detail, 300))
		}
		var denied []string
		for k := range it.Counters {
			if h, ok := strings.CutPrefix(k, "sig/egress/denied/"); ok {
				denied = append(denied, h)
			}
		}
		if len(denied) > 0 {
			slices.Sort(denied)
			msg += "\n      egress denied: " + strings.Join(denied, ", ")
		}
		return "", errors.New(msg)
	}
	detail := fmt.Sprintf("%s %s", name, it.State)
	if len(f.Expect.Result) > 1 {
		detail += fmt.Sprintf(" (one of %s)", strings.Join(f.Expect.Result, ", "))
	}

	if want := f.Expect.Detail; want != "" {
		if it.LastRun == nil || !strings.Contains(it.LastRun.Detail, want) {
			return "", fmt.Errorf("%s: the last run's detail lacks %q: %+v", name, want, it.LastRun)
		}
		detail += ", refused as: " + oneLine(want, 80)
	}

	if it.PR > 0 {
		var pr struct {
			IsDraft     bool   `json:"isDraft"`
			HeadRefName string `json:"headRefName"`
		}
		out, err := sh("", "gh", "pr", "view", fmt.Sprint(it.PR), "-R", repo, "--json", "isDraft,headRefName")
		if err != nil {
			return "", fmt.Errorf("pull request #%d: %w", it.PR, err)
		}
		if err := json.Unmarshal([]byte(out), &pr); err != nil {
			return "", err
		}
		if !pr.IsDraft || pr.HeadRefName != it.Branch {
			return "", fmt.Errorf("#%d: draft=%v head=%s, want a draft from %s", it.PR, pr.IsDraft, pr.HeadRefName, it.Branch)
		}
		msg, err := sh("", "gh", "api", fmt.Sprintf("repos/%s/commits/%s", repo, it.Branch), "-q", ".commit.message")
		if err != nil {
			return "", err
		}
		for _, t := range []string{"YNF-Item: ", "YNF-Step: ", "YNF-Run: "} {
			if !strings.Contains(msg, t) {
				return "", fmt.Errorf("#%d's commit has no %s trailer", it.PR, strings.TrimSpace(t))
			}
		}
		detail += fmt.Sprintf(", draft #%d with trailers", it.PR)
	}

	rp, err := sh("", ynf, "--config", cfg, "--format", "json", "replay", ref)
	if err != nil {
		return "", fmt.Errorf("replay: %w\n%s", err, rp)
	}
	var r struct {
		Decisions []json.RawMessage `json:"decisions"`
		Differ    int               `json:"differ"`
	}
	if err := json.Unmarshal([]byte(rp), &r); err != nil {
		return "", err
	}
	detail += fmt.Sprintf(", %d decisions replay the same", len(r.Decisions))
	// Every decision records the configuration repository's commit beside the repository's.
	logged, err := sh("", ynf, "--config", cfg, "--format", "json", "items", "log", ref)
	if err != nil {
		return "", fmt.Errorf("items log: %w", err)
	}
	if !strings.Contains(logged, `config_sha`) {
		return "", fmt.Errorf("%s's decisions do not record the configuration repository's commit", name)
	}
	var ticket *trackerTicket
	if f.Kind == "ticket" {
		if ticket, err = readTicket(trackerData, f.Ticket); err != nil {
			return "", err
		}
		// The pull request is linked on the ticket, by URL: it is not a GitHub issue.
		link := fmt.Sprintf("proposed https://github.com/%s/pull/%d", repo, it.PR)
		if it.PR == 0 || !slices.ContainsFunc(ticket.Comments, func(c string) bool { return strings.Contains(c, link) }) {
			return "", fmt.Errorf("%s: the pull request is not linked on the ticket: %v", name, ticket.Comments)
		}
		detail += ", linked on the ticket"
	}
	if want := f.Expect.Labels; len(want.Present)+len(want.Absent) > 0 {
		var have []string
		if ticket != nil {
			have = ticket.Labels
		} else {
			out, err := sh("", "gh", "issue", "view", strings.TrimPrefix(name, "#"), "-R", repo, "--json", "labels", "-q", "[.labels[].name] | join(\",\")")
			if err != nil {
				return "", fmt.Errorf("%s's labels: %w", name, err)
			}
			have = strings.Split(strings.TrimSpace(out), ",")
		}
		for _, l := range want.Present {
			if !slices.Contains(have, l) {
				return "", fmt.Errorf("%s lacks the label %s: %v", name, l, have)
			}
		}
		for _, l := range want.Absent {
			if slices.Contains(have, l) {
				return "", fmt.Errorf("%s still has the label %s: %v", name, l, have)
			}
		}
		detail += ", labelled " + strings.Join(want.Present, ",")
	}
	if want := f.Expect.Runner; want != nil {
		logged, err := sh("", ynf, "--config", cfg, "--format", "json", "items", "log", ref)
		if err != nil {
			return "", fmt.Errorf("items log: %w", err)
		}
		var entries []struct {
			Kind string          `json:"kind"`
			Body json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal([]byte(logged), &entries); err != nil {
			return "", fmt.Errorf("items log output: %w", err)
		}
		var got *struct {
			Runner         string `json:"runner"`
			RunnerDetected bool   `json:"runner_detected"`
		}
		for _, en := range entries {
			var r struct {
				Runner         string `json:"runner"`
				RunnerDetected bool   `json:"runner_detected"`
			}
			if en.Kind == "run" && json.Unmarshal(en.Body, &r) == nil {
				got = &r
			}
		}
		if got == nil || got.Runner != want.Name || got.RunnerDetected != want.Detected {
			return "", fmt.Errorf("%s: its last run record says %+v, expected runner %s, detected %v", name, got, want.Name, want.Detected)
		}
		detail += fmt.Sprintf(", ran as %s (detected %v)", got.Runner, got.RunnerDetected)
	}
	if f.Expect.Crash {
		log, err := sh("", ynf, "--config", cfg, "items", "log", ref)
		if err != nil {
			return "", fmt.Errorf("items log: %w", err)
		}
		if !strings.Contains(log, "did not finish") {
			return "", fmt.Errorf("%s: no restart after the crash in its log", name)
		}
		detail += ", restarted after the crash"
	}
	return detail, nil
}

// ynmOutage is ynm taken away for a run. ynf finds ynm on PATH, so a shim named ynm goes first:
// while the down file exists it fails the way an unreachable ynm does, otherwise it is the real
// one. The shim and a scratch YNM_HOME keep the outage off the developer's own memory.
type ynmOutage struct{ tmp, realYnm string }

func startOutage(tmp string) (*ynmOutage, error) {
	realYnm, err := exec.LookPath("ynm")
	if err != nil {
		return nil, err
	}
	o := &ynmOutage{tmp: tmp, realYnm: realYnm}
	home := filepath.Join(tmp, "ynm-home")
	if err := os.MkdirAll(home, 0o755); err != nil {
		return nil, err
	}
	_ = os.Setenv("YNM_HOME", home)
	if out, err := sh("", realYnm, "init", "--personal"); err != nil {
		return nil, fmt.Errorf("ynm init: %w\n%s", err, out)
	}
	shim := fmt.Sprintf("#!/bin/sh\nif [ -e %q ]; then echo 'ynm: unreachable (ynf e2e -memory-outage)' >&2; exit 1; fi\nexec %q \"$@\"\n", o.flag(), realYnm)
	if err := os.MkdirAll(filepath.Join(tmp, "shim"), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "shim", "ynm"), []byte(shim), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(o.flag(), nil, 0o644); err != nil {
		return nil, err
	}
	_ = os.Setenv("PATH", filepath.Join(tmp, "shim")+string(os.PathListSeparator)+os.Getenv("PATH"))
	return o, nil
}

func (o *ynmOutage) flag() string { return filepath.Join(o.tmp, "ynm-down") }

// failure is one failure memory a fixture is expected to leave: its item and signature.
type failure struct{ item, sig string }

// expectedFailures are the failure memories the fixtures in the lanes should leave, which is what
// puts memory writes in the queue.
func expectedFailures(fs []fixture, lanes []string, numbers map[string]int, repo string) []failure {
	var out []failure
	for _, f := range fs {
		if len(f.Expect.Signatures) == 0 || !slices.Contains(lanes, f.Lane) {
			continue
		}
		_, key, _, err := subject(f, numbers, repo)
		if err != nil {
			continue
		}
		for _, sig := range f.Expect.Signatures {
			out = append(out, failure{key, sig})
		}
	}
	return out
}

// queued is how many memory writes `ynf doctor` says are queued; zero when it says nothing.
func (o *ynmOutage) queued(ynf, cfg string) (int, error) {
	out, err := sh("", ynf, "--config", cfg, "--format", "json", "doctor")
	var rep struct {
		Checks []struct{ Name, Detail string } `json:"checks"`
	}
	if jerr := json.Unmarshal([]byte(out), &rep); jerr != nil {
		return 0, fmt.Errorf("ynf doctor: %v: %w", jerr, err)
	}
	for _, c := range rep.Checks {
		if c.Name == "memory queue" {
			var n int
			if _, err := fmt.Sscanf(c.Detail, "%d memory writes queued since", &n); err != nil {
				return 0, fmt.Errorf("ynf doctor says %q", c.Detail)
			}
			return n, nil
		}
	}
	return 0, nil
}

// recover checks the outage from both ends: while ynm was down, nothing reached it and doctor
// reports the queue; once it is back, one sweep sends the queue, doctor reports nothing, and the
// memories are there for checkMemory to find.
func (o *ynmOutage) recover(repo, ynf, cfg, logPath string, since time.Time, lanes []string, want []failure) error {
	if len(want) == 0 {
		return errors.New("no fixture in these lanes leaves a failure memory, so nothing would queue: the outage check needs one (the outage lane)")
	}
	n, err := o.queued(ynf, cfg)
	if err != nil {
		return err
	}
	if n < len(want) {
		return fmt.Errorf("%d failure memories were expected, yet doctor reports %d queued memory writes", len(want), n)
	}
	// The real ynm, not the shim: what the outage kept from reaching it.
	if out, err := sh("", o.realYnm, "list", "--json", "--namespace", namespaceFor(repo), "--limit", "10000", "--since", since.Format(time.RFC3339)); err != nil {
		return fmt.Errorf("ynm list: %w", err)
	} else if strings.Contains(out, `"memoryId"`) {
		return errors.New("a memory reached ynm while it was unreachable")
	}
	fmt.Printf("ok    %-24s %d memory writes queued while ynm was unreachable, none reached it\n", "memory outage", n)

	if err := os.Remove(o.flag()); err != nil {
		return err
	}
	fmt.Println("ynm is back; one sweep sends the queue")
	if out, err := stream(ynf, sweepArgs(cfg, logPath, lanes)...); err != nil {
		return fmt.Errorf("ynf sweep after the outage: %w\n%s", err, out)
	}
	if left, err := o.queued(ynf, cfg); err != nil {
		return err
	} else if left != 0 {
		return fmt.Errorf("%d memory writes are still queued after ynm came back", left)
	}
	got, err := listMemory(repo, since)
	if err != nil {
		return err
	}
	for _, w := range want {
		if !slices.ContainsFunc(got, func(m memoryRecord) bool {
			return m.Current.DataSchema == "ynf.failure.v1" && m.Current.Subject == w.sig && m.Current.Data["item"] == w.item
		}) {
			return fmt.Errorf("the queued %s on %s never reached ynm", w.sig, w.item)
		}
	}
	fmt.Printf("ok    %-24s queue drained; the %d queued failure memories are in ynm\n", "memory outage", len(want))
	return nil
}

// sweepArgs is a single sweep of the given lanes only: a sweep after the run must not take on work
// the run was told to leave alone.
func sweepArgs(cfg, logPath string, lanes []string) []string {
	args := []string{"--config", cfg, "--format", "json", "--log-file", logPath, "sweep"}
	for _, l := range lanes {
		args = append(args, "--lane", l)
	}
	return args
}

// memoryNamespace is where the sandbox's memories go; {repo} is host/owner/name.
const memoryNamespace = "factory/{repo}"

func namespaceFor(repo string) string {
	return strings.ReplaceAll(memoryNamespace, "{repo}", "github.com/"+repo)
}

type memoryRecord struct {
	MemoryID string `json:"memoryId"`
	Current  struct {
		Namespace  string         `json:"namespace"`
		Subject    string         `json:"subject"`
		Content    string         `json:"content"`
		Tags       []string       `json:"tags"`
		DataSchema string         `json:"dataSchema"`
		Data       map[string]any `json:"data"`
	} `json:"current"`
}

// listMemory lists the sandbox namespace's memories, exactly that namespace (ynm matches a
// prefix), written since a time; zero means all.
func listMemory(repo string, since time.Time) ([]memoryRecord, error) {
	ns := namespaceFor(repo)
	args := []string{"list", "--json", "--namespace", ns, "--limit", "10000"}
	if !since.IsZero() {
		args = append(args, "--since", since.Format(time.RFC3339))
	}
	out, err := sh("", "ynm", args...)
	if err != nil {
		return nil, fmt.Errorf("ynm list: %w", err)
	}
	var all []memoryRecord
	if err := json.Unmarshal([]byte(out), &all); err != nil {
		return nil, fmt.Errorf("ynm list output: %w", err)
	}
	var mine []memoryRecord
	for _, m := range all {
		if m.Current.Namespace == ns {
			mine = append(mine, m)
		}
	}
	return mine, nil
}

// forgetMemory tombstones every memory in the sandbox's namespace, so the next run starts from
// the same memory. History is kept; nothing outside the namespace is touched.
func forgetMemory(repo string) error {
	if _, err := exec.LookPath("ynm"); err != nil {
		fmt.Println("memory: ynm is not installed, nothing to forget")
		return nil
	}
	ms, err := listMemory(repo, time.Time{})
	if err != nil {
		return err
	}
	for _, m := range ms {
		if _, err := sh("", "ynm", "forget", "--memory-id", m.MemoryID, "--reason", "ynf sandbox reset"); err != nil {
			return fmt.Errorf("ynm forget %s: %w", m.MemoryID, err)
		}
	}
	fmt.Printf("memory: forgot %d memories in %s\n", len(ms), namespaceFor(repo))
	return nil
}

// checkMemory checks what an item that ran left in memory (ADR-008): no step records, since ynf's
// own store is the run history, and a ynf.failure.v1 memory for each failure signature it counted,
// tagged with its schema and naming its item.
func checkMemory(f fixture, numbers map[string]int, items []item, repo string, memories []memoryRecord) (string, error) {
	_, key, name, err := subject(f, numbers, repo)
	if err != nil {
		return "", err
	}
	i := slices.IndexFunc(items, func(it item) bool { return it.Key == key })
	if i < 0 || items[i].LastRun == nil {
		return "", nil // nothing ran, so nothing to remember
	}
	it := items[i]
	if slices.ContainsFunc(memories, func(m memoryRecord) bool { return m.Current.DataSchema == "ynf.step.v1" }) {
		return "", fmt.Errorf("%s: a step record reached memory; ynf's own store is the run history", name)
	}
	n := 0
	for sig := range it.Counters {
		if !strings.HasPrefix(sig, "sig/") {
			continue
		}
		if !slices.ContainsFunc(memories, func(m memoryRecord) bool {
			c := m.Current
			return c.DataSchema == "ynf.failure.v1" && c.Subject == sig && c.Data["item"] == it.Key && slices.Contains(c.Tags, "ynf.failure.v1") && slices.Contains(c.Tags, "occurrence")
		}) {
			return "", fmt.Errorf("%s: no ynf.failure.v1 memory for %s", name, sig)
		}
		// Each says what the run reported, not only what ynf decided: the exit code and the run's
		// own message are in its text and in its data (ADR-008).
		for _, m := range memories {
			c := m.Current
			if c.DataSchema != "ynf.failure.v1" || c.Subject != sig || c.Data["item"] != it.Key {
				continue
			}
			for _, want := range f.Expect.Memory {
				if !strings.Contains(c.Content, want) {
					return "", fmt.Errorf("%s: the %s memory does not say %q: %s", name, sig, want, c.Content)
				}
			}
			if len(f.Expect.Memory) > 0 && (c.Data["outcome"] == nil || c.Data["excerpt"] == nil) {
				return "", fmt.Errorf("%s: the %s memory's data lacks the run's outcome or excerpt: %v", name, sig, c.Data)
			}
		}
		n++
	}
	if n == 0 {
		return ", no failures to remember", nil
	}
	return fmt.Sprintf(", %d failure memories written", n), nil
}

// trackerTicket is a ticket in the sandbox tracker's data file (sandbox/e2e/tracker).
type trackerTicket struct {
	Title    string   `json:"title"`
	Body     string   `json:"body"`
	Labels   []string `json:"labels"`
	Status   string   `json:"status"`
	Repo     string   `json:"repo,omitempty"`
	Comments []string `json:"comments,omitempty"`
}

func readTicket(path, key string) (*trackerTicket, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var ts map[string]*trackerTicket
	if err := json.Unmarshal(b, &ts); err != nil {
		return nil, err
	}
	if ts[key] == nil {
		return nil, fmt.Errorf("the tracker has no %s", key)
	}
	return ts[key], nil
}

// inImageWrapper writes a ynf that runs in the factory image, hardened as a job runner would run
// it (ADR-007): root with only SETUID, SETGID and CHOWN, no new privileges, the run folder shared
// at the same path, work on a fresh volume, and the sandbox tracker for linux on its PATH.
func inImageWrapper(root, tmp, arch, image string) (string, error) {
	linux := filepath.Join(tmp, "linux")
	if out, err := shEnv(filepath.Join(root, "e2e"), []string{"GOOS=linux", "GOARCH=" + arch, "CGO_ENABLED=0"}, "go", "build", "-o", filepath.Join(linux, "ynf-sandbox-tracker"), "./tracker"); err != nil {
		return "", fmt.Errorf("build the sandbox tracker for linux: %w\n%s", err, out)
	}
	env, err := sh("", "docker", "image", "inspect", "--format", "{{range .Config.Env}}{{println .}}{{end}}", image)
	if err != nil {
		return "", fmt.Errorf("the image %s: %w", image, err)
	}
	path := "/usr/local/bin:/usr/bin:/bin"
	for l := range strings.SplitSeq(env, "\n") {
		if p, ok := strings.CutPrefix(l, "PATH="); ok {
			path = p
		}
	}
	const volume = "ynf-e2e-work"
	_, _ = sh("", "docker", "volume", "rm", "-f", volume)
	wrapper := filepath.Join(tmp, "ynf-in-image")
	script := fmt.Sprintf(`#!/bin/sh
exec docker run --rm --user root --cap-drop ALL --cap-add SETUID --cap-add SETGID --cap-add CHOWN \
  --security-opt no-new-privileges -e GITHUB_TOKEN -e YNF_SANDBOX_TRACKER_DATA -e PATH=%s:%s \
  -v %s:%s -v %s:/work -w %s --entrypoint ynf %s "$@"
`, linux, path, tmp, tmp, volume, tmp, image)
	return wrapper, os.WriteFile(wrapper, []byte(script), 0o755)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

func issueNumbers(repo string) (map[string]int, error) {
	out, err := sh("", "gh", "issue", "list", "-R", repo, "--state", "all", "--limit", "200", "--json", "number,title")
	if err != nil {
		return nil, err
	}
	var is []struct {
		Number int    `json:"number"`
		Title  string `json:"title"`
	}
	if err := json.Unmarshal([]byte(out), &is); err != nil {
		return nil, err
	}
	m := map[string]int{}
	for _, i := range is {
		m[i.Title] = i.Number
	}
	return m, nil
}

func sh(dir, name string, args ...string) (string, error) { return shEnv(dir, nil, name, args...) }

// stream runs a command, returning its stdout and showing its stderr (ynf's log) as it happens,
// indented under the e2e output.
func stream(name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	var stdout bytes.Buffer
	c.Stdout = &stdout
	pr, pw := io.Pipe()
	c.Stderr = pw
	done := make(chan struct{})
	go func() {
		defer close(done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Println("  │ " + sc.Text())
		}
	}()
	err := c.Run()
	_ = pw.Close()
	<-done
	return stdout.String(), err
}

// streamUntil streams a command like stream, and kills it (SIGKILL: no clean-up, no lease
// release) at the first log line kill matches. It reports whether it killed it.
func streamUntil(kill func(string) bool, name string, args ...string) (bool, error) {
	c := exec.Command(name, args...)
	c.Stdout = io.Discard
	pr, pw := io.Pipe()
	c.Stderr = pw
	if err := c.Start(); err != nil {
		return false, err
	}
	killed := make(chan bool, 1)
	go func() {
		k := false
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 64*1024), 1024*1024)
		for sc.Scan() {
			fmt.Println("  │ " + sc.Text())
			if !k && kill(sc.Text()) {
				k = true
				_ = c.Process.Kill()
			}
		}
		killed <- k
	}()
	err := c.Wait()
	_ = pw.Close()
	k := <-killed
	if k {
		return true, nil
	}
	return false, err
}

func shEnv(dir string, env []string, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	if env != nil {
		c.Env = append(os.Environ(), env...)
	}
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%w: %s", err, strings.TrimSpace(lastLines(stderr.String(), 8)))
	}
	return stdout.String(), nil
}

func lastLines(s string, n int) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	if len(l) > n {
		l = l[len(l)-n:]
	}
	return strings.Join(l, "\n")
}

// shadowStage proves shadow mode on the live sandbox, with the gofmt lane (no model spend): the
// fmt-format issue is closed by a merged pull request that formats internal/format, which is the
// human fix, and shadow mode runs the lane on the commit before it.
// gateStage adopts the gate fixture's pull request, whose base branch has a ruleset requiring a
// check, gate-never-runs, that no workflow reports. The lane's command pushes one commit; lint,
// test and docs then pass on it, and the required check never starts. The item must stay proposed
// with CI pending: never in_review, which is what ynf did when only the checks that had reported
// counted. Its decisions must show the check as expected and required, read from the ruleset (the
// base branch has no classic protection), and replay the same. The item never settles, so this is a
// stage of its own with its own loop, not part of the sweep.
func gateStage(fixtures []fixture, repo, ynf, cfg, logPath string) error {
	i := slices.IndexFunc(fixtures, func(f fixture) bool { return f.ID == "gate-required-check" })
	if i < 0 {
		return errors.New("no gate-required-check fixture")
	}
	const branch = "human/gate-required"
	out, err := sh("", "gh", "pr", "list", "-R", repo, "--head", branch, "--state", "open", "--json", "number", "-q", ".[0].number")
	if err != nil || strings.TrimSpace(out) == "" {
		return fmt.Errorf("no open pull request from %s in %s (this stage needs a rebuilt sandbox): %v", branch, repo, err)
	}
	// The short reference finds an adopted pull request's item, whose key says pulls, not issues.
	ref := fmt.Sprintf("%s#%s", repo, strings.TrimSpace(out))
	fmt.Printf("\nrequired checks: adopting %s, whose base branch requires a check nothing reports\n", ref)

	type snapshot struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
		PRHead string `json:"pr_head"`
	}
	read := func() (snapshot, error) {
		var it snapshot
		o, err := sh("", ynf, "--config", cfg, "--format", "json", "items", "show", ref)
		if err != nil {
			return it, fmt.Errorf("items show: %w\n%s", err, o)
		}
		return it, json.Unmarshal([]byte(o), &it)
	}
	// reported says whether lint, test and docs have all concluded on sha, and how.
	reported := func(sha string) (bool, error) {
		o, err := sh("", "gh", "api", fmt.Sprintf("repos/%s/commits/%s/check-runs", repo, sha), "-q", `[.check_runs[] | select(.name=="lint" or .name=="test" or .name=="docs") | .status+"/"+(.conclusion // "")] | sort | join(",")`)
		if err != nil {
			return false, err
		}
		got := strings.TrimSpace(o)
		if got == "completed/success,completed/success,completed/success" {
			return true, nil
		}
		if strings.Contains(got, "completed/failure") {
			return false, fmt.Errorf("a check that should pass failed on %.7s: %s", sha, got)
		}
		return false, nil
	}
	sweep := func() error {
		o, err := sh("", ynf, "--config", cfg, "--log-file", logPath, "sweep", "--lane", "gate")
		if err != nil {
			return fmt.Errorf("ynf sweep --lane gate: %w\n%s", err, o)
		}
		return nil
	}

	deadline := time.Now().Add(12 * time.Minute)
	for settled := 0; settled < 2; {
		if time.Now().After(deadline) {
			it, _ := read()
			return fmt.Errorf("%s did not reach proposed with every reported check passing in time: %+v", ref, it)
		}
		if err := sweep(); err != nil {
			return err
		}
		it, err := read()
		if err != nil {
			return err
		}
		switch it.State {
		case "proposed", "ready", "running", "intake":
		default:
			return fmt.Errorf("%s is %s (%s); it should stay proposed while a required check has not started", ref, it.State, it.Reason)
		}
		// Count a poll only once ynf has pushed and CI has finished on its commit: from then on every
		// check that reports is green, and only the missing one holds the item.
		if it.State == "proposed" && it.PRHead != "" {
			ok, err := reported(it.PRHead)
			if err != nil {
				return err
			}
			if ok {
				settled++
				continue
			}
		}
		time.Sleep(15 * time.Second)
	}
	// CI has finished; let ynf probe again after that (its CI poll is 15s), so its last decision
	// is made on facts where only the missing check is outstanding.
	time.Sleep(20 * time.Second)
	if err := sweep(); err != nil {
		return err
	}
	it, err := read()
	if err != nil {
		return err
	}
	if it.State != "proposed" {
		return fmt.Errorf("%s: %s (%s), want proposed", ref, it.State, it.Reason)
	}

	// What ynf saw, from the decisions it recorded; the reason it shows is checked on the item.
	o, err := sh("", ynf, "--config", cfg, "--format", "json", "items", "log", ref)
	if err != nil {
		return fmt.Errorf("items log: %w", err)
	}
	var entries []struct {
		Kind string          `json:"kind"`
		Body json.RawMessage `json:"body"`
	}
	if err := json.Unmarshal([]byte(o), &entries); err != nil {
		return err
	}
	type check struct {
		Name       string `json:"name"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		Required   bool   `json:"required"`
	}
	var last []check
	for _, en := range entries {
		var d struct {
			Input struct {
				Facts struct {
					PR *struct {
						Checks          []check `json:"checks"`
						RequiredUnknown bool    `json:"required_unknown"`
					} `json:"pr"`
				} `json:"facts"`
			} `json:"input"`
			Decision struct {
				Item struct {
					State string `json:"state"`
				} `json:"item"`
			} `json:"decision"`
		}
		if en.Kind != "decision" || json.Unmarshal(en.Body, &d) != nil {
			continue
		}
		if d.Decision.Item.State == "in_review" {
			return fmt.Errorf("%s was moved to in_review while a required check had not started", ref)
		}
		if pr := d.Input.Facts.PR; pr != nil {
			if pr.RequiredUnknown {
				return fmt.Errorf("%s: ynf could not read the required checks, so it could not have read the ruleset", ref)
			}
			last = pr.Checks
		}
	}
	// A decision that keeps the item waiting still sets the reason items show.
	if !strings.Contains(it.Reason, "CI pending") {
		return fmt.Errorf("%s: items show gives the reason %q, want CI pending", ref, it.Reason)
	}
	byName := map[string]check{}
	for _, c := range last {
		byName[c.Name] = c
	}
	if g := byName["gate-never-runs"]; g.Status != "expected" || !g.Required || g.Conclusion != "" {
		return fmt.Errorf("%s: the ruleset's required check should be expected and required in the facts, got %+v from %+v", ref, g, last)
	}
	for _, n := range []string{"lint", "test", "docs"} {
		if c := byName[n]; c.Status != "completed" || c.Conclusion != "success" || c.Required {
			return fmt.Errorf("%s: %s should have passed and not be required (only the ruleset's check is), got %+v", ref, n, c)
		}
	}
	rp, err := sh("", ynf, "--config", cfg, "--format", "json", "replay", ref)
	if err != nil {
		return fmt.Errorf("replay: %w\n%s", err, rp)
	}
	var r struct {
		Decisions []json.RawMessage `json:"decisions"`
		Differ    int               `json:"differ"`
	}
	if err := json.Unmarshal([]byte(rp), &r); err != nil {
		return err
	}
	if r.Differ != 0 {
		return fmt.Errorf("%s: %d of %d decisions do not replay the same", ref, r.Differ, len(r.Decisions))
	}
	fmt.Printf("ok    required checks         %s: lint, test and docs passed, gate-never-runs (a ruleset requirement) is expected, and the item stays proposed, never in_review; %d decisions replay the same\n", ref, len(r.Decisions))
	return nil
}

func shadowStage(fixtures []fixture, numbers map[string]int, repo, ynf, cfg string) error {
	i := slices.IndexFunc(fixtures, func(f fixture) bool { return f.ID == "fmt-format" })
	if i < 0 {
		return errors.New("no fmt-format fixture")
	}
	n, ok := numbers[fixtures[i].Title]
	if !ok {
		return fmt.Errorf("no issue titled %q in the sandbox", fixtures[i].Title)
	}
	ref := fmt.Sprintf("%s#%d", repo, n)
	fmt.Printf("\nshadow mode: merging the human fix that closes %s\n", ref)
	if err := mergeHumanFix(repo, n); err != nil {
		return err
	}

	// What shadow mode must not change, read now that the setup is done.
	before, err := outwardState(repo, n, ynf, cfg)
	if err != nil {
		return err
	}
	ynfJSON := func(args ...string) (string, error) {
		return sh("", ynf, append([]string{"--config", cfg, "--format", "json"}, args...)...)
	}
	out, err := ynfJSON("shadow", "run", "gofmt", "--repo", repo, "--ticket", ref)
	if err != nil {
		return fmt.Errorf("shadow run: %w\n%s", err, out)
	}
	var run struct {
		ID         string `json:"id"`
		Candidates int    `json:"candidates"`
		Attempted  int    `json:"attempted"`
		Attempts   []struct {
			ID      string `json:"id"`
			Outcome string `json:"outcome"`
		} `json:"attempts"`
	}
	if err := json.Unmarshal([]byte(out), &run); err != nil {
		return fmt.Errorf("shadow run output: %w\n%s", err, out)
	}
	if run.Candidates != 1 || run.Attempted != 1 || len(run.Attempts) != 1 || run.Attempts[0].Outcome != "converged" {
		return fmt.Errorf("want one converged attempt, got %s", out)
	}
	type rate struct{ Successes, N int }
	type report struct {
		Attempted, Graded int
		Pooled            struct {
			Yield      *rate `json:"yield"`
			UpperBound *rate `json:"upper_bound"`
		} `json:"pooled"`
		Grader struct{ Graded int } `json:"grader_check"`
	}
	read := func() (report, error) {
		var r report
		out, err := ynfJSON("shadow", "report", run.ID)
		if err != nil {
			return r, fmt.Errorf("shadow report: %w\n%s", err, out)
		}
		return r, json.Unmarshal([]byte(out), &r)
	}
	r, err := read()
	if err != nil {
		return err
	}
	// Before grading, only the automatic upper bound is known: the lane converged and its diff
	// gate would have accepted the change.
	if r.Pooled.Yield != nil || r.Pooled.UpperBound == nil || r.Pooled.UpperBound.Successes != 1 {
		return fmt.Errorf("before grading, want only the upper bound 1/1: %+v", r)
	}
	if out, err := ynfJSON("shadow", "grade", run.ID, "--attempt", run.Attempts[0].ID, "--a", "equivalent", "--b", "equivalent"); err != nil {
		return fmt.Errorf("shadow grade: %w\n%s", err, out)
	}
	if r, err = read(); err != nil {
		return err
	}
	if r.Pooled.Yield == nil || r.Pooled.Yield.Successes != 1 || r.Pooled.Yield.N != 1 || r.Graded != 1 || r.Grader.Graded != 1 {
		return fmt.Errorf("after grading, want yield 1/1 and one graded human patch: %+v", r)
	}
	after, err := outwardState(repo, n, ynf, cfg)
	if err != nil {
		return err
	}
	for name, was := range before {
		if after[name] != was {
			return fmt.Errorf("shadow mode changed %s:\n  before %s\n  after  %s", name, oneLine(was, 300), oneLine(after[name], 300))
		}
	}
	fmt.Printf("ok    shadow mode             %s: one attempt on the commit before the human fix, graded, yield 1/1, nothing outward changed\n", ref)
	return nil
}

// outwardState is everything shadow mode must leave alone, as text to compare: the issue's
// comments and labels, every pull request and branch in the repository, and ynf's items and stats.
func outwardState(repo string, issue int, ynf, cfg string) (map[string]string, error) {
	state := map[string]string{}
	for name, cmd := range map[string][]string{
		"the issue's comments and labels": {"gh", "issue", "view", fmt.Sprint(issue), "-R", repo, "--json", "comments,labels"},
		"the pull requests":               {"gh", "pr", "list", "-R", repo, "--state", "all", "--limit", "200", "--json", "number,state,headRefName,comments,labels"},
		"the branches":                    {"gh", "api", "repos/" + repo + "/branches", "--paginate", "-q", ".[].name"},
		"ynf's items":                     {ynf, "--config", cfg, "--format", "json", "items", "ls"},
		"ynf's stats":                     {ynf, "--config", cfg, "--format", "json", "stats"},
	} {
		out, err := sh("", cmd[0], cmd[1:]...)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		state[name] = out
	}
	return state, nil
}

// mergeHumanFix is what a person did: a pull request that formats internal/format and says it
// closes the issue, merged once the sandbox's required checks pass. Merging closes the issue.
func mergeHumanFix(repo string, issue int) error {
	dir, err := os.MkdirTemp("", "ynf-e2e-human-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	auth := []string{"-c", "credential.helper=", "-c", "credential.helper=!gh auth git-credential"}
	run := func(name string, args ...string) (string, error) { return sh(dir, name, args...) }
	if out, err := sh("", "git", append(auth, "clone", "-q", "--depth", "1", "--branch", "main", "https://github.com/"+repo+".git", dir)...); err != nil {
		return fmt.Errorf("clone: %w\n%s", err, out)
	}
	const branch = "human-fix-format"
	for _, c := range [][]string{
		{"git", "checkout", "-q", "-b", branch},
		{"gofmt", "-w", "internal/format"},
		{"git", "add", "-A"},
		{"git", "-c", "user.name=a person", "-c", "user.email=person@example.com", "commit", "-q", "-m", "fix: gofmt internal/format"},
	} {
		if out, err := run(c[0], c[1:]...); err != nil {
			return fmt.Errorf("%s: %w\n%s", strings.Join(c, " "), err, out)
		}
	}
	if out, err := run("git", append(auth, "push", "-q", "origin", "HEAD:refs/heads/"+branch)...); err != nil {
		return fmt.Errorf("push: %w\n%s", err, out)
	}
	if out, err := sh("", "gh", "pr", "create", "--repo", repo, "--base", "main", "--head", branch,
		"--title", "fix: gofmt internal/format", "--body", fmt.Sprintf("Closes #%d", issue)); err != nil {
		return fmt.Errorf("open the fix: %w\n%s", err, out)
	}
	// The required checks start a moment after the pull request opens.
	for try := 0; ; try++ {
		out, err := sh("", "gh", "pr", "checks", branch, "-R", repo, "--watch", "--interval", "10")
		if err == nil {
			break
		}
		if try >= 24 || !strings.Contains(err.Error()+out, "no checks reported") {
			return fmt.Errorf("the human fix's checks: %w\n%s", err, out)
		}
		time.Sleep(5 * time.Second)
	}
	if out, err := sh("", "gh", "pr", "merge", branch, "-R", repo, "--squash", "--delete-branch"); err != nil {
		return fmt.Errorf("merge the human fix: %w\n%s", err, out)
	}
	for range 30 {
		out, err := sh("", "gh", "issue", "view", fmt.Sprint(issue), "-R", repo, "--json", "state", "-q", ".state")
		if err == nil && strings.TrimSpace(out) == "CLOSED" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("merging the fix did not close #%d", issue)
}
