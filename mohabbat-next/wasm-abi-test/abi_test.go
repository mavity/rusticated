//go:build wasip1

package wasmabitest

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRusticatedABIFileOperations(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "payload.bin")
	want := []byte{0, 1, 0x7f, 0xff}
	if err := os.WriteFile(path, want, 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read file: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("read bytes = %v, want %v", got, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat file: %v", err)
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(want)) {
		t.Fatalf("stat = mode %v size %d", info.Mode(), info.Size())
	}
}

func TestRusticatedABIRenameAndRemove(t *testing.T) {
	root := t.TempDir()
	from := filepath.Join(root, "before")
	to := filepath.Join(root, "after")
	if err := os.WriteFile(from, []byte("rename"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := os.Rename(from, to); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(from); !os.IsNotExist(err) {
		t.Fatalf("source stat error = %v, want not-exist", err)
	}
	if err := os.Remove(to); err != nil {
		t.Fatalf("remove: %v", err)
	}
}

func TestRusticatedABIEnvironmentAndWorkingDirectory(t *testing.T) {
	const key = "RUSTICATED_ABI_TEST_VALUE"
	const value = "environment-is-visible"
	if err := os.Setenv(key, value); err != nil {
		t.Fatalf("set environment: %v", err)
	}
	defer os.Unsetenv(key)
	if got := os.Getenv(key); got != value {
		t.Fatalf("environment value = %q, want %q", got, value)
	}
	workingDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	info, err := os.Stat(workingDir)
	if err != nil || !info.IsDir() {
		t.Fatalf("working directory %q is unavailable: %v", workingDir, err)
	}
}

func TestRusticatedABITimerProgress(t *testing.T) {
	started := time.Now()
	timer := time.NewTimer(20 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if time.Since(started) < 10*time.Millisecond {
		t.Fatal("timer fired before its requested deadline")
	}
}
