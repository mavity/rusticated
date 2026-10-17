//go:build !windows && !wasip1 && !wasm

package browser

import (
	"os/exec"
	"syscall"
)

func startDetachedBrowserProcess(binPath string, args []string) error {
	cmd := exec.Command(binPath, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true,
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}
