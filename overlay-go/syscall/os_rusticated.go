//go:build wasip1

package syscall

import "unsafe"

//go:wasmimport env process_exit
func ProcExit(code int32)

//go:wasmimport env process_spawn
//go:noescape
func rusticated_process_spawn(overlapped unsafe.Pointer, cfgPtr *byte, cfgLen uint32)

//go:wasmimport env process_wait
//go:noescape
func rusticated_process_wait(overlapped unsafe.Pointer, handle uint64)

//go:wasmimport env get_platform_info
func rusticated_get_platform_info(ptr unsafe.Pointer, maxLen uint32) uint32

type PlatformInfo struct {
	Flags                uint32
	PathSeparator        byte
	PathListSeparator    byte
	OSKind               uint16
	OSVersion            [4]uint16
	OSName               string
	CPUType              uint16
	CPUBitness           uint8
	WasmPlatform         string
	WasmVersion          [4]uint16
	WasmVersionStr       string
	RusticatedName       string
	RusticatedVersion    [4]uint16
	RusticatedVersionStr string
}

var (
	platformOnce uint32
	platformData *PlatformInfo
)

func GetPlatformInfo() *PlatformInfo {
	// Sync/atomic spin-once-lock (WASM is single-threaded usually, but stay safe)
	for {
		state := atomicLoad(&platformOnce)
		if state == 2 {
			return platformData
		}
		if state == 0 {
			if atomicCompareAndSwap(&platformOnce, 0, 1) {
				// We won the race (if any)
				break
			}
		}
		// Yield/Spin (minimal)
	}

	// Fetch raw bytes
	var buf [512]byte
	res := rusticated_get_platform_info(unsafe.Pointer(&buf[0]), uint32(len(buf)))
	if res != 0 {
		panic("syscall: rusticated_get_platform_info failed")
	}

	pi := &PlatformInfo{}
	flags := readU32(buf[0:4])
	pi.Flags = flags
	pi.PathSeparator = byte(buf[4])
	pi.PathListSeparator = byte(buf[5])
	pi.OSKind = readU16(buf[6:8])
	pi.OSVersion = [4]uint16{readU16(buf[8:10]), readU16(buf[10:12]), readU16(buf[12:14]), readU16(buf[14:16])}
	pi.CPUType = readU16(buf[16:18])
	pi.CPUBitness = buf[18]
	pi.OSName = stringFromBuf(buf[20:84])
	pi.WasmPlatform = stringFromBuf(buf[84:148])
	pi.WasmVersion = [4]uint16{readU16(buf[148:150]), readU16(buf[150:152]), readU16(buf[152:154]), readU16(buf[154:156])}
	pi.WasmVersionStr = stringFromBuf(buf[156:220])
	pi.RusticatedName = stringFromBuf(buf[220:284])
	pi.RusticatedVersion = [4]uint16{readU16(buf[284:286]), readU16(buf[286:288]), readU16(buf[288:290]), readU16(buf[290:292])}
	pi.RusticatedVersionStr = stringFromBuf(buf[292:356])
	platformData = pi

	atomicStore(&platformOnce, 2)
	return platformData
}

// Helpers since we don't have binary.LittleEndian in low-level runtime/syscall overlays sometimes
func readU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
func readU16(b []byte) uint16 {
	return uint16(b[0]) | uint16(b[1])<<8
}
func stringFromBuf(b []byte) string {
	n := 0
	for n < len(b) && b[n] != 0 {
		n++
	}
	return string(b[:n])
}

//go:linkname atomicLoad runtime/internal/atomic.Load
func atomicLoad(ptr *uint32) uint32

//go:linkname atomicStore runtime/internal/atomic.Store
func atomicStore(ptr *uint32, val uint32)

//go:linkname atomicCompareAndSwap runtime/internal/atomic.Cas
func atomicCompareAndSwap(ptr *uint32, old, new uint32) bool
