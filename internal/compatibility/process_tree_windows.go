//go:build windows

package compatibility

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var ntResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type appServerProcessTree struct {
	command  *exec.Cmd
	job      windows.Handle
	mu       sync.Mutex
	assigned bool
	closed   bool
}

func prepareProcessTree(command *exec.Cmd) (*appServerProcessTree, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	attributes := syscall.SysProcAttr{}
	if command.SysProcAttr != nil {
		attributes = *command.SysProcAttr
	}
	attributes.CreationFlags |= windows.CREATE_SUSPENDED
	command.SysProcAttr = &attributes
	return &appServerProcessTree{command: command, job: job}, nil
}

func attachProcessTree(processTree *appServerProcessTree, command *exec.Cmd) error {
	processTree.mu.Lock()
	defer processTree.mu.Unlock()
	if processTree.closed {
		return fmt.Errorf("job object is already closed")
	}
	if command.Process == nil {
		return fmt.Errorf("process is unavailable")
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SUSPEND_RESUME,
		false,
		uint32(command.Process.Pid),
	)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(processTree.job, process); err != nil {
		return err
	}
	processTree.assigned = true
	status, _, _ := ntResumeProcess.Call(uintptr(process))
	if status != 0 {
		return windows.NTStatus(status)
	}
	return nil
}

func releaseProcessTree(processTree *appServerProcessTree) {
	if processTree == nil {
		return
	}
	processTree.mu.Lock()
	defer processTree.mu.Unlock()
	if processTree.closed {
		return
	}
	processTree.closed = true
	_ = windows.CloseHandle(processTree.job)
}

func killProcessTree(processTree *appServerProcessTree, command *exec.Cmd, timeout time.Duration) error {
	_ = timeout
	if processTree == nil {
		if command.Process == nil {
			return nil
		}
		return command.Process.Kill()
	}
	processTree.mu.Lock()
	defer processTree.mu.Unlock()
	if processTree.closed {
		return os.ErrProcessDone
	}
	if processTree.assigned {
		return windows.TerminateJobObject(processTree.job, 1)
	}
	if command.Process == nil {
		return nil
	}
	return command.Process.Kill()
}
