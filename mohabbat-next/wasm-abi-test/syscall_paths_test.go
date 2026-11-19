//go:build wasip1

package wasmabitest

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestRusticatedABISyscallSymlinkAndReadlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	link := filepath.Join(dir, "link")
	if err := os.WriteFile(target, []byte("symlink-target"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	if err := syscall.Symlink(target, link); err != nil {
		t.Fatalf("create symlink through syscall.Symlink: %v", err)
	}
	gotTarget, err := os.Readlink(link)
	if err != nil {
		t.Fatalf("read symlink through os.Readlink: %v", err)
	}
	if gotTarget != target {
		t.Fatalf("symlink target = %q, want %q", gotTarget, target)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatalf("lstat symlink: %v", err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("symlink mode = %v, want ModeSymlink", info.Mode())
	}
}

func TestRusticatedABISyscallChmodChangesWritePermission(t *testing.T) {
	path := filepath.Join(t.TempDir(), "permissions")
	if err := os.WriteFile(path, []byte("mode"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	defer func() {
		if err := syscall.Chmod(path, 0o600); err != nil {
			t.Errorf("restore file permissions: %v", err)
		}
	}()
	if err := syscall.Chmod(path, 0o400); err != nil {
		t.Fatalf("chmod through syscall.Chmod: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat chmodded file: %v", err)
	}
	if info.Mode().Perm()&0o222 != 0 {
		t.Fatalf("permissions after chmod = %#o, still includes write access", info.Mode().Perm())
	}
}

func TestRusticatedABISyscallUtimesNanoSetsModificationTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "timestamps")
	if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}

	want := time.Unix(1_600_000_000, 0)
	times := []syscall.Timespec{
		{Sec: want.Unix(), Nsec: 0},
		{Sec: want.Unix(), Nsec: 0},
	}
	if err := syscall.UtimesNano(path, times); err != nil {
		t.Fatalf("set timestamps through syscall.UtimesNano: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat timestamped file: %v", err)
	}
	if got := info.ModTime().Unix(); got != want.Unix() {
		t.Fatalf("modification time = %d, want %d", got, want.Unix())
	}
}
