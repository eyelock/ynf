// Package policy loads lane policy (ADR-006): .agents/factory/lanes.yaml, validated against the
// published schema, with defaults applied, a hash per lane for the decision record, CEL guards
// over structured facts, and placeholders filled only from labels.
package policy

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/eyelock/ynf"
	"gopkg.in/yaml.v3"
)

// FactoryDirs is where a repository keeps ynf's files, in lookup order (ADR-009). The first found
// wins; the others are shadowed and reported, never merged.
var FactoryDirs = []string{".agents/factory", ".ynh/ynf", ".ynm/ynf", ".ynf"}

// LanesFile is the name of the lane policy file in a factory folder.
const LanesFile = "lanes.yaml"

// File is a parsed lanes.yaml.
type File struct {
	Version  int             `yaml:"version" json:"version"`
	Defaults Defaults        `yaml:"defaults" json:"defaults"`
	Lanes    map[string]Lane `yaml:"lanes" json:"lanes"`
}

// Defaults are what every lane inherits unless it sets its own.
type Defaults struct {
	Executor  string  `yaml:"executor" json:"executor,omitempty"`
	Attempts  int     `yaml:"attempts" json:"attempts,omitempty"`
	Retention string  `yaml:"retention" json:"retention,omitempty"`
	Egress    *Egress `yaml:"egress" json:"egress,omitempty"`
	Stop      *Stop   `yaml:"stop" json:"stop,omitempty"`
	PR        *PR     `yaml:"pr" json:"pr,omitempty"`
	Labels    *Labels `yaml:"labels" json:"labels,omitempty"`
}

// Labels are what ynf writes on the ticket as the item enters a state (ADR-003, ADR-006).
type Labels struct {
	OnClaim    *LabelChange `yaml:"on_claim" json:"on_claim,omitempty"`
	OnPropose  *LabelChange `yaml:"on_propose" json:"on_propose,omitempty"`
	OnReview   *LabelChange `yaml:"on_review" json:"on_review,omitempty"`
	OnEscalate *LabelChange `yaml:"on_escalate" json:"on_escalate,omitempty"`
	OnDone     *LabelChange `yaml:"on_done" json:"on_done,omitempty"`
}

// LabelChange adds and removes labels.
type LabelChange struct {
	Add    []string `yaml:"add" json:"add,omitempty"`
	Remove []string `yaml:"remove" json:"remove,omitempty"`
}

// Lane is one lane, with defaults applied after Load.
type Lane struct {
	Name      string              `yaml:"-" json:"name"`
	Kind      string              `yaml:"kind" json:"kind"`
	Enabled   *bool               `yaml:"enabled" json:"enabled,omitempty"`
	Intake    []Intake            `yaml:"intake" json:"intake"`
	Guards    Guards              `yaml:"guards" json:"guards"`
	Run       Run                 `yaml:"run" json:"run"`
	When      map[string]Reaction `yaml:"when" json:"when"`
	PR        PR                  `yaml:"pr" json:"pr"`
	Stop      Stop                `yaml:"stop" json:"stop"`
	Executor  string              `yaml:"executor" json:"executor,omitempty"`
	Attempts  int                 `yaml:"attempts" json:"attempts,omitempty"`
	Retention string              `yaml:"retention" json:"retention,omitempty"`
	Labels    *Labels             `yaml:"labels" json:"labels,omitempty"`
}

// On reports whether the lane is enabled.
func (l Lane) On() bool { return l.Enabled == nil || *l.Enabled }

// Intake is one source of work for a lane.
type Intake struct {
	GitHubSearch string `yaml:"github.search" json:"github.search,omitempty"`
	JiraSearch   string `yaml:"jira.search" json:"jira.search,omitempty"`
	Every        string `yaml:"every" json:"every,omitempty"`
}

// Interval parses Every.
func (i Intake) Interval() time.Duration { d, _ := ParseDuration(i.Every); return d }

// Guards are CEL expressions over structured facts.
type Guards struct {
	Eligible string `yaml:"eligible" json:"eligible,omitempty"`
}

// Run is how a lane's runs are made.
type Run struct {
	Runner   string   `yaml:"runner" json:"runner,omitempty"`
	Executor string   `yaml:"executor" json:"executor,omitempty"`
	Image    string   `yaml:"image" json:"image,omitempty"`
	Egress   *Egress  `yaml:"egress" json:"egress,omitempty"`
	Env      []string `yaml:"env" json:"env,omitempty"`
	Ynh      *Ynh     `yaml:"ynh" json:"ynh,omitempty"`
	Command  *Command `yaml:"command" json:"command,omitempty"`
}

// Egress is what a run may reach (ADR-007).
type Egress struct {
	Allow []string `yaml:"allow" json:"allow"`
}

// Ynh is the ynh runner's settings.
type Ynh struct {
	Harness string `yaml:"harness" json:"harness"`
	Vendor  string `yaml:"vendor" json:"vendor,omitempty"`
	Base    string `yaml:"base" json:"base,omitempty"`
	Focus   string `yaml:"focus" json:"focus,omitempty"`
	Profile string `yaml:"profile" json:"profile,omitempty"`
	Sandbox string `yaml:"sandbox" json:"sandbox,omitempty"`
	// Model is ynh's --model: the model the agent runs on, as the vendor names it. Empty is the
	// vendor's default. The schema limits it to a plain name, so it never reaches argv as a flag.
	Model string `yaml:"model" json:"model,omitempty"`
	// Effort is ynh's --effort (low, medium or high): the reasoning effort the agent is asked to
	// run at, which ynh maps to each vendor's own setting and refuses where a vendor has none.
	// Empty is the harness's agent.effort, else the vendor's default. It needs ynh 0.10.0 or later.
	Effort string `yaml:"effort" json:"effort,omitempty"`
	// AutoApprove is ynh's --auto-approve (edits or all): the worker runs without approval prompts.
	// ynf passes it only to a contained run (ADR-007).
	AutoApprove string            `yaml:"auto_approve" json:"auto_approve,omitempty"`
	Budgets     *Budgets          `yaml:"budgets" json:"budgets,omitempty"`
	SensorScope map[string]string `yaml:"sensor_scope" json:"sensor_scope,omitempty"`
}

// Budgets may only tighten the harness's own.
type Budgets struct {
	MaxTurns  int    `yaml:"max_turns" json:"max_turns,omitempty"`
	MaxTokens int    `yaml:"max_tokens" json:"max_tokens,omitempty"`
	MaxWall   string `yaml:"max_wall" json:"max_wall,omitempty"`
}

// Command is the command runner's settings.
type Command struct {
	Argv       []string `yaml:"argv" json:"argv"`
	ResultFile string   `yaml:"result_file" json:"result_file,omitempty"`
}

// PR is how changes are proposed.
type PR struct {
	AllowedPaths   []string `yaml:"allowed_paths" json:"allowed_paths,omitempty"`
	ProtectedPaths []string `yaml:"protected_paths" json:"protected_paths,omitempty"`
	Draft          *bool    `yaml:"draft" json:"draft,omitempty"`
}

// IsDraft reports whether pull requests open as drafts (the default).
func (p PR) IsDraft() bool { return p.Draft == nil || *p.Draft }

// Stop holds stop conditions (ADR-010).
type Stop struct {
	YieldFloor        float64 `yaml:"yield_floor" json:"yield_floor,omitempty"`
	ReviewTimeCeiling string  `yaml:"review_time_ceiling" json:"review_time_ceiling,omitempty"`
	EscapedDefects    int     `yaml:"escaped_defects" json:"escaped_defects,omitempty"`
	MaxOpenProposals  int     `yaml:"max_open_proposals" json:"max_open_proposals,omitempty"`
	MinSample         int     `yaml:"min_sample" json:"min_sample,omitempty"`
}

// Reaction is what to do for an outcome: an action, a retry then an action, or a resume.
type Reaction struct {
	Action     string `json:"action,omitempty"`
	Retry      int    `json:"retry,omitempty"`
	Then       string `json:"then,omitempty"`
	ResumeWith string `json:"resume_with,omitempty"`
	Max        int    `json:"max,omitempty"`
}

// UnmarshalYAML accepts the schema's three shapes.
func (r *Reaction) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		r.Action = n.Value
		return nil
	}
	var m struct {
		Retry      int    `yaml:"retry"`
		Then       string `yaml:"then"`
		ResumeWith string `yaml:"resume_with"`
		Max        int    `yaml:"max"`
	}
	if err := n.Decode(&m); err != nil {
		return err
	}
	*r = Reaction{Retry: m.Retry, Then: m.Then, ResumeWith: m.ResumeWith, Max: m.Max}
	return nil
}

// Load validates lanes.yaml against the published schema and returns it with defaults applied.
func Load(doc []byte) (*File, error) {
	if err := ValidateYAML(ynf.LanesSchema, "https://eyelock.github.io/ynf/schema/lanes.schema.json", doc); err != nil {
		return nil, fmt.Errorf("lanes.yaml does not match the schema:\n%w", err)
	}
	var f File
	if err := yaml.Unmarshal(doc, &f); err != nil {
		return nil, fmt.Errorf("lanes.yaml: %w", err)
	}
	for name, l := range f.Lanes {
		l.Name = name
		f.Lanes[name] = f.Defaults.apply(l)
	}
	return &f, nil
}

func (d Defaults) apply(l Lane) Lane {
	if l.Executor == "" {
		l.Executor = d.Executor
	}
	if l.Run.Executor == "" {
		l.Run.Executor = l.Executor
	}
	if l.Run.Executor == "" {
		l.Run.Executor = "docker"
	}
	if l.Attempts == 0 {
		l.Attempts = d.Attempts
	}
	if l.Attempts == 0 {
		l.Attempts = 3
	}
	if l.Retention == "" {
		l.Retention = d.Retention
	}
	if l.Run.Egress == nil {
		l.Run.Egress = d.Egress
	}
	if d.Labels != nil {
		lb := Labels{}
		if l.Labels != nil {
			lb = *l.Labels
		}
		for _, p := range []struct{ lane, def **LabelChange }{
			{&lb.OnClaim, &d.Labels.OnClaim}, {&lb.OnPropose, &d.Labels.OnPropose}, {&lb.OnReview, &d.Labels.OnReview},
			{&lb.OnEscalate, &d.Labels.OnEscalate}, {&lb.OnDone, &d.Labels.OnDone},
		} {
			if *p.lane == nil {
				*p.lane = *p.def
			}
		}
		l.Labels = &lb
	}
	if l.Run.Egress == nil {
		l.Run.Egress = &Egress{}
	}
	if d.Stop != nil && l.Stop == (Stop{}) {
		l.Stop = *d.Stop
	}
	if d.PR != nil {
		if l.PR.AllowedPaths == nil {
			l.PR.AllowedPaths = d.PR.AllowedPaths
		}
		if l.PR.ProtectedPaths == nil {
			l.PR.ProtectedPaths = d.PR.ProtectedPaths
		}
		if l.PR.Draft == nil {
			l.PR.Draft = d.PR.Draft
		}
	}
	if l.When == nil {
		l.When = map[string]Reaction{}
	}
	return l
}

// Hash is the SHA-256 of the lane's normalised form, recorded with every decision (ADR-006).
func (l Lane) Hash() string {
	b, _ := json.Marshal(l) // maps marshal with sorted keys, so this is stable
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Names returns lane names, sorted.
func (f *File) Names() []string { return slices.Sorted(maps.Keys(f.Lanes)) }

var placeholder = regexp.MustCompile(`\{label\.([a-z0-9-]+)\}`)

// SafeValue is what a substituted value must match, so ticket data never reaches a shell
// as anything but a plain path-like token (ADR-006).
var SafeValue = regexp.MustCompile(`^[A-Za-z0-9._/-]+$`)

// SafeLabel is what a label given with a prompt must match: a plain name or <prefix>:<value>.
var SafeLabel = regexp.MustCompile(`^[A-Za-z0-9._/-]+(:[A-Za-z0-9._/-]+)?$`)

// Expand fills {label.<prefix>} placeholders from '<prefix>:<value>' labels.
func Expand(tmpl string, labels []string) (string, error) {
	var bad error
	out := placeholder.ReplaceAllStringFunc(tmpl, func(m string) string {
		prefix := placeholder.FindStringSubmatch(m)[1]
		v, ok := LabelValue(labels, prefix)
		switch {
		case !ok:
			bad = fmt.Errorf("no %s: label for %s", prefix, m)
		case !SafeValue.MatchString(v):
			bad = fmt.Errorf("label %s:%s is not a safe value", prefix, v)
		}
		return v
	})
	return out, bad
}

// ExpandShape fills every {label.<prefix>} placeholder with a safe dummy path segment, to check
// the shape of a template before any item's labels are known.
func ExpandShape(tmpl string) string {
	return placeholder.ReplaceAllString(tmpl, "x")
}

// LabelValue returns the value of the first '<prefix>:<value>' label.
func LabelValue(labels []string, prefix string) (string, bool) {
	for _, l := range labels {
		if v, ok := strings.CutPrefix(l, prefix+":"); ok {
			return v, true
		}
	}
	return "", false
}

// ParseDuration accepts the schema's durations, including days.
func ParseDuration(s string) (time.Duration, error) {
	if d, ok := strings.CutSuffix(s, "d"); ok {
		var n int
		if _, err := fmt.Sscanf(d, "%d", &n); err != nil {
			return 0, fmt.Errorf("bad duration %q", s)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	return time.ParseDuration(s)
}

// Resolve picks the factory folder from those that exist, and lists the shadowed ones.
func Resolve(exists func(dir string) bool) (dir string, shadowed []string) {
	for _, d := range FactoryDirs {
		if !exists(d) {
			continue
		}
		if dir == "" {
			dir = d
		} else {
			shadowed = append(shadowed, d)
		}
	}
	return dir, shadowed
}

// For is the change for an item entering state, or nil.
func (l *Labels) For(state string) *LabelChange {
	if l == nil {
		return nil
	}
	switch state {
	case "ready":
		return l.OnClaim
	case "proposed":
		return l.OnPropose
	case "in_review":
		return l.OnReview
	case "escalated", "quarantined":
		return l.OnEscalate
	case "done", "closed":
		return l.OnDone
	}
	return nil
}

// CheckLabels reports whether labels fill every {label.<prefix>} placeholder the lane's runs use,
// with safe values: its command's arguments and its ynh sensor scopes. Work the lane cannot run is
// refused before it starts, rather than failing in its first run.
func (l Lane) CheckLabels(labels []string) error {
	var tmpls []string
	if c := l.Run.Command; c != nil {
		tmpls = append(tmpls, c.Argv...)
	}
	if y := l.Run.Ynh; y != nil {
		for _, name := range slices.Sorted(maps.Keys(y.SensorScope)) {
			tmpls = append(tmpls, y.SensorScope[name])
		}
	}
	for _, t := range tmpls {
		if _, err := Expand(t, labels); err != nil {
			return err
		}
	}
	return nil
}
