//go:build windows

package experiments

import (
	"fmt"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

var forkNtResumeProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type windowsForkProcessController struct {
	cmd      *exec.Cmd
	job      windows.Handle
	mu       sync.Mutex
	assigned bool
	closed   bool
}

func newForkProcessController(cmd *exec.Cmd) (forkProcessController, error) {
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, err
	}
	info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
		_ = windows.CloseHandle(job)
		return nil, err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
	return &windowsForkProcessController{cmd: cmd, job: job}, nil
}
func (controller *windowsForkProcessController) AfterStart() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return fmt.Errorf("job object is already closed")
	}
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_SUSPEND_RESUME, false, uint32(controller.cmd.Process.Pid))
	if err != nil {
		return err
	}
	defer windows.CloseHandle(process)
	if err := windows.AssignProcessToJobObject(controller.job, process); err != nil {
		return err
	}
	controller.assigned = true
	status, _, _ := forkNtResumeProcess.Call(uintptr(process))
	if status != 0 {
		return windows.NTStatus(status)
	}
	return nil
}
func (controller *windowsForkProcessController) Cancel() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return os.ErrProcessDone
	}
	if controller.assigned {
		return windows.TerminateJobObject(controller.job, 1)
	}
	if controller.cmd.Process == nil {
		return nil
	}
	return controller.cmd.Process.Kill()
}
func (controller *windowsForkProcessController) Close() error {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	if controller.closed {
		return nil
	}
	controller.closed = true
	return windows.CloseHandle(controller.job)
}
