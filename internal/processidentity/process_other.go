//go:build !linux && !windows && !darwin

package processidentity

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func startedAt(pid int) (string, error) {
	output, err := exec.Command("ps", "-o", "lstart=", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	fields := strings.Fields(string(output))
	if len(fields) < 6 {
		return "", fmt.Errorf("process %d has no reported start time", pid)
	}
	if strings.HasPrefix(fields[5], "Z") {
		return "", os.ErrNotExist
	}
	return strings.Join(fields[:5], " "), nil
}
