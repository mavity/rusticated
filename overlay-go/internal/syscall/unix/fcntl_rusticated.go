//go:build wasip1

package unix

import (
	_ "unsafe" // for go:linkname
	"syscall"
)

// Replaces internal/syscall/unix/fcntl_wasip1.go.
// fd_fdstat_get_flags is provided by overlay syscall/fs_rusticated.go via go:linkname.

//go:linkname fd_fdstat_get_flags syscall.fd_fdstat_get_flags
func fd_fdstat_get_flags(fd int) (uint32, error)

func Fcntl(fd int, cmd int, arg int) (int, error) {
	if cmd == syscall.F_GETFL {
		flags, err := fd_fdstat_get_flags(fd)
		return int(flags), err
	}
	return 0, syscall.ENOSYS
}