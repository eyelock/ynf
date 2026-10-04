// Package config loads ynf's own config.yaml (ADR-009): found in the home factory folder by the
// same lookup as lanes, validated against the published schema, with defaults applied.
package config

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/eyelock/ynf"
	"github.com/eyelock/ynf/internal/policy"
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
	Images struct {
		Build *bool `yaml:"build"`
	} `yaml:"images"`
	Memory *struct {
		Provider            string `yaml:"provider"`
		Namespace           string `yaml:"namespace"`
		ContextBudgetTokens int    `yaml:"context_budget_tokens"`
		Cwd                 string `yaml:"cwd"`
	} `yaml:"memory"`

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
	return &c, nil
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

// MemorySettings returns whether memory is wanted (nil means detect), the namespace template, the
// budget and ynm's working directory.
func (c *Config) MemorySettings() (enabled *bool, namespace string, budget int, cwd string) {
	namespace, budget = "factory/{repo}", 1000
	m := c.Memory
	if m == nil {
		return nil, namespace, budget, ""
	}
	if m.Provider != "" {
		on := m.Provider == "ynm"
		enabled = &on
	}
	if m.Namespace != "" {
		namespace = m.Namespace
	}
	if m.ContextBudgetTokens > 0 {
		budget = m.ContextBudgetTokens
	}
	if m.Cwd != "" {
		cwd = c.rel(m.Cwd)
	}
	return enabled, namespace, budget, cwd
}
