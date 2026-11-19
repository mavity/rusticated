//go:build wasip1

package wasmabitest

import (
	"errors"
	"syscall"
	"testing"
	"unsafe"
)

func TestRusticatedABISyscallIsattyRejectsDescriptorBoundaries(t *testing.T) {
	for _, fd := range []uintptr{
		3,
		10,
		0x7fff,
		0x7ffffffd,
		0x7ffffffe,
		0x7fffffff,
		0x80000000,
		0xfffffffd,
		0xfffffffe,
		0xffffffff,
		^uintptr(0),
	} {
		if syscall.Isatty(fd) {
			t.Errorf("Isatty(%#x) = true, want false for an unmapped descriptor", fd)
		}
	}
}

func TestRusticatedABISyscallTtyGetSizeWritesOnlyDimensions(t *testing.T) {
	for _, fd := range []uintptr{0, 1, 2} {
		size := syscall.Winsize{
			Row:    0xffff,
			Col:    0xfffe,
			Xpixel: 0x1234,
			Ypixel: 0x5678,
		}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
		if errno != 0 {
			t.Errorf("TIOCGWINSZ fd %d: %v", fd, errno)
			continue
		}
		if size.Row == 0xffff || size.Col == 0xfffe {
			t.Errorf("TIOCGWINSZ fd %d did not replace dimension sentinels: %+v", fd, size)
		}
		if size.Row == 0 || size.Col == 0 {
			t.Errorf("TIOCGWINSZ fd %d returned empty dimensions: %+v", fd, size)
		}
		if size.Xpixel != 0x1234 || size.Ypixel != 0x5678 {
			t.Errorf("TIOCGWINSZ fd %d changed pixel fields: %+v", fd, size)
		}
	}

	for _, fd := range []uintptr{3, 10, 99, 0x7fffffff, 0x80000000, 0xfffffffd, 0xfffffffe, 0xffffffff} {
		size := syscall.Winsize{Row: 1, Col: 2, Xpixel: 3, Ypixel: 4}
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
		if errno != syscall.EBADF {
			t.Errorf("TIOCGWINSZ invalid fd %#x errno = %v, want EBADF", fd, errno)
		}
		if size != (syscall.Winsize{Row: 1, Col: 2, Xpixel: 3, Ypixel: 4}) {
			t.Errorf("TIOCGWINSZ invalid fd %#x changed output: %+v", fd, size)
		}
	}
}

func TestRusticatedABISyscallTtySetSizeReportsSupport(t *testing.T) {
	for _, fd := range []int{0, 1, 2} {
		var size syscall.Winsize
		_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, uintptr(fd), syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&size)))
		if errno != 0 {
			t.Fatalf("TIOCGWINSZ fd %d: %v", fd, errno)
		}

		err := syscall.RawTtySetSize(fd, int(size.Col), int(size.Row))
		if syscall.Isatty(uintptr(fd)) {
			if err != nil {
				t.Errorf("RawTtySetSize TTY fd %d: %v", fd, err)
			}
			continue
		}
		if err != nil && !errors.Is(err, syscall.ENOTTY) {
			t.Errorf("RawTtySetSize non-TTY fd %d error = %v, want success or ENOTTY", fd, err)
		}
	}

	for _, fd := range []int{
		-1,
		3,
		10,
		0x7ffffffd,
		0x7ffffffe,
		0x7fffffff,
		-0x80000000,
		-0x7fffffff,
		-2,
		-3,
	} {
		if err := syscall.RawTtySetSize(fd, 80, 24); !errors.Is(err, syscall.EBADF) {
			t.Errorf("RawTtySetSize invalid fd %d error = %v, want EBADF", fd, err)
		}
	}
}

func TestRusticatedABISyscallTtySetModeIoctlVariants(t *testing.T) {
	defer setTtyNormalMode(t)
	tests := []struct {
		name  string
		trap  uintptr
		lflag uint32
	}{
		{"raw", syscall.TCSETS, 0},
		{"canonical", syscall.TCSETS, 0x02},
		{"echo", syscall.TCSETS, 0x08},
		{"canonical-and-echo", syscall.TCSETS, 0x0a},
		{"unrelated-flags", syscall.TCSETS, 0x100},
		{"canonical-with-unrelated-flags", syscall.TCSETS, 0x102},
		{"echo-with-unrelated-flags", syscall.TCSETS, 0x108},
		{"both-with-unrelated-flags", syscall.TCSETS, 0x10a},
		{"drain-raw", syscall.TCSETSW, 0},
		{"drain", syscall.TCSETSW, 0x0a},
		{"flush-raw", syscall.TCSETSF, 0},
		{"flush", syscall.TCSETSF, 0x0a},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			termios := syscall.Termios{Lflag: tt.lflag}
			_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, 0, tt.trap, uintptr(unsafe.Pointer(&termios)))
			if errno != 0 {
				t.Fatalf("ioctl trap %#x: %v", tt.trap, errno)
			}
			setTtyNormalMode(t)
		})
	}

	termios := syscall.Termios{}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, 99, syscall.TCSETS, uintptr(unsafe.Pointer(&termios)))
	if errno != syscall.EBADF {
		t.Errorf("TCSETS invalid fd errno = %v, want EBADF", errno)
	}
}

func setTtyNormalMode(t *testing.T) {
	t.Helper()
	termios := syscall.Termios{Lflag: 0x02 | 0x08}
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, 0, syscall.TCSETS, uintptr(unsafe.Pointer(&termios)))
	if errno != 0 {
		t.Errorf("restore TTY normal mode: %v", errno)
	}
}
