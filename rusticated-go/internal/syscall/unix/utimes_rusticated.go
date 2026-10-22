//go:build wasip1

package unix

import "syscall"

// Replaces internal/syscall/unix/utimes_wasip1.go.
// The original uses path_filestat_set_times from wasi_snapshot_preview1.

func Utimensat(dirfd int, path string, times *[2]syscall.Timespec, flag int) error {
	if times == nil {
		return syscall.UtimesNanoAt(dirfd, path, nil, flag)
	}
	return syscall.UtimesNanoAt(dirfd, path, times[:], flag)
}
