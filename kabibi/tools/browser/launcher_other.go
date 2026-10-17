//go:build wasip1 || wasm

package browser

import "os/exec"

func startDetachedBrowserProcess(binPath string, args []string) error {
	cmd := exec.Command(binPath, args...)
	if err := cmd.Start(); err != nil {
		return err
	}
	if cmd.Process != nil {
		_ = cmd.Process.Release()
	}
	return nil
}
