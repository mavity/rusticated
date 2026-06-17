//go:build wasip1

package unix

import "syscall"

// Stubs for internal/syscall/unix/net_wasip1.go.
// These are used by the net package but not needed for the demo.

func Recvfrom(fd int, p []byte, flags int) (n int, from syscall.Sockaddr, err error) {
	return 0, nil, syscall.ENOSYS
}

func Sendto(fd int, p []byte, flags int, to syscall.Sockaddr) (err error) {
	return syscall.ENOSYS
}

func RecvfromInet4(fd int, p []byte, flags int, from *syscall.SockaddrInet4) (int, error) {
	return 0, syscall.ENOSYS
}

func RecvfromInet6(fd int, p []byte, flags int, from *syscall.SockaddrInet6) (n int, err error) {
	return 0, syscall.ENOSYS
}

func SendtoInet4(fd int, p []byte, flags int, to *syscall.SockaddrInet4) (err error) {
	return syscall.ENOSYS
}

func SendtoInet6(fd int, p []byte, flags int, to *syscall.SockaddrInet6) (err error) {
	return syscall.ENOSYS
}

func SendmsgNInet4(fd int, p, oob []byte, to *syscall.SockaddrInet4, flags int) (n int, err error) {
	return 0, syscall.ENOSYS
}

func SendmsgNInet6(fd int, p, oob []byte, to *syscall.SockaddrInet6, flags int) (n int, err error) {
	return 0, syscall.ENOSYS
}

func RecvmsgInet4(fd int, p, oob []byte, flags int, from *syscall.SockaddrInet4) (n, oobn int, recvflags int, err error) {
	return 0, 0, 0, syscall.ENOSYS
}

func RecvmsgInet6(fd int, p, oob []byte, flags int, from *syscall.SockaddrInet6) (n, oobn int, recvflags int, err error) {
	return 0, 0, 0, syscall.ENOSYS
}
