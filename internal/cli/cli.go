// Package cli is the ynf command line (ADR-011). Every command takes --format json and returns one
// object; exit codes follow ADR-011.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynf"
	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/runner"
	"github.com/eyelock/ynf/internal/store"
	"github.com/eyelock/ynf/internal/store/s3store"
	"github.com/eyelock/ynf/internal/store/sqlite"
	"github.com/eyelock/ynf/internal/tracker"
	"github.com/eyelock/ynf/internal/tracker/mcptracker"
	"github.com/eyelock/ynf/internal/workspace"
	"github.com/oklog/ulid/v2"
)

// Exit codes (ADR-011).
const (
	ExitOK           = 0
	ExitUsage        = 2
	ExitAdapter      = 20 // the forge, git, docker or the store failed
	ExitPolicy       = 30 // config or lanes invalid
	ExitUnsettled    = 31 // sweep --until-settled timed out
	ExitDifferences  = 32 // replay found decisions that differ
	ExitStartRefused = 33 // start refused the item before creating anything
)

const usage = `ynf: the outer loop around agent runs

Usage:
  ynf version
  ynf doctor
  ynf lanes validate [--file lanes.yaml] [--repo owner/name]
  ynf lanes show --repo owner/name [lane]
  ynf sweep [--until-settled] [--timeout 20m] [--interval 15s] [--lane name]...
  ynf serve [--interval 1m] [--listen :8080 [--webhook-secret-env YNF_WEBHOOK_SECRET] [--start-token-env YNF_START_TOKEN]] [--lane name]...
  ynf start <ref> [--repo host/owner/name] [--lane name] [--auto-approve edits|all] [--detach]
  ynf start --prompt <text> [--label prefix:value]... --repo owner/name [--lane name] [--auto-approve edits|all] [--detach]
  ynf handle --github-event <file> --github-event-name <name> [--lane name]...    (CI: GITHUB_EVENT_PATH, GITHUB_EVENT_NAME)
  ynf items ls
  ynf items show|log|retry|release <owner/name#number | key>
  ynf replay <owner/name#number | key> [--policy lanes.yaml]
  ynf pause|resume <lane> --reason <text> [--repo owner/name]
  ynf stats [--lane name]...
  ynf forges                    the forges ynf works with, each checked
  ynf trackers                  the trackers ynf works with, each checked
  ynf ticket <ref>              read a ticket as start would, without starting it
  ynf harness [repo]...         how each lane runs, and the harness it is held to
  ynf egress-proxy --allow host,*.domain [--listen :3128] [--log file]   (run inside a container)

Global flags (before the command):
  --config <path>   config file (default: config.yaml in ~/.agents/factory/ or a fallback)
  --format text|json
  --interactive     allow the uncontained process executor (ADR-007)
  --log-file <path> also write the log to this file (YNF_LOG_FILE)
  --log-format text|json   (YNF_LOG_FORMAT)
  -v                debug logging
`

type app struct {
	stdout, stderr io.Writer
	cfgPath        string
	format         string
	interactive    bool
	verbose        bool
	logFile        string
	logFormat      string
	lanes          multi
	logClose       func()

	cfg *config.Config
	eng *engine.Engine
}

type multi []string

func (m *multi) String() string     { return strings.Join(*m, ",") }
func (m *multi) Set(v string) error { *m = append(*m, v); return nil }

// Run runs the command line and returns the exit code.
func Run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	a := &app{stdout: stdout, stderr: stderr}
	fs := flag.NewFlagSet("ynf", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() { _, _ = fmt.Fprint(stderr, usage) }
	fs.StringVar(&a.cfgPath, "config", os.Getenv("YNF_CONFIG"), "")
	fs.StringVar(&a.format, "format", envOr("YNF_FORMAT", "text"), "")
	fs.BoolVar(&a.interactive, "interactive", false, "")
	fs.BoolVar(&a.verbose, "v", false, "")
	fs.StringVar(&a.logFile, "log-file", os.Getenv("YNF_LOG_FILE"), "")
	fs.StringVar(&a.logFormat, "log-format", envOr("YNF_LOG_FORMAT", "text"), "")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return ExitUsage
	}
	cmd, rest := rest[0], rest[1:]
	var err error
	switch cmd {
	case "version":
		err = a.out(map[string]string{"version": ynf.Version}, "ynf "+ynf.Version)
	case "doctor":
		err = a.doctor(ctx)
	case "lanes":
		err = a.lanesCmd(ctx, rest)
	case "sweep":
		err = a.sweep(ctx, rest, false)
	case "serve":
		err = a.sweep(ctx, rest, true)
	case "items":
		err = a.items(ctx, rest)
	case "replay":
		err = a.replay(ctx, rest)
	case "start":
		err = a.start(ctx, rest)
	case "handle":
		err = a.handle(ctx, rest)
	case "pause", "resume":
		err = a.pause(ctx, cmd, rest)
	case "stats":
		err = a.stats(ctx, rest)
	case "forges":
		err = a.connections(ctx, "forge", rest)
	case "trackers":
		err = a.connections(ctx, "tracker", rest)
	case "ticket":
		err = a.ticket(ctx, rest)
	case "harness":
		err = a.harness(ctx, rest)
	case "egress-proxy":
		err = a.egressProxy(ctx, rest)
	case "help", "-h", "--help":
		_, _ = fmt.Fprint(stdout, usage)
		return ExitOK
	default:
		_, _ = fmt.Fprintf(stderr, "ynf: unknown command %q\n\n%s", cmd, usage)
		return ExitUsage
	}
	if a.eng != nil {
		a.eng.CloseTrackers()
		_ = a.eng.Store.Close()
	}
	if a.logClose != nil {
		a.logClose()
	}
	return a.exit(err)
}

// codedError carries an exit code.
type codedError struct {
	code int
	err  error
}

func (e codedError) Error() string { return e.err.Error() }
func (e codedError) Unwrap() error { return e.err }

func withCode(code int, err error) error {
	if err == nil {
		return nil
	}
	return codedError{code, err}
}

func (a *app) exit(err error) int {
	if err == nil {
		return ExitOK
	}
	code := ExitAdapter
	var ce codedError
	if errors.As(err, &ce) {
		code = ce.code
	}
	if a.format == "json" {
		_ = json.NewEncoder(a.stdout).Encode(map[string]any{"error": map[string]any{"code": code, "message": err.Error()}})
	} else {
		_, _ = fmt.Fprintln(a.stderr, "ynf:", err)
	}
	return code
}

// out prints v as JSON, or text as text.
func (a *app) out(v any, text string) error {
	if a.format == "json" {
		enc := json.NewEncoder(a.stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	}
	_, err := fmt.Fprintln(a.stdout, strings.TrimRight(text, "\n"))
	return err
}

func (a *app) loadConfig() error {
	if a.cfg != nil {
		return nil
	}
	path := a.cfgPath
	if path == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		p, shadowed, err := config.Find(home)
		if err != nil {
			return withCode(ExitPolicy, err)
		}
		for _, s := range shadowed {
			_, _ = fmt.Fprintf(a.stderr, "ynf: warning: %s is shadowed by %s\n", s, p)
		}
		path = p
	}
	c, err := config.Load(path)
	if err != nil {
		return withCode(ExitPolicy, err)
	}
	if a.cfgPath == "" {
		home, _ := os.UserHomeDir()
		_, c.Shadowed, _ = config.Find(home)
	}
	a.cfg = c
	return nil
}

// logger writes structured logs to stderr as they happen and, with --log-file, to that file too:
// text (logfmt) for people, json (one object per line) for tools.
func (a *app) logger() (*slog.Logger, error) {
	level := slog.LevelInfo
	if a.verbose {
		level = slog.LevelDebug
	}
	w := a.stderr
	if a.logFile != "" {
		f, err := os.OpenFile(a.logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return nil, fmt.Errorf("--log-file: %w", err)
		}
		a.logClose = func() { _ = f.Close() }
		w = io.MultiWriter(a.stderr, f)
	}
	opts := &slog.HandlerOptions{Level: level}
	switch a.logFormat {
	case "text":
		return slog.New(slog.NewTextHandler(w, opts)), nil
	case "json":
		return slog.New(slog.NewJSONHandler(w, opts)), nil
	}
	return nil, fmt.Errorf("--log-format %q: want text or json", a.logFormat)
}

// openStore opens the configured store (ADR-004).
func openStore(ctx context.Context, c *config.Config) (store.Store, error) {
	switch c.StoreKind() {
	case "sqlite":
		db, err := c.SQLitePath()
		if err != nil {
			return nil, withCode(ExitPolicy, err)
		}
		return sqlite.Open(db)
	case "s3":
		return s3store.Open(ctx, c.Store)
	}
	return nil, withCode(ExitPolicy, fmt.Errorf("store %s: dynamodb:// is not built yet", c.Store))
}

// engine builds the engine from config.
func (a *app) engine() (*engine.Engine, error) {
	if a.eng != nil {
		return a.eng, nil
	}
	if err := a.loadConfig(); err != nil {
		return nil, err
	}
	c := a.cfg
	ttl, hb, ci, review, err := c.Durations()
	if err != nil {
		return nil, withCode(ExitPolicy, err)
	}
	st, err := openStore(context.Background(), c)
	if err != nil {
		return nil, err
	}
	token, err := c.Token()
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	fg, err := forge.NewGitHub(token, os.Getenv("YNF_GITHUB_API"))
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	name, email := c.Author()
	logger, err := a.logger()
	if err != nil {
		_ = st.Close()
		return nil, withCode(ExitUsage, err)
	}
	var mu sync.Mutex
	entropy := ulid.Monotonic(cryptoReader{}, 0)
	mem, ns, level, err := memoryFor(c)
	if err != nil {
		_ = st.Close()
		return nil, withCode(ExitPolicy, err)
	}
	a.eng = &engine.Engine{
		Memory:          mem,
		MemoryNamespace: ns,
		MemoryLevel:     level,
		Store:           st, Forge: fg, ForgeHost: fg.Host(),
		Trackers:          map[string]tracker.Tracker{fg.Host(): forge.IssueTracker(fg)},
		Git:               workspace.Workspace{Root: c.WorkPath(), Token: token, Author: workspace.Author{Name: name, Email: email}},
		NewForge:          newForge(c.WorkPath(), workspace.Author{Name: name, Email: email}, githubAPI),
		NewTracker:        newTracker,
		Executor:          a.executor,
		BuildImage:        imageBuilder(c.Images.Build == nil || *c.Images.Build),
		ImageHarness:      imageHarness,
		ImageCapabilities: imageCapabilities,
		HostCapabilities:  hostCapabilities,
		DetectYnh:         runner.DetectedYnh,
		Repos:             c.Repos,
		ConfigRepo:        configRepo(c),
		Lanes:             a.lanes,
		WorkDir:           c.WorkPath(),
		Owner:             c.Owner,
		LeaseTTL:          ttl,
		Heartbeat:         hb,
		Poll:              decide.Poll{CI: ci, Review: review},
		RunTimeout:        time.Hour,
		Interactive:       a.interactive,
		Now:               func() time.Time { return time.Now().UTC() },
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			return ulid.MustNew(ulid.Now(), entropy).String()
		},
		Log: logger,
	}
	return a.eng, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

// configRepo is the configuration repository the config names, as owner/name on the forge.
func configRepo(c *config.Config) string {
	if c.Factory == nil {
		return ""
	}
	if parts := strings.Split(c.Factory.Repo, "/"); len(parts) == 3 {
		return parts[1] + "/" + parts[2]
	}
	return c.Factory.Repo
}

// githubAPI is a GitHub instance's API: api.github.com for the public service, /api/v3 on an
// Enterprise Server.
func githubAPI(host string) string {
	if host == "github.com" {
		return ""
	}
	return "https://" + host + "/api/v3/"
}

// newForge builds the forge instances a configuration repository declares (ADR-003): a client,
// a git workspace that clones and pushes with the forge's own token, and the tracker for its
// issues.
func newForge(root string, author workspace.Author, api func(host string) string) func(string, map[string]any) (engine.ForgeInstance, error) {
	return func(name string, cfg map[string]any) (engine.ForgeInstance, error) {
		raw, _ := cfg["url"].(string)
		u, err := url.Parse(raw)
		if err != nil || u.Hostname() == "" {
			return engine.ForgeInstance{}, fmt.Errorf("url %q is not a web address", raw)
		}
		host := u.Hostname()
		env, _ := cfg["token_env"].(string)
		token := os.Getenv(env)
		if token == "" {
			return engine.ForgeInstance{}, fmt.Errorf("its token is not set: %s is empty", env)
		}
		fg, err := forge.NewGitHub(token, api(host))
		if err != nil {
			return engine.ForgeInstance{}, err
		}
		ws := workspace.Workspace{Root: root, Token: token, Author: author, RemoteURL: func(repo string) string {
			return "https://" + host + "/" + strings.TrimPrefix(repo, host+"/") + ".git"
		}}
		return engine.ForgeInstance{Host: host, Forge: fg, Git: ws, Tracker: forge.IssueTracker(fg)}, nil
	}
}

// newTracker builds a tracker the configuration repository declares (ADR-003). GitHub's issues
// come with its forge (forges:), so a tracker here is one that is not a forge.
func newTracker(name string, cfg map[string]any) (string, tracker.Tracker, error) {
	switch p, _ := cfg["provider"].(string); p {
	case "mcp":
		c, err := mcptracker.Parse(cfg)
		if err != nil {
			return "", nil, err
		}
		t, err := mcptracker.New(c)
		if err != nil {
			return "", nil, err
		}
		return t.Host(), t, nil
	default:
		return "", nil, fmt.Errorf("provider %q: trackers here are mcp; GitHub's issues come with its forge", p)
	}
}
