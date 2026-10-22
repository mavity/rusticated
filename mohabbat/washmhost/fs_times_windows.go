//go:build windows

package main

import (
	"errors"
	"fmt"
	"syscall"
	"time"
)

const (
	fileWriteAttributes   = 0x0100
	fileShareReadWriteDel = 0x00000001 | 0x00000002 | 0x00000004
	errorNotSupported     = syscall.Errno(50)
	errorInvalidParameter = syscall.Errno(87)
)

func setNativeFileTimes(path string, r pathTimesRequest) error {
	const timeFlags = pathTimesSetAtime | pathTimesAtimeNow | pathTimesSetMtime | pathTimesMtimeNow
	atimeTime, mtimeTime, err := resolvePathTimes(path, r)
	if err != nil {
		return err
	}
	if r.flags&timeFlags == 0 {
		return nil
	}

	pathPtr, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	attrs := uint32(syscall.FILE_FLAG_BACKUP_SEMANTICS)
	if r.flags&pathTimesNoFollow != 0 {
		attrs |= syscall.FILE_FLAG_OPEN_REPARSE_POINT
	}
	handle, err := syscall.CreateFile(pathPtr, fileWriteAttributes, fileShareReadWriteDel, nil, syscall.OPEN_EXISTING, attrs, 0)
	if err != nil {
		if r.flags&pathTimesNoFollow != 0 && errors.Is(err, errorNotSupported) {
			return unsupportedTimes(fmt.Errorf("set link timestamps on %q: %w", path, err))
		}
		return err
	}
	defer syscall.CloseHandle(handle)

	atimeSec, atimeNsec := atimeTime.Unix(), uint32(atimeTime.Nanosecond())
	if r.flags&(pathTimesSetAtime|pathTimesAtimeNow) != 0 {
		atimeSec, atimeNsec = r.atimeSec, r.atimeNsec
	}
	mtimeSec, mtimeNsec := mtimeTime.Unix(), uint32(mtimeTime.Nanosecond())
	if r.flags&(pathTimesSetMtime|pathTimesMtimeNow) != 0 {
		mtimeSec, mtimeNsec = r.mtimeSec, r.mtimeNsec
	}
	atime, err := requestedFileTime(atimeSec, atimeNsec, r.flags&pathTimesAtimeNow != 0)
	if err != nil {
		return err
	}
	mtime, err := requestedFileTime(mtimeSec, mtimeNsec, r.flags&pathTimesMtimeNow != 0)
	if err != nil {
		return err
	}
	if err := syscall.SetFileTime(handle, nil, &atime, &mtime); err != nil {
		if r.flags&pathTimesNoFollow != 0 && errors.Is(err, errorNotSupported) {
			return unsupportedTimes(fmt.Errorf("set link timestamps on %q: %w", path, err))
		}
		return err
	}
	return nil
}

func requestedFileTime(sec int64, nsec uint32, now bool) (syscall.Filetime, error) {
	if now {
		now := time.Now()
		sec = now.Unix()
		nsec = uint32(now.Nanosecond())
	}
	const (
		unixToFiletimeEpoch = int64(11_644_473_600)
		ticksPerSecond      = uint64(10_000_000)
	)
	if sec < -unixToFiletimeEpoch || sec > int64(^uint64(0)/ticksPerSecond)-unixToFiletimeEpoch {
		return syscall.Filetime{}, errorInvalidParameter
	}
	seconds := uint64(sec + unixToFiletimeEpoch)
	ticks := uint64(nsec / 100)
	if seconds > (^uint64(0)-ticks)/ticksPerSecond {
		return syscall.Filetime{}, errorInvalidParameter
	}
	ticks += seconds * ticksPerSecond
	return syscall.Filetime{LowDateTime: uint32(ticks), HighDateTime: uint32(ticks >> 32)}, nil
}
