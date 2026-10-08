package engine

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/eyelock/ynf/internal/decide"
	"github.com/eyelock/ynf/internal/item"
	"github.com/eyelock/ynf/internal/telemetry"
)

// excerptMax is how many bytes of what a run reported a failure memory keeps. A record at
// distributed level is shared, so what it holds is bounded as well as scrubbed (ADR-008).
const excerptMax = 1024

// namesMax is how many sensor or check names one failure memory lists.
const namesMax = 20

// failureDetail is what a run reported about a failure, as a failure memory carries it (ADR-008):
// only what the run itself said, never ticket text or a prompt, scrubbed and cut to a fixed size.
// A field the run did not report is empty.
type failureDetail struct {
	Outcome        string
	Exit           *int
	BoundBy        string
	FailedSensors  []string
	FailedChecks   []string
	Excerpt        string
	Harness        string
	HarnessVersion string
}

// failureDetailFor reads what the run behind a signature reported. A signature from a run
// (outcome, budget, stuck sensor, egress) takes the item's last run, with the exit code and the
// runner's findings when this step made that run; a signature from CI takes the checks that failed.
func (s *step) failureDetailFor(in decide.Input, it item.Item, name string) failureDetail {
	var d failureDetail
	if strings.HasPrefix(name, "sig/ci") {
		if pr := in.Facts.PR; pr != nil {
			d.FailedChecks = scrubNames(pr.Failed())
		}
		return d
	}
	if it.LastRun == nil {
		return d
	}
	d.Outcome = it.LastRun.Outcome
	d.Excerpt = excerpt(it.LastRun.Detail)
	if r := s.run; r != nil && r.RunID == it.LastRun.ID {
		exit := r.Exit
		d.Exit = &exit
		d.BoundBy = telemetry.Scrub(r.BoundBy)
		d.FailedSensors = scrubNames(r.FailedSensors)
		d.Harness, d.HarnessVersion = splitHarness(r.Harness) // not scrubbed: name@version reads as an address to Scrub
	}
	return d
}

// sentence is the detail as a clause of a failure memory's text, "" when the run reported nothing.
func (d failureDetail) sentence() string {
	var parts []string
	if d.Outcome != "" {
		p := "the run ended " + d.Outcome
		if d.Exit != nil {
			p += fmt.Sprintf(", exit %d", *d.Exit)
		}
		parts = append(parts, p)
	}
	if d.BoundBy != "" {
		parts = append(parts, "bound by the "+d.BoundBy+" cap")
	}
	if len(d.FailedSensors) > 0 {
		parts = append(parts, "failing sensors: "+strings.Join(d.FailedSensors, ", "))
	}
	if len(d.FailedChecks) > 0 {
		parts = append(parts, "failing checks: "+strings.Join(d.FailedChecks, ", "))
	}
	if d.Harness != "" {
		h := "harness " + d.Harness
		if d.HarnessVersion != "" {
			h += "@" + d.HarnessVersion
		}
		parts = append(parts, h)
	}
	out := strings.Join(parts, ", ")
	if d.Excerpt != "" {
		if out != "" {
			out += "; it reported: "
		}
		out += fmt.Sprintf("%q", d.Excerpt)
	}
	return out
}

// data adds the detail to a failure memory's data, leaving out what the run did not report.
func (d failureDetail) data(m map[string]any) {
	set := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	set("outcome", d.Outcome)
	if d.Exit != nil {
		m["exit"] = *d.Exit
	}
	set("bound_by", d.BoundBy)
	if len(d.FailedSensors) > 0 {
		m["failed_sensors"] = d.FailedSensors
	}
	if len(d.FailedChecks) > 0 {
		m["failed_checks"] = d.FailedChecks
	}
	set("excerpt", d.Excerpt)
	set("harness", d.Harness)
	set("harness_version", d.HarnessVersion)
}

// excerpt scrubs what a run reported and keeps its last excerptMax bytes, where a failure says
// why. It scrubs first, so a cut never leaves half a secret that no longer matches.
func excerpt(s string) string {
	s = strings.TrimSpace(telemetry.Scrub(s))
	if len(s) <= excerptMax {
		return s
	}
	s = s[len(s)-excerptMax:]
	for len(s) > 0 && !utf8.RuneStart(s[0]) {
		s = s[1:]
	}
	return "…" + s
}

func scrubNames(names []string) []string {
	var out []string
	for _, n := range names {
		if len(out) == namesMax {
			break
		}
		if n = strings.TrimSpace(telemetry.Scrub(n)); n != "" {
			out = append(out, n)
		}
	}
	return out
}

// splitHarness splits a runner's name@version.
func splitHarness(h string) (name, version string) {
	if i := strings.LastIndex(h, "@"); i > 0 {
		return h[:i], h[i+1:]
	}
	return h, ""
}
