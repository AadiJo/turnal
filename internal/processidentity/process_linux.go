//go:build linux

package processidentity

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

func startedAt(pid int) (string, error) {
	data, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/stat")
	if err != nil {
		return "", err
	}
	// The command name is parenthesized and may contain spaces or parentheses.
	// Field 3 begins after its final ") "; starttime is field 22, index 19 below.
	end := strings.LastIndex(string(data), ") ")
	if end < 0 {
		return "", fmt.Errorf("malformed /proc/%d/stat", pid)
	}
	fields := strings.Fields(string(data)[end+2:])
	if len(fields) <= 19 {
		return "", fmt.Errorf("malformed /proc/%d/stat: missing start time", pid)
	}
	if fields[0] == "Z" {
		return "", os.ErrNotExist
	}
	if _, err := strconv.ParseUint(fields[19], 10, 64); err != nil {
		return "", fmt.Errorf("malformed /proc/%d/stat start time: %w", pid, err)
	}
	return fields[19], nil
}
