package cli

import (
	"strings"

	"github.com/eyelock/ynf/internal/config"
	"github.com/eyelock/ynf/internal/memory"
)

// memoryFor builds memory from config: ynm when configured, or detected when not; nothing when
// switched off or absent (ADR-008, ADR-012).
func memoryFor(c *config.Config) (memory.Memory, func(string) string) {
	enabled, template, cwd := c.MemorySettings()
	ns := func(repo string) string { return strings.ReplaceAll(template, "{repo}", repo) }
	switch {
	case enabled != nil && !*enabled:
		return nil, ns
	case enabled != nil:
		return memory.Ynm{Cwd: cwd}, ns
	}
	if memory.Detect() == nil {
		return nil, ns
	}
	return memory.Ynm{Cwd: cwd}, ns
}
