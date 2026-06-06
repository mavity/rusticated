//go:build wasip1

package unix

import (
"syscall"
)

// Replaces internal/syscall/unix/nonblocking_wasip1.go.
// fd_fdstat_get_flags is declared once in fcntl_rusticated.go (also in this package).

func IsNonblock(fd int) (nonblocking bool, err error) {
return false, nil
}

func HasNonblockFlag(flag int) bool {
return flag&syscall.FDFLAG_NONBLOCK != 0
}
