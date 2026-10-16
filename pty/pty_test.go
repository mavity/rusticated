package pty_test

import (
	"bytes"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"mohabbat/pty"
)

func TestPtyExecution(t *testing.T) {
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe", "/c", "echo", "123")
	} else {
		cmd = exec.Command("sh", "-c", "echo 123")
	}

	p, err := pty.Start(cmd, pty.Options{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("pty.Start failed: %v", err)
	}
	defer p.Close()

	if err := p.Resize(100, 30); err != nil {
		t.Errorf("Resize failed: %v", err)
	}

	var buf bytes.Buffer
	readDone := make(chan error, 1)
	go func() {
		tmp := make([]byte, 1024)
		for {
			n, err := p.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if err != nil {
				if err == io.EOF || strings.Contains(err.Error(), "closed") {
					readDone <- nil
				} else {
					readDone <- err
				}
				return
			}
		}
	}()

	waitErr := p.Wait()
	if waitErr != nil {
		t.Errorf("p.Wait() returned error: %v", waitErr)
	}

	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		p.Close()
	}

	out := buf.String()
	if !strings.Contains(out, "123") {
		t.Fatalf("expected PTY output to contain '123', got: %q", out)
	}
}

func TestPtyInteractive(t *testing.T) {
	var cmd *exec.Cmd
	var inputCmd string
	if runtime.GOOS == "windows" {
		cmd = exec.Command("cmd.exe")
		inputCmd = "echo 456\r\nexit\r\n"
	} else {
		cmd = exec.Command("sh")
		inputCmd = "echo 456\nexit\n"
	}

	p, err := pty.Start(cmd, pty.Options{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatalf("pty.Start failed: %v", err)
	}
	defer p.Close()

	var buf bytes.Buffer
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		tmp := make([]byte, 1024)
		for {
			n, err := p.Read(tmp)
			if n > 0 {
				buf.Write(tmp[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	// Allow shell to initialize
	time.Sleep(100 * time.Millisecond)

	if _, err := p.Write([]byte(inputCmd)); err != nil {
		t.Fatalf("Write to PTY failed: %v", err)
	}

	if err := p.Wait(); err != nil {
		t.Errorf("p.Wait() returned error: %v", err)
	}

	select {
	case <-readDone:
	case <-time.After(2 * time.Second):
		p.Close()
	}

	out := buf.String()
	if !strings.Contains(out, "456") {
		t.Fatalf("expected PTY output to contain '456', got: %q", out)
	}
}
