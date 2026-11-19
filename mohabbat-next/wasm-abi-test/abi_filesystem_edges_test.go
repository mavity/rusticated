//go:build wasip1

package wasmabitest

import (
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestRusticatedABIFilesystemOpenEdges(t *testing.T) {
	t.Run("truncate-on-open clears prior contents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncate-on-open")
		if err := os.WriteFile(path, []byte("old contents"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_TRUNC, 0)
		if err != nil {
			t.Fatalf("open with truncate: %v", err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatalf("close: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || len(got) != 0 {
			t.Fatalf("contents after truncate-on-open = (%q, %v), want empty", got, err)
		}
	})

	t.Run("create without truncate preserves an existing file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "create-existing")
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "keep" {
			t.Fatalf("contents after non-truncating create = (%q, %v), want %q", got, err, "keep")
		}
	})

	t.Run("append open writes after existing bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "append")
		if err := os.WriteFile(path, []byte("head"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND, 0)
		if err != nil {
			t.Fatalf("open append: %v", err)
		}
		if n, err := syscall.Write(fd, []byte("-tail")); err != nil || n != 5 {
			t.Fatalf("append write = (%d, %v), want (5, nil)", n, err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatalf("close: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "head-tail" {
			t.Fatalf("contents after append = (%q, %v), want %q", got, err, "head-tail")
		}
	})

	t.Run("append mode remains effective after seeking to the beginning", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "append-after-seek")
		if err := os.WriteFile(path, []byte("first"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_APPEND, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := syscall.Seek(fd, 0, syscall.WHENCE_SET); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if n, err := syscall.Write(fd, []byte("!")); err != nil || n != 1 {
			_ = syscall.Close(fd)
			t.Fatalf("append after seek = (%d, %v)", n, err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "first!" {
			t.Fatalf("contents after append at non-end position = (%q, %v), want %q", got, err, "first!")
		}
	})

	t.Run("separate opens allocate usable descriptors", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "two-handles")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		first, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		second, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			_ = syscall.Close(first)
			t.Fatal(err)
		}
		if first == second {
			t.Fatalf("two open calls returned descriptor %d", first)
		}
		if err := syscall.Close(first); err != nil {
			t.Fatalf("close first descriptor: %v", err)
		}
		if n, err := syscall.Read(second, make([]byte, 4)); err != nil || n != 4 {
			t.Fatalf("read from remaining descriptor = (%d, %v), want 4 bytes", n, err)
		}
		if err := syscall.Close(second); err != nil {
			t.Fatalf("close second descriptor: %v", err)
		}
	})

	t.Run("read-write open supports reading and overwriting on one handle", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "read-write")
		if err := os.WriteFile(path, []byte("abcd"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 2)
		if n, err := syscall.Read(fd, buf); err != nil || n != 2 || string(buf) != "ab" {
			_ = syscall.Close(fd)
			t.Fatalf("read from read-write handle = (%d, %q, %v)", n, buf, err)
		}
		if n, err := syscall.Write(fd, []byte("XY")); err != nil || n != 2 {
			_ = syscall.Close(fd)
			t.Fatalf("write from read-write handle = (%d, %v)", n, err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "abXY" {
			t.Fatalf("contents after read-write handle = (%q, %v), want %q", got, err, "abXY")
		}
	})

	t.Run("create and truncate opens a missing path as an empty file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "create-truncate")
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_TRUNC|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != 0 {
			t.Fatalf("created file stat = (%v, %v), want an empty file", info, err)
		}
	})

	t.Run("create below a missing parent fails without creating a path", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-parent", "child")
		if fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_WRONLY, 0o600); err == nil {
			_ = syscall.Close(fd)
			t.Fatal("create below missing parent succeeded")
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("missing parent after failed create = %v, want not-exist", err)
		}
	})

	t.Run("openat absolute path does not depend on the directory descriptor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absolute-openat")
		if err := os.WriteFile(path, []byte("absolute"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Openat(-1, path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("openat absolute path: %v", err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, len("absolute"))
		if n, err := syscall.Read(fd, buf); err != nil || n != len(buf) || string(buf) != "absolute" {
			t.Fatalf("read absolute openat path = (%d, %q, %v)", n, buf, err)
		}
	})

	t.Run("openat resolves a relative path under its directory handle", func(t *testing.T) {
		root := t.TempDir()
		dirFD, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatalf("open directory: %v", err)
		}
		defer syscall.Close(dirFD)

		fd, err := syscall.Openat(dirFD, "relative.txt", syscall.O_CREAT|syscall.O_RDWR, 0o600)
		if err != nil {
			t.Fatalf("openat relative file: %v", err)
		}
		if n, err := syscall.Write(fd, []byte("relative")); err != nil || n != len("relative") {
			t.Fatalf("write through openat descriptor = (%d, %v)", n, err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatalf("close file: %v", err)
		}
		got, err := os.ReadFile(filepath.Join(root, "relative.txt"))
		if err != nil || string(got) != "relative" {
			t.Fatalf("openat file contents = (%q, %v), want %q", got, err, "relative")
		}
	})
}

func TestRusticatedABIFilesystemReadWriteEdges(t *testing.T) {
	t.Run("successive reads advance by the requested buffer length", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "read-chunks")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		first, second := make([]byte, 2), make([]byte, 2)
		if n, err := syscall.Read(fd, first); err != nil || n != 2 || string(first) != "ab" {
			t.Fatalf("first chunk = (%d, %q, %v)", n, first, err)
		}
		if n, err := syscall.Read(fd, second); err != nil || n != 2 || string(second) != "cd" {
			t.Fatalf("second chunk = (%d, %q, %v)", n, second, err)
		}
	})

	t.Run("independent opens maintain independent read positions", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "independent-read-positions")
		if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		first, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(first)
		second, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(second)
		firstByte, secondByte := make([]byte, 1), make([]byte, 1)
		if n, err := syscall.Read(first, firstByte); err != nil || n != 1 || string(firstByte) != "a" {
			t.Fatalf("first descriptor read = (%d, %q, %v)", n, firstByte, err)
		}
		if n, err := syscall.Read(second, secondByte); err != nil || n != 1 || string(secondByte) != "a" {
			t.Fatalf("independent descriptor read = (%d, %q, %v), want first byte", n, secondByte, err)
		}
	})

	t.Run("short read returns available bytes before EOF", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "short-read")
		if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 8)
		n, err := syscall.Read(fd, buf)
		if err != nil || n != 3 || string(buf[:n]) != "abc" {
			t.Fatalf("short read = (%d, %q, %v), want (3, %q, nil)", n, buf[:n], err, "abc")
		}
		n, err = syscall.Read(fd, buf)
		if err != nil || n != 0 {
			t.Fatalf("read at EOF = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("partial read leaves the unused destination tail untouched", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "read-tail")
		if err := os.WriteFile(path, []byte("xy"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := []byte{'!', '!', '!'}
		if n, err := syscall.Read(fd, buf); err != nil || n != 2 || string(buf) != "xy!" {
			t.Fatalf("partial read buffer = (%d, %q, %v), want %q", n, buf, err, "xy!")
		}
	})

	t.Run("zero-length read on invalid descriptor is a no-op", func(t *testing.T) {
		if n, err := syscall.Read(-1, nil); n != 0 || err != nil {
			t.Fatalf("empty read = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("zero-length read on a valid descriptor leaves its position unchanged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty-read")
		if err := os.WriteFile(path, []byte("data"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Read(fd, nil); err != nil || n != 0 {
			t.Fatalf("empty read = (%d, %v), want (0, nil)", n, err)
		}
		buf := make([]byte, 1)
		if n, err := syscall.Read(fd, buf); err != nil || n != 1 || string(buf) != "d" {
			t.Fatalf("read after empty read = (%d, %q, %v), want first byte", n, buf, err)
		}
	})

	t.Run("pread does not move the sequential read position", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pread")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		positioned := make([]byte, 2)
		if n, err := syscall.Pread(fd, positioned, 3); err != nil || n != 2 || string(positioned) != "de" {
			t.Fatalf("pread = (%d, %q, %v), want (2, %q, nil)", n, positioned, err, "de")
		}
		sequential := make([]byte, 2)
		if n, err := syscall.Read(fd, sequential); err != nil || n != 2 || string(sequential) != "ab" {
			t.Fatalf("read after pread = (%d, %q, %v), want (2, %q, nil)", n, sequential, err, "ab")
		}
	})

	t.Run("pread at or beyond EOF returns zero bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pread-eof")
		if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		for _, offset := range []int64{3, 10} {
			buf := make([]byte, 2)
			if n, err := syscall.Pread(fd, buf, offset); err != nil || n != 0 {
				t.Fatalf("pread at offset %d = (%d, %v), want (0, nil)", offset, n, err)
			}
		}
	})

	t.Run("pread near EOF returns only the remaining bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pread-short")
		if err := os.WriteFile(path, []byte("abc"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 5)
		if n, err := syscall.Pread(fd, buf, 1); err != nil || n != 2 || string(buf[:n]) != "bc" {
			t.Fatalf("short pread = (%d, %q, %v), want two remaining bytes", n, buf[:n], err)
		}
	})

	t.Run("pread rejects a negative offset", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "negative-pread"), syscall.O_CREAT|syscall.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Pread(fd, make([]byte, 1), -1); n != 0 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("negative-offset pread = (%d, %v), want (0, EINVAL)", n, err)
		}
	})

	t.Run("read from a write-only handle fails", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "write-only-read"), syscall.O_CREAT|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Read(fd, make([]byte, 1)); n != 0 || err == nil {
			t.Fatalf("read from write-only handle = (%d, %v), want an error", n, err)
		}
	})

	t.Run("read after a seek starts at the selected offset", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "read-after-seek")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if offset, err := syscall.Seek(fd, 3, syscall.WHENCE_SET); err != nil || offset != 3 {
			t.Fatalf("seek before read = (%d, %v)", offset, err)
		}
		buf := make([]byte, 2)
		if n, err := syscall.Read(fd, buf); err != nil || n != 2 || string(buf) != "de" {
			t.Fatalf("read after seek = (%d, %q, %v), want %q", n, buf, err, "de")
		}
	})

	t.Run("read observes the new end after descriptor truncation", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "read-after-truncate")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if _, err := syscall.Seek(fd, 2, syscall.WHENCE_SET); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Ftruncate(fd, 3); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if n, err := syscall.Read(fd, buf); err != nil || n != 1 || string(buf[:n]) != "c" {
			t.Fatalf("read at truncated end = (%d, %q, %v), want one byte", n, buf[:n], err)
		}
	})

	t.Run("pwrite does not move the sequential file position", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pwrite")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Pwrite(fd, []byte("XY"), 3); err != nil || n != 2 {
			t.Fatalf("pwrite = (%d, %v), want (2, nil)", n, err)
		}
		if offset, err := syscall.Seek(fd, 0, syscall.WHENCE_CUR); err != nil || offset != 0 {
			t.Fatalf("position after pwrite = (%d, %v), want (0, nil)", offset, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "abcXYf" {
			t.Fatalf("contents after pwrite = (%q, %v), want %q", got, err, "abcXYf")
		}
	})

	t.Run("pwrite beyond EOF creates a zero-filled gap", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "pwrite-gap")
		if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Pwrite(fd, []byte("z"), 3); err != nil || n != 1 {
			t.Fatalf("pwrite beyond EOF = (%d, %v), want (1, nil)", n, err)
		}
		got, err := os.ReadFile(path)
		want := []byte{'a', 0, 0, 'z'}
		if err != nil || string(got) != string(want) {
			t.Fatalf("contents after pwrite gap = (%v, %v), want %v", got, err, want)
		}
	})

	t.Run("pwrite rejects a negative offset", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "negative-pwrite"), syscall.O_CREAT|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Pwrite(fd, []byte("x"), -1); n != 0 || !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("negative-offset pwrite = (%d, %v), want (0, EINVAL)", n, err)
		}
	})

	t.Run("write advances the shared file position", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "write-position"), syscall.O_CREAT|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Write(fd, []byte("abc")); err != nil || n != 3 {
			t.Fatalf("write = (%d, %v)", n, err)
		}
		if offset, err := syscall.Seek(fd, 0, syscall.WHENCE_CUR); err != nil || offset != 3 {
			t.Fatalf("position after write = (%d, %v), want (3, nil)", offset, err)
		}
	})

	t.Run("sequential writes compose without replacing earlier bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sequential-writes")
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range []string{"one", "-", "two"} {
			if n, err := syscall.Write(fd, []byte(part)); err != nil || n != len(part) {
				t.Fatalf("write %q = (%d, %v)", part, n, err)
			}
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "one-two" {
			t.Fatalf("contents after sequential writes = (%q, %v), want %q", got, err, "one-two")
		}
	})

	t.Run("zero-length write on invalid descriptor is a no-op", func(t *testing.T) {
		if n, err := syscall.Write(-1, nil); n != 0 || err != nil {
			t.Fatalf("empty write = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("write after seeking past EOF fills the gap with zeroes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "sparse-gap")
		if err := os.WriteFile(path, []byte("a"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if offset, err := syscall.Seek(fd, 4, syscall.WHENCE_SET); err != nil || offset != 4 {
			_ = syscall.Close(fd)
			t.Fatalf("seek past EOF = (%d, %v), want (4, nil)", offset, err)
		}
		if n, err := syscall.Write(fd, []byte("z")); err != nil || n != 1 {
			_ = syscall.Close(fd)
			t.Fatalf("write past EOF = (%d, %v), want (1, nil)", n, err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		want := []byte{'a', 0, 0, 0, 'z'}
		if err != nil || string(got) != string(want) {
			t.Fatalf("contents after gap write = (%v, %v), want %v", got, err, want)
		}
	})
}

func TestRusticatedABIFilesystemSeekEdges(t *testing.T) {
	path := filepath.Join(t.TempDir(), "seek")
	if err := os.WriteFile(path, []byte("012345"), 0o600); err != nil {
		t.Fatal(err)
	}
	fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer syscall.Close(fd)

	for _, test := range []struct {
		name   string
		offset int64
		whence int
		want   int64
	}{
		{"absolute start", 2, syscall.WHENCE_SET, 2},
		{"relative current", 2, syscall.WHENCE_CUR, 4},
		{"relative end", -1, syscall.WHENCE_END, 5},
		{"zero from end", 0, syscall.WHENCE_END, 6},
		{"negative offset from end", -3, syscall.WHENCE_END, 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "relative current" {
				if _, err := syscall.Seek(fd, 2, syscall.WHENCE_SET); err != nil {
					t.Fatal(err)
				}
			}
			if got, err := syscall.Seek(fd, test.offset, test.whence); err != nil || got != test.want {
				t.Fatalf("seek = (%d, %v), want (%d, nil)", got, err, test.want)
			}
		})
	}

	t.Run("invalid whence is rejected", func(t *testing.T) {
		if _, err := syscall.Seek(fd, 0, 99); err == nil {
			t.Fatal("seek with invalid whence succeeded")
		}
	})
	t.Run("negative resulting position is rejected", func(t *testing.T) {
		if _, err := syscall.Seek(fd, -1, syscall.WHENCE_SET); err == nil {
			t.Fatal("seek before the start of the file succeeded")
		}
	})
	t.Run("zero relative seek preserves current position", func(t *testing.T) {
		if _, err := syscall.Seek(fd, 3, syscall.WHENCE_SET); err != nil {
			t.Fatal(err)
		}
		if got, err := syscall.Seek(fd, 0, syscall.WHENCE_CUR); err != nil || got != 3 {
			t.Fatalf("zero relative seek = (%d, %v), want (3, nil)", got, err)
		}
	})
	t.Run("seek on invalid descriptor reports bad descriptor", func(t *testing.T) {
		if _, err := syscall.Seek(-1, 0, syscall.WHENCE_SET); !errors.Is(err, syscall.EBADF) {
			t.Fatalf("seek invalid descriptor error = %v, want EBADF", err)
		}
	})
	t.Run("seek past EOF does not itself extend the file", func(t *testing.T) {
		if _, err := syscall.Seek(fd, 20, syscall.WHENCE_SET); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() != 6 {
			t.Fatalf("size after seek past EOF = %d, want unchanged size 6", info.Size())
		}
	})
	t.Run("sequential read advances the seek position", func(t *testing.T) {
		if _, err := syscall.Seek(fd, 0, syscall.WHENCE_SET); err != nil {
			t.Fatal(err)
		}
		if n, err := syscall.Read(fd, make([]byte, 2)); err != nil || n != 2 {
			t.Fatalf("read before tell = (%d, %v)", n, err)
		}
		if got, err := syscall.Seek(fd, 0, syscall.WHENCE_CUR); err != nil || got != 2 {
			t.Fatalf("position after read = (%d, %v), want (2, nil)", got, err)
		}
	})
}

func TestRusticatedABIFilesystemTruncateAndCloseEdges(t *testing.T) {
	t.Run("descriptor truncate shrinks the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "shrink")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Ftruncate(fd, 3); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "abc" {
			t.Fatalf("contents after shrink = (%q, %v), want %q", got, err, "abc")
		}
	})

	t.Run("descriptor truncate extends with zero-filled bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "extend")
		if err := os.WriteFile(path, []byte("xy"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Ftruncate(fd, 5); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		want := []byte{'x', 'y', 0, 0, 0}
		if err != nil || string(got) != string(want) {
			t.Fatalf("contents after extension = (%v, %v), want %v", got, err, want)
		}
	})

	t.Run("descriptor truncate to zero empties the file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncate-zero")
		if err := os.WriteFile(path, []byte("remove all"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Ftruncate(fd, 0); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != 0 {
			t.Fatalf("stat after zero truncate = (%v, %v), want size zero", info, err)
		}
	})

	t.Run("truncate to the existing size preserves contents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncate-same-size")
		if err := os.WriteFile(path, []byte("same"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Ftruncate(fd, 4); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "same" {
			t.Fatalf("contents after same-size truncate = (%q, %v), want %q", got, err, "same")
		}
	})

	t.Run("descriptor truncate preserves the current seek position", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncate-position")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := syscall.Seek(fd, 5, syscall.WHENCE_SET); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if err := syscall.Ftruncate(fd, 2); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if pos, err := syscall.Seek(fd, 0, syscall.WHENCE_CUR); err != nil || pos != 5 {
			_ = syscall.Close(fd)
			t.Fatalf("position after truncate = (%d, %v), want (5, nil)", pos, err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("truncate through one descriptor is visible through another", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "truncate-shared")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		truncFD, err := syscall.Open(path, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(truncFD)
		readFD, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(readFD)
		if err := syscall.Ftruncate(truncFD, 2); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8)
		if n, err := syscall.Read(readFD, buf); err != nil || n != 2 || string(buf[:n]) != "ab" {
			t.Fatalf("other descriptor after truncate = (%d, %q, %v)", n, buf[:n], err)
		}
	})

	t.Run("truncate through a hard link changes the shared file", func(t *testing.T) {
		root := t.TempDir()
		source, alias := filepath.Join(root, "source"), filepath.Join(root, "alias")
		if err := os.WriteFile(source, []byte("shared-data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, alias); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(alias, syscall.O_RDWR, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if err := syscall.Ftruncate(fd, 6); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(source)
		if err != nil || string(got) != "shared" {
			t.Fatalf("original name after linked truncate = (%q, %v), want %q", got, err, "shared")
		}
	})

	t.Run("path truncate changes only the requested length", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "path-truncate")
		if err := os.WriteFile(path, []byte("abcdef"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Truncate(path, 4); err != nil {
			t.Fatalf("path truncate: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "abcd" {
			t.Fatalf("contents after path truncate = (%q, %v), want %q", got, err, "abcd")
		}
	})

	t.Run("path truncate extends with zero-filled bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "path-extend")
		if err := os.WriteFile(path, []byte("p"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Truncate(path, 3); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		want := []byte{'p', 0, 0}
		if err != nil || string(got) != string(want) {
			t.Fatalf("contents after path extension = (%v, %v), want %v", got, err, want)
		}
	})

	t.Run("path truncate to zero removes all file bytes", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "path-zero")
		if err := os.WriteFile(path, []byte("remove"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Truncate(path, 0); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() != 0 {
			t.Fatalf("stat after path truncate to zero = (%v, %v)", info, err)
		}
	})

	t.Run("negative path truncate length is invalid", func(t *testing.T) {
		if err := syscall.Truncate(filepath.Join(t.TempDir(), "file"), -1); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("negative truncate error = %v, want EINVAL", err)
		}
	})

	t.Run("negative descriptor truncate length is invalid", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "negative-ftruncate"), syscall.O_CREAT|syscall.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if err := syscall.Ftruncate(fd, -1); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("negative descriptor truncate error = %v, want EINVAL", err)
		}
	})

	t.Run("second close reports bad descriptor", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "close-twice"), syscall.O_CREAT|syscall.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Close(fd); !errors.Is(err, syscall.EBADF) {
			t.Fatalf("second close error = %v, want EBADF", err)
		}
	})

	t.Run("closed descriptor cannot write", func(t *testing.T) {
		fd, err := syscall.Open(filepath.Join(t.TempDir(), "closed-write"), syscall.O_CREAT|syscall.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		if n, err := syscall.Write(fd, []byte("x")); n != 0 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("write after close = (%d, %v), want (0, EBADF)", n, err)
		}
	})

	t.Run("closing an invalid descriptor reports bad descriptor", func(t *testing.T) {
		if err := syscall.Close(1 << 15); !errors.Is(err, syscall.EBADF) {
			t.Fatalf("close invalid descriptor error = %v, want EBADF", err)
		}
	})

	t.Run("closing a directory handle invalidates subsequent enumeration", func(t *testing.T) {
		fd, err := syscall.Open(t.TempDir(), syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		if n, err := syscall.ReadDir(fd, make([]byte, 64), 0); n != 0 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("directory read after close = (%d, %v), want (0, EBADF)", n, err)
		}
	})
}

func parseRusticatedDirents(buf []byte) (map[string]byte, error) {
	entries := make(map[string]byte)
	for offset := 0; offset < len(buf); {
		if len(buf)-offset < 24 {
			return nil, io.ErrUnexpectedEOF
		}
		nameLen := int(binary.LittleEndian.Uint32(buf[offset+16 : offset+20]))
		end := offset + 24 + nameLen
		if end > len(buf) {
			return nil, io.ErrUnexpectedEOF
		}
		name := string(buf[offset+24 : end])
		entries[name] = buf[offset+20]
		offset = end
	}
	return entries, nil
}

func TestRusticatedABIFilesystemDirectoryReadEdges(t *testing.T) {
	t.Run("small buffers retain entries for the next read", func(t *testing.T) {
		root := t.TempDir()
		for _, name := range []string{"a", "longer-name"} {
			if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)

		all := make(map[string]byte)
		for _, capacity := range []int{25, 128, 128} {
			buf := make([]byte, capacity)
			n, err := syscall.ReadDir(fd, buf, 0)
			if err != nil {
				t.Fatalf("read directory: %v", err)
			}
			entries, err := parseRusticatedDirents(buf[:n])
			if err != nil {
				t.Fatalf("decode directory entries: %v", err)
			}
			for name, kind := range entries {
				all[name] = kind
			}
		}
		if len(all) != 2 || all["a"] != 4 || all["longer-name"] != 4 {
			t.Fatalf("directory entries = %v, want both regular files", all)
		}
	})

	t.Run("directory entry types distinguish files and subdirectories", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "file"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(root, "subdir"), 0o700); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 256)
		n, err := syscall.ReadDir(fd, buf, 0)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := parseRusticatedDirents(buf[:n])
		if err != nil {
			t.Fatal(err)
		}
		if entries["file"] != 4 || entries["subdir"] != 3 {
			t.Fatalf("directory entry types = %v, want file=4 and subdir=3", entries)
		}
	})

	t.Run("empty directory reaches end without entries", func(t *testing.T) {
		fd, err := syscall.Open(t.TempDir(), syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		n, err := syscall.ReadDir(fd, make([]byte, 128), 0)
		if err != nil || n != 0 {
			t.Fatalf("empty directory read = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("zero-length directory read is a no-op", func(t *testing.T) {
		if n, err := syscall.ReadDir(-1, nil, 0); n != 0 || err != nil {
			t.Fatalf("empty directory read = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("non-empty directory read rejects an invalid descriptor", func(t *testing.T) {
		if n, err := syscall.ReadDir(-1, make([]byte, 64), 0); n != 0 || !errors.Is(err, syscall.EBADF) {
			t.Fatalf("invalid directory read = (%d, %v), want (0, EBADF)", n, err)
		}
	})

	t.Run("a completed directory read advances its cursor to EOF", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "only-entry"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 128)
		n, err := syscall.ReadDir(fd, buf, 0)
		if err != nil || n == 0 {
			t.Fatalf("first directory read = (%d, %v), want an entry", n, err)
		}
		entries, err := parseRusticatedDirents(buf[:n])
		if err != nil || entries["only-entry"] != 4 {
			t.Fatalf("first entries = (%v, %v), want only-entry", entries, err)
		}
		if n, err := syscall.ReadDir(fd, buf, 0); err != nil || n != 0 {
			t.Fatalf("read at directory EOF = (%d, %v), want (0, nil)", n, err)
		}
	})

	t.Run("separate directory descriptors enumerate independently", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "entry"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		first, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(first)
		second, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(second)
		for _, fd := range []int{first, second} {
			buf := make([]byte, 128)
			n, err := syscall.ReadDir(fd, buf, 0)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := parseRusticatedDirents(buf[:n])
			if err != nil || entries["entry"] != 4 {
				t.Fatalf("entries from descriptor %d = (%v, %v), want entry", fd, entries, err)
			}
		}
	})

	t.Run("a zero-capacity read does not consume directory entries", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "entry"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.ReadDir(fd, nil, 0); err != nil || n != 0 {
			t.Fatalf("zero-capacity read = (%d, %v), want (0, nil)", n, err)
		}
		buf := make([]byte, 128)
		n, err := syscall.ReadDir(fd, buf, 0)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := parseRusticatedDirents(buf[:n])
		if err != nil || entries["entry"] != 4 {
			t.Fatalf("entries after zero-capacity read = (%v, %v), want entry", entries, err)
		}
	})

	t.Run("directory enumeration identifies symlink entries", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
		if err := os.WriteFile(target, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 256)
		n, err := syscall.ReadDir(fd, buf, 0)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := parseRusticatedDirents(buf[:n])
		if err != nil || entries["link"] != 7 {
			t.Fatalf("symlink directory entry = (%v, %v), want type 7", entries, err)
		}
	})

	t.Run("enumerating a child directory returns only its children", func(t *testing.T) {
		root := t.TempDir()
		child := filepath.Join(root, "child")
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "sibling"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(child, "inside"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(child, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 128)
		n, err := syscall.ReadDir(fd, buf, 0)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := parseRusticatedDirents(buf[:n])
		if err != nil || len(entries) != 1 || entries["inside"] != 4 {
			t.Fatalf("child directory entries = (%v, %v), want only inside", entries, err)
		}
	})

	t.Run("directory entry names preserve UTF-8 bytes", func(t *testing.T) {
		root := t.TempDir()
		const name = "café"
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		buf := make([]byte, 128)
		n, err := syscall.ReadDir(fd, buf, 0)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := parseRusticatedDirents(buf[:n])
		if err != nil || entries[name] != 4 {
			t.Fatalf("UTF-8 directory entries = (%v, %v), want %q", entries, err, name)
		}
	})

	t.Run("directory reads continue across the host buffer boundary", func(t *testing.T) {
		root := t.TempDir()
		want := make(map[string]bool)
		for i := 0; i < 30; i++ {
			nameBytes := make([]byte, 80)
			for j := range nameBytes {
				nameBytes[j] = 'x'
			}
			nameBytes[0], nameBytes[1], nameBytes[2] = 'e', byte('0'+i/10), byte('0'+i%10)
			name := string(nameBytes)
			want[name] = true
			if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		fd, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		got := make(map[string]bool)
		buf := make([]byte, 4096)
		for {
			n, err := syscall.ReadDir(fd, buf, 0)
			if err != nil {
				t.Fatal(err)
			}
			if n == 0 {
				break
			}
			entries, err := parseRusticatedDirents(buf[:n])
			if err != nil {
				t.Fatal(err)
			}
			for name := range entries {
				got[name] = true
			}
		}
		if len(got) != len(want) {
			t.Fatalf("entries across host-buffer reads = %d, want %d", len(got), len(want))
		}
		for name := range want {
			if !got[name] {
				t.Errorf("missing directory entry %q", name)
			}
		}
	})

}

func TestRusticatedABIFilesystemStatEdges(t *testing.T) {
	t.Run("fstat observes the file behind an open descriptor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fstat")
		if err := os.WriteFile(path, []byte("fstat-data"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		if st.Size != int64(len("fstat-data")) || st.Filetype != 4 {
			t.Fatalf("fstat = (type %d, size %d), want (regular, %d)", st.Filetype, st.Size, len("fstat-data"))
		}
	})

	t.Run("fstatat resolves a child relative to an open directory", func(t *testing.T) {
		root := t.TempDir()
		if err := os.WriteFile(filepath.Join(root, "child"), []byte("child"), 0o600); err != nil {
			t.Fatal(err)
		}
		dirFD, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(dirFD)
		var st syscall.Stat_t
		if err := syscall.Fstatat(dirFD, "child", &st, 0); err != nil {
			t.Fatal(err)
		}
		if st.Size != 5 || st.Filetype != 4 {
			t.Fatalf("relative fstatat = (type %d, size %d), want (regular, 5)", st.Filetype, st.Size)
		}
	})

	t.Run("fstatat no-follow distinguishes symlink from its target", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		link := filepath.Join(root, "link")
		if err := os.WriteFile(target, []byte("target-data"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		var followed, notFollowed syscall.Stat_t
		if err := syscall.Fstatat(-100, link, &followed, 0); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Fstatat(-100, link, &notFollowed, 0x100); err != nil {
			t.Fatal(err)
		}
		if followed.Filetype != 4 || followed.Size != int64(len("target-data")) || notFollowed.Filetype != 7 {
			t.Fatalf("follow/no-follow stats = (%d,%d)/(%d,%d), want regular target and symlink",
				followed.Filetype, followed.Size, notFollowed.Filetype, notFollowed.Size)
		}
	})

	t.Run("fstat rejects an invalid descriptor", func(t *testing.T) {
		var st syscall.Stat_t
		if err := syscall.Fstat(-1, &st); !errors.Is(err, syscall.ENOSYS) {
			t.Fatalf("fstat invalid descriptor error = %v, want ENOSYS", err)
		}
	})

	t.Run("stat reflects file size changes after truncate", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "stat-after-truncate")
		if err := os.WriteFile(path, []byte("longer"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Truncate(path, 2); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Fatal(err)
		}
		if st.Size != 2 {
			t.Fatalf("size after truncate = %d, want 2", st.Size)
		}
	})

	t.Run("fstat reports directory kind through an open directory", func(t *testing.T) {
		fd, err := syscall.Open(t.TempDir(), syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		if st.Filetype != 3 {
			t.Fatalf("directory fstat filetype = %d, want 3", st.Filetype)
		}
	})

	t.Run("fstatat accepts an absolute path regardless of dirfd", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "absolute")
		if err := os.WriteFile(path, []byte("absolute"), 0o600); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Fstatat(-1, path, &st, 0); err != nil {
			t.Fatal(err)
		}
		if st.Size != int64(len("absolute")) {
			t.Fatalf("absolute fstatat size = %d, want %d", st.Size, len("absolute"))
		}
	})

	t.Run("lstat reports a dangling symlink while stat reports its missing target", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "dangling-stat")
		if err := syscall.Symlink("no-target", link); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(link, &st); err != nil || st.Filetype != 7 {
			t.Fatalf("lstat dangling symlink = (type %d, %v), want symlink", st.Filetype, err)
		}
		if err := syscall.Stat(link, &st); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat dangling target error = %v, want not-exist", err)
		}
	})

	t.Run("stat reports an empty regular file with zero length", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "empty-stat")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Fatal(err)
		}
		if st.Filetype != 4 || st.Size != 0 {
			t.Fatalf("empty-file stat = (type %d, size %d), want (4, 0)", st.Filetype, st.Size)
		}
	})

	t.Run("fstat reflects writes made through its descriptor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fstat-update")
		fd, err := syscall.Open(path, syscall.O_CREAT|syscall.O_RDWR, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		if n, err := syscall.Write(fd, []byte("updated")); err != nil || n != 7 {
			t.Fatalf("write before fstat = (%d, %v)", n, err)
		}
		var st syscall.Stat_t
		if err := syscall.Fstat(fd, &st); err != nil {
			t.Fatal(err)
		}
		if st.Size != 7 {
			t.Fatalf("fstat size after write = %d, want 7", st.Size)
		}
	})
}

func TestRusticatedABIFilesystemLinkEdges(t *testing.T) {
	t.Run("writes through one hard link are visible through the other", func(t *testing.T) {
		root := t.TempDir()
		source, alias := filepath.Join(root, "source"), filepath.Join(root, "alias")
		if err := os.WriteFile(source, []byte("before"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, alias); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(alias, syscall.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := syscall.Write(fd, []byte("after!")); err != nil {
			_ = syscall.Close(fd)
			t.Fatal(err)
		}
		if err := syscall.Close(fd); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(source)
		if err != nil || string(got) != "after!" {
			t.Fatalf("source through hard link = (%q, %v), want %q", got, err, "after!")
		}
	})

	t.Run("hard link remains usable after removing the original name", func(t *testing.T) {
		root := t.TempDir()
		source, alias := filepath.Join(root, "source"), filepath.Join(root, "alias")
		if err := os.WriteFile(source, []byte("survives"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, alias); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Unlink(source); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(alias)
		if err != nil || string(got) != "survives" {
			t.Fatalf("remaining hard link = (%q, %v), want %q", got, err, "survives")
		}
	})

	t.Run("linking a missing source fails without creating a destination", func(t *testing.T) {
		root := t.TempDir()
		dst := filepath.Join(root, "new-link")
		if err := syscall.Link(filepath.Join(root, "missing"), dst); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("link missing source error = %v, want not-exist", err)
		}
		if _, err := os.Lstat(dst); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("destination after failed link = %v, want not-exist", err)
		}
	})

	t.Run("linking to an existing destination does not replace it", func(t *testing.T) {
		root := t.TempDir()
		source, dst := filepath.Join(root, "source"), filepath.Join(root, "destination")
		if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, []byte("destination"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, dst); err == nil {
			t.Fatal("link to existing destination succeeded")
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != "destination" {
			t.Fatalf("destination after failed link = (%q, %v), want unchanged contents", got, err)
		}
	})

	t.Run("follow-source hard link targets the symlink referent", func(t *testing.T) {
		root := t.TempDir()
		target, sourceLink, hardLink := filepath.Join(root, "target"), filepath.Join(root, "source-link"), filepath.Join(root, "hard-link")
		if err := os.WriteFile(target, []byte("referent"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(target, sourceLink); err != nil {
			t.Fatal(err)
		}
		if err := syscall.LinkWithFlags(sourceLink, hardLink, syscall.LinkFlagFollowSource); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(hardLink, &st); err != nil {
			t.Fatal(err)
		}
		if st.Filetype != 4 {
			t.Fatalf("follow-source hard-link type = %d, want regular file (4)", st.Filetype)
		}
		got, err := os.ReadFile(hardLink)
		if err != nil || string(got) != "referent" {
			t.Fatalf("follow-source hard-link contents = (%q, %v)", got, err)
		}
	})

	t.Run("hard-linking a directory is rejected", func(t *testing.T) {
		root := t.TempDir()
		if err := syscall.Link(root, filepath.Join(t.TempDir(), "directory-link")); err == nil {
			t.Fatal("hard link to a directory succeeded")
		}
	})

	t.Run("hard link with a missing destination parent leaves no alias", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		dst := filepath.Join(root, "missing-parent", "alias")
		if err := os.WriteFile(source, []byte("source"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, dst); err == nil {
			t.Fatal("link below a missing parent succeeded")
		}
		if _, err := os.Lstat(filepath.Dir(dst)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("destination parent after failed link = %v, want not-exist", err)
		}
	})

	t.Run("hard link destination may be in a different directory", func(t *testing.T) {
		root := t.TempDir()
		sourceDir, linkDir := filepath.Join(root, "source-dir"), filepath.Join(root, "link-dir")
		if err := os.Mkdir(sourceDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(linkDir, 0o700); err != nil {
			t.Fatal(err)
		}
		source, alias := filepath.Join(sourceDir, "source"), filepath.Join(linkDir, "alias")
		if err := os.WriteFile(source, []byte("cross-directory"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, alias); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(alias)
		if err != nil || string(got) != "cross-directory" {
			t.Fatalf("cross-directory hard link = (%q, %v)", got, err)
		}
	})

	t.Run("hard-linking a path to itself fails without changing contents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "self-link")
		if err := os.WriteFile(path, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(path, path); err == nil {
			t.Fatal("hard link to the same path succeeded")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "unchanged" {
			t.Fatalf("contents after self-link failure = (%q, %v)", got, err)
		}
	})

	t.Run("removing one of several aliases leaves the other names usable", func(t *testing.T) {
		root := t.TempDir()
		source, first, second := filepath.Join(root, "source"), filepath.Join(root, "first"), filepath.Join(root, "second")
		if err := os.WriteFile(source, []byte("shared"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, first); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, second); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Unlink(first); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{source, second} {
			if got, err := os.ReadFile(path); err != nil || string(got) != "shared" {
				t.Fatalf("remaining name %q = (%q, %v)", path, got, err)
			}
		}
		if _, err := os.Stat(first); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removed alias stat error = %v, want not-exist", err)
		}
	})
}

func TestRusticatedABIFilesystemSymlinkAndReadlinkEdges(t *testing.T) {
	t.Run("relative symlink preserves its literal target", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		link := filepath.Join(root, "link")
		if err := os.WriteFile(target, []byte("relative-target"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink("target", link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 64)
		n, err := syscall.Readlink(link, buf)
		if err != nil || string(buf[:n]) != "target" {
			t.Fatalf("readlink = (%q, %v), want literal target %q", buf[:n], err, "target")
		}
		got, err := os.ReadFile(link)
		if err != nil || string(got) != "relative-target" {
			t.Fatalf("relative symlink contents = (%q, %v)", got, err)
		}
	})

	t.Run("directory symlink resolves as a directory", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "directory"), filepath.Join(root, "directory-link")
		if err := os.Mkdir(target, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.SymlinkWithType(target, link, syscall.SymlinkTargetDirectory); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(link)
		if err != nil || !info.IsDir() {
			t.Fatalf("stat directory symlink = (%v, %v), want directory", info, err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(link, &st); err != nil || st.Filetype != 7 {
			t.Fatalf("lstat directory symlink = (type %d, %v), want symlink", st.Filetype, err)
		}
	})

	t.Run("file-kind symlink resolves to its file target", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "target"), filepath.Join(root, "file-link")
		if err := os.WriteFile(target, []byte("file-target"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.SymlinkWithType(target, link, syscall.SymlinkTargetFile); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(link)
		if err != nil || string(got) != "file-target" {
			t.Fatalf("file-kind symlink contents = (%q, %v)", got, err)
		}
	})

	t.Run("symlink creation refuses to replace an existing destination", func(t *testing.T) {
		root := t.TempDir()
		dst := filepath.Join(root, "destination")
		if err := os.WriteFile(dst, []byte("original"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink("replacement-target", dst); err == nil {
			t.Fatal("symlink replaced an existing destination")
		}
		got, err := os.ReadFile(dst)
		if err != nil || string(got) != "original" {
			t.Fatalf("destination after failed symlink = (%q, %v), want unchanged contents", got, err)
		}
	})

	t.Run("nested symlinks resolve through each link", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
		if err := os.WriteFile(target, []byte("chain"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(target, first); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink("first", second); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(second)
		if err != nil || string(got) != "chain" {
			t.Fatalf("nested symlink contents = (%q, %v)", got, err)
		}
	})

	t.Run("symlink resolution handles a parent-relative target", func(t *testing.T) {
		root := t.TempDir()
		parent := filepath.Join(root, "parent")
		child := filepath.Join(parent, "child")
		link := filepath.Join(root, "parent-relative")
		if err := os.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(child, []byte("parent-relative"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(filepath.Join("parent", "child"), link); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(link)
		if err != nil || string(got) != "parent-relative" {
			t.Fatalf("parent-relative symlink contents = (%q, %v)", got, err)
		}
	})

	t.Run("symlink paths preserve UTF-8 target and link names", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "café"), filepath.Join(root, "lien")
		if err := os.WriteFile(target, []byte("utf8"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink("café", link); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(link)
		if err != nil || string(got) != "utf8" {
			t.Fatalf("UTF-8 symlink contents = (%q, %v)", got, err)
		}
	})

	t.Run("symlink creation rejects an empty target", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "empty-target-link")
		if err := syscall.Symlink("", link); err == nil {
			t.Fatal("symlink with an empty target succeeded")
		}
		if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("link after empty-target failure = %v, want not-exist", err)
		}
	})

	t.Run("self-referential symlink is visible but cannot be followed", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "self-loop")
		if err := syscall.Symlink("self-loop", link); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(link, &st); err != nil || st.Filetype != 7 {
			t.Fatalf("lstat self-loop = (type %d, %v), want symlink", st.Filetype, err)
		}
		if _, err := os.Stat(link); err == nil {
			t.Fatal("following a self-referential symlink succeeded")
		}
	})

	t.Run("dangling symlink remains visible to lstat", func(t *testing.T) {
		root := t.TempDir()
		link := filepath.Join(root, "dangling")
		if err := syscall.Symlink("absent-target", link); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(link, &st); err != nil {
			t.Fatalf("lstat dangling symlink: %v", err)
		}
		if st.Filetype != 7 {
			t.Fatalf("dangling symlink filetype = %d, want symlink (7)", st.Filetype)
		}
		if _, err := os.Stat(link); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat dangling target error = %v, want not-exist", err)
		}
	})

	t.Run("short readlink buffer returns a target prefix without a terminator", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "short-link")
		if err := syscall.Symlink("target-name", link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		n, err := syscall.Readlink(link, buf)
		if err != nil || n != len(buf) || string(buf) != "targ" {
			t.Fatalf("short readlink = (%d, %q, %v), want (4, %q, nil)", n, buf, err, "targ")
		}
	})

	t.Run("exact-sized readlink buffer returns the complete target length", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "exact-link")
		if err := syscall.Symlink("target", link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len("target"))
		n, err := syscall.Readlink(link, buf)
		if err != nil || n != len("target") || string(buf) != "target" {
			t.Fatalf("exact-sized readlink = (%d, %q, %v), want complete target", n, buf, err)
		}
	})

	t.Run("larger readlink buffer is not NUL-terminated", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "long-buffer-link")
		if err := syscall.Symlink("target", link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 12)
		for i := range buf {
			buf[i] = 0xff
		}
		n, err := syscall.Readlink(link, buf)
		if err != nil || n != len("target") || string(buf[:n]) != "target" || buf[n] != 0xff {
			t.Fatalf("large-buffer readlink = (%d, %v, %v), want six bytes and unchanged trailing byte", n, buf, err)
		}
	})

	t.Run("readlink on a regular file fails", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-link")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := syscall.Readlink(path, make([]byte, 16)); err == nil {
			t.Fatal("readlink on regular file succeeded")
		}
	})

	t.Run("readlink returns the immediate target of a link chain", func(t *testing.T) {
		root := t.TempDir()
		first, second := filepath.Join(root, "first"), filepath.Join(root, "second")
		if err := syscall.Symlink("final-target", first); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink("first", second); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 32)
		n, err := syscall.Readlink(second, buf)
		if err != nil || string(buf[:n]) != "first" {
			t.Fatalf("readlink chain target = (%q, %v), want immediate target %q", buf[:n], err, "first")
		}
	})

	t.Run("readlink does not follow a dangling link target", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "dangling-readlink")
		if err := syscall.Symlink("missing-target", link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 32)
		n, err := syscall.Readlink(link, buf)
		if err != nil || string(buf[:n]) != "missing-target" {
			t.Fatalf("readlink dangling link = (%q, %v), want its literal target", buf[:n], err)
		}
	})

	t.Run("readlink preserves parent components in relative targets", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "parent-components")
		const target = "../outside/target"
		if err := syscall.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len(target))
		n, err := syscall.Readlink(link, buf)
		got := strings.ReplaceAll(string(buf), `\`, "/")
		if err != nil || n != len(target) || got != strings.ReplaceAll(target, `\`, "/") {
			t.Fatalf("readlink parent-relative target = (%d, %q, %v)", n, buf, err)
		}
	})

	t.Run("readlink preserves UTF-8 target bytes", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "utf8-link")
		if err := syscall.Symlink("café", link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, len("café"))
		n, err := syscall.Readlink(link, buf)
		if err != nil || n != len("café") || string(buf) != "café" {
			t.Fatalf("UTF-8 readlink = (%d, %q, %v)", n, buf, err)
		}
	})

	t.Run("readlink retries for a long target and honors caller capacity", func(t *testing.T) {
		targetBytes := make([]byte, 180)
		for i := range targetBytes {
			targetBytes[i] = 'x'
		}
		target := string(targetBytes)
		link := filepath.Join(t.TempDir(), "long-target-link")
		if err := syscall.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 100)
		n, err := syscall.Readlink(link, buf)
		if err != nil || n != len(buf) || string(buf) != target[:len(buf)] {
			t.Fatalf("long-target readlink = (%d bytes, %v), want caller-sized target prefix", n, err)
		}
	})

	t.Run("zero-capacity readlink reports no copied bytes", func(t *testing.T) {
		link := filepath.Join(t.TempDir(), "empty-buffer-link")
		if err := syscall.Symlink("target", link); err != nil {
			t.Fatal(err)
		}
		if n, err := syscall.Readlink(link, nil); err != nil || n != 0 {
			t.Fatalf("empty readlink = (%d, %v), want (0, nil)", n, err)
		}
	})
}

func TestRusticatedABIFilesystemChmodAndTimestampEdges(t *testing.T) {
	t.Run("chmod follows a symlink to update target permissions", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
		if err := os.WriteFile(target, []byte("mode"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := syscall.Chmod(target, 0o600); err != nil {
				t.Errorf("restore target permissions: %v", err)
			}
		})
		if err := syscall.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Chmod(link, 0o400); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(target)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("target permissions after chmod through symlink = %#o, still writable", info.Mode().Perm())
		}
	})

	t.Run("chmod on a missing path fails", func(t *testing.T) {
		if err := syscall.Chmod(filepath.Join(t.TempDir(), "missing"), 0o600); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("chmod missing path error = %v, want not-exist", err)
		}
	})

	t.Run("fchmod updates permissions through an open descriptor", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "fchmod")
		if err := os.WriteFile(path, []byte("mode"), 0o600); err != nil {
			t.Fatal(err)
		}
		fd, err := syscall.Open(path, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(fd)
		t.Cleanup(func() {
			if err := syscall.Chmod(path, 0o600); err != nil {
				t.Errorf("restore fchmod permissions: %v", err)
			}
		})
		if err := syscall.Fchmod(fd, 0o400); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("permissions after fchmod = %#o, still writable", info.Mode().Perm())
		}
	})

	t.Run("chmod applies to directories as well as regular files", func(t *testing.T) {
		dir := t.TempDir()
		t.Cleanup(func() {
			if err := syscall.Chmod(dir, 0o700); err != nil {
				t.Errorf("restore directory permissions: %v", err)
			}
		})
		if err := syscall.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory stat after chmod = (%v, %v)", info, err)
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("directory permissions after chmod = %#o, still writable", info.Mode().Perm())
		}
	})

	t.Run("chmod can remove and then restore file write permission", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "permission-transition")
		if err := os.WriteFile(path, []byte("mode"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := syscall.Chmod(path, 0o600); err != nil {
				t.Errorf("restore file permissions: %v", err)
			}
		})
		if err := syscall.Chmod(path, 0o400); err != nil {
			t.Fatal(err)
		}
		readOnly, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if readOnly.Mode().Perm()&0o222 != 0 {
			t.Fatalf("permissions after removing write bit = %#o", readOnly.Mode().Perm())
		}
		if err := syscall.Chmod(path, 0o600); err != nil {
			t.Fatal(err)
		}
		writable, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if writable.Mode().Perm()&0o200 == 0 {
			t.Fatalf("permissions after restoring owner write = %#o", writable.Mode().Perm())
		}
	})

	t.Run("write-only chmod mode retains write permission", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "write-only-mode")
		if err := os.WriteFile(path, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := syscall.Chmod(path, 0o600); err != nil {
				t.Errorf("restore write-only mode test file: %v", err)
			}
		})
		if err := syscall.Chmod(path, 0o200); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o222 == 0 {
			t.Fatalf("permissions after write-only chmod = %#o, missing write bits", info.Mode().Perm())
		}
	})

	t.Run("chmod through a hard link changes the shared file mode", func(t *testing.T) {
		root := t.TempDir()
		source, alias := filepath.Join(root, "source"), filepath.Join(root, "alias")
		if err := os.WriteFile(source, []byte("shared mode"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Link(source, alias); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := syscall.Chmod(source, 0o600); err != nil {
				t.Errorf("restore hard-linked file mode: %v", err)
			}
		})
		if err := syscall.Chmod(alias, 0o400); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(source)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o222 != 0 {
			t.Fatalf("source permissions after chmod through alias = %#o", info.Mode().Perm())
		}
	})

	t.Run("timestamp update can omit the existing modification time", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "omit-mtime")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1_600_000_000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		times := []syscall.Timespec{
			{Sec: 1_610_000_000, Nsec: 0},
			{Sec: 0, Nsec: (1 << 30) - 2},
		}
		if err := syscall.UtimesNano(path, times); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Unix() != old.Unix() {
			t.Fatalf("modification time after omit = %d, want %d", info.ModTime().Unix(), old.Unix())
		}
	})

	t.Run("timestamp update can set modification time to now", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mtime-now")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1_500_000_000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		before := time.Now().Add(-time.Minute)
		times := []syscall.Timespec{
			{Sec: 0, Nsec: (1 << 30) - 2},
			{Sec: 0, Nsec: (1 << 30) - 1},
		}
		if err := syscall.UtimesNano(path, times); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Before(before) {
			t.Fatalf("modification time after UTIME_NOW = %s, want current time", info.ModTime())
		}
	})

	t.Run("timestamp update can set access time to now while omitting mtime", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "atime-now")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1_500_000_000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		times := []syscall.Timespec{
			{Nsec: int64((1 << 30) - 1)},
			{Nsec: int64((1 << 30) - 2)},
		}
		if err := syscall.UtimesNano(path, times); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Unix() != old.Unix() {
			t.Fatalf("mtime after atime-now update = %d, want unchanged %d", info.ModTime().Unix(), old.Unix())
		}
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Fatal(err)
		}
		if st.Atim.Sec < time.Now().Add(-time.Minute).Unix() {
			t.Fatalf("atime after UTIME_NOW = %d, want recent time", st.Atim.Sec)
		}
	})

	t.Run("omitting both timestamps leaves modification time unchanged", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "omit-both")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1_550_000_000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		omit := int64((1 << 30) - 2)
		if err := syscall.UtimesNano(path, []syscall.Timespec{{Nsec: omit}, {Nsec: omit}}); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Unix() != old.Unix() {
			t.Fatalf("modification time after omit-both = %d, want %d", info.ModTime().Unix(), old.Unix())
		}
	})

	t.Run("nil timestamps set modification time to current time", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "now")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1_500_000_000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		before := time.Now().Add(-time.Minute)
		if err := syscall.UtimesNano(path, nil); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Before(before) {
			t.Fatalf("modification time after nil update = %s, want current time", info.ModTime())
		}
	})

	t.Run("invalid nanoseconds are rejected before host update", func(t *testing.T) {
		times := []syscall.Timespec{
			{Sec: 1_600_000_000, Nsec: 1_000_000_000},
			{Sec: 1_600_000_000, Nsec: 0},
		}
		if err := syscall.UtimesNano(filepath.Join(t.TempDir(), "bad-time"), times); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("invalid nanoseconds error = %v, want EINVAL", err)
		}
	})

	t.Run("negative nanoseconds are rejected", func(t *testing.T) {
		times := []syscall.Timespec{
			{Sec: 1_600_000_000, Nsec: -1},
			{Sec: 1_600_000_000, Nsec: 0},
		}
		if err := syscall.UtimesNano(filepath.Join(t.TempDir(), "negative-time"), times); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("negative nanoseconds error = %v, want EINVAL", err)
		}
	})

	t.Run("mtime can be updated while atime is omitted", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "mtime-only")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		old := time.Unix(1_500_000_000, 0)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		wantMtime := time.Unix(1_610_000_123, 0)
		times := []syscall.Timespec{
			{Nsec: int64((1 << 30) - 2)},
			{Sec: wantMtime.Unix(), Nsec: 0},
		}
		if err := syscall.UtimesNano(path, times); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Fatal(err)
		}
		if st.Atim.Sec != old.Unix() || st.Mtim.Sec != wantMtime.Unix() {
			t.Fatalf("atime/mtime = (%d, %d), want (%d, %d)", st.Atim.Sec, st.Mtim.Sec, old.Unix(), wantMtime.Unix())
		}
	})

	t.Run("independent atime and mtime values are not swapped", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "independent-times")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		wantAtime := time.Unix(1_610_000_123, 0)
		wantMtime := time.Unix(1_620_000_456, 0)
		times := []syscall.Timespec{
			{Sec: wantAtime.Unix(), Nsec: 0},
			{Sec: wantMtime.Unix(), Nsec: 0},
		}
		if err := syscall.UtimesNano(path, times); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Stat(path, &st); err != nil {
			t.Fatal(err)
		}
		if st.Atim.Sec != wantAtime.Unix() || st.Mtim.Sec != wantMtime.Unix() {
			t.Fatalf("atime/mtime = (%d, %d), want (%d, %d)", st.Atim.Sec, st.Mtim.Sec, wantAtime.Unix(), wantMtime.Unix())
		}
	})

	t.Run("pre-epoch timestamps round-trip or report unsupported", func(t *testing.T) {
		platform := syscall.GetPlatformInfo()
		nodeWindows := platform.OSKind == 1 && platform.WasmPlatform == "node"
		path := filepath.Join(t.TempDir(), "pre-epoch")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := time.Date(1960, time.January, 2, 3, 4, 5, 0, time.UTC)
		times := []syscall.Timespec{
			{Sec: want.Unix(), Nsec: 0},
			{Sec: want.Unix(), Nsec: 0},
		}
		err = syscall.UtimesNano(path, times)
		if nodeWindows {
			if !errors.Is(err, syscall.ENOTSUP) {
				t.Fatalf("pre-epoch timestamp error = %v, want ENOTSUP on Node/Windows", err)
			}
			after, statErr := os.Stat(path)
			if statErr != nil {
				t.Fatal(statErr)
			}
			if !after.ModTime().Equal(before.ModTime()) {
				t.Fatalf("mtime after unsupported update = %s, want unchanged %s", after.ModTime(), before.ModTime())
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Unix() != want.Unix() {
			t.Fatalf("pre-epoch mtime = %d, want %d", info.ModTime().Unix(), want.Unix())
		}
	})

	t.Run("timestamp update resolves a relative path under its directory", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "relative-time")
		if err := os.WriteFile(path, []byte("time"), 0o600); err != nil {
			t.Fatal(err)
		}
		dirFD, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(dirFD)
		want := time.Unix(1_620_000_000, 0)
		times := []syscall.Timespec{
			{Sec: want.Unix(), Nsec: 0},
			{Sec: want.Unix(), Nsec: 0},
		}
		if err := syscall.UtimesNanoAt(dirFD, "relative-time", times, 0); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.ModTime().Unix() != want.Unix() {
			t.Fatalf("relative timestamp = %d, want %d", info.ModTime().Unix(), want.Unix())
		}
	})
}

func TestRusticatedABIFilesystemRemoveMkdirRenameEdges(t *testing.T) {
	t.Run("unlink removes a symlink without removing its target", func(t *testing.T) {
		root := t.TempDir()
		target, link := filepath.Join(root, "target"), filepath.Join(root, "link")
		if err := os.WriteFile(target, []byte("still here"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Unlink(link); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("link after unlink error = %v, want not-exist", err)
		}
		got, err := os.ReadFile(target)
		if err != nil || string(got) != "still here" {
			t.Fatalf("target after unlink = (%q, %v)", got, err)
		}
	})

	t.Run("unlinkat resolves a relative file under a directory descriptor", func(t *testing.T) {
		root := t.TempDir()
		path := filepath.Join(root, "child")
		if err := os.WriteFile(path, []byte("remove"), 0o600); err != nil {
			t.Fatal(err)
		}
		dirFD, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(dirFD)
		if err := syscall.Unlinkat(dirFD, "child", 0); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("child after unlinkat error = %v, want not-exist", err)
		}
	})

	t.Run("unlinkat with remove-directory flag removes an empty child directory", func(t *testing.T) {
		root := t.TempDir()
		child := filepath.Join(root, "empty")
		if err := os.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		dirFD, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(dirFD)
		if err := syscall.Unlinkat(dirFD, "empty", 0x200); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(child); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("directory after unlinkat removedir = %v, want not-exist", err)
		}
	})

	t.Run("unlink refuses to remove a directory", func(t *testing.T) {
		dir := t.TempDir()
		if err := syscall.Unlink(dir); err == nil {
			t.Fatal("unlink of a directory succeeded")
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("directory after failed unlink = (%v, %v), want directory", info, err)
		}
	})

	t.Run("unlinkat rejects unsupported flags before removing anything", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "bad-unlinkat-flags")
		if err := os.WriteFile(path, []byte("keep"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Unlinkat(-100, path, 1); !errors.Is(err, syscall.EINVAL) {
			t.Fatalf("unlinkat invalid flags error = %v, want EINVAL", err)
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "keep" {
			t.Fatalf("file after rejected unlinkat = (%q, %v)", got, err)
		}
	})

	t.Run("rmdir refuses a regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "not-a-directory")
		if err := os.WriteFile(path, []byte("file"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rmdir(path); err == nil {
			t.Fatal("rmdir of a regular file succeeded")
		}
		if got, err := os.ReadFile(path); err != nil || string(got) != "file" {
			t.Fatalf("file after failed rmdir = (%q, %v)", got, err)
		}
	})

	t.Run("unlinkat with removedir rejects a non-empty directory", func(t *testing.T) {
		root := t.TempDir()
		dir := filepath.Join(root, "non-empty")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(dir, "child")
		if err := os.WriteFile(child, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		dirFD, err := syscall.Open(root, syscall.O_RDONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer syscall.Close(dirFD)
		if err := syscall.Unlinkat(dirFD, "non-empty", 0x200); err == nil {
			t.Fatal("unlinkat removed a non-empty directory")
		}
		if _, err := os.Stat(child); err != nil {
			t.Fatalf("child after failed unlinkat removedir: %v", err)
		}
	})

	t.Run("rmdir removes an empty directory", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "empty-to-remove")
		if err := os.Mkdir(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rmdir(dir); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("directory after rmdir = %v, want not-exist", err)
		}
	})

	t.Run("relative unlinkat with an invalid directory descriptor fails", func(t *testing.T) {
		if err := syscall.Unlinkat(-1, "relative", 0); !errors.Is(err, syscall.EBADF) {
			t.Fatalf("unlinkat invalid directory descriptor error = %v, want EBADF", err)
		}
	})

	t.Run("non-empty directory removal fails without deleting children", func(t *testing.T) {
		dir := t.TempDir()
		child := filepath.Join(dir, "child")
		if err := os.WriteFile(child, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rmdir(dir); err == nil {
			t.Fatal("rmdir of non-empty directory succeeded")
		}
		if _, err := os.Stat(child); err != nil {
			t.Fatalf("child after failed rmdir: %v", err)
		}
	})

	t.Run("mkdir creates nested directories when parents already exist", func(t *testing.T) {
		root := t.TempDir()
		parent := filepath.Join(root, "parent")
		if err := syscall.Mkdir(parent, 0o700); err != nil {
			t.Fatal(err)
		}
		child := filepath.Join(parent, "child")
		if err := syscall.Mkdir(child, 0o700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(child)
		if err != nil || !info.IsDir() {
			t.Fatalf("created child = (%v, %v), want directory", info, err)
		}
	})

	t.Run("mkdir existing path fails without changing its type", func(t *testing.T) {
		dir := t.TempDir()
		if err := syscall.Mkdir(dir, 0o700); err == nil {
			t.Fatal("mkdir of existing directory succeeded")
		}
		info, err := os.Stat(dir)
		if err != nil || !info.IsDir() {
			t.Fatalf("existing path after failed mkdir = (%v, %v), want directory", info, err)
		}
	})

	t.Run("mkdir does not create missing parents", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing-parent", "child")
		if err := syscall.Mkdir(path, 0o700); err == nil {
			t.Fatal("mkdir below missing parent succeeded")
		}
		if _, err := os.Stat(filepath.Dir(path)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("parent after failed mkdir = %v, want not-exist", err)
		}
	})

	t.Run("mkdir does not replace a regular file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(path, []byte("preserve"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Mkdir(path, 0o700); err == nil {
			t.Fatal("mkdir replaced a regular file")
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "preserve" {
			t.Fatalf("file after failed mkdir = (%q, %v)", got, err)
		}
	})

	t.Run("mkdir rejects an empty path", func(t *testing.T) {
		if err := syscall.Mkdir("", 0o700); err == nil {
			t.Fatal("mkdir empty path succeeded")
		}
	})

	t.Run("mkdir preserves UTF-8 path names", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "café")
		if err := syscall.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			t.Fatalf("UTF-8 directory stat = (%v, %v), want directory", info, err)
		}
	})

	t.Run("rename moves a directory and preserves its child", func(t *testing.T) {
		root := t.TempDir()
		from, to := filepath.Join(root, "before"), filepath.Join(root, "after")
		if err := os.Mkdir(from, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(from, "child"), []byte("moved"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(to, "child"))
		if err != nil || string(got) != "moved" {
			t.Fatalf("renamed child = (%q, %v), want %q", got, err, "moved")
		}
		if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("old directory path error = %v, want not-exist", err)
		}
	})

	t.Run("rename moves a file between sibling directories", func(t *testing.T) {
		root := t.TempDir()
		fromDir, toDir := filepath.Join(root, "from"), filepath.Join(root, "to")
		if err := os.Mkdir(fromDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(toDir, 0o700); err != nil {
			t.Fatal(err)
		}
		from, to := filepath.Join(fromDir, "file"), filepath.Join(toDir, "file")
		if err := os.WriteFile(from, []byte("move"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(from); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("source after cross-directory rename = %v, want not-exist", err)
		}
		if got, err := os.ReadFile(to); err != nil || string(got) != "move" {
			t.Fatalf("destination after cross-directory rename = (%q, %v)", got, err)
		}
	})

	t.Run("rename moves a symlink without moving its referent", func(t *testing.T) {
		root := t.TempDir()
		target, from, to := filepath.Join(root, "target"), filepath.Join(root, "link"), filepath.Join(root, "renamed-link")
		if err := os.WriteFile(target, []byte("referent"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Symlink(target, from); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		var st syscall.Stat_t
		if err := syscall.Lstat(to, &st); err != nil || st.Filetype != 7 {
			t.Fatalf("renamed link lstat = (type %d, %v), want symlink", st.Filetype, err)
		}
		if got, err := os.ReadFile(target); err != nil || string(got) != "referent" {
			t.Fatalf("referent after symlink rename = (%q, %v)", got, err)
		}
	})

	t.Run("rename failure for a missing destination parent leaves source intact", func(t *testing.T) {
		root := t.TempDir()
		source := filepath.Join(root, "source")
		destination := filepath.Join(root, "missing-parent", "destination")
		if err := os.WriteFile(source, []byte("unchanged"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rename(source, destination); err == nil {
			t.Fatal("rename below missing parent succeeded")
		}
		if got, err := os.ReadFile(source); err != nil || string(got) != "unchanged" {
			t.Fatalf("source after failed rename = (%q, %v)", got, err)
		}
	})

	t.Run("rename rejects empty path operands", func(t *testing.T) {
		if err := syscall.Rename("", "destination"); err == nil {
			t.Fatal("rename with an empty source path succeeded")
		}
		if err := syscall.Rename("source", ""); err == nil {
			t.Fatal("rename with an empty destination path succeeded")
		}
	})

	t.Run("rename preserves UTF-8 path names", func(t *testing.T) {
		root := t.TempDir()
		from, to := filepath.Join(root, "café"), filepath.Join(root, "renamed-café")
		if err := os.WriteFile(from, []byte("utf8"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := syscall.Rename(from, to); err != nil {
			t.Fatal(err)
		}
		if got, err := os.ReadFile(to); err != nil || string(got) != "utf8" {
			t.Fatalf("UTF-8 renamed file = (%q, %v)", got, err)
		}
	})
}
