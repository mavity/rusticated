//go:build !windows && !js && !wasip1

package main

import (
	"io"
	"os/exec"
	"strings"
	"testing"
)

func TestStartWithPtyChildCanUseControllingTerminal(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not found in PATH: %v", err)
	}

	cmd := exec.Command(sh, "-c", `printf 'READY\n' >/dev/tty && read -r line && printf 'GOT:%s\n' "$line" >/dev/tty`)
	session, proc, err := startWithPty(cmd, 80, 24)
	if err != nil {
		t.Fatalf("startWithPty failed: %v", err)
	}
	defer session.Close()

	if _, err := session.Write([]byte("probe\n")); err != nil {
		_ = proc.Kill()
		_, _ = proc.Wait()
		t.Fatalf("write to PTY failed: %v", err)
	}
	state, err := proc.Wait()
	if err != nil {
		t.Fatalf("wait for interactive child failed: %v", err)
	}
	if !state.Success() {
		t.Fatalf("interactive child exited with %v", state)
	}

	output, err := io.ReadAll(session)
	if err != nil {
		t.Fatalf("read PTY output failed: %v", err)
	}
	if !strings.Contains(string(output), "READY") || !strings.Contains(string(output), "GOT:probe") {
		t.Fatalf("expected child to use its controlling terminal and read input, got %q", output)
	}
}
