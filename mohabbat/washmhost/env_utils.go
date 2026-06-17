package main

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
func (h *HostEnv) sys_get_platform_info(ctx context.Context, m api.Module, stack []uint64) {
	ptr := uint32(stack[0])
	maxLen := uint32(stack[1])

	mem := m.Memory()
	if mem == nil {
		stack[0] = 22 // EINVAL
		return
	}

	// Traditional binary structure (fixed layout)
	// Offset 0: Flags (u32)
	// Offset 4: Path separator type (u8)
	// Offset 5: Path list separator type (u8)
	// Offset 6: OS Kind (u16)
	// Offset 8: OS Version (4x u16)
	// Offset 16: CPU Type (u16)
	// Offset 18: CPU Biteness (u8)
	// Offset 19: reserved
	// Offset 20: OS name string (64 bytes)
	// Offset 84: WASM platform name string (64 bytes)
	// Offset 148: WASM platform version (4x u16)
	// Offset 156: WASM platform version string (64 bytes)
	// Offset 220: Rusticated name string (64 bytes)
	// Offset 284: Rusticated version (4x u16)
	// Offset 292: Rusticated version string (64 bytes)
	// Total roughly ~356 bytes. MaxLen should be checked.

	structSize := uint32(356)
	if maxLen < structSize {
		stack[0] = 7 // E2BIG
		return
	}

	buf := make([]byte, structSize)

	// Flags (bit 0: case sensitive)
	flags := uint32(0)
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		flags |= 1
	}
	binary.LittleEndian.PutUint32(buf[0:4], flags)

	// Separators
	pathSep := byte('/')
	listSep := byte(':')
	if runtime.GOOS == "windows" {
		pathSep = '\\'
		listSep = ';'
	}
	buf[4] = pathSep
	buf[5] = listSep

	// OS Kind (1=Windows, 2=Linux, 3=Darwin, 4=Bsd)
	osKind := uint16(0)
	switch runtime.GOOS {
	case "windows":
		osKind = 1
	case "linux":
		osKind = 2
	case "darwin":
		osKind = 3
	default:
		osKind = 4
	}
	binary.LittleEndian.PutUint16(buf[6:8], osKind)

	// OS Version (stub for now, could use syscall.GetVersion on Windows)
	// binary.LittleEndian.PutUint16(buf[8:10], 10) ...

	// CPU Type (1=x86_64, 2=arm64)
	cpuType := uint16(0)
	switch runtime.GOARCH {
	case "amd64":
		cpuType = 1
	case "arm64":
		cpuType = 2
	}
	binary.LittleEndian.PutUint16(buf[16:18], cpuType)
	buf[18] = 64 // bitness

	copySafe := func(offset int, s string) {
		b := []byte(s)
		if len(b) > 63 {
			b = b[:63]
		}
		copy(buf[offset:offset+64], b)
	}

	copySafe(20, runtime.GOOS)
	copySafe(84, "wazero") // category
	// WASM platform version string (could be injected from host build)
	copySafe(156, "1.26.4")
	copySafe(220, "washmhost")
	copySafe(292, "0.1.0")

	if ok := mem.Write(ptr, buf); !ok {
		stack[0] = 14 // EFAULT
		return
	}
	stack[0] = 0
}
