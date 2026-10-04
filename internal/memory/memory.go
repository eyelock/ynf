// Package memory is ynf's memory port (ADR-008, ADR-012): ynm when it is installed, nothing when
// it is not. ynf writes what only it can see, outcomes and failures recurring across runs; it
// never decides anything with memory, and never puts memory into an agent's task: a harness that
// wants memory connects its agent to ynm itself.
package memory

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Record is one memory ynf writes.
type Record struct {
	Type       string // episodic
	Namespace  string // factory/<owner>/<repo>
	Subject    string // the item key, or a failure signature
	Summary    string // one line
	Content    string // markdown
	Tags       []string
	DataSchema string // ynf.step.v1, ynf.failure.v1: ynf owns these schemas
	Data       map[string]any
	Source     string // the step that wrote it
}

// Memory is the port.
type Memory interface {
	Remember(ctx context.Context, r Record) error
}

// Ynm talks to ynm through its CLI, which works against whatever store ynm is configured for.
type Ynm struct {
	Bin string // default "ynm"
	Cwd string // the directory ynm runs as if from; default the process's
}

func (y Ynm) bin() string {
	if y.Bin == "" {
		return "ynm"
	}
	return y.Bin
}

// Remember implements Memory.
func (y Ynm) Remember(ctx context.Context, r Record) error {
	args := []string{"remember", "--json", "--type", r.Type, "--content", r.Content, "--namespace", r.Namespace}
	if r.Subject != "" {
		args = append(args, "--subject", r.Subject)
	}
	if r.Summary != "" {
		args = append(args, "--summary", r.Summary)
	}
	if len(r.Tags) > 0 {
		args = append(args, "--tags", strings.Join(r.Tags, ","))
	}
	if r.Data != nil {
		b, err := json.Marshal(r.Data)
		if err != nil {
			return err
		}
		args = append(args, "--data", string(b))
	}
	if r.DataSchema != "" {
		args = append(args, "--data-schema", r.DataSchema)
	}
	if r.Source != "" {
		args = append(args, "--source", r.Source)
	}
	_, err := y.run(ctx, args...)
	return err
}

func (y Ynm) run(ctx context.Context, args ...string) (string, error) {
	if y.Cwd != "" {
		args = append(args, "--cwd", y.Cwd)
	}
	c := exec.CommandContext(ctx, y.bin(), args...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("ynm %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// Detect returns ynm when it is on PATH, or nil (ADR-012: detected, never required).
func Detect() Memory {
	if _, err := exec.LookPath("ynm"); err != nil {
		return nil
	}
	return Ynm{}
}
