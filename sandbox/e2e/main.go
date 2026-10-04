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
	Title  string   `yaml:"title"`
	Labels []string `yaml:"labels"`
	Lane   string   `yaml:"lane"`
	Expect struct {
		Result     results  `yaml:"result"`
		Signatures []string `yaml:"signatures"`
		Crash      bool     `yaml:"crash"`
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
	lanes := flag.String("lanes", "gofmt,deps", "lanes to run and check")
	timeout := flag.Duration("timeout", 15*time.Minute, "how long to wait for items to settle")
	forget := flag.Bool("forget-memory", false, "empty the sandbox's ynm namespace and exit")
	flag.Parse()
	if *forget {
		if err := forgetMemory(*repo); err != nil {
			fmt.Fprintln(os.Stderr, "e2e:", err)
			os.Exit(1)
		}
		return
	}
	if err := run(*root, *repo, strings.Split(*lanes, ","), *timeout); err != nil {
		fmt.Fprintln(os.Stderr, "e2e:", err)
		os.Exit(1)
	}
}

func run(root, repo string, lanes []string, timeout time.Duration) error {
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
	cfg := filepath.Join(tmp, "config.yaml")
	// A short lease so the crash test's restart comes about half a minute after the kill.
	// Memory is explicit: ynm in the sandbox's namespace when it is installed, and checked;
	// otherwise off, and said so.
	_, ynmErr := exec.LookPath("ynm")
	memoryOn := ynmErr == nil
	mem := "memory: {provider: none}\n"
	if memoryOn {
		mem = "memory: {provider: ynm, namespace: \"" + memoryNamespace + "\"}\n"
	} else {
		fmt.Println("memory not checked: ynm is not installed")
	}
	if err := os.WriteFile(cfg, fmt.Appendf(nil, "version: 1\nrepos: [%s]\npoll: {ci: 15s, review: 1m}\nlease: {ttl: 30s, heartbeat: 10s}\n%s", repo, mem), 0o644); err != nil {
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
	for _, f := range ff.Fixtures {
		if !f.Expect.Crash || !slices.Contains(lanes, f.Lane) {
			continue
		}
		n, ok := numbers[f.Title]
		if !ok {
			return fmt.Errorf("no issue titled %q in the sandbox", f.Title)
		}
		key := fmt.Sprintf("item=item/github/%s/issues/%d ", repo, n)
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

	var memories []memoryRecord
	if memoryOn {
		if memories, err = listMemory(repo, memSince); err != nil {
			return err
		}
	}
	failed := 0
	for _, f := range ff.Fixtures {
		if !slices.Contains(lanes, f.Lane) {
			continue
		}
		detail, err := check(f, numbers, res.Items, repo, ynf, cfg)
		if err == nil && memoryOn {
			var m string
			if m, err = checkMemory(f, numbers, res.Items, memories); err == nil {
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

func check(f fixture, numbers map[string]int, items []item, repo, ynf, cfg string) (string, error) {
	n, ok := numbers[f.Title]
	if !ok {
		return "", fmt.Errorf("no issue titled %q in the sandbox", f.Title)
	}
	i := slices.IndexFunc(items, func(it item) bool { return it.Number == n })
	if i < 0 {
		return "", fmt.Errorf("#%d was never tracked", n)
	}
	it := items[i]
	if !f.Expect.Result.accepts(it.State) {
		msg := fmt.Sprintf("#%d ended %s (%s), expected %s", n, it.State, it.Reason, strings.Join(f.Expect.Result, " or "))
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
	detail := fmt.Sprintf("#%d %s", n, it.State)
	if len(f.Expect.Result) > 1 {
		detail += fmt.Sprintf(" (one of %s)", strings.Join(f.Expect.Result, ", "))
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

	rp, err := sh("", ynf, "--config", cfg, "--format", "json", "replay", fmt.Sprintf("%s#%d", repo, n))
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
	if f.Expect.Crash {
		log, err := sh("", ynf, "--config", cfg, "items", "log", fmt.Sprintf("%s#%d", repo, n))
		if err != nil {
			return "", fmt.Errorf("items log: %w", err)
		}
		if !strings.Contains(log, "did not finish") {
			return "", fmt.Errorf("#%d: no restart after the crash in its log", n)
		}
		detail += ", restarted after the crash"
	}
	return detail, nil
}

// memoryNamespace is where the sandbox's memories go; {repo} is owner/name.
const memoryNamespace = "factory/{repo}"

func namespaceFor(repo string) string { return strings.ReplaceAll(memoryNamespace, "{repo}", repo) }

type memoryRecord struct {
	MemoryID string `json:"memoryId"`
	Current  struct {
		Namespace  string         `json:"namespace"`
		Subject    string         `json:"subject"`
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

// checkMemory checks an item that ran left its memories: a ynf.step.v1 memory for its last run,
// and a ynf.failure.v1 memory for each failure signature it counted.
func checkMemory(f fixture, numbers map[string]int, items []item, memories []memoryRecord) (string, error) {
	i := slices.IndexFunc(items, func(it item) bool { return it.Number == numbers[f.Title] })
	if i < 0 || items[i].LastRun == nil {
		return "", nil // nothing ran, so nothing to remember
	}
	it := items[i]
	has := func(schema, subject string, match func(map[string]any) bool) bool {
		return slices.ContainsFunc(memories, func(m memoryRecord) bool {
			return m.Current.DataSchema == schema && m.Current.Subject == subject && match(m.Current.Data)
		})
	}
	if !has("ynf.step.v1", it.Key, func(d map[string]any) bool { return d["run_id"] == it.LastRun.ID }) {
		return "", fmt.Errorf("#%d: no ynf.step.v1 memory for its last run %s", it.Number, it.LastRun.ID)
	}
	n := 1
	for sig := range it.Counters {
		if !strings.HasPrefix(sig, "sig/") {
			continue
		}
		if !has("ynf.failure.v1", sig, func(d map[string]any) bool { return d["item"] == it.Key }) {
			return "", fmt.Errorf("#%d: no ynf.failure.v1 memory for %s", it.Number, sig)
		}
		n++
	}
	if n == 1 {
		return ", its memory written", nil
	}
	return fmt.Sprintf(", its %d memories written", n), nil
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
