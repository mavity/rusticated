//go:build !wasip1

package shell

import (
	"errors"
	"fmt"
	"os/exec"
	"runtime"
)

func openExternalURL(target string) error {
	switch runtime.GOOS {
	case "windows":
		return startURLCommand("cmd.exe", "/c", "start", "", target)
	case "darwin":
		return startURLCommand("open", target)
	default:
		openers := []struct {
			program string
			args    []string
		}{
			{program: "xdg-open", args: []string{target}},
			{program: "gio", args: []string{"open", target}},
			{program: "firefox", args: []string{target}},
			{program: "google-chrome", args: []string{target}},
			{program: "chromium", args: []string{target}},
			{program: "brave-browser", args: []string{target}},
			{program: "microsoft-edge", args: []string{target}},
		}
		var failures []error
		for _, opener := range openers {
			path, err := exec.LookPath(opener.program)
			if err != nil {
				failures = append(failures, err)
				continue
			}
			if err := startURLCommand(path, opener.args...); err != nil {
				failures = append(failures, err)
				continue
			}
			return nil
		}
		return fmt.Errorf("no browser URL handler could be started: %w", errors.Join(failures...))
	}
}

func startURLCommand(program string, args ...string) error {
	cmd := exec.Command(program, args...)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", program, err)
	}
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("release %s process: %w", program, err)
	}
	return nil
}
