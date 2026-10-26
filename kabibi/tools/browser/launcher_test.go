package browser

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBrowserCandidatePathsWindows(t *testing.T) {
	t.Setenv("ProgramFiles", `C:\Program Files`)
	t.Setenv("ProgramFiles(x86)", `C:\Program Files (x86)`)
	t.Setenv("LocalAppData", `C:\Users\tester\AppData\Local`)

	got := browserCandidatePaths("windows")
	if len(got) != 15 {
		t.Fatalf("got %d Windows candidates, want 15: %v", len(got), got)
	}
	for _, candidate := range got {
		if !strings.HasSuffix(strings.ToLower(candidate), ".exe") {
			t.Errorf("Windows candidate %q is not an executable", candidate)
		}
	}
	want := filepath.Join(`C:\Program Files`, `Google\Chrome\Application\chrome.exe`)
	if !containsPath(got, want) {
		t.Errorf("Windows candidates omit Chrome under ProgramFiles: %v", got)
	}
}

func TestBrowserCandidatePathsWindowsUseHomeWhenInstallEnvironmentIsMissing(t *testing.T) {
	t.Setenv("ProgramFiles", "")
	t.Setenv("ProgramFiles(x86)", "")
	t.Setenv("LocalAppData", "")
	t.Setenv("HOME", `C:\Users\tester`)
	t.Setenv("USERPROFILE", `C:\Users\tester`)

	got := browserCandidatePaths("windows")
	for _, want := range []string{
		filepath.Join(`C:`, "Program Files", `Google\Chrome\Application\chrome.exe`),
		filepath.Join(`C:`, "Program Files (x86)", `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(`C:\Users\tester`, "AppData", "Local", `Google\Chrome\Application\chrome.exe`),
	} {
		if !containsPath(got, want) {
			t.Errorf("Windows candidates omit fallback %q: %v", want, got)
		}
	}
}

func TestBrowserCandidatePathsNonWindows(t *testing.T) {
	got := browserCandidatePaths("linux")
	for _, candidate := range got {
		if strings.HasSuffix(strings.ToLower(candidate), ".exe") {
			t.Errorf("non-Windows candidate %q unexpectedly uses Windows executable suffix", candidate)
		}
	}
	if len(nativeBrowserNames("linux")) == 0 {
		t.Fatal("Linux native executable lookup names are empty")
	}
	if len(nativeBrowserNames("darwin")) != 0 {
		t.Fatal("macOS should use application-path candidates rather than Linux executable names")
	}
}

func containsPath(paths []string, want string) bool {
	for _, path := range paths {
		if path == want {
			return true
		}
	}
	return false
}
