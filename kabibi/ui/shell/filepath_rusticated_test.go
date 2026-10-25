//go:build wasip1

package shell

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRusticatedFilepathTreatsWindowsDrivePathsAsAbsolute(t *testing.T) {
	for _, path := range []string{`C:\`, `C:\Users\test\file`, `C:/Users/test/file`, `/C:/Users/test/file`} {
		if !filepath.IsAbs(path) {
			t.Errorf("IsAbs(%q) = false, want true", path)
		}
		absolute, err := filepath.Abs(path)
		if err != nil {
			t.Errorf("Abs(%q): %v", path, err)
			continue
		}
		if !filepath.IsAbs(absolute) {
			t.Errorf("Abs(%q) = %q, want an absolute path", path, absolute)
		}
		if filepath.IsLocal(path) {
			t.Errorf("IsLocal(%q) = true, want false", path)
		}
	}

	if filepath.IsAbs(`C:relative`) {
		t.Error(`IsAbs("C:relative") = true, want false`)
	}
	if filepath.IsLocal(`C:relative`) {
		t.Error(`IsLocal("C:relative") = true, want false`)
	}

	joined := filepath.Join("working-directory", `C:\Users\test\file`)
	if !filepath.IsAbs(joined) {
		t.Errorf("Join with drive-rooted path = %q, want absolute", joined)
	}
	if filepath.Base(joined) != "file" {
		t.Errorf("Base(Join(...)) = %q, want %q", filepath.Base(joined), "file")
	}
}

func TestRusticatedOpenAcceptsDriveRootedWorkingDirectory(t *testing.T) {
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if filepath.VolumeName(cwd) == "" {
		t.Skipf("working directory %q is not drive-rooted", cwd)
	}

	if _, err := os.ReadDir(cwd); err != nil {
		t.Fatalf("ReadDir(%q): %v", cwd, err)
	}
}

func TestRusticatedUserHomeAndTempDirHaveNoLeadingSlashBeforeDrive(t *testing.T) {
	if filepath.Separator != '\\' {
		t.Skip("requires Windows-style path separator")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	if len(home) > 0 && (home[0] == '/' || home[0] == '\\') {
		t.Fatalf("UserHomeDir() = %q, must not start with leading slash/backslash on Windows", home)
	}
	if filepath.VolumeName(home) == "" {
		t.Fatalf("UserHomeDir() = %q, want Windows drive volume", home)
	}

	joined := filepath.Join(home, "AppData", "Local", "kabibi", "litert_cache")
	if len(joined) > 0 && (joined[0] == '/' || joined[0] == '\\') {
		t.Fatalf("filepath.Join(UserHomeDir(), ...) = %q, must not start with leading slash/backslash", joined)
	}

	temp := os.TempDir()
	if len(temp) > 0 && (temp[0] == '/' || temp[0] == '\\') {
		t.Fatalf("TempDir() = %q, must not start with leading slash/backslash on Windows", temp)
	}
}

