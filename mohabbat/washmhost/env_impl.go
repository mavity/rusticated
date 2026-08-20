package main

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unsafe"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"golang.org/x/term"
)

const sigwinch = syscall.Signal(0x1c) // SIGWINCH (28)

type HostEnv struct {
	mu                sync.Mutex
	activeOps         map[uint32]*OpState
	nextOpID          uint64
	handles           map[uint64]interface{}
	nextHandle        uint64
	outstandingOps    int32
	fileOpsQueue      chan func()
	ttyRawState       *term.State
	ttyRawFd          int
	signals           chan os.Signal
	signalWaiters     map[uint32]*OpState // signum -> state
	pendingSignals    chan *OpState       // state to complete
	timers            map[uint32]*time.Timer
	lastLog           time.Time
	forcedExitCode    int32
	args              []string
	callbackQueue     chan *CallbackEvent
	owningGID         uint64
	activeInvocations map[uint32]chan uintptr
	nextInvocationID  uint32
	activeCallState   *OpState
	activeCallOvPtr   uint32
	activeCallRet     uintptr
}

type OpState struct {
	ovPtr       uint32
	opID        uint64
	handle      interface{}
	deadline    time.Time
	signum      uint32
	isCancelled bool
	decDone     int32
	reserved    uint64
}

type CallbackEvent struct {
	CallbackHandle uint64
	Args           []uintptr
	BufParams      [][]byte
	RespChan       chan uintptr
}

func NewHostEnv() *HostEnv {
	env := &HostEnv{

		activeOps:         make(map[uint32]*OpState),
		handles:           make(map[uint64]interface{}),
		nextHandle:        3, // 0,1,2 reserved
		outstandingOps:    0,
		fileOpsQueue:      make(chan func(), 1000),
		signals:           make(chan os.Signal, 10),
		signalWaiters:     make(map[uint32]*OpState),
		pendingSignals:    make(chan *OpState, 100),
		timers:            make(map[uint32]*time.Timer),
		forcedExitCode:    -1,
		callbackQueue:     make(chan *CallbackEvent, 100),
		activeInvocations: make(map[uint32]chan uintptr),
	}
	env.handles[0] = os.Stdin
	env.handles[1] = os.Stdout
	env.handles[2] = os.Stderr

	env.args = os.Args

	var notifySigs = []os.Signal{syscall.SIGINT, syscall.SIGTERM}
	if runtime.GOOS != "windows" {
		notifySigs = append(notifySigs, sigwinch)
	}
	signal.Notify(env.signals, notifySigs...)

	go func() {
		for sig := range env.signals {
			var signum uint32
			switch sig {
			case syscall.SIGINT:
				signum = 2
			case syscall.SIGTERM:
				signum = 15
			case sigwinch:
				signum = 27
			}
			if signum != 0 {
				env.notifySignal(signum)
			}
		}
	}()

	if runtime.GOOS == "windows" {
		go func() {
			lastW, lastH, _ := term.GetSize(int(os.Stdin.Fd()))
			for {
				time.Sleep(500 * time.Millisecond)
				w, h2, err := term.GetSize(int(os.Stdin.Fd()))
				if err == nil && (w != lastW || h2 != lastH) {
					lastW, lastH = w, h2
					env.notifySignal(27)
				}
			}
		}()
	}

	return env
}

func (h *HostEnv) Close() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.ttyRawState != nil {
		_ = term.Restore(h.ttyRawFd, h.ttyRawState)
		h.ttyRawState = nil
	}
}

func (h *HostEnv) notifySignal(signum uint32) {
	h.mu.Lock()
	state, ok := h.signalWaiters[signum]
	if ok {
		delete(h.signalWaiters, signum)
		h.mu.Unlock()
		h.pendingSignals <- state
		h.fileOpsQueue <- func() {}
	} else {
		h.mu.Unlock()
	}
}

func (h *HostEnv) IncOps() {
	atomic.AddInt32(&h.outstandingOps, 1)
}

func (h *HostEnv) RegisterOp(ovPtr uint32, handle interface{}) *OpState {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.registerOpLocked(ovPtr, handle)
}

func (h *HostEnv) registerOpLocked(ovPtr uint32, handle interface{}) *OpState {
	h.nextOpID++
	state := &OpState{
		ovPtr:  ovPtr,
		opID:   h.nextOpID,
		handle: handle,
	}
	h.activeOps[ovPtr] = state
	h.IncOps()
	return state
}

func (h *HostEnv) IsOpActive(ovPtr uint32, id uint64) bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.isOpActiveLocked(ovPtr, id)
}

func (h *HostEnv) isOpActiveLocked(ovPtr uint32, id uint64) bool {
	current, ok := h.activeOps[ovPtr]
	if !ok || current.opID != id {
		return false
	}
	if current.isCancelled {
		delete(h.activeOps, ovPtr)
		return false
	}
	return true
}

func (h *HostEnv) DecOps() {
	for {
		old := atomic.LoadInt32(&h.outstandingOps)
		if old <= 0 {
			return
		}
		if atomic.CompareAndSwapInt32(&h.outstandingOps, old, old-1) {
			newVal := old - 1
			if newVal == 0 {
				h.fileOpsQueue <- func() {} // Wake up Poll
			}
			return
		}
	}
}

func (h *HostEnv) DecOpsFor(state *OpState) {
	if state == nil {
		return
	}
	if atomic.CompareAndSwapInt32(&state.decDone, 0, 1) {
		h.DecOps()
	}
}

func (h *HostEnv) PendingOps() int32 {
	return atomic.LoadInt32(&h.outstandingOps)
}

func (h *HostEnv) HasOutstandingOps() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return atomic.LoadInt32(&h.outstandingOps) > 0
}

func (h *HostEnv) HasActiveOps() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, op := range h.activeOps {
		if !op.isCancelled {
			return true
		}
	}
	for _, op := range h.signalWaiters {
		if !op.isCancelled {
			return true
		}
	}
	return false
}

func (h *HostEnv) HasLiveOps() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	for _, state := range h.activeOps {
		if !state.isCancelled {
			return true
		}
	}
	return false
}

func (h *HostEnv) CancelOp(ovPtr uint32) {
	pastTime := time.Unix(1, 0)
	h.mu.Lock()

	if t, ok := h.timers[ovPtr]; ok {
		t.Stop()
		delete(h.timers, ovPtr)
		if state, exists := h.activeOps[ovPtr]; exists {
			state.isCancelled = true
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			h.DecOpsFor(state)
			return
		}
	}

	state, ok := h.activeOps[ovPtr]
	if ok && !state.isCancelled {
		state.isCancelled = true
		if state.signum != 0 {
			delete(h.signalWaiters, state.signum)
		}
		delete(h.activeOps, ovPtr)
		h.mu.Unlock()

		if state.handle != nil {
			if c, ok := state.handle.(interface{ SetDeadline(time.Time) error }); ok {
				_ = c.SetDeadline(pastTime)
			}
		}
		h.DecOpsFor(state)
	} else {
		h.mu.Unlock()
	}
}

func (h *HostEnv) Register(ctx context.Context, r wazero.Runtime) error {
	builder := r.NewHostModuleBuilder("env")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_panic), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("host_panic")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_get_time), []api.ValueType{}, []api.ValueType{api.ValueTypeI64}).Export("get_time")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_get_random), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("get_random")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_get_args), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("get_args")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_get_env), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("get_env")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_get_cwd), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("get_cwd")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_set_cwd), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("set_cwd")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_timer_set), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("timer_set")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_read), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("read")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_write), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("write")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_seek), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).Export("seek")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_handle_close), []api.ValueType{api.ValueTypeI64}, []api.ValueType{}).Export("handle_close")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_path_open), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_open")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dir_read), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dir_read")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_path_stat), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_stat")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_path_chmod), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_chmod")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_path_remove), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_remove")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_path_mkdir), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_mkdir")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_path_rename), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_rename")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_net_open), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("net_open")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_net_accept), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).Export("net_accept")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_net_lookup), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("net_lookup")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_net_cert_verify), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("net_cert_verify")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_process_spawn), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("process_spawn")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_process_pipe), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).Export("process_pipe")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_process_wait), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).Export("process_wait")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_process_signal), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).Export("process_signal")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_signal_wait), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("signal_wait")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_cancel), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).Export("cancel")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_get_platform_info), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("get_platform_info")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_tty_set_mode), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).Export("tty_set_mode")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_tty_get_size), []api.ValueType{api.ValueTypeI64}, []api.ValueType{api.ValueTypeI32}).Export("tty_get_size")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_fd_isatty), []api.ValueType{api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("fd_isatty")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_process_exit), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).Export("process_exit")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_open), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_open")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_sym), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_sym")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_call), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_call")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_callback_create), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_callback_create")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_callback_respond), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).Export("dylib_callback_respond")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_close), []api.ValueType{api.ValueTypeI64}, []api.ValueType{}).Export("dylib_close")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_read_cstr), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_read_cstr")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			val := int32(stack[0])
			debugLog("GUEST DEBUG: %d (0x%x)\n", val, val)
		}), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).
		Export("rusticated_debug")

	_, err := builder.Instantiate(ctx)
	return err
}

func (h *HostEnv) Poll(ctx context.Context, mod api.Module) {
	// 0. Drain cross-thread callbacks that arrived since the last host call.
	h.drainCallbacks(mod)

	// 1. Drain every completion that is already ready, writing them into guest
	// memory. Track whether we delivered anything.
	delivered := false
	for {
		select {
		case op := <-h.fileOpsQueue:
			op()
			delivered = true
		case state := <-h.pendingSignals:
			h.handleSignal(mod, state)
			delivered = true
		default:
			goto check
		}
	}

check:
	// 2. If we delivered at least one completion, return immediately so the
	// driver loop re-enters the guest to consume it. We must NOT block here just
	// because other ops (e.g. the netpoll deadline timer) remain outstanding ÃƒÆ’Ã†â€™Ãƒâ€ Ã¢â‚¬â„¢ÃƒÆ’Ã¢â‚¬Å¡Ãƒâ€šÃ‚Â¢ÃƒÆ’Ã†â€™Ãƒâ€šÃ‚Â¢ÃƒÆ’Ã‚Â¢ÃƒÂ¢Ã¢â‚¬Å¡Ã‚Â¬Ãƒâ€¦Ã‚Â¡ÃƒÆ’Ã¢â‚¬Å¡Ãƒâ€šÃ‚Â¬ÃƒÆ’Ã†â€™Ãƒâ€šÃ‚Â¢ÃƒÆ’Ã‚Â¢ÃƒÂ¢Ã¢â€šÂ¬Ã…Â¡Ãƒâ€šÃ‚Â¬ÃƒÆ’Ã¢â‚¬Å¡Ãƒâ€šÃ‚Â
	// doing so would starve the guest of the completion we just delivered until
	// that unrelated timer fires. This mirrors the JS host's drain->run->await loop.
	// driver loop re-enters the guest to consume it.
	if delivered {
		return
	}

	// 3. Nothing was ready. If the guest is genuinely waiting on outstanding
	// ops, block until at least one event arrives, then return so the guest can
	// consume it on re-entry.
	if h.HasOutstandingOps() {
		// Periodically log status if we are stuck.
		if time.Since(h.lastLog) > 5*time.Second {
			h.mu.Lock()
			activeCount := len(h.activeOps)
			h.mu.Unlock()
			debugLog("HOST: Poll waiting (pending=%d, active=%d, signals=%d, queue=%d)\n",
				h.PendingOps(), activeCount, len(h.pendingSignals), len(h.fileOpsQueue))
			h.lastLog = time.Now()
		}

		select {
		case op := <-h.fileOpsQueue:
			op()
		case state := <-h.pendingSignals:
			h.handleSignal(mod, state)
		case <-ctx.Done():
			return
		}
	}
}

func (h *HostEnv) handleSignal(mod api.Module, state *OpState) {
	if h.IsOpActive(state.ovPtr, state.opID) {
		h.mu.Lock()
		delete(h.activeOps, state.ovPtr)
		h.mu.Unlock()
		writeOverlapped(mod, state.ovPtr, 0, 0, uint64(state.signum))
	}
	h.DecOpsFor(state)
}

func (h *HostEnv) log(format string, a ...interface{}) {
	debugLog(format+"\n", a...)
}

func (h *HostEnv) wrapFunc(f func(context.Context, api.Module, []uint64)) api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		h.setOwningGID()
		h.drainCallbacks(m)
		f(ctx, m, stack)
		h.drainCallbacks(m)
	})
}

func (h *HostEnv) setOwningGID() {
	h.mu.Lock()
	h.owningGID = getGID()
	h.mu.Unlock()
}

func (h *HostEnv) drainCallbacks(mod api.Module) {
	for {
		select {
		case ev := <-h.callbackQueue:
			h.executeCrossThreadCallback(mod, ev)
		default:
			return
		}
	}
}

func (h *HostEnv) executeCrossThreadCallback(mod api.Module, ev *CallbackEvent) {
	h.mu.Lock()
	cbAny, ok := h.handles[ev.CallbackHandle]
	h.mu.Unlock()
	if !ok {
		ev.RespChan <- 0
		return
	}
	cbState, ok := cbAny.(*CallbackState)
	if !ok {
		ev.RespChan <- 0
		return
	}

	fn := mod.ExportedFunction(cbState.GuestFn)
	if fn == nil {
		fmt.Fprintf(os.Stderr, "washmhost ERROR: guest exported function %q not found in WASM module!\n", cbState.GuestFn)
		ev.RespChan <- 0
		return
	}

	// Marshal host pointers into guest memory so the guest can dereference them.
	wargs := h.marshalCallbackArgs(mod, cbState, ev.Args, ev.BufParams)

	results, err := fn.Call(context.Background(), wargs...)
	if err != nil {
		ev.RespChan <- 0
		return
	}

	var ret uintptr
	if len(results) > 0 {
		ret = uintptr(results[0])
	}
	ev.RespChan <- ret
}

func (h *HostEnv) marshalCallbackArgs(mod api.Module, cb *CallbackState, args []uintptr, bufs [][]byte) []uint64 {
	var wargs []uint64

	// Look up the guest scratch buffer address (cached lazily).
	scratchFn := mod.ExportedFunction("wasmCallbackScratchAddr")
	if scratchFn == nil {
		wargs = make([]uint64, len(args))
		for i, arg := range args {
			wargs[i] = uint64(arg)
		}
		return wargs
	}

	scratchRes, err := scratchFn.Call(context.Background())
	if err != nil || len(scratchRes) == 0 {
		wargs = make([]uint64, len(args))
		for i, arg := range args {
			wargs[i] = uint64(arg)
		}
		return wargs
	}

	scratchBase := uint32(scratchRes[0])
	scratchOffset := uint32(0)
	const scratchSize = 65536

	mem := mod.Memory()

	for i := 0; i < len(args) && i < int(cb.Sig.ArgCount); i++ {
		if i >= len(cb.Sig.ArgTypes) {
			break
		}

		tag := cb.Sig.ArgTypes[i]

		if tag == 0x09 {
			hostPtr := args[i]
			if hostPtr == 0 {
				wargs = append(wargs, 0)
				continue
			}

			// Safely copy NUL-terminated string
			var buf []byte
			for offset := uintptr(0); offset < 1048576; offset++ {
				b := *(*byte)(unsafe.Pointer(hostPtr + offset))
				if b == 0 {
					break
				}
				buf = append(buf, b)
			}

			needed := uint32(len(buf) + 1)
			if scratchOffset+needed > scratchSize {
				wargs = append(wargs, uint64(hostPtr))
				continue
			}

			guestAddr := scratchBase + scratchOffset
			data := make([]byte, needed)
			copy(data, buf)
			data[needed-1] = 0

			mem.Write(guestAddr, data)
			wargs = append(wargs, uint64(guestAddr))
			scratchOffset += needed

		} else if tag == 0x0B {
			// struct LiteRtLmStreamChunk { const char* text; bool is_final; const char* error_msg; }
			structPtr := args[i]
			var strPtr uintptr
			var isFinal uint64

			if structPtr != 0 {
				strPtr = *(*uintptr)(unsafe.Pointer(structPtr))
				isFinal = uint64(*(*byte)(unsafe.Pointer(structPtr + 8)))
			}

			if strPtr == 0 {
				wargs = append(wargs, 0)       // chunk = null
				wargs = append(wargs, isFinal) // isFinal = val
				continue
			}

			var buf []byte
			for offset := uintptr(0); offset < 1048576; offset++ {
				b := *(*byte)(unsafe.Pointer(strPtr + offset))
				if b == 0 {
					break
				}
				buf = append(buf, b)
			}

			needed := uint32(len(buf) + 1)
			if scratchOffset+needed > scratchSize {
				wargs = append(wargs, uint64(strPtr))
				wargs = append(wargs, isFinal)
				continue
			}

			guestAddr := scratchBase + scratchOffset
			data := make([]byte, needed)
			copy(data, buf)
			data[needed-1] = 0

			mem.Write(guestAddr, data)
			wargs = append(wargs, uint64(guestAddr))
			wargs = append(wargs, isFinal)
			scratchOffset += needed
		} else {
			wargs = append(wargs, uint64(args[i]))
		}
	}

	return wargs
}

func getGID() uint64 {
	b := make([]byte, 64)
	b = b[:runtime.Stack(b, false)]
	b = bytes.TrimPrefix(b, []byte("goroutine "))
	i := bytes.IndexByte(b, ' ')
	if i < 0 {
		return 0
	}
	b = b[:i]
	var gid uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0
		}
		gid = gid*10 + uint64(c-'0')
	}
	return gid
}
