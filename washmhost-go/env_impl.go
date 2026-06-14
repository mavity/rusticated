package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"golang.org/x/term"
)

type HostEnv struct {
	mu             sync.Mutex
	activeOps      map[uint32]*OpState
	nextOpID       uint64
	handles        map[uint64]interface{}
	nextHandle     uint64
	outstandingOps int32
	fileOpsQueue   chan func()
	ttyRawState    *term.State
	ttyRawFd       int
	signals        chan os.Signal
	signalWaiters  map[uint32]*OpState // signum -> state
	pendingSignals chan *OpState       // state to complete
	timers         map[uint32]*time.Timer
	lastLog        time.Time
}

type OpState struct {
	ovPtr       uint32
	opID        uint64
	handle      interface{}
	deadline    time.Time
	signum      uint32
	isCancelled bool
}

func NewHostEnv() *HostEnv {
	env := &HostEnv{
		timers:         make(map[uint32]*time.Timer),
		activeOps:      make(map[uint32]*OpState),
		handles:        make(map[uint64]interface{}),
		nextHandle:     3, // 0,1,2 reserved
		outstandingOps: 0,
		fileOpsQueue:   make(chan func(), 1000),
		signals:        make(chan os.Signal, 10),
		signalWaiters:  make(map[uint32]*OpState),
		pendingSignals: make(chan *OpState, 100),
	}
	env.handles[0] = os.Stdin
	env.handles[1] = os.Stdout
	env.handles[2] = os.Stderr

	signal.Notify(env.signals, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		for sig := range env.signals {
			var signum uint32
			switch sig {
			case syscall.SIGINT:
				signum = 2
			case syscall.SIGTERM:
				signum = 15
			}
			if signum != 0 {
				env.mu.Lock()
				state, ok := env.signalWaiters[signum]
				if ok {
					delete(env.signalWaiters, signum)
					env.mu.Unlock()
					env.pendingSignals <- state
					env.fileOpsQueue <- func() {}
				} else {
					env.mu.Unlock()
				}
			}
		}
	}()

	return env
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
	return ok && current.opID == id
}

func (h *HostEnv) DecOps() {
	for {
		old := atomic.LoadInt32(&h.outstandingOps)
		if old <= 0 {
			return
		}
		if atomic.CompareAndSwapInt32(&h.outstandingOps, old, old-1) {
			return
		}
	}
}

func (h *HostEnv) PendingOps() int32 {
	return atomic.LoadInt32(&h.outstandingOps)
}

func (h *HostEnv) HasOutstandingOps() bool {
	return atomic.LoadInt32(&h.outstandingOps) > 0
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
			h.DecOps()
			return
		}
	}

	state, ok := h.activeOps[ovPtr]
	if ok && !state.isCancelled {
		state.isCancelled = true
		wasSignal := false
		if state.signum != 0 {
			delete(h.signalWaiters, state.signum)
			wasSignal = true
		}
		h.mu.Unlock()
		fmt.Printf("HOST: cancel(0x%x) id=%d handle=%T\n", ovPtr, state.opID, state.handle)
		if state.handle != nil {
			if c, ok := state.handle.(interface{ SetDeadline(time.Time) error }); ok {
				_ = c.SetDeadline(pastTime)
			}
		}
		if wasSignal {
			h.DecOps()
		}
	} else {
		h.mu.Unlock()
	}
}

func (h *HostEnv) Register(ctx context.Context, r wazero.Runtime) error {
	builder := r.NewHostModuleBuilder("env")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_panic), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("host_panic")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_get_time), []api.ValueType{}, []api.ValueType{api.ValueTypeI64}).Export("get_time")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_get_random), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("get_random")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_get_args), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("get_args")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_get_env), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("get_env")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_get_cwd), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI64}).Export("get_cwd")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_set_cwd), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("set_cwd")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_timer_set), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("timer_set")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_read), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("read")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_write), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("write")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_handle_close), []api.ValueType{api.ValueTypeI64}, []api.ValueType{}).Export("handle_close")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_path_open), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_open")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_dir_read), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dir_read")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_path_stat), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_stat")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_path_chmod), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("path_chmod")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_net_open), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("net_open")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_net_accept), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).Export("net_accept")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_process_spawn), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("process_spawn")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_process_wait), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).Export("process_wait")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_process_signal), []api.ValueType{api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).Export("process_signal")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_signal_wait), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("signal_wait")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_cancel), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).Export("cancel")
	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_get_platform_info), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{api.ValueTypeI32}).Export("get_platform_info")

	builder.NewFunctionBuilder().WithGoModuleFunction(api.GoModuleFunc(h.sys_process_exit), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).Export("process_exit")

	builder.NewFunctionBuilder().
		WithGoModuleFunction(api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
			val := int32(stack[0])
			fmt.Printf("GUEST DEBUG: %d (0x%x)\n", val, val)
		}), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).
		Export("rusticated_debug")

	_, err := builder.Instantiate(ctx)
	return err
}

func (h *HostEnv) Poll(ctx context.Context, mod api.Module) bool {
	processed := false
	for {
		select {
		case op := <-h.fileOpsQueue:
			op()
			processed = true
		default:
			goto signals
		}
	}

signals:
	for {
		select {
		case state := <-h.pendingSignals:
			if h.IsOpActive(state.ovPtr, state.opID) {
				h.mu.Lock()
				delete(h.activeOps, state.ovPtr)
				h.mu.Unlock()
				writeOverlapped(mod, state.ovPtr, 0, 0, uint64(state.signum))
			}
			h.DecOps()
			processed = true
		default:
			goto block
		}
	}

block:
	if processed {
		return true
	}

	if h.PendingOps() == 0 {
		return false
	}

	select {
	case op := <-h.fileOpsQueue:
		op()
		for {
			select {
			case op := <-h.fileOpsQueue:
				op()
			default:
				return true
			}
		}
	case state := <-h.pendingSignals:
		if h.IsOpActive(state.ovPtr, state.opID) {
			h.mu.Lock()
			delete(h.activeOps, state.ovPtr)
			h.mu.Unlock()
			writeOverlapped(mod, state.ovPtr, 0, 0, uint64(state.signum))
		}
		h.DecOps()
		return true
	case <-ctx.Done():
		return false
	}
}
