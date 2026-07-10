//go:build windows

package telemetry

import (
	"os/exec"
	"syscall"

	"golang.org/x/sys/windows"
)

func configureDetachedProcess(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: windows.CREATE_NEW_PROCESS_GROUP | windows.DETACHED_PROCESS,
		HideWindow:    true,
	}
}
