package shell

import (
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"
)

func TestNewHostFSUsesFilesystemRootAndFastStatInterfaces(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "tracked.txt")
	if err := os.WriteFile(file, []byte("contents"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	fsys, virtualDir, err := newHostFS(dir)
	if err != nil {
		t.Fatalf("newHostFS: %v", err)
	}
	if _, ok := any(fsys).(fs.StatFS); !ok {
		t.Fatal("Git filesystem does not implement fs.StatFS")
	}
	if _, ok := any(fsys).(fs.ReadDirFS); !ok {
		t.Fatal("Git filesystem does not implement fs.ReadDirFS")
	}
	info, err := fsys.Stat(path.Join(virtualDir, "tracked.txt"))
	if err != nil {
		t.Fatalf("stat virtual file: %v", err)
	}
	if info.Size() != int64(len("contents")) {
		t.Fatalf("file size = %d, want %d", info.Size(), len("contents"))
	}
	entries, err := fsys.ReadDir(virtualDir)
	if err != nil {
		t.Fatalf("read virtual directory: %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "tracked.txt" {
		t.Fatalf("directory entries = %#v, want tracked.txt", entries)
	}
	if got := fsys.toOSPath(path.Join(virtualDir, "tracked.txt")); got != file {
		t.Fatalf("virtual path maps to %q, want %q", got, file)
	}
}
