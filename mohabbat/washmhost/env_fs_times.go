package main

import (
	"context"
	"errors"
	"os"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero/api"
)

const (
	pathTimesSetAtime uint32 = 1 << iota
	pathTimesAtimeNow
	pathTimesSetMtime
	pathTimesMtimeNow
	pathTimesNoFollow
)

const pathTimesKnownFlags = pathTimesSetAtime |
	pathTimesAtimeNow |
	pathTimesSetMtime |
	pathTimesMtimeNow |
	pathTimesNoFollow

type pathTimesRequest struct {
	atimeSec  int64
	atimeNsec uint32
	mtimeSec  int64
	mtimeNsec uint32
	flags     uint32
}

func (r pathTimesRequest) validate() error {
	if r.flags&^pathTimesKnownFlags != 0 ||
		r.flags&(pathTimesSetAtime|pathTimesAtimeNow) == pathTimesSetAtime|pathTimesAtimeNow ||
		r.flags&(pathTimesSetMtime|pathTimesMtimeNow) == pathTimesSetMtime|pathTimesMtimeNow {
		return syscall.EINVAL
	}
	if r.flags&pathTimesSetAtime != 0 && r.atimeNsec >= 1_000_000_000 {
		return syscall.EINVAL
	}
	if r.flags&pathTimesSetMtime != 0 && r.mtimeNsec >= 1_000_000_000 {
		return syscall.EINVAL
	}
	return nil
}

func resolvePathTimes(path string, r pathTimesRequest) (time.Time, time.Time, error) {
	follow := r.flags&pathTimesNoFollow == 0
	if !follow && r.flags&(pathTimesSetAtime|pathTimesAtimeNow|pathTimesSetMtime|pathTimesMtimeNow) == 0 {
		_, err := os.Lstat(path)
		return time.Time{}, time.Time{}, err
	}
	if follow && r.flags&(pathTimesSetAtime|pathTimesAtimeNow|pathTimesSetMtime|pathTimesMtimeNow) == 0 {
		_, err := os.Stat(path)
		return time.Time{}, time.Time{}, err
	}

	var info os.FileInfo
	if r.flags&pathTimesNoFollow != 0 {
		var err error
		info, err = os.Lstat(path)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	} else if r.flags&(pathTimesSetAtime|pathTimesAtimeNow) == 0 || r.flags&(pathTimesSetMtime|pathTimesMtimeNow) == 0 {
		var err error
		info, err = os.Stat(path)
		if err != nil {
			return time.Time{}, time.Time{}, err
		}
	}

	now := time.Now()
	atime := time.Unix(r.atimeSec, int64(r.atimeNsec))
	mtime := time.Unix(r.mtimeSec, int64(r.mtimeNsec))
	if r.flags&pathTimesAtimeNow != 0 {
		atime = now
	} else if r.flags&pathTimesSetAtime == 0 {
		atime = info.ModTime()
		if info != nil {
			stat := createAbiStatAt(info, path, r.flags&pathTimesNoFollow != 0)
			if stat.ValidMask&statValidAtime != 0 {
				atime = time.Unix(0, stat.AccessedNs)
			}
		}
	}
	if r.flags&pathTimesMtimeNow != 0 {
		mtime = now
	} else if r.flags&pathTimesSetMtime == 0 {
		mtime = info.ModTime()
	}
	return atime, mtime, nil
}

func unsupportedTimes(err error) error {
	if errors.Is(err, errors.ErrUnsupported) {
		return err
	}
	return errors.Join(errors.ErrUnsupported, err)
}

func (h *HostEnv) sys_path_set_times(_ context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	request := pathTimesRequest{
		atimeSec:  int64(stack[3]),
		atimeNsec: uint32(stack[4]),
		mtimeSec:  int64(stack[5]),
		mtimeNsec: uint32(stack[6]),
		flags:     uint32(stack[7]),
	}
	mem := m.Memory()
	pathBytes, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if len(pathBytes) == 0 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	if err := request.validate(); err != nil {
		writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
		return
	}
	hostPath := h.resolveGuestPathForHost(string(pathBytes))
	state := h.RegisterOp(ovPtr, nil)
	go func() {
		err := setNativeFileTimes(hostPath, request)
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
		}
	}()
}
