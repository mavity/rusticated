package mohabbat

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveWashmhostCommandDir(t *testing.T) {
	root := t.TempDir()
	projectDir := filepath.Join(root, "kabibi")
	if err := os.MkdirAll(projectDir, 0o755); err != nil {
		t.Fatal(err)
	}

	got := resolveWashmhostCommandDir(root, projectDir)
	if got != projectDir {
		t.Fatalf("resolveWashmhostCommandDir(%q, %q) = %q, want %q", root, projectDir, got, projectDir)
	}

	got = resolveWashmhostCommandDir(root, "")
	if got != root {
		t.Fatalf("resolveWashmhostCommandDir(%q, %q) = %q, want %q", root, "", got, root)
	}
}
