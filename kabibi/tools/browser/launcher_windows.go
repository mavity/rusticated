//go:build windows

package browser

import (
	"os/exec"
	"syscall"
)

const (
	createBreakawayFromJob = 0x01000000
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
)

func startDetachedBrowserProcess(binPath string, args []string) error {
	// First try direct spawn with CREATE_BREAKAWAY_FROM_JOB | DETACHED_PROCESS | CREATE_NEW_PROCESS_GROUP
	cmd := exec.Command(binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: createBreakawayFromJob | detachedProcess | createNewProcessGroup,
		HideWindow:    false,
	}
	if err := cmd.Start(); err == nil {
		_ = cmd.Process.Release()
		return nil
	}

	// If the enclosing Job Object does not have JOB_OBJECT_LIMIT_BREAKAWAY_OK,
	// try detached process group without breakaway flag, and also fallback to cmd /c start.
	cmd2 := exec.Command(binPath, args...)
	cmd2.SysProcAttr = &syscall.SysProcAttr{
		CreationFlags: detachedProcess | createNewProcessGroup,
		HideWindow:    false,
	}
	if err := cmd2.Start(); err == nil {
		_ = cmd2.Process.Release()
		return nil
	}

	startArgs := make([]string, 0, len(args)+4)
	startArgs = append(startArgs, "/c", "start", "", binPath)
	startArgs = append(startArgs, args...)
	fallback := exec.Command("cmd.exe", startArgs...)
	if err := fallback.Start(); err != nil {
		return err
	}
	_ = fallback.Process.Release()
	return nil
}
