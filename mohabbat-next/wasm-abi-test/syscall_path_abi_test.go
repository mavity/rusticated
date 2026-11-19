//go:build wasip1

package wasmabitest

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestRusticatedABISyscallDirectoryAndHardLinkOperations(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "created")
	if err := syscall.Mkdir(dir, 0o700); err != nil {
		t.Fatalf("mkdir through syscall.Mkdir: %v", err)
	}
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		t.Fatalf("created directory stat = (%v, %v), want a directory", info, err)
	}

	source := filepath.Join(dir, "source")
	link := filepath.Join(dir, "hard-link")
	want := []byte("hard-link-data")
	if err := os.WriteFile(source, want, 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	if err := syscall.Link(source, link); err != nil {
		t.Fatalf("create hard link through syscall.Link: %v", err)
	}
	got, err := os.ReadFile(link)
	if err != nil {
		t.Fatalf("read hard link: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("hard-link contents = %q, want %q", got, want)
	}
	if err := syscall.Unlink(link); err != nil {
		t.Fatalf("unlink through syscall.Unlink: %v", err)
	}
	if _, err := os.Stat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unlinked path stat error = %v, want not-exist", err)
	}
	if got, err := os.ReadFile(source); err != nil || string(got) != string(want) {
		t.Fatalf("source after unlink = (%q, %v), want original contents", got, err)
	}
	if err := syscall.Unlink(source); err != nil {
		t.Fatalf("unlink source: %v", err)
	}
	if err := syscall.Rmdir(dir); err != nil {
		t.Fatalf("remove directory through syscall.Rmdir: %v", err)
	}
}

func TestRusticatedABISyscallPathValidationAndErrorMapping(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing-link")
	for _, test := range []struct {
		name string
		call func() error
		want error
	}{
		{"link empty source", func() error { return syscall.Link("", "link") }, syscall.EINVAL},
		{"link unsupported flags", func() error { return syscall.LinkWithFlags("source", "link", 2) }, syscall.EINVAL},
		{"symlink empty destination", func() error { return syscall.Symlink("target", "") }, syscall.EINVAL},
		{"symlink invalid kind", func() error { return syscall.SymlinkWithType("target", "link", 3) }, syscall.EINVAL},
		{"readlink empty path", func() error { _, err := syscall.Readlink("", nil); return err }, syscall.EINVAL},
		{"chmod empty path", func() error { return syscall.Chmod("", 0) }, syscall.EINVAL},
		{"fchmod invalid descriptor", func() error { return syscall.Fchmod(-1, 0) }, syscall.EBADF},
		{"utimes malformed times", func() error { return syscall.UtimesNano("path", []syscall.Timespec{}) }, syscall.EINVAL},
		{"utimes unsupported flags", func() error { return syscall.UtimesNanoAt(-100, "path", nil, 1) }, syscall.EINVAL},
		{"utimes invalid directory descriptor", func() error { return syscall.UtimesNanoAt(-1, "relative", nil, 0) }, syscall.EBADF},
		{"readlink missing path", func() error { _, err := syscall.Readlink(missing, make([]byte, 8)); return err }, syscall.ENOENT},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := test.call(); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}
