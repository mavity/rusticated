//go:build darwin

package main

import (
	"errors"
	"fmt"

	"golang.org/x/sys/unix"
)

func setNativeFileTimes(path string, r pathTimesRequest) error {
	atime, mtime, err := resolvePathTimes(path, r)
	if err != nil {
		return err
	}
	if r.flags&(pathTimesSetAtime|pathTimesAtimeNow|pathTimesSetMtime|pathTimesMtimeNow) == 0 {
		return nil
	}
	flags := 0
	if r.flags&pathTimesNoFollow != 0 {
		flags = unix.AT_SYMLINK_NOFOLLOW
	}
	times := []unix.Timespec{
		{Sec: atime.Unix(), Nsec: int64(atime.Nanosecond())},
		{Sec: mtime.Unix(), Nsec: int64(mtime.Nanosecond())},
	}
	err = unix.UtimesNanoAt(unix.AT_FDCWD, path, times, flags)
	if errors.Is(err, unix.ENOTSUP) {
		return unsupportedTimes(fmt.Errorf("set timestamps on %q: %w", path, err))
	}
	return err
}
