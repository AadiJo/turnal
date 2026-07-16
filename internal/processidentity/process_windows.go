//go:build windows

package processidentity

import (
	"errors"
	"fmt"
	"os"
	"strconv"

	"golang.org/x/sys/windows"
)

func startedAt(pid int) (string, error) {
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(pid))
	if err != nil {
		if errors.Is(err, windows.ERROR_INVALID_PARAMETER) {
			return "", os.ErrNotExist
		}
		return "", err
	}
	defer windows.CloseHandle(handle)
	state, err := windows.WaitForSingleObject(handle, 0)
	if err != nil {
		return "", fmt.Errorf("query process liveness: %w", err)
	}
	if state == windows.WAIT_OBJECT_0 {
		return "", os.ErrNotExist
	}
	if state != uint32(windows.WAIT_TIMEOUT) {
		return "", fmt.Errorf("query process liveness: unexpected wait state %d", state)
	}
	var creation, exit, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &creation, &exit, &kernel, &user); err != nil {
		return "", fmt.Errorf("get process times: %w", err)
	}
	return strconv.FormatInt(creation.Nanoseconds(), 10), nil
}
