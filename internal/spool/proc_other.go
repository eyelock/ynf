//go:build !linux

package spool

import (
	"errors"
	"os/exec"
	"strconv"
	"strings"
)

// procStart is the start time of a process as ps prints it, to the second, which with the PID
// names one process for as long as the machine is up.
func procStart(pid int) (string, error) {
	cmd := exec.Command("ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = []string{"LC_ALL=C"}
	out, err := cmd.Output()
	var ee *exec.ExitError
	if errors.As(err, &ee) && strings.TrimSpace(string(out)) == "" {
		return "", errNoProcess // ps exits 1 when nothing matches
	}
	if err != nil {
		return "", err
	}
	s := strings.TrimSpace(string(out))
	if s == "" {
		return "", errNoProcess
	}
	return s, nil
}
