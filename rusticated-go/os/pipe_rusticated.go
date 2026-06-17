//go:build wasip1

package os

import "syscall"

func Pipe() (r *File, w *File, err error) {
	var p [2]int
	e := syscall.Pipe(p[:])
	if e != nil {
		return nil, nil, &PathError{Op: "pipe", Path: "|", Err: e}
	}
	return newFile(p[0], "|0", kindPipe, false), newFile(p[1], "|1", kindPipe, false), nil
}
