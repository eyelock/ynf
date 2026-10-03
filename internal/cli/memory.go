package cli

import (
	"strings"

	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/memory"
)

// memoryFor builds memory from config: ynm when configured, or detected when not; nothing when
// switched off or absent (ADR-008, ADR-012).
func memoryFor(c *config.Config) (memory.Memory, func(string) string, int) {
	enabled, template, budget, cwd := c.MemorySettings()
	ns := func(repo string) string { return strings.ReplaceAll(template, "{repo}", repo) }
	switch {
	case enabled != nil && !*enabled:
		return nil, ns, budget
	case enabled != nil:
		return memory.Ynm{Cwd: cwd}, ns, budget
	}
	if memory.Detect() == nil {
		return nil, ns, budget
	}
	return memory.Ynm{Cwd: cwd}, ns, budget
}
