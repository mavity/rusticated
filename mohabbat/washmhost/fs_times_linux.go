//go:build linux

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func setNativeFileTimes(path string, r pathTimesRequest) error {
	const timeFlags = pathTimesSetAtime | pathTimesAtimeNow | pathTimesSetMtime | pathTimesMtimeNow
	if r.flags&timeFlags == 0 {
		_, _, err := resolvePathTimes(path, r)
		return err
	}

	atime := unix.Timespec{Sec: r.atimeSec, Nsec: int64(r.atimeNsec)}
	switch {
	case r.flags&pathTimesAtimeNow != 0:
		atime.Nsec = unix.UTIME_NOW
	case r.flags&pathTimesSetAtime == 0:
		atime.Nsec = unix.UTIME_OMIT
	}
	mtime := unix.Timespec{Sec: r.mtimeSec, Nsec: int64(r.mtimeNsec)}
	switch {
	case r.flags&pathTimesMtimeNow != 0:
		mtime.Nsec = unix.UTIME_NOW
	case r.flags&pathTimesSetMtime == 0:
		mtime.Nsec = unix.UTIME_OMIT
	}
	flags := 0
	if r.flags&pathTimesNoFollow != 0 {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	err := unix.UtimesNanoAt(unix.AT_FDCWD, path, []unix.Timespec{atime, mtime}, flags)
	if errors.Is(err, unix.ENOTSUP) {
		return unsupportedTimes(fmt.Errorf("set timestamps on %q: %w", path, err))
	}
	return err
}
