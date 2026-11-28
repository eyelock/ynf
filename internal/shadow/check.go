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
}

// CheckSensor is one sensor's result: its status, the failures not in the baseline (new) and the
// ones already in it (known).
type CheckSensor struct {
	Name      string `json:"name"`
	Status    string `json:"status"`
	New       int    `json:"new,omitempty"`
	Known     int    `json:"known,omitempty"`
	NewOutput string `json:"new_output,omitempty"`
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
			NewCount  int    `json:"new_count"`
			KnownCnt  int    `json:"known_count"`
			NewOutput string `json:"new_output"`
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
		out.Sensors = append(out.Sensors, CheckSensor{Name: s.Name, Status: s.Status, New: s.NewCount, Known: s.KnownCnt, NewOutput: s.NewOutput})
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

// NewFindings is how many failures the check found that its baseline did not forgive.
func (c *Check) NewFindings() int {
	n := 0
	for _, s := range c.Sensors {
		n += s.New
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
	if c.Verdict != "" {
		fmt.Fprintf(&b, "%sverdict %s: %d new, %d known (forgiven by the baseline), %d fixed\n", indent, c.Verdict, c.NewFindings(), c.Known, c.Fixed)
	}
	for _, s := range c.Sensors {
		if s.New == 0 && s.Known == 0 {
			continue
		}
		fmt.Fprintf(&b, "%s  %s (%s): %d new, %d known\n", indent, s.Name, s.Status, s.New, s.Known)
		for _, l := range strings.Split(strings.TrimRight(s.NewOutput, "\n"), "\n") {
			if l != "" {
				fmt.Fprintf(&b, "%s    new: %s\n", indent, l)
			}
		}
	}
	for _, f := range c.Forgiven {
		for _, l := range f.Findings {
			fmt.Fprintf(&b, "%s  forgiven (%s): %s\n", indent, f.Sensor, l)
		}
	}
	return b.String()
}
