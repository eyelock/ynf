// Package config loads ynf's own config.yaml (ADR-009): found in the home factory folder by the
// same lookup as lanes, validated against the published schema, with defaults applied.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/eyelock/ynf"
	"github.com/eyelock/ynf/internal/policy"
	"github.com/eyelock/ynf/internal/spool"
	"gopkg.in/yaml.v3"
)

// File is the config file name in a factory folder.
const File = "config.yaml"

// Config is a loaded config.
type Config struct {
	Version int      `yaml:"version"`
	Repos   []string `yaml:"repos"`
	// Factory names the configuration repository, which enrols repositories instead of Repos.
	Factory *struct {
		Repo string `yaml:"repo"`
	} `yaml:"factory"`
	Store   string `yaml:"store"`
	WorkDir string `yaml:"work_dir"`
	Owner   string `yaml:"owner"`
	GitHub  struct {
		TokenEnv string `yaml:"token_env"`
		Author   *struct {
			Name  string `yaml:"name"`
			Email string `yaml:"email"`
		} `yaml:"author"`
	} `yaml:"github"`
	Lease struct {
		TTL       string `yaml:"ttl"`
		Heartbeat string `yaml:"heartbeat"`
	} `yaml:"lease"`
	Poll struct {
		CI     string `yaml:"ci"`
		Review string `yaml:"review"`
	} `yaml:"poll"`
	// Executor inline declares this instance runs inside containment the operator provides, such
	// as the factory image as a job (ADR-007): every run is inline, as InlineUser.
	Executor   string `yaml:"executor"`
	InlineUser string `yaml:"inline_user"`
	Images     struct {
		Build *bool `yaml:"build"`
	} `yaml:"images"`
	Memory *struct {
		Provider  string `yaml:"provider"`
		Namespace string `yaml:"namespace"`
		Cwd       string `yaml:"cwd"`
		// Transport is cli (the ynm CLI, a store chosen by cwd) or http (a hosted ynm at
		// Endpoint, with the bearer token in TokenEnv): the shared store for a pool or CI.
		Transport string `yaml:"transport"`
		Endpoint  string `yaml:"endpoint"`
		TokenEnv  string `yaml:"token_env"`
		// Level is personal or distributed; over http it defaults to distributed, since a hosted
		// store keeps nothing at the personal level.
		Level string `yaml:"level"`
	} `yaml:"memory"`

	// Telemetry is where ynf and its runs write OpenTelemetry, and whether ynf starts ynr serve for
	// a factory job (ADR-009, ADR-011). Without it, ynf writes where the environment says.
	Telemetry *struct {
		// Spool is the spool root: factory/ for ynf, runs/<run id>/ for each run, and
		// manifests/<run id>.json.
		Spool string `yaml:"spool"`
		// RunQuota is how large a run's folder may grow, such as 64MiB.
		RunQuota  string `yaml:"run_quota"`
		Collector *struct {
			Enabled  bool   `yaml:"enabled"`
			ID       string `yaml:"id"`
			Instance string `yaml:"instance"`
			Upstream string `yaml:"upstream"`
			// Archive is how long ynr serve has at a job's end to ship what is left.
			Archive string `yaml:"archive"`
		} `yaml:"collector"`
	} `yaml:"telemetry"`

	Path     string   `yaml:"-"` // the file it was loaded from
	Shadowed []string `yaml:"-"` // other candidates found at the same level
}

// Find returns the config file in the first home factory folder that has one.
func Find(home string) (path string, shadowed []string, err error) {
	dir, shadowed := policy.Resolve(func(d string) bool {
		_, err := os.Stat(filepath.Join(home, d, File))
		return err == nil
	})
	if dir == "" {
		return "", nil, fmt.Errorf("no %s in any of %v under %s (or pass --config)", File, policy.FactoryDirs, home)
	}
	sh := make([]string, len(shadowed))
	for i, s := range shadowed {
		sh[i] = filepath.Join(home, s, File)
	}
	return filepath.Join(home, dir, File), sh, nil
}

// Load reads and validates a config file and applies defaults.
func Load(path string) (*Config, error) {
	doc, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := policy.ValidateYAML(ynf.ConfigSchema, "https://eyelock.github.io/ynf/schema/config.schema.json", doc); err != nil {
		return nil, fmt.Errorf("%s does not match the schema:\n%w", path, err)
	}
	var c Config
	if err := yaml.Unmarshal(doc, &c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	c.Path = abs
	def := func(p *string, v string) {
		if *p == "" {
			*p = v
		}
	}
	def(&c.Store, "sqlite://state.db")
	def(&c.WorkDir, "work")
	def(&c.GitHub.TokenEnv, "GITHUB_TOKEN")
	def(&c.Lease.TTL, "90s")
	def(&c.Lease.Heartbeat, "30s")
	def(&c.Poll.CI, "30s")
	def(&c.Poll.Review, "5m")
	if c.Owner == "" {
		host, _ := os.Hostname()
		c.Owner = fmt.Sprintf("ynf@%s/%d", host, os.Getpid())
	}
	if _, err := c.TelemetrySettings(os.Getenv); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &c, nil
}

// TelemetrySettings is the telemetry block, resolved: the spool root as an absolute path, the run
// quota, and the collector with its upstream filled from the operator's OpenTelemetry environment
// when the block names none. getenv reads that environment. It is an error for the collector to be
// enabled with no upstream: ynr serve has nowhere to ship until it has an object store of its own.
func (c *Config) TelemetrySettings(getenv func(string) string) (TelemetrySettings, error) {
	var t TelemetrySettings
	b := c.Telemetry
	if b == nil {
		return t, nil
	}
	if b.Spool != "" {
		t.Root = c.rel(b.Spool)
	}
	if b.RunQuota != "" {
		q, err := ParseSize(b.RunQuota)
		if err != nil {
			return t, fmt.Errorf("telemetry.run_quota: %w", err)
		}
		t.Quota = q
	}
	col := b.Collector
	if col == nil || !col.Enabled {
		return t, nil
	}
	if t.Root == "" {
		return t, errors.New("telemetry.collector is enabled, and it reads a spool: set telemetry.spool")
	}
	t.Collector = spool.Collector{Enabled: true, ID: col.ID, Instance: col.Instance, Upstream: col.Upstream, Archive: spool.DefaultArchive}
	if col.Archive != "" {
		d, err := time.ParseDuration(col.Archive)
		if err != nil || d <= 0 {
			return t, fmt.Errorf("telemetry.collector.archive %q is not a duration", col.Archive)
		}
		t.Collector.Archive = d
	}
	if t.Collector.Upstream == "" {
		t.Collector.Upstream = spool.UpstreamFromEnv(getenv)
	}
	if t.Collector.Upstream == "" {
		return t, errors.New("telemetry.collector is enabled with no upstream: ynr serve needs somewhere to ship until it has an object store of its own. " +
			"Set telemetry.collector.upstream, or OTEL_EXPORTER_OTLP_ENDPOINT in the environment")
	}
	return t, nil
}

// TelemetrySettings is the telemetry block, resolved.
type TelemetrySettings struct {
	Root      string // the spool root, absolute; empty when none is configured
	Quota     int64  // bytes a run's folder may hold; 0 is the default
	Collector spool.Collector
}

// ParseSize reads a size such as 64MiB: a whole number and KiB, MiB or GiB.
func ParseSize(s string) (int64, error) {
	for _, u := range []struct {
		suffix string
		mult   int64
	}{{"KiB", 1 << 10}, {"MiB", 1 << 20}, {"GiB", 1 << 30}} {
		if n, ok := strings.CutSuffix(s, u.suffix); ok {
			v, err := strconv.ParseInt(n, 10, 64)
			if err != nil || v <= 0 {
				break
			}
			return v * u.mult, nil
		}
	}
	return 0, fmt.Errorf("%q is not a size: want a whole number and KiB, MiB or GiB", s)
}

// Dir is the folder the config was loaded from; relative paths in it resolve against this.
func (c *Config) Dir() string { return filepath.Dir(c.Path) }

func (c *Config) rel(p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	return filepath.Join(c.Dir(), p)
}

// SQLitePath returns the database path for a sqlite:// store.
func (c *Config) SQLitePath() (string, error) {
	p, ok := strings.CutPrefix(c.Store, "sqlite://")
	if !ok {
		return "", fmt.Errorf("store %s is not sqlite://", c.Store)
	}
	return c.rel(p), nil
}

// StoreKind is the store's scheme: sqlite, s3 or dynamodb.
func (c *Config) StoreKind() string {
	kind, _, _ := strings.Cut(c.Store, "://")
	return kind
}

// WorkPath is the absolute work folder.
func (c *Config) WorkPath() string { return c.rel(c.WorkDir) }

// Durations returns the lease TTL, heartbeat and poll intervals.
func (c *Config) Durations() (ttl, heartbeat, ci, review time.Duration, err error) {
	ds := []*time.Duration{&ttl, &heartbeat, &ci, &review}
	for i, s := range []string{c.Lease.TTL, c.Lease.Heartbeat, c.Poll.CI, c.Poll.Review} {
		if *ds[i], err = time.ParseDuration(s); err != nil {
			return
		}
	}
	if heartbeat >= ttl {
		err = fmt.Errorf("lease.heartbeat (%s) must be shorter than lease.ttl (%s)", heartbeat, ttl)
	}
	return
}

// Token returns the GitHub token from the configured variable, falling back to `gh auth token`.
func (c *Config) Token() (string, error) {
	if t := os.Getenv(c.GitHub.TokenEnv); t != "" {
		return t, nil
	}
	out, err := exec.Command("gh", "auth", "token").Output()
	if err != nil {
		return "", fmt.Errorf("no GitHub token: set %s or log in with `gh auth login`", c.GitHub.TokenEnv)
	}
	t := strings.TrimSpace(string(out))
	if t == "" {
		return "", errors.New("`gh auth token` returned nothing")
	}
	return t, nil
}

// Author returns the git identity for ynf's commits.
func (c *Config) Author() (name, email string) {
	if a := c.GitHub.Author; a != nil {
		return a.Name, a.Email
	}
	return "ynf", "ynf@users.noreply.github.com"
}

// MemorySettings returns whether memory is wanted (nil means detect), the namespace template
// ({repo} is host/owner/name), and the folder ynm runs from, which picks its store.
func (c *Config) MemorySettings() (enabled *bool, namespace, cwd string) {
	namespace = "factory/{repo}"
	m := c.Memory
	if m == nil {
		return nil, namespace, ""
	}
	if m.Provider != "" {
		on := m.Provider == "ynm"
		enabled = &on
	}
	if m.Namespace != "" {
		namespace = m.Namespace
	}
	if m.Cwd != "" {
		cwd = c.rel(m.Cwd)
	}
	return enabled, namespace, cwd
}

// MemoryTransport returns how ynf reaches ynm (cli or http), the endpoint and token variable for
// http, and the level it writes at.
func (c *Config) MemoryTransport() (transport, endpoint, tokenEnv, level string) {
	transport = "cli"
	m := c.Memory
	if m == nil {
		return transport, "", "", ""
	}
	if m.Transport != "" {
		transport = m.Transport
	}
	level = m.Level
	if transport == "http" && level == "" {
		level = "distributed"
	}
	return transport, m.Endpoint, m.TokenEnv, level
}
