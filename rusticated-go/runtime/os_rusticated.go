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
	if ctx.o.flags&1 != 0 {
		// Already completed, no need to wait.
		return
	}

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

// go:linkname cancelOverlapped_syscall syscall.cancelOverlapped
func cancelOverlapped_syscall(ctx *overlappedContext) {
	// Remove from pending list
	rusticated_cancel(unsafe.Pointer(&ctx.o))
}

func parkunlock_rusticated(gp *g, _ unsafe.Pointer) bool {
	// Confirm the part. Do NOT call pause here - let the scheduler fall through
	// to findRunnable->beforeIdle, which starts a goroutine that pauses with
	// the M's P held, ensuring handleContinuation->goready works
	return true
}

//go:wasmimport env process_exit
func exit(code int32)

//go:wasmimport env cancel
func rusticated_cancel(overlappedPtr unsafe.Pointer)

//go:wasmimport env signal_wait
func rusticated_signal_wait(overlappedPtr unsafe.Pointer, signum uint32)

//go:wasmimport env timer_set
func rusticated_timer_set(overlappedPtr unsafe.Pointer, delayMs uint32)

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
	islibrary = false

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

type callbackRegistration struct {
	ovPtr unsafe.Pointer
	fn    func(args []uint64, invID, cbID uint64)
}

var (
	registeredCallbacksMu mutex
	registeredCallbacks   []*callbackRegistration
)

//go:linkname registerCallbackRuntime_syscall syscall.registerCallbackRuntime
func registerCallbackRuntime_syscall(ovPtr unsafe.Pointer, fn func(args []uint64, invID, cbID uint64)) bool {
	lock(&registeredCallbacksMu)
	defer unlock(&registeredCallbacksMu)
	for i, cb := range registeredCallbacks {
		if cb == nil {
			registeredCallbacks[i] = &callbackRegistration{
				ovPtr: ovPtr,
				fn:    fn,
			}
			return true
		}
	}
	registeredCallbacks = append(registeredCallbacks, &callbackRegistration{
		ovPtr: ovPtr,
		fn:    fn,
	})
	return true
}

//go:linkname unregisterCallbackRuntime_syscall syscall.unregisterCallbackRuntime
func unregisterCallbackRuntime_syscall(ovPtr unsafe.Pointer) {
	lock(&registeredCallbacksMu)
	defer unlock(&registeredCallbacksMu)
	for i, cb := range registeredCallbacks {
		if cb != nil && cb.ovPtr == ovPtr {
			registeredCallbacks[i] = nil
			return
		}
	}
}

//go:linkname pauseToHost_syscall syscall.pauseToHost
func pauseToHost_syscall() {
	pause(sys.GetCallerSP() - 16)
}

//go:linkname currentG_syscall syscall.currentG
func currentG_syscall() uintptr {
	return uintptr(unsafe.Pointer(getg()))
}

// handleContinuation is called by the assembly entry point on re-entry (continuation)
// after one or moree I/O completions have been written into guest Overlapped memory.
// It processes completions and marks waiting goroutines as ready.
func handleContinuation() {
	// 1. Process any callback invocations projected by host into callback overlappeds
	lock(&registeredCallbacksMu)
	for i := range registeredCallbacks {
		cb := registeredCallbacks[i]
		if cb != nil {
			flags := *(*uint32)(cb.ovPtr)
			if flags&1 != 0 {
				cbID := *(*uint64)(unsafe.Pointer(uintptr(cb.ovPtr) + 24))
				invID := *(*uint64)(unsafe.Pointer(uintptr(cb.ovPtr) + 32))
				argLen := *(*uint32)(unsafe.Pointer(uintptr(cb.ovPtr) + 40))
				argCount := int(argLen / 8)
				args := make([]uint64, argCount)
				for a := 0; a < argCount; a++ {
					args[a] = *(*uint64)(unsafe.Pointer(uintptr(cb.ovPtr) + 48 + uintptr(a*8)))
				}

				// Atomically clear completion flag so host knows invocation was accepted
				*(*uint32)(cb.ovPtr) = 0

				// Queue a new goroutine to execute callback function with copied arguments
				go cb.fn(args, invID, cbID)
			}
		}
	}
	unlock(&registeredCallbacksMu)

	// 2. Process any completions
	for i := range pendingov {
		if pendingov[i] != nil && (pendingov[i].o.flags&1) != 0 {
			ctx := pendingov[i]
			pendingov[i] = nil
			goready((*g)(unsafe.Pointer(ctx.gp)), 0)
		}
	}

	// Return normally so the assembly entry point falls through
	// to wasm_pc_f_loop, which runs the scheduler and lets unparked
	// goroutines run.
}

func usleep(usec uint32) {}

// This is a goroutine that yields to the hjost by calling pause.
// Started by beforeIdle when the scheduler has no runnable goroutines.
func handleAsyncEvent() {
	pause(sys.GetCallerSP() - 16)
}

func setNetpollTimer(delayMs uint32) {
	if netpollTimerActive {
		if netpollTimerOv.flags&1 == 0 {
			netpollTimerOv.flags = 0
		} else {
			rusticated_cancel(unsafe.Pointer(&netpollTimerOv))
		}
	}

	netpollTimerOv.flags = 0
	rusticated_timer_set(unsafe.Pointer(&netpollTimerOv), delayMs)
	netpollTimerActive = true
}

// ── Signal delivery bridge ─────────────────────────────────────────────────
//
// The Go runtime's signal machinery (runtime/sigqueue.go) and the os/signal
// package are retained unchanged. The stock wasm backend (runtime/os_wasm.go)
// hardcodes _NSIG = 0 and stubs out sigenable/sigdisable/sigignore, which makes
// os/signal a no-op. We supply that backend here: each enabled signal gets a
// monitor goroutine that waits on the host signal_wait ABI and feeds arriving
// signals into sigsend, the standard producer side of the Go signal queue.
//
// Memory for the overlapped structs is held in this package-level array so the
// pointers handed to the host stay stable (globals are not moved by the GC).
var sigMonitorCtx [_NSIG]overlappedContext
var sigMonitorRunning [_NSIG]bool
var sigMonitorAlive [_NSIG]bool

// rusticated_sigenable is the backend for runtime.sigenable (called from
// os/signal via signal_enable). It ensures a single monitor goroutine is
// running for the signal.
func rusticated_sigenable(s uint32) {
	if s >= _NSIG {
		return
	}
	sigMonitorRunning[s] = true
	if !sigMonitorAlive[s] {
		sigMonitorAlive[s] = true
		go sigMonitor(s)
	}
}

// rusticated_sigdisable is the backend for runtime.sigdisable. It asks the host
// to drop the pending wait and then wakes the parked monitor so it can observe
// the disable and stop re-arming.
func rusticated_sigdisable(s uint32) {
	if s >= _NSIG || !sigMonitorRunning[s] {
		return
	}
	sigMonitorRunning[s] = false
	ctx := &sigMonitorCtx[s]
	rusticated_cancel(unsafe.Pointer(&ctx.o))
	// Complete the overlapped ourselves: the host has dropped the waiter and
	// will not write it again, so the parked monitor must be released here.
	// resultExt is cleared so a racing re-enable that observes the wake treats
	// it as signal 0 (SIGNONE), which sigsend ignores.
	ctx.o.resultExt = 0
	ctx.o.flags = 1
}

// rusticated_sigignore is the backend for runtime.sigignore. With no default
// handlers on wasm, ignoring a signal is equivalent to no longer monitoring it.
func rusticated_sigignore(s uint32) {
	rusticated_sigdisable(s)
}

func sigMonitor(s uint32) {
	ctx := &sigMonitorCtx[s]
	for {
		ctx.o.flags = 0
		rusticated_signal_wait(unsafe.Pointer(&ctx.o), s)
		awaitOverlapped_syscall(ctx)
		if !sigMonitorRunning[s] {
			sigMonitorAlive[s] = false
			return
		}
		sigsend(uint32(ctx.o.resultExt))
	}
}
