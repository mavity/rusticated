package main

import (
	"bytes"
	"context"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"golang.org/x/term"
)

const sigwinch = syscall.Signal(0x1c) // SIGWINCH (28)

type dylibTargetKey struct {
	goos   string
	goarch string
}

type satelliteEntryState int

const (
	satelliteEntryIdle satelliteEntryState = iota
	satelliteEntryStarting
	satelliteEntryReady
	satelliteEntryFailed
)

type satelliteEntry struct {
	target   dylibTargetKey
	manager  *DylibHostManager
	state    satelliteEntryState
	readyCh  chan struct{}
	startErr error
}

type DylibSatelliteRegistry struct {
	mu      sync.Mutex
	hEnv    *HostEnv
	entries map[dylibTargetKey]*satelliteEntry
}

type dylibLibraryRef struct {
	manager *DylibHostManager
	remote  uint64
	target  dylibTargetKey
}

type dylibSymbolRef struct {
	manager   *DylibHostManager
	remote    uint64
	libraryID uint64
}

func NewDylibSatelliteRegistry(hEnv *HostEnv) *DylibSatelliteRegistry {
	return &DylibSatelliteRegistry{
		hEnv:    hEnv,
		entries: make(map[dylibTargetKey]*satelliteEntry),
	}
}

func (r *DylibSatelliteRegistry) Close() {
	r.mu.Lock()
	entries := make([]*satelliteEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	r.mu.Unlock()

	for _, entry := range entries {
		if entry != nil && entry.manager != nil {
			entry.manager.Close()
		}
	}
}

func (r *DylibSatelliteRegistry) HasActiveWork() bool {
	r.mu.Lock()
	entries := make([]*satelliteEntry, 0, len(r.entries))
	for _, entry := range r.entries {
		entries = append(entries, entry)
	}
	r.mu.Unlock()

	for _, entry := range entries {
		if entry != nil && entry.manager != nil && entry.manager.HasActiveWork() {
			return true
		}
	}
	return false
}

func (r *DylibSatelliteRegistry) ReadyManagers() []*DylibHostManager {
	r.mu.Lock()
	defer r.mu.Unlock()

	managers := make([]*DylibHostManager, 0, len(r.entries))
	for _, entry := range r.entries {
		if entry == nil || entry.state != satelliteEntryReady || entry.manager == nil {
			continue
		}
		managers = append(managers, entry.manager)
	}
	return managers
}

func (r *DylibSatelliteRegistry) SetReadyManager(target dylibTargetKey, manager *DylibHostManager) {
	r.mu.Lock()
	defer r.mu.Unlock()
	manager.target = target
	entry := &satelliteEntry{
		target:  target,
		manager: manager,
		state:   satelliteEntryReady,
		readyCh: make(chan struct{}),
	}
	close(entry.readyCh)
	r.entries[target] = entry
}

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
	forcedExitCode int32
	args           []string
	owningGID      uint64
	mod            api.Module
	dylibRegistry  *DylibSatelliteRegistry
	dylibMgr       *DylibHostManager
	nextDylibID    uint64
	dylibLibraries map[uint64]dylibLibraryRef
	dylibSymbols   map[uint64]dylibSymbolRef
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

func NewHostEnv() *HostEnv {
	env := &HostEnv{

		activeOps:      make(map[uint32]*OpState),
		handles:        make(map[uint64]interface{}),
		nextHandle:     3, // 0,1,2 reserved
		outstandingOps: 0,
		fileOpsQueue:   make(chan func(), 1000),
		signals:        make(chan os.Signal, 10),
		signalWaiters:  make(map[uint32]*OpState),
		pendingSignals: make(chan *OpState, 100),
		timers:         make(map[uint32]*time.Timer),
		forcedExitCode: -1,
		nextDylibID:    1,
		dylibLibraries: make(map[uint64]dylibLibraryRef),
		dylibSymbols:   make(map[uint64]dylibSymbolRef),
	}
	env.dylibRegistry = NewDylibSatelliteRegistry(env)
	env.dylibMgr = NewDylibHostManager(env)
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
	if h.dylibMgr != nil {
		h.dylibMgr.Close()
	}
	if h.dylibRegistry != nil {
		h.dylibRegistry.Close()
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
	if atomic.LoadInt32(&h.outstandingOps) > 0 {
		return true
	}
	if h.dylibRegistry != nil && h.dylibRegistry.HasActiveWork() {
		return true
	}
	if h.dylibMgr != nil && h.dylibMgr.HasActiveWork() {
		return true
	}
	return false
}

func (h *HostEnv) HasActiveOps() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	if atomic.LoadInt32(&h.outstandingOps) > 0 {
		return true
	}
	if len(h.fileOpsQueue) > 0 {
		return true
	}
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
	if h.dylibRegistry != nil && h.dylibRegistry.HasActiveWork() {
		return true
	}
	if h.dylibMgr != nil && h.dylibMgr.HasActiveWork() {
		return true
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

func (h *HostEnv) DylibManagersForPolling() []*DylibHostManager {
	h.mu.Lock()
	registry := h.dylibRegistry
	legacy := h.dylibMgr
	h.mu.Unlock()

	if registry != nil {
		managers := registry.ReadyManagers()
		if len(managers) > 0 {
			return managers
		}
	}
	if legacy != nil {
		return []*DylibHostManager{legacy}
	}
	return nil
}

func (h *HostEnv) registerDylibLibrary(manager *DylibHostManager, remote uint64, target dylibTargetKey) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	id := h.nextDylibID
	h.nextDylibID++
	h.dylibLibraries[id] = dylibLibraryRef{manager: manager, remote: remote, target: target}
	return id
}

func (h *HostEnv) lookupDylibLibrary(id uint64) (dylibLibraryRef, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, ok := h.dylibLibraries[id]
	return ref, ok
}

func (h *HostEnv) deleteDylibLibrary(id uint64) {
	h.mu.Lock()
	delete(h.dylibLibraries, id)
	h.mu.Unlock()
}

func (h *HostEnv) registerDylibSymbol(manager *DylibHostManager, libraryID uint64, remote uint64) uint64 {
	h.mu.Lock()
	defer h.mu.Unlock()
	if existing, ok := h.dylibSymbols[remote]; ok {
		if existing.manager != manager || existing.libraryID != libraryID || existing.remote != remote {
			h.dylibSymbols[remote] = dylibSymbolRef{}
			return remote
		}
		return remote
	}
	h.dylibSymbols[remote] = dylibSymbolRef{manager: manager, remote: remote, libraryID: libraryID}
	return remote
}

func (h *HostEnv) lookupDylibSymbol(id uint64) (dylibSymbolRef, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	ref, ok := h.dylibSymbols[id]
	return ref, ok
}

func (h *HostEnv) deleteDylibSymbol(id uint64) {
	h.mu.Lock()
	delete(h.dylibSymbols, id)
	h.mu.Unlock()
}

func (h *HostEnv) deleteDylibResourcesForManager(manager *DylibHostManager) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for id, ref := range h.dylibLibraries {
		if ref.manager == manager {
			delete(h.dylibLibraries, id)
		}
	}
	for id, ref := range h.dylibSymbols {
		if ref.manager == manager {
			delete(h.dylibSymbols, id)
		}
	}
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

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_open), []api.ValueType{api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_open")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_sym), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_sym")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_close), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64}, []api.ValueType{}).Export("dylib_close")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_call), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_call")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_alloc), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI64, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_alloc")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_free), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI64}, []api.ValueType{}).Export("dylib_free")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_read_mem), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_read_mem")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_write_mem), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_write_mem")
	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_dylib_callback_register), []api.ValueType{api.ValueTypeI32, api.ValueTypeI64, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32, api.ValueTypeI32}, []api.ValueType{}).Export("dylib_callback_register")

	builder.NewFunctionBuilder().WithGoModuleFunction(h.wrapFunc(h.sys_process_exit), []api.ValueType{api.ValueTypeI32}, []api.ValueType{}).Export("process_exit")

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

// wrapFunc wraps a host import handler. It must NOT call drainCallbacks here:
// host imports may be invoked by the Go scheduler on g0 (e.g. nanotime1 from
// findRunnable). Calling fn.Call() to deliver a callback would re-enter the
// module on g0's fixed-size stack, crashing with "morestack on g0".
//
// Callbacks are instead drained in Poll, which runs between module executions
// when the guest is paused on handleAsyncEvent — a user goroutine with a
// growable stack — so fn.Call() re-enters the module safely.
func (h *HostEnv) wrapFunc(f func(context.Context, api.Module, []uint64)) api.GoModuleFunction {
	return api.GoModuleFunc(func(ctx context.Context, m api.Module, stack []uint64) {
		h.setOwningGID()
		f(ctx, m, stack)
	})
}

func (h *HostEnv) setOwningGID() {
	h.mu.Lock()
	h.owningGID = getGID()
	h.mu.Unlock()
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
