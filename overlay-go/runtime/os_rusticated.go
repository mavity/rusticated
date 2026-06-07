//go:build wasip1

package runtime

import (
	"internal/runtime/sys"
	"structs"
	"unsafe"
)

type uintptr32 = uint32
type size = uint32

type errno = uint32

type filesize = uint64

type timestamp = uint64

type clockid = uint32

const (
	clockRealtime  clockid = 0
	clockMonotonic clockid = 1
)

type iovec struct {
	buf    uintptr32
	bufLen size
}

type overlapped struct {
	_         structs.HostLayout
	flags     uint32
	hostError uint32
	continued uint64
	resultExt uint64
}

type overlappedContext struct {
	gp uintptr
	o  overlapped
}

var pendingov [1024]*overlappedContext

//go:linkname awaitOverlapped_syscall syscall.awaitOverlapped
func awaitOverlapped_syscall(ctx *overlappedContext) {
	ctx.gp = uintptr(unsafe.Pointer(getg()))

	// Add to pending list
	added := false
	for i := range pendingov {
		if pendingov[i] == nil {
			pendingov[i] = ctx
			added = true
			break
		}
	}
	if !added {
		throw("too many pending async operations")
	}

	gopark(parkunlock_rusticated, nil, waitReasonIOWait, traceBlockGeneric, 1)
}

func parkunlock_rusticated(gp *g, _ unsafe.Pointer) bool {
	// After the G is parked, we must trigger the pause to return to host.
	// We use the stack pointer of the g0 which is currently running the scheduler.
	pause(sys.GetCallerSP() - 16)
	return true
}

//go:wasmimport env process_exit
func exit(code int32)

//go:wasmimport env get_time
func rusticated_get_time() uint64

//go:wasmimport env get_args
func rusticated_get_args(stringsPtr *byte, stringsLen size) uint64

//go:wasmimport env get_env
func rusticated_get_env(stringsPtr *byte, stringsLen size) uint64

//go:wasmimport env get_random
func rusticated_random_get(buf *byte, bufLen size)

//go:wasmimport env write
//go:noescape
func rusticated_write(overlappedPtr unsafe.Pointer, handle uint64, bufPtr *byte, bufLen size)

//go:nosplit
func write1(fd uintptr, p unsafe.Pointer, n int32) int32 {
	if n == 0 {
		return 0
	}
	var ov overlapped
	ov.flags = 0
	rusticated_write(unsafe.Pointer(&ov), uint64(fd), (*byte)(p), size(n))
	for *(*uint32)(unsafe.Pointer(uintptr(unsafe.Pointer(&ov.flags))))&1 == 0 {
		pause(sys.GetCallerSP() - 16)
	}
	return int32(ov.resultExt)
}

func nanotime1() int64 {
	return int64(rusticated_get_time())
}

func walltime() (sec int64, nsec int32) { return walltime1() }

func walltime1() (sec int64, nsec int32) {
	t := rusticated_get_time()
	return int64(t / 1_000_000_000), int32(t % 1_000_000_000)
}

func readRandom(r []byte) int {
	if len(r) == 0 {
		return 0
	}
	rusticated_random_get(&r[0], size(len(r)))
	return len(r)
}

func splitNL(buf []byte, dst []string) {
	count := 0
	start := 0
	for i, b := range buf {
		if b == 0 { // host writes NUL-terminated strings
			if count < len(dst) {
				dst[count] = string(buf[start:i])
			}
			count++
			start = i + 1
		}
	}
}

func goenvs() {
	packed := rusticated_get_args(nil, 0)
	count := int(packed >> 32)
	bufLen := size(packed & 0xffffffff)

	argslice = make([]string, count)
	if count > 0 {
		buf := make([]byte, bufLen)
		rusticated_get_args(&buf[0], bufLen)
		splitNL(buf, argslice)
	}

	packed = rusticated_get_env(nil, 0)
	count = int(packed >> 32)
	bufLen = size(packed & 0xffffffff)
	envs = make([]string, count)
	if count > 0 {
		buf := make([]byte, bufLen)
		rusticated_get_env(&buf[0], bufLen)
		splitNL(buf, envs)
	}

	// wasm doesn't have a traditional GRP or UID/GID, but we can set defaults.
}

//go:linkname rt0_init _rt0_wasm_wasip1
func rt0_init()

var initialized uint32

// run is called by the rusticated host (washmhost) after one or more I/O
// completions have been written into guest Overlapped memory. It re-enters
// wasm_pc_f_loop, resuming any goroutines that were waiting on pause().
//
//go:wasmexport run
func run() {
	if initialized == 0 {
		initialized = 1
		rt0_init()
		return
	}

	// Process any completions
	for i := range pendingov {
		if pendingov[i] != nil && (pendingov[i].o.flags&1) != 0 {
			ctx := pendingov[i]
			pendingov[i] = nil
			goready((*g)(unsafe.Pointer(ctx.gp)), 0)
		}
	}

	wasm_pc_f_loop()
}

//go:wasmexport resume
func resume() {
	resume_asm()
}

func resume_asm()

//go:wasmexport getsp
func getsp() uint32 {
	return getsp_asm()
}

func getsp_asm() uint32

func usleep(usec uint32) {}
