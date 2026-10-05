package spool

import (
	"fmt"
	"os"
	"strings"
)

// procStart is the start time of a process, as proc(5) gives it: the 22nd field of its stat, in
// clock ticks since boot, which with the PID names one process for the life of the boot.
func procStart(pid int) (string, error) {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if os.IsNotExist(err) {
		return "", errNoProcess
	}
	if err != nil {
		return "", err
	}
	// The command name is in parentheses and may hold spaces: the fields count from the last ")".
	i := strings.LastIndex(string(b), ")")
	if i < 0 {
		return "", fmt.Errorf("unreadable /proc/%d/stat", pid)
	}
	f := strings.Fields(string(b)[i+1:])
	if len(f) < 20 {
		return "", fmt.Errorf("unreadable /proc/%d/stat", pid)
	}
	return f[19], nil
}
