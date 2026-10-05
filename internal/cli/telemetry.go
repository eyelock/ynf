package cli

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"

	"github.com/eyelock/ynf"
	"github.com/eyelock/ynf/internal/telemetry/registry"
)

// telemetryCmd is `ynf telemetry registry`: the names ynf emits in OpenTelemetry, as ynr asks every
// tool to print (ynr ADR-007). The registry is embedded in the binary, and its tool name and
// version are what service.name and service.version on every record say.
func (a *app) telemetryCmd(args []string) error {
	if len(args) == 0 || args[0] != "registry" {
		return withCode(ExitUsage, errors.New("telemetry: want `registry`"))
	}
	fl := a.flags("telemetry registry")
	format := fl.String("format", a.format, "")
	if err := fl.Parse(args[1:]); err != nil {
		return withCode(ExitUsage, err)
	}
	if *format != "text" && *format != "json" {
		return withCode(ExitUsage, fmt.Errorf("--format %q: want text or json", *format))
	}
	sub, err := fs.Sub(ynf.TelemetryRegistry, "telemetry/registry")
	if err != nil {
		return err
	}
	r, err := registry.Load(sub, ynf.Version)
	if err != nil {
		return err
	}
	a.format = *format
	return a.out(r, registryText(r))
}

func registryText(r *registry.Registry) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s, following %s %s\n\n", r.Tool, r.Version, r.Semconv.Name, r.Semconv.Version)
	b.WriteString("attributes\n")
	for _, at := range r.Attributes {
		fmt.Fprintf(&b, "  %s (%s)\n", at.ID, at.Type)
	}
	b.WriteString("spans\n")
	for _, s := range r.Spans {
		fmt.Fprintf(&b, "  %s\n", s.Name)
	}
	b.WriteString("events\n")
	for _, e := range r.Events {
		fmt.Fprintf(&b, "  %s\n", e.Name)
	}
	b.WriteString("metrics\n")
	for _, m := range r.Metrics {
		fmt.Fprintf(&b, "  %s (%s, %s)\n", m.Name, m.Instrument, m.Unit)
	}
	return b.String()
}
