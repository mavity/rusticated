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

	"github.com/tetratelabs/wazero/api"
)

const (
	// WASI (wasip1) errno mapping.
	// Our ABI mandates that all system handlers return these values to the guest.
	// These differ significantly from Linux/Darwin host errnos.
	// Documentation: https://github.com/WebAssembly/WASI/blob/main/legacy/preview1/docs.md#errno
	wasiSuccess         uint32 = 0
	wasiE2BIG           uint32 = 1
	wasiEACCES          uint32 = 2
	wasiEADDRINUSE      uint32 = 3
	wasiEADDRNOTAVAIL   uint32 = 4
	wasiEAFNOSUPPORT    uint32 = 5
	wasiEAGAIN          uint32 = 6
	wasiEALREADY        uint32 = 7
	wasiEBADF           uint32 = 8
	wasiEBADMSG         uint32 = 9
	wasiEBUSY           uint32 = 10
	wasiECANCELED       uint32 = 11
	wasiECHILD          uint32 = 12
	wasiECONNABORTED    uint32 = 13
	wasiECONNREFUSED    uint32 = 14
	wasiECONNRESET      uint32 = 15
	wasiEDEADLK         uint32 = 16
	wasiEDESTADDRREQ    uint32 = 17
	wasiEDOM            uint32 = 18
	wasiEDQUOT          uint32 = 19
	wasiEEXIST          uint32 = 20
	wasiEFAULT          uint32 = 21
	wasiEFBIG           uint32 = 22
	wasiEHOSTUNREACH    uint32 = 23
	wasiEIDRM           uint32 = 24
	wasiEILSEQ          uint32 = 25
	wasiEINPROGRESS     uint32 = 26
	wasiEINTR           uint32 = 27
	wasiEINVAL          uint32 = 28
	wasiEIO             uint32 = 29
	wasiEISCONN         uint32 = 30
	wasiEISDIR          uint32 = 31
	wasiELOOP           uint32 = 32
	wasiEMFILE          uint32 = 33
	wasiEMLINK          uint32 = 34
	wasiEMSGSIZE        uint32 = 35
	wasiEMULTIHOP       uint32 = 36
	wasiENAMETOOLONG    uint32 = 37
	wasiENETDOWN        uint32 = 38
	wasiENETRESET       uint32 = 39
	wasiENETUNREACH     uint32 = 40
	wasiENFILE          uint32 = 41
	wasiENOBUFS         uint32 = 42
	wasiENODEV          uint32 = 43
	wasiENOENT          uint32 = 44
	wasiENOEXEC         uint32 = 45
	wasiENOLCK          uint32 = 46
	wasiENOLINK         uint32 = 47
	wasiENOMEM          uint32 = 48
	wasiENOMSG          uint32 = 49
	wasiENOPROTOOPT     uint32 = 50
	wasiENOSPC          uint32 = 51
	wasiENOSYS          uint32 = 52
	wasiENOTCONN        uint32 = 53
	wasiENOTDIR         uint32 = 54
	wasiENOTEMPTY       uint32 = 55
	wasiENOTRECOVERABLE uint32 = 56
	wasiENOTSOCK        uint32 = 57
	wasiENOTSUP         uint32 = 58
	wasiENOTTY          uint32 = 59
	wasiENXIO           uint32 = 60
	wasiEOVERFLOW       uint32 = 61
	wasiEOWNERDEAD      uint32 = 62
	wasiEPERM           uint32 = 63
	wasiEPIPE           uint32 = 64
	wasiEPROTO          uint32 = 65
	wasiEPROTONOSUPPORT uint32 = 66
	wasiEPROTOTYPE      uint32 = 67
	wasiERANGE          uint32 = 68
	wasiEROFS           uint32 = 69
	wasiESPIPE          uint32 = 70
	wasiESRCH           uint32 = 71
	wasiESTALE          uint32 = 72
	wasiETIMEDOUT       uint32 = 73
	wasiETXTBSY         uint32 = 74
	wasiEXDEV           uint32 = 75
	wasiENOTCAPABLE     uint32 = 76
)

func mapErrno(err error) uint32 {
	if err == nil {
		return wasiSuccess
	}
	if os.IsNotExist(err) {
		return wasiENOENT
	}
	if os.IsPermission(err) {
		return wasiEACCES
	}
	var errno syscall.Errno
	if errors.As(err, &errno) {
		// Native errnos must be translated to WASI errnos.
		// Go's syscall package uses host values.
		switch errno {
		case syscall.EACCES:
			return wasiEACCES
		case syscall.EBADF:
			return wasiEBADF
		case syscall.EEXIST:
			return wasiEEXIST
		case syscall.EINVAL:
			return wasiEINVAL
		case syscall.EIO:
			return wasiEIO
		case syscall.EISDIR:
			return wasiEISDIR
		case syscall.ELOOP:
			return wasiELOOP
		case syscall.EMFILE:
			return wasiEMFILE
		case syscall.EMLINK:
			return wasiEMLINK
		case syscall.ENAMETOOLONG:
			return wasiENAMETOOLONG
		case syscall.ENFILE:
			return wasiENFILE
		case syscall.ENODEV:
			return wasiENODEV
		case syscall.ENOENT:
			return wasiENOENT
		case syscall.ENOSYS:
			return wasiENOSYS
		case syscall.ENOTDIR:
			return wasiENOTDIR
		case syscall.ENOTEMPTY:
			return wasiENOTEMPTY
		case syscall.ENOTTY:
			return wasiENOTTY
		case syscall.ENXIO:
			return wasiENXIO
		case syscall.EPERM:
			return wasiEPERM
		case syscall.EPIPE:
			return wasiEPIPE
		case syscall.EROFS:
			return wasiEROFS
		case syscall.ESPIPE:
			return wasiESPIPE
		case syscall.EXDEV:
			return wasiEXDEV
		case syscall.ETIMEDOUT:
			return wasiETIMEDOUT
		case syscall.ECONNREFUSED:
			return wasiECONNREFUSED
		case syscall.ECONNRESET:
			return wasiECONNRESET
		case syscall.ECONNABORTED:
			return wasiECONNABORTED
		case syscall.EADDRINUSE:
			return wasiEADDRINUSE
		case syscall.EADDRNOTAVAIL:
			return wasiEADDRNOTAVAIL
		case syscall.EAFNOSUPPORT:
			return wasiEAFNOSUPPORT
		case syscall.EAGAIN:
			return wasiEAGAIN
		case syscall.EALREADY:
			return wasiEALREADY
		case syscall.EFAULT:
			return wasiEFAULT
		case syscall.ERANGE:
			return wasiERANGE
		}
	}
	return wasiEIO
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
	// Offset 356: Build version string (64 bytes)
	// Offset 420: Build time string (64 bytes)
	// Offset 484: Build platform string (64 bytes)
	// Total roughly ~548 bytes. MaxLen should be checked.

	structSize := uint32(548)
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
	copySafe(292, BuildVersion)

	copySafe(356, BuildVersion)
	copySafe(420, BuildTime)
	copySafe(484, BuildPlatform)

	if ok := mem.Write(ptr, buf); !ok {
		stack[0] = uint64(wasiEFAULT)
		return
	}
	stack[0] = uint64(wasiSuccess)
}
