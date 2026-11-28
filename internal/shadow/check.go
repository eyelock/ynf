package shadow

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Check is what ynh check found on an attempt's change, and what its baseline forgave to let it
// through, read from ynh's JSON (`ynh check --format json`, `ynh baseline --explain --format json`).
// ynh stays raw signal; ynf keeps it beside the attempt so a grader sees what the gate saw
// (ynh #238, ynf #257).
type Check struct {
	// Verdict is ynh check's: pass or blocked.
	Verdict string        `json:"verdict,omitempty"`
	Sensors []CheckSensor `json:"sensors,omitempty"`
	// Known is how many pre-existing failures the baseline forgave across the sensors, and Fixed how
	// many baseline entries no longer fail (debt the change paid off).
	Known int `json:"known"`
	Fixed int `json:"fixed"`
	// Forgiven are the findings the baseline forgave, by sensor, where ynh could say which.
	Forgiven []Forgiven `json:"forgiven,omitempty"`
	// Error says why the check could not be read: ynh missing, a refusal, or output that was not
	// JSON. The attempt is kept either way.
	Error string `json:"error,omitempty"`
	// ExplainError says why the findings the baseline forgave were not read, when the check itself
	// was: `ynh baseline --explain` failed or does not take this harness. The counts stand.
	ExplainError string `json:"explain_error,omitempty"`
}

// CheckSensor is one sensor's result: its status, the failures not in the baseline (new) and the
// ones already in it (known).
type CheckSensor struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	New       int    `json:"new,omitempty"`
	Known     int    `json:"known,omitempty"`
	NewOutput string `json:"new_output,omitempty"`
	// Unbaselined is true for a failing sensor ynh gave no counts for: ynh counts new and known
	// failures only when a baseline exists, so with none the sensor failed and every failure is new.
	Unbaselined bool `json:"unbaselined,omitempty"`
	// CountDelta is how far a count-ratchet sensor's total has moved since the baseline; positive is
	// new findings. ynh sets it for such a sensor only.
	CountDelta int `json:"count_delta,omitempty"`
	// Exit, Stdout, Stderr and Note say what the sensor did when ynh gave no new_output.
	Exit   int    `json:"exit,omitempty"`
	Stdout string `json:"stdout,omitempty"`
	Stderr string `json:"stderr,omitempty"`
	Note   string `json:"note,omitempty"`
}

// Forgiven is the findings one sensor's baseline forgave.
type Forgiven struct {
	Sensor   string   `json:"sensor"`
	Findings []string `json:"findings"`
}

// ParseCheck reads ynh check's JSON and, when it has it, ynh baseline --explain's. A nil explain is
// fine: the counts are still recorded, and the forgiven findings are not.
func ParseCheck(check, explain []byte) (*Check, error) {
	var c struct {
		Verdict string `json:"verdict"`
		Sensors []struct {
			Name      string `json:"name"`
			Status    string `json:"status"`
			NewCount  *int   `json:"new_count"`
			KnownCnt  *int   `json:"known_count"`
			Delta     *int   `json:"count_delta"`
			NewOutput string `json:"new_output"`
			ExitCode  int    `json:"exit_code"`
			Stdout    string `json:"stdout"`
			Stderr    string `json:"stderr"`
			Note      string `json:"note"`
		} `json:"sensors"`
		Baseline *struct {
			Known int `json:"known"`
			Fixed int `json:"fixed"`
		} `json:"baseline"`
	}
	if err := json.Unmarshal(check, &c); err != nil || c.Verdict == "" {
		if err == nil {
			err = fmt.Errorf("no verdict")
		}
		return nil, fmt.Errorf("ynh check output: %w", err)
	}
	out := &Check{Verdict: c.Verdict}
	for _, s := range c.Sensors {
		cs := CheckSensor{Name: s.Name, Status: s.Status, NewOutput: s.NewOutput, Exit: s.ExitCode,
			Stdout: clip(s.Stdout), Stderr: clip(s.Stderr), Note: s.Note}
		if s.NewCount != nil {
			cs.New = *s.NewCount
		}
		if s.KnownCnt != nil {
			cs.Known = *s.KnownCnt
		}
		if s.Delta != nil {
			cs.CountDelta = *s.Delta
		}
		// ynh omits new_count when it is zero, and sets no counts without a baseline. A failing sensor
		// with a known count or a count delta has a baseline: a count-ratchet sensor that regressed has
		// no new lines, only a positive count_delta, and that is how far it moved. Only a failing
		// sensor with no counts at all had no baseline.
		if s.Status == "fail" && s.NewCount == nil {
			switch {
			case s.KnownCnt == nil && s.Delta == nil && c.Baseline == nil:
				cs.Unbaselined = true
			case cs.CountDelta > 0:
				cs.New = cs.CountDelta
			default:
				cs.New = 1
			}
		}
		out.Sensors = append(out.Sensors, cs)
	}
	if c.Baseline != nil {
		out.Known, out.Fixed = c.Baseline.Known, c.Baseline.Fixed
	}
	if len(explain) > 0 {
		var b struct {
			Sensors []struct {
				Name     string   `json:"name"`
				Findings []string `json:"findings"`
			} `json:"sensors"`
		}
		if err := json.Unmarshal(explain, &b); err != nil {
			return out, fmt.Errorf("ynh baseline output: %w", err)
		}
		for _, s := range b.Sensors {
			if len(s.Findings) > 0 {
				out.Forgiven = append(out.Forgiven, Forgiven{Sensor: s.Name, Findings: s.Findings})
			}
		}
	}
	return out, nil
}

// clip keeps the start of a sensor's output, enough to read what failed.
func clip(s string) string {
	const max = 1500
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}

// NewFindings is how many failures the check found that its baseline did not forgive. A failing
// sensor ynh gave no counts for (no baseline) counts as one: it failed, and nothing was forgiven.
func (c *Check) NewFindings() int {
	n := 0
	for _, s := range c.Sensors {
		n += s.New
		if s.Unbaselined {
			n++
		}
	}
	return n
}

// ForgivenFindings is how many findings the baseline forgave.
func (c *Check) ForgivenFindings() int {
	n := 0
	for _, f := range c.Forgiven {
		n += len(f.Findings)
	}
	return n
}

// Lines renders the check for a grader or a report: what was new, what was forgiven, what was
// fixed. indent prefixes each line.
func (c *Check) Lines(indent string) string {
	if c == nil {
		return ""
	}
	var b strings.Builder
	if c.Error != "" {
		fmt.Fprintf(&b, "%synh check could not be read: %s\n", indent, c.Error)
	}
	if c.ExplainError != "" {
		fmt.Fprintf(&b, "%sthe findings the baseline forgave were not read: %s\n", indent, c.ExplainError)
	}
	if c.Verdict != "" {
		fmt.Fprintf(&b, "%sverdict %s: %d new, %d known (forgiven by the baseline), %d fixed\n", indent, c.Verdict, c.NewFindings(), c.Known, c.Fixed)
	}
	for _, s := range c.Sensors {
		if s.New == 0 && s.Known == 0 && s.Status != "fail" && s.Status != "known" && s.Status != "reported" && s.Status != "deferred" {
			continue
		}
		switch {
		case s.Unbaselined:
			fmt.Fprintf(&b, "%s  %s (%s): failed, no baseline, so every failure is new (exit %d)\n", indent, s.Name, s.Status, s.Exit)
		case s.CountDelta != 0:
			fmt.Fprintf(&b, "%s  %s (%s): count moved by %+d since the baseline, %d new, %d known\n", indent, s.Name, s.Status, s.CountDelta, s.New, s.Known)
		default:
			fmt.Fprintf(&b, "%s  %s (%s): %d new, %d known\n", indent, s.Name, s.Status, s.New, s.Known)
		}
		out := s.NewOutput
		if out == "" {
			out = strings.TrimSpace(s.Stdout + "\n" + s.Stderr)
		}
		for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
			if l != "" {
				fmt.Fprintf(&b, "%s    new: %s\n", indent, l)
			}
		}
		if s.Note != "" {
			fmt.Fprintf(&b, "%s    note: %s\n", indent, s.Note)
		}
	}
	for _, f := range c.Forgiven {
		for _, l := range f.Findings {
			fmt.Fprintf(&b, "%s  forgiven (%s): %s\n", indent, f.Sensor, l)
		}
	}
	return b.String()
}
