//go:build windows

package compatibility

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const createNewProcessGroup = 0x00000200

func prepareProcessTree(command *exec.Cmd) {
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.CreationFlags |= createNewProcessGroup
	command.SysProcAttr = &attributes
}

type appServerProcessTree struct {
	job windows.Handle
}

func attachProcessTree(command *exec.Cmd) (*appServerProcessTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	failed := true
	defer func() {
		if failed {
			_ = windows.CloseHandle(job)
		}
	}()
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		return nil, err
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(command.Process.Pid))
	if err != nil {
		return nil, err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(job, process); err != nil {
		return nil, err
	}
	failed = false
	return &appServerProcessTree{job: job}, nil
}

func releaseProcessTree(processTree *appServerProcessTree) {
	if processTree != nil && processTree.job != 0 {
		_ = windows.CloseHandle(processTree.job)
		processTree.job = 0
	}
}

func killProcessTree(processTree *appServerProcessTree, command *exec.Cmd, timeout time.Duration) error {
	if command.Process == nil {
		return nil
	}
	if processTree != nil && processTree.job != 0 {
		err := windows.CloseHandle(processTree.job)
		processTree.job = 0
		if err == nil {
			return nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	treeErr := exec.CommandContext(ctx, "taskkill", "/T", "/F", "/PID", strconv.Itoa(command.Process.Pid)).Run()
	if treeErr == nil {
		return nil
	}
	if directErr := command.Process.Kill(); directErr != nil && !errors.Is(directErr, os.ErrProcessDone) {
		return errors.Join(fmt.Errorf("taskkill: %w", treeErr), directErr)
	}
	return nil
}
