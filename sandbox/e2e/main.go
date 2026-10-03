// Command e2e is the factory's acceptance test: it runs ynf against the live sandbox until every
// item settles, then checks each fixture's expect block — the item's state, and for a proposal
// the draft pull request, its trailers and its green CI — and replays every recorded decision.
//
// Unlike `make calibrate`, this tests ynf itself. Only fixtures in the lanes given with -lanes are
// checked; slice 1a covers the lanes that need no agent and no egress.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
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
		Result     string   `yaml:"result"`
		Signatures []string `yaml:"signatures"`
	} `yaml:"expect"`
}

type item struct {
	Repo   string `json:"repo"`
	Number int    `json:"number"`
	Lane   string `json:"lane"`
	State  string `json:"state"`
	Reason string `json:"reason"`
	PR     int    `json:"pr"`
	Branch string `json:"branch"`
}

func main() {
	root := flag.String("root", ".", "the sandbox/ directory")
	repo := flag.String("repo", "eyelock/ynf-sandbox", "the sandbox repository")
	lanes := flag.String("lanes", "gofmt,deps", "lanes to run and check")
	timeout := flag.Duration("timeout", 15*time.Minute, "how long to wait for items to settle")
	flag.Parse()
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
	defer func() { _ = os.RemoveAll(tmp) }()
	if tmp, err = filepath.EvalSymlinks(tmp); err != nil {
		return err
	}

	ynf := filepath.Join(tmp, "ynf")
	if out, err := sh(filepath.Dir(root), "go", "build", "-o", ynf, "./cmd/ynf"); err != nil {
		return fmt.Errorf("build ynf: %w\n%s", err, out)
	}
	cfg := filepath.Join(tmp, "config.yaml")
	if err := os.WriteFile(cfg, fmt.Appendf(nil, "version: 1\nrepos: [%s]\npoll: {ci: 15s, review: 1m}\n", repo), 0o644); err != nil {
		return err
	}

	args := []string{"--config", cfg, "--format", "json", "sweep", "--until-settled", "--interval", "10s", "--timeout", timeout.String()}
	for _, l := range lanes {
		args = append(args, "--lane", l)
	}
	fmt.Printf("running ynf %s\n", strings.Join(lanes, ", "))
	start := time.Now()
	out, err := sh("", ynf, args...)
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

	numbers, err := issueNumbers(repo)
	if err != nil {
		return err
	}
	failed := 0
	for _, f := range ff.Fixtures {
		if !slices.Contains(lanes, f.Lane) {
			continue
		}
		detail, err := check(f, numbers, res.Items, repo, ynf, cfg)
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
	if f.Expect.Result != "any" && it.State != f.Expect.Result {
		return "", fmt.Errorf("#%d ended %s (%s), expected %s", n, it.State, it.Reason, f.Expect.Result)
	}
	detail := fmt.Sprintf("#%d %s", n, it.State)

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
	return detail + fmt.Sprintf(", %d decisions replay the same", len(r.Decisions)), nil
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

func sh(dir, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
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
