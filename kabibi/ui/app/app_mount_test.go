package app

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMountPathDialogDefaultsToCurrentShellDirectory(t *testing.T) {
	initialDir := t.TempDir()
	activeDir := filepath.Join(initialDir, "active")
	if err := os.Mkdir(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	widget := NewWidget(AppWidgetOptions{WorkingDir: initialDir})
	defer widget.Close()
	if err := widget.Shell().SetDir(activeDir); err != nil {
		t.Fatal(err)
	}

	widget.openMountPathDialog()
	if got := widget.dialog.Fields[0].Input.Value(); got != activeDir {
		t.Fatalf("mount dialog default = %q, want current shell directory %q", got, activeDir)
	}
}

func TestResolveMountGuestPath(t *testing.T) {
	initialDir := t.TempDir()
	activeDir := filepath.Join(initialDir, "active")
	if err := os.Mkdir(activeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	homeDir := filepath.Join(initialDir, "home")
	if err := os.Mkdir(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeDir)
	t.Setenv("USERPROFILE", homeDir)

	widget := NewWidget(AppWidgetOptions{WorkingDir: initialDir})
	defer widget.Close()
	if err := widget.Shell().SetDir(activeDir); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "relative", path: "mounts/../data", want: filepath.Join(activeDir, "data")},
		{name: "home", path: "~", want: homeDir},
		{name: "home-relative", path: "~/mounts/../data", want: filepath.Join(homeDir, "data")},
		{name: "absolute", path: filepath.Join(initialDir, "mount"), want: filepath.Join(initialDir, "mount")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := widget.resolveMountGuestPath(test.path)
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("resolveMountGuestPath(%q) = %q, want %q", test.path, got, test.want)
			}
		})
	}

	if _, err := widget.resolveMountGuestPath(""); err == nil {
		t.Fatal("empty mount path should fail")
	}
}

func TestResolveMountGuestPathReportsMissingHome(t *testing.T) {
	t.Setenv("HOME", "")
	t.Setenv("USERPROFILE", "")

	widget := NewWidget(AppWidgetOptions{WorkingDir: t.TempDir()})
	defer widget.Close()
	if _, err := widget.resolveMountGuestPath("~/mount"); err == nil {
		t.Fatal("tilde path should report an unavailable home directory")
	}
}
