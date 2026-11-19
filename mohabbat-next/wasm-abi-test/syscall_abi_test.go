//go:build wasip1

package wasmabitest

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"syscall"
)

func TestRusticatedABISyscallOpenWriteReadAndExclusiveCreate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "payload.bin")
	fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0o600)
	if err != nil {
		t.Fatalf("create file through syscall.Open: %v", err)
	}
	if fd < 3 {
		t.Fatalf("opened file descriptor = %d, want an allocated descriptor", fd)
	}
	defer func() {
		if fd >= 3 {
			_ = syscall.Close(fd)
		}
	}()

	want := []byte{0, 0x7f, 0x80, 0xff}
	n, err := syscall.Write(fd, want)
	if err != nil {
		t.Fatalf("write through syscall.Write: %v", err)
	}
	if n != len(want) {
		t.Fatalf("write count = %d, want %d", n, len(want))
	}
	if err := syscall.Close(fd); err != nil {
		t.Fatalf("close created file: %v", err)
	}
	fd = -1

	duplicateFD, err := syscall.Open(path, syscall.O_CREAT|syscall.O_EXCL|syscall.O_WRONLY, 0o600)
	if duplicateFD != -1 || !errors.Is(err, os.ErrExist) || !errors.Is(err, syscall.EEXIST) {
		t.Fatalf("exclusive open = (%d, %v), want -1 and an exist error", duplicateFD, err)
	}

	readFD, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("open file for reading: %v", err)
	}
	defer func() {
		if readFD >= 3 {
			_ = syscall.Close(readFD)
		}
	}()
	got := make([]byte, len(want))
	n, err = syscall.Read(readFD, got)
	if err != nil {
		t.Fatalf("read through syscall.Read: %v", err)
	}
	if n != len(want) || string(got) != string(want) {
		t.Fatalf("read = (%d, %v), want (%d, %v)", n, got, len(want), want)
	}
	if err := syscall.Close(readFD); err != nil {
		t.Fatalf("close read descriptor: %v", err)
	}
	closedFD := readFD
	readFD = -1
	if n, err := syscall.Read(closedFD, make([]byte, 1)); n != 0 || !errors.Is(err, syscall.EBADF) {
		t.Fatalf("read closed descriptor = (%d, %v), want 0 and EBADF", n, err)
	}
}

func TestRusticatedABISyscallInputValidationAndErrnoMapping(t *testing.T) {
	fd, err := syscall.Open("", syscall.O_RDONLY, 0)
	if fd != -1 || !errors.Is(err, syscall.EINVAL) {
		t.Errorf("open empty path = (%d, %v), want -1 and EINVAL", fd, err)
	}

	var stat syscall.Stat_t
	if err := syscall.Stat("", &stat); !errors.Is(err, syscall.EINVAL) {
		t.Errorf("stat empty path error = %v, want EINVAL", err)
	}

	n, err := syscall.Read(-1, []byte{0})
	if n != 0 || !errors.Is(err, syscall.EBADF) {
		t.Errorf("read invalid descriptor = (%d, %v), want 0 and EBADF", n, err)
	}
	n, err = syscall.Write(-1, []byte{1})
	if n != 0 || !errors.Is(err, syscall.EBADF) {
		t.Errorf("write invalid descriptor = (%d, %v), want 0 and EBADF", n, err)
	}
	if err := syscall.Ftruncate(-1, 0); !errors.Is(err, syscall.EBADF) {
		t.Errorf("truncate invalid descriptor error = %v, want EBADF", err)
	}
}
