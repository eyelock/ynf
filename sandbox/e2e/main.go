// Command e2e is the factory's acceptance test: it runs ynf against the live sandbox until every
// item settles, then checks each fixture's expect block — the item's state, and for a proposal
// the draft pull request, its trailers and its green CI — and replays every recorded decision.
//
// Unlike `make calibrate`, this tests ynf itself. Only fixtures in the lanes given with -lanes are
// checked. A fixture with expect.crash is the crash test: ynf is killed (SIGKILL) as that
// fixture's run starts, and a fresh ynf must restart it once its lease runs out.
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
		Detail     string   `yaml:"detail"`
		Crash      bool     `yaml:"crash"`
		Runner     *struct {
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
	hideYnh := flag.Bool("hide-ynh", false, "hide ynh from ynf (YNF_YNH_BIN names nothing), so a lane with no runner falls back to its command")
	image := flag.String("image", "", "run ynf inside this factory-flavoured harness image, as a job runner would (ADR-009, shape B)")
	flag.Parse()
	if *forget {
		if err := forgetMemory(*repo); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*root, *repo, *factory, *image, *outage, strings.Split(*lanes, ","), *timeout, *hideYnh); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
}

func run(root, repo, factory, image string, outage bool, lanes []string, timeout time.Duration, hideYnh bool) error {
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
	if hideYnh {
		// ynf detects ynh by asking this binary (ADR-012): nothing is there, so a lane with no
		// runner runs its command, as it would on a machine without ynh.
		_ = os.Setenv("YNF_YNH_BIN", filepath.Join(tmp, "no-ynh"))
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
	if err := os.WriteFile(cfg, fmt.Appendf(nil, "version: 1\nfactory: {repo: %s}\npoll: {ci: 15s, review: 1m}\nlease: {ttl: 30s, heartbeat: 10s}\n%s%s", factory, mem, inImage), 0o644); err != nil {
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
