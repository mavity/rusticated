//go:build wasip1

package unix

import "syscall"

// Stubs for internal/syscall/unix/at_wasip1.go.
// The at_wasip1.go file has direct wasi_snapshot_preview1 imports; we stub
// them all since the demo does not need AT-style directory operations.

const (
UTIME_OMIT = -0x2

AT_REMOVEDIR        = 0x200
AT_SYMLINK_NOFOLLOW = 0x100
)

type (
size = uint32
)

func Unlinkat(dirfd int, path string, flags int) error         { return syscall.ENOSYS }
func Openat(dirfd int, path string, flags int, perm uint32) (int, error) {
return syscall.Open(path, flags, perm)
}
func Fstatat(dirfd int, path string, stat *syscall.Stat_t, flags int) error {
if flags&AT_SYMLINK_NOFOLLOW != 0 {
return syscall.Lstat(path, stat)
}
return syscall.Stat(path, stat)
}
func Readlinkat(dirfd int, path string, buf []byte) (int, error) { return 0, syscall.ENOSYS }
func Mkdirat(dirfd int, path string, mode uint32) error          { return syscall.ENOSYS }
func Fchmodat(dirfd int, path string, mode uint32, flags int) error { return syscall.ENOSYS }
func Fchownat(dirfd int, path string, uid, gid int, flags int) error { return syscall.ENOSYS }
func Renameat(olddirfd int, oldpath string, newdirfd int, newpath string) error {
return syscall.ENOSYS
}
func Linkat(olddirfd int, oldpath string, newdirfd int, newpath string, flag int) error {
return syscall.ENOSYS
}
func Symlinkat(oldpath string, newdirfd int, newpath string) error { return syscall.ENOSYS }

func errnoErr(errno syscall.Errno) error {
if errno == 0 {
return nil
}
return errno
}
