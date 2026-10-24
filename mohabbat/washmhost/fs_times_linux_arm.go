//go:build linux && arm

package main

import (
	"errors"
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

type kernelTimespec64 struct {
	sec  int64
	nsec int64
}

func setNativeFileTimes(path string, r pathTimesRequest) error {
	const timeFlags = pathTimesSetAtime | pathTimesAtimeNow | pathTimesSetMtime | pathTimesMtimeNow
	if r.flags&timeFlags == 0 {
		_, _, err := resolvePathTimes(path, r)
		return err
	}

	times := [2]kernelTimespec64{
		{sec: r.atimeSec, nsec: int64(r.atimeNsec)},
		{sec: r.mtimeSec, nsec: int64(r.mtimeNsec)},
	}
	switch {
	case r.flags&pathTimesAtimeNow != 0:
		times[0].nsec = int64(unix.UTIME_NOW)
	case r.flags&pathTimesSetAtime == 0:
		times[0].nsec = int64(unix.UTIME_OMIT)
	}
	switch {
	case r.flags&pathTimesMtimeNow != 0:
		times[1].nsec = int64(unix.UTIME_NOW)
	case r.flags&pathTimesSetMtime == 0:
		times[1].nsec = int64(unix.UTIME_OMIT)
	}

	flags := 0
	if r.flags&pathTimesNoFollow != 0 {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	pathPtr, err := unix.BytePtrFromString(path)
	if err != nil {
		return err
	}
	dirfd := unix.AT_FDCWD
	_, _, errno := unix.Syscall6(
		uintptr(unix.SYS_UTIMENSAT_TIME64),
		uintptr(dirfd),
		uintptr(unsafe.Pointer(pathPtr)),
		uintptr(unsafe.Pointer(&times[0])),
		uintptr(flags),
		0,
		0,
	)
	runtime.KeepAlive(pathPtr)
	runtime.KeepAlive(times)
	if errno != 0 {
		err = errno
	}
	if errors.Is(err, unix.ENOTSUP) {
		return unsupportedTimes(fmt.Errorf("set timestamps on %q: %w", path, err))
	}
	return err
}
