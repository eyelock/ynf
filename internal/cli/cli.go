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
	"os"
	"strings"
	"sync"
	"time"

	"github.com/eyelock/ynf"
	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/engine"
	"github.com/eyelock/ynf/internal/forge"
	"github.com/eyelock/ynf/internal/store/sqlite"
	"github.com/eyelock/ynf/internal/workspace"
	"github.com/oklog/ulid/v2"
)

// Exit codes (ADR-011).
const (
	ExitOK          = 0
	ExitUsage       = 2
	ExitAdapter     = 20 // the forge, git, docker or the store failed
	ExitPolicy      = 30 // config or lanes invalid
	ExitUnsettled   = 31 // sweep --until-settled timed out
	ExitDifferences = 32 // replay found decisions that differ
)

const usage = `ynf: the outer loop around agent runs

Usage:
  ynf version
  ynf doctor
  ynf lanes validate [--file lanes.yaml]
  ynf lanes show --repo owner/name [lane]
  ynf sweep [--until-settled] [--timeout 20m] [--interval 15s] [--lane name]...
  ynf serve [--interval 1m] [--listen :8080 [--webhook-secret-env YNF_WEBHOOK_SECRET]] [--lane name]...
  ynf step --github-event <file> --github-event-name <name>      (CI: GITHUB_EVENT_PATH, GITHUB_EVENT_NAME)
  ynf items ls
  ynf items show|log|retry|release <owner/name#number | key>
  ynf replay <owner/name#number | key> [--policy lanes.yaml]
  ynf pause|resume <lane> --reason <text> [--repo owner/name]
  ynf stats [--lane name]...
  ynf egress-proxy --allow host,*.domain [--listen :3128] [--log file]   (run inside a container)

Global flags (before the command):
  --config <path>   config file (default: config.yaml in ~/.agents/factory/ or a fallback)
  --format text|json
  --interactive     allow the uncontained process executor (ADR-007)
  -v                debug logging
`

type app struct {
	stdout, stderr io.Writer
	cfgPath        string
	format         string
	interactive    bool
	verbose        bool
	lanes          multi

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
	case "step":
		err = a.step(ctx, rest)
	case "pause", "resume":
		err = a.pause(ctx, cmd, rest)
	case "stats":
		err = a.stats(ctx, rest)
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
		_ = a.eng.Store.Close()
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
	db, err := c.SQLitePath()
	if err != nil {
		return nil, withCode(ExitPolicy, err)
	}
	st, err := sqlite.Open(db)
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
	level := slog.LevelInfo
	if a.verbose {
		level = slog.LevelDebug
	}
	var mu sync.Mutex
	entropy := ulid.Monotonic(cryptoReader{}, 0)
	mem, ns, budget := memoryFor(c)
	a.eng = &engine.Engine{
		Memory:          mem,
		MemoryNamespace: ns,
		MemoryBudget:    budget,
		Store:           st, Forge: fg,
		Git:         workspace.Workspace{Root: c.WorkPath(), Token: token, Author: workspace.Author{Name: name, Email: email}},
		Executor:    a.executor,
		BuildImage:  imageBuilder(),
		Repos:       c.Repos,
		Lanes:       a.lanes,
		WorkDir:     c.WorkPath(),
		Owner:       c.Owner,
		LeaseTTL:    ttl,
		Heartbeat:   hb,
		Poll:        decide.Poll{CI: ci, Review: review},
		RunTimeout:  time.Hour,
		Interactive: a.interactive,
		Now:         func() time.Time { return time.Now().UTC() },
		NewID: func() string {
			mu.Lock()
			defer mu.Unlock()
			return ulid.MustNew(ulid.Now(), entropy).String()
		},
		Log: slog.New(slog.NewTextHandler(a.stderr, &slog.HandlerOptions{Level: level})),
	}
	return a.eng, nil
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
