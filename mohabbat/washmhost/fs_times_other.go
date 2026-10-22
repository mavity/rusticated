//go:build !linux && !darwin && !windows

package main

import (
	"errors"
	"os"
)

func setNativeFileTimes(path string, r pathTimesRequest) error {
	if r.flags&pathTimesNoFollow != 0 &&
		r.flags&(pathTimesSetAtime|pathTimesAtimeNow|pathTimesSetMtime|pathTimesMtimeNow) != 0 {
		return errors.ErrUnsupported
	}
	atime, mtime, err := resolvePathTimes(path, r)
	if err != nil {
		return err
	}
	if r.flags&(pathTimesSetAtime|pathTimesAtimeNow|pathTimesSetMtime|pathTimesMtimeNow) == 0 {
		return nil
	}
	return os.Chtimes(path, atime, mtime)
}
