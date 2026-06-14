package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero/api"
)

func mapErrno(err error) uint32 {
	if err == nil {
		return 0
	}
	if os.IsNotExist(err) {
		return 2 // ENOENT
	}
	if os.IsPermission(err) {
		return 13 // EACCES
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		return uint32(errno)
	}
	return 5 // EIO
}

func resolveUsableCwd() (string, error) {
	if cwd, err := os.Getwd(); err == nil {
		return cwd, nil
	}

	if veg := os.Getenv("MOHABBAT_VEGETABLE_PATH"); veg != "" {
		dir := filepath.Dir(veg)
		if err := os.Chdir(dir); err == nil {
			if cwd, err := os.Getwd(); err == nil {
				return cwd, nil
			}
		}
	}

	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		if err := os.Chdir(dir); err == nil {
			if cwd, err := os.Getwd(); err == nil {
				return cwd, nil
			}
		}
	}

	return "", fmt.Errorf("unable to resolve cwd")
}

func debugLog(format string, args ...interface{}) {
	f, err := os.OpenFile(filepath.Join(os.TempDir(), "washmhost-debug.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintf(f, "%s ", time.Now().Format("15:04:05.000"))
	_, _ = fmt.Fprintf(f, format, args...)
	_, _ = f.Write([]byte("\n"))
}

func writeOverlapped(mod api.Module, ovPtr uint32, errorCode uint32, continued uint64, resultExt uint64) error {
	mem := mod.Memory()
	if mem == nil {
		return fmt.Errorf("no memory export")
	}

	buf := make([]byte, 24)
	binary.LittleEndian.PutUint32(buf[0:4], 1)
	binary.LittleEndian.PutUint32(buf[4:8], errorCode)
	binary.LittleEndian.PutUint64(buf[8:16], continued)
	binary.LittleEndian.PutUint64(buf[16:24], resultExt)

	if ok := mem.Write(ovPtr, buf); !ok {
		return fmt.Errorf("ovPtr %d out of bounds", ovPtr)
	}
	return nil
}
