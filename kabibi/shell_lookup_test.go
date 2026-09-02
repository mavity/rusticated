package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWindowsExecutableUsesPATHEXT(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "git.cmd")
	if err := os.WriteFile(cmdPath, []byte("@echo ok\n"), 0o644); err != nil {
		t.Fatalf("write test cmd: %v", err)
	}

	got, ok := resolvePlatformExecutable("git", dir, map[string]string{
		"PATHEXT": ".COM;.EXE;.BAT;.CMD",
		"PATH":    dir,
	})
	if !ok {
		t.Fatal("expected windows PATH lookup to resolve git.cmd")
	}
	if got != cmdPath {
		t.Fatalf("got %q, want %q", got, cmdPath)
	}
}

func TestResolveWindowsExecutableIgnoresExecutableBit(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "hello.bat")
	if err := os.WriteFile(cmdPath, []byte("@echo hello\n"), 0o644); err != nil {
		t.Fatalf("write test batch file: %v", err)
	}

	got, ok := resolvePlatformExecutable("hello", dir, map[string]string{
		"PATHEXT": ".COM;.EXE;.BAT;.CMD",
		"PATH":    dir,
	})
	if !ok {
		t.Fatal("expected windows PATHEXT lookup to resolve hello.bat")
	}
	if got != cmdPath {
		t.Fatalf("got %q, want %q", got, cmdPath)
	}
}

func TestResolveWindowsExecutableUsesCaseInsensitiveEnvKeys(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "git.cmd")
	if err := os.WriteFile(cmdPath, []byte("@echo ok\n"), 0o644); err != nil {
		t.Fatalf("write test cmd: %v", err)
	}

	got, ok := resolvePlatformExecutable("git", dir, map[string]string{
		"Path":    dir,
		"Pathext": ".COM;.EXE;.BAT;.CMD",
	})
	if !ok {
		t.Fatal("expected Windows PATH lookup to honor case-insensitive env keys")
	}
	if got != cmdPath {
		t.Fatalf("got %q, want %q", got, cmdPath)
	}
}

func TestResolveWindowsExecutableExpandsPercentEnvVarsInPath(t *testing.T) {
	dir := t.TempDir()
	toolDir := filepath.Join(dir, "tools")
	if err := os.MkdirAll(toolDir, 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	cmdPath := filepath.Join(toolDir, "git.cmd")
	if err := os.WriteFile(cmdPath, []byte("@echo ok\n"), 0o644); err != nil {
		t.Fatalf("write test cmd: %v", err)
	}

	key := "TOOLS_DIR"
	if err := os.Setenv(key, toolDir); err != nil {
		t.Fatalf("set env: %v", err)
	}
	defer os.Unsetenv(key)

	got, ok := resolvePlatformExecutable("git", dir, map[string]string{
		"PATH":    "%TOOLS_DIR%",
		"PATHEXT": ".COM;.EXE;.BAT;.CMD",
	})
	if !ok {
		t.Fatal("expected Windows PATH lookup to expand %VAR% entries")
	}
	if got != cmdPath {
		t.Fatalf("got %q, want %q", got, cmdPath)
	}
}

func TestResolveWindowsExecutableHonorsWindowsEnvFlags(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "git.cmd")
	if err := os.WriteFile(cmdPath, []byte("@echo ok\n"), 0o644); err != nil {
		t.Fatalf("write test cmd: %v", err)
	}

	got, ok := resolvePlatformExecutable("git", dir, map[string]string{
		"GOOS":    "windows",
		"PATH":    dir,
		"PATHEXT": ".COM;.EXE;.BAT;.CMD",
	})
	if !ok {
		t.Fatal("expected GOOS=windows to trigger Windows PATH lookup")
	}
	if got != cmdPath {
		t.Fatalf("got %q, want %q", got, cmdPath)
	}
}

func TestCheckExecutableFileAllowsWindowsStyleExecOnNonWindowsHost(t *testing.T) {
	dir := t.TempDir()
	cmdPath := filepath.Join(dir, "git.cmd")
	if err := os.WriteFile(cmdPath, []byte("@echo ok\n"), 0o644); err != nil {
		t.Fatalf("write test cmd: %v", err)
	}

	got, err := checkExecutableFile(cmdPath, dir, map[string]string{"GOOS": "windows"}, true)
	if err != nil {
		t.Fatalf("expected GOOS=windows to bypass Unix exec-bit checks: %v", err)
	}
	if got != cmdPath {
		t.Fatalf("got %q, want %q", got, cmdPath)
	}
}
