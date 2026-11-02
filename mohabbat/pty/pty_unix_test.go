//go:build !windows && !js && !wasip1

package pty_test

import (
	"io"
	"os/exec"
	"strings"
	"testing"

	"pty"
)

func TestPtyChildCanUseControllingTerminal(t *testing.T) {
	sh, err := exec.LookPath("sh")
	if err != nil {
		t.Skipf("sh not found in PATH: %v", err)
	}

	cmd := exec.Command(sh, "-c", `printf 'READY\n' >/dev/tty && read -r line && printf 'GOT:%s\n' "$line" >/dev/tty`)
	p, err := pty.Start(cmd, pty.Options{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("pty.Start failed: %v", err)
	}
	defer p.Close()

	if _, err := p.Write([]byte("probe\n")); err != nil {
		_ = p.Wait()
		t.Fatalf("write to PTY failed: %v", err)
	}
	if err := p.Wait(); err != nil {
		t.Fatalf("interactive child failed: %v", err)
	}

	output, err := io.ReadAll(p)
	if err != nil {
		t.Fatalf("read PTY output failed: %v", err)
	}
	if !strings.Contains(string(output), "READY") || !strings.Contains(string(output), "GOT:probe") {
		t.Fatalf("expected child to use its controlling terminal and read input, got %q", output)
	}
}
