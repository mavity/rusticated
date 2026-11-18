package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func TestRunCommandWithStdioIdleTimeoutResetsOnOutput(t *testing.T) {
	for _, mode := range []string{"stdout", "stderr"} {
		t.Run(mode, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			cmd := exec.Command(os.Args[0], "-test.run=^TestStdioIdleTimeoutHelper$", "--", mode)

			err := runCommandWithStdioIdleTimeout(cmd, time.Second, &stdout, &stderr)
			if err != nil {
				t.Fatalf("run command: %v", err)
			}
			if mode == "stdout" && stdout.Len() == 0 {
				t.Fatal("expected stdout activity to be forwarded")
			}
			if mode == "stderr" && stderr.Len() == 0 {
				t.Fatal("expected stderr activity to be forwarded")
			}
		})
	}
}

func TestRunCommandWithStdioIdleTimeoutStopsSilentCommand(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestStdioIdleTimeoutHelper$", "--", "silent")

	start := time.Now()
	err := runCommandWithStdioIdleTimeout(cmd, 150*time.Millisecond, nil, nil)
	if err == nil || !strings.Contains(err.Error(), "no stdout/stderr output") {
		t.Fatalf("expected stdio idle timeout, got %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("silent command was not stopped promptly: %s", elapsed)
	}
}

func TestNormalizeGuestTestFlag(t *testing.T) {
	tests := []struct {
		name string
		arg  string
		want string
	}{
		{name: "single hyphen", arg: "-test.v", want: "--test.v"},
		{name: "double hyphen", arg: "--test.run=TestOne", want: "--test.run=TestOne"},
		{name: "unrelated argument", arg: "kabibi", want: "kabibi"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normalizeGuestTestFlag(tt.arg); got != tt.want {
				t.Fatalf("normalizeGuestTestFlag(%q) = %q, want %q", tt.arg, got, tt.want)
			}
		})
	}
}

func TestStdioIdleTimeoutHelper(t *testing.T) {
	if len(os.Args) < 2 {
		return
	}
	mode := os.Args[len(os.Args)-1]
	switch mode {
	case "stdout", "stderr":
		for i := 0; i < 5; i++ {
			output := os.Stdout
			if mode == "stderr" {
				output = os.Stderr
			}
			if _, err := fmt.Fprint(output, "."); err != nil {
				t.Fatalf("write test output: %v", err)
			}
			time.Sleep(300 * time.Millisecond)
		}
	case "silent":
		time.Sleep(5 * time.Second)
	}
}
