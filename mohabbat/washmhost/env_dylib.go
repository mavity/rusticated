package main

import (
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"sync"

	"github.com/tetratelabs/wazero/api"
)

type DylibState struct {
	Handle   uintptr
	Children []uint64
	Mu       sync.Mutex
}

type SymbolState struct {
	Handle uintptr
	Parent uint64
}

type CallbackState struct {
	Trampoline uintptr
	Parent     uint64
	Sig        CallbackSig
	GuestFn    string
	Closure    interface{} // Pins the closure from GC!
}

type CallbackSig struct {
	ArgCount uint8
	RetType  uint8
	ArgTypes []uint8
}

func (cb *CallbackState) Close() {
	// Go purego/NewCallback doesn't support freeing, but we can do cleanups here
}

func (h *HostEnv) closeDylibLocked(libHandle uint64, libState *DylibState) {
	for _, child := range libState.Children {
		if childAny, ok := h.handles[child]; ok {
			if cbState, ok := childAny.(*CallbackState); ok {
				cbState.Close()
			}
			delete(h.handles, child)
		}
	}

	req := DylibRequest{
		Op:        "Close",
		LibHandle: uint64(libState.Handle),
	}
	_, _ = callSatellite(h, req)

	delete(h.handles, libHandle)
}

func (h *HostEnv) sys_dylib_open(ctx context.Context, m api.Module, stack []uint64) {

	ovPtr := uint32(stack[0])
	pathPtr := uint32(stack[1])
	pathLen := uint32(stack[2])
	_ = uint32(stack[3]) // flags reserved

	mem := m.Memory()
	pathBuf, ok := mem.Read(pathPtr, pathLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	path := string(pathBuf)

	// The satellite arch is decided by the DLL being opened (first open wins).
	if tgt, derr := detectDylibTarget(path); derr == nil {
		h.mu.Lock()
		if h.satTargetGOOS == "" {
			h.satTargetGOOS, h.satTargetGOARCH = tgt.goos, tgt.goarch
		}
		mismatch := h.satTargetGOOS != tgt.goos || h.satTargetGOARCH != tgt.goarch
		h.mu.Unlock()
		if mismatch {
			writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
			return
		}
	}

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		req := DylibRequest{
			Op:   "Open",
			Path: path,
		}

		retCode := uint32(0)
		var libHandle uint64

		resp, err := callSatellite(h, req)
		if err != nil {
			fmt.Fprintf(os.Stderr, "satellite spawn error: %v\n", err)
			retCode = wasiEIO
		} else if resp.ErrCode != 0 {
			retCode = resp.ErrCode
		} else {
			h.mu.Lock()
			libHandle = h.nextHandle
			h.nextHandle++
			h.handles[libHandle] = &DylibState{
				Handle:   uintptr(resp.Handle),
				Children: []uint64{},
			}
			h.mu.Unlock()
		}

		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()
			writeOverlapped(m, ovPtr, retCode, 0, libHandle)
		}
	}()
}

func (h *HostEnv) sys_dylib_sym(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	namePtr := uint32(stack[2])
	nameLen := uint32(stack[3])

	mem := m.Memory()
	nameBuf, ok := mem.Read(namePtr, nameLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	name := string(nameBuf)

	h.mu.Lock()
	libAny, ok := h.handles[libHandle]
	h.mu.Unlock()
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	libState, ok := libAny.(*DylibState)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}

	req := DylibRequest{
		Op:        "Sym",
		LibHandle: uint64(libState.Handle),
		Name:      name,
	}
	resp, err := callSatellite(h, req)
	if err != nil || resp.ErrCode != 0 {
		ret := wasiEIO
		if resp.ErrCode != 0 {
			ret = resp.ErrCode
		}
		writeOverlapped(m, ovPtr, ret, 0, 0)
		return
	}
	sym := uintptr(resp.Handle)

	h.mu.Lock()
	symHandle := h.nextHandle
	h.nextHandle++
	h.handles[symHandle] = &SymbolState{
		Handle: sym,
		Parent: libHandle,
	}
	libState.Children = append(libState.Children, symHandle)
	h.mu.Unlock()

	writeOverlapped(m, ovPtr, 0, 0, symHandle)
}

func (h *HostEnv) sys_dylib_close(ctx context.Context, m api.Module, stack []uint64) {
	libHandle := stack[0]

	h.mu.Lock()
	defer h.mu.Unlock()
	if libAny, ok := h.handles[libHandle]; ok {
		if libState, ok := libAny.(*DylibState); ok {
			h.closeDylibLocked(libHandle, libState)
		}
	}
}

type BufArg struct {
	gPtr    uint32
	gLen    uint32
	hostBuf []byte
}

func (h *HostEnv) sys_dylib_call(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	symHandle := stack[1]
	descPtr := uint32(stack[2])
	descLen := uint32(stack[3])

	mem := m.Memory()
	descBuf, ok := mem.Read(descPtr, descLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEFAULT, 0, 0)
		return
	}

	h.mu.Lock()
	symAny, ok := h.handles[symHandle]
	h.mu.Unlock()
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	symState, ok := symAny.(*SymbolState)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}

	if len(descBuf) < 4 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	_ = descBuf[0] // cc (0 = cdecl, 1 = stdcall) - ignored as stdcall is cdecl on non-windows
	argCount := descBuf[1]
	_ = descBuf[2] // retType
	_ = descBuf[3] // reserved

	if argCount > 15 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}

	offset := 4
	var bufArgs []BufArg
	var bufParams [][]byte

	for i := byte(0); i < argCount; i++ {
		if offset >= len(descBuf) {
			writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
			return
		}
		tag := descBuf[offset]
		offset++

		switch tag {
		case 0x01, 0x03, 0x05: // i32, u32, f32
			offset += 4
		case 0x02, 0x04, 0x06, 0x07, 0x0A: // i64, u64, f64, ptr, cb
			offset += 8
		case 0x08: // buf: (guest_ptr, len)
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			gPtr := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
			gLen := binary.LittleEndian.Uint32(descBuf[offset+4 : offset+8])
			offset += 8

			gBuf, ok := mem.Read(gPtr, gLen)
			if !ok {
				writeOverlapped(m, ovPtr, wasiEFAULT, 0, 0)
				return
			}
			hostBuf := make([]byte, gLen)
			copy(hostBuf, gBuf)
			bufParams = append(bufParams, hostBuf)
			bufArgs = append(bufArgs, BufArg{gPtr: gPtr, gLen: gLen, hostBuf: hostBuf})

		case 0x09: // cstr: (guest_ptr, len)
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			gPtr := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
			gLen := binary.LittleEndian.Uint32(descBuf[offset+4 : offset+8])
			offset += 8

			gBuf, ok := mem.Read(gPtr, gLen)
			if !ok {
				writeOverlapped(m, ovPtr, wasiEFAULT, 0, 0)
				return
			}
			hostBuf := make([]byte, gLen+1)
			copy(hostBuf, gBuf)
			hostBuf[gLen] = 0
			bufParams = append(bufParams, hostBuf)

		default:
			writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
			return
		}
	}

	// A reentrant call is issued by the guest while it is servicing a native
	// callback (the guest callback runs nested on this owning goroutine). It must
	// execute on the satellite thread parked in that callback's C trampoline, so
	// we service it synchronously here — staying interruptible for any deeper
	// callbacks the native call triggers — and pre-complete the overlapped inline
	// so the guest never parks mid-callback.
	if h.currentReentrant() {
		respCh, err := sendSatellite(h, DylibRequest{
			Op:        "Call",
			SymHandle: uint64(symState.Handle),
			DescBuf:   descBuf,
			BufParams: bufParams,
			Reentrant: true,
		})
		if err != nil {
			writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
			return
		}
		var resp DylibResponse
	awaitReentrant:
		for {
			select {
			case resp = <-respCh:
				break awaitReentrant
			case ev := <-h.callbackQueue:
				h.executeCrossThreadCallback(m, ev)
			}
		}
		if resp.ErrCode != 0 {
			writeOverlapped(m, ovPtr, resp.ErrCode, 0, 0)
			return
		}
		bufIdx := 0
		for _, ba := range bufArgs {
			if ba.gLen > 0 && bufIdx < len(resp.BufParams) {
				mem.Write(ba.gPtr, resp.BufParams[bufIdx])
			}
			bufIdx++
		}
		writeOverlapped(m, ovPtr, 0, 0, uint64(uintptr(resp.Result)))
		return
	}

	state := h.RegisterOp(ovPtr, nil)

	go func() {
		req := DylibRequest{
			Op:        "Call",
			SymHandle: uint64(symState.Handle),
			DescBuf:   descBuf,
			BufParams: bufParams,
		}

		resp, err := callSatellite(h, req)

		if err != nil {
			h.fileOpsQueue <- func() {
				defer h.DecOpsFor(state)
				if !h.IsOpActive(ovPtr, state.opID) {
					return
				}
				h.mu.Lock()
				delete(h.activeOps, ovPtr)
				h.mu.Unlock()
				writeOverlapped(m, ovPtr, wasiEIO, 0, 0)
			}
			return
		}

		r1 := uintptr(resp.Result)

		// The native call returned; that is this operation's completion. This
		// marshalling layer forwards the result verbatim and never inspects call
		// semantics — any per-call orchestration belongs to the guest and the DLL.
		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				return
			}
			h.mu.Lock()
			delete(h.activeOps, ovPtr)
			h.mu.Unlock()

			if resp.ErrCode != 0 {
				writeOverlapped(m, ovPtr, resp.ErrCode, 0, 0)
				return
			}

			// Copy-out buffers
			bufIdx := 0
			for _, ba := range bufArgs {
				if ba.gLen > 0 {
					if bufIdx < len(resp.BufParams) {
						mem.Write(ba.gPtr, resp.BufParams[bufIdx])
					}
				}
				bufIdx++
			}

			writeOverlapped(m, ovPtr, 0, 0, uint64(r1))
		}
	}()
}

func (h *HostEnv) sys_dylib_callback_create(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	libHandle := stack[1]
	sigPtr := uint32(stack[2])
	sigLen := uint32(stack[3])
	guestFnNamePtr := uint32(stack[4])
	guestFnNameLen := uint32(stack[5])

	mem := m.Memory()
	sigBuf, ok := mem.Read(sigPtr, sigLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEFAULT, 0, 0)
		return
	}
	if len(sigBuf) < 2 {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	argCount := sigBuf[0]
	retType := sigBuf[1]
	if len(sigBuf) < 2+int(argCount) {
		writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
		return
	}
	argTypes := make([]byte, argCount)
	copy(argTypes, sigBuf[2:2+argCount])

	nameBuf, ok := mem.Read(guestFnNamePtr, guestFnNameLen)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEFAULT, 0, 0)
		return
	}
	guestFnName := string(nameBuf)

	h.mu.Lock()
	libAny, ok := h.handles[libHandle]
	h.mu.Unlock()
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}
	libState, ok := libAny.(*DylibState)
	if !ok {
		writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
		return
	}

	h.mu.Lock()
	cbHandle := h.nextHandle
	h.nextHandle++
	h.mu.Unlock()

	sig := CallbackSig{
		ArgCount: argCount,
		RetType:  retType,
		ArgTypes: argTypes,
	}

	req := DylibRequest{
		Op:          "CallbackCreate",
		CbHandle:    cbHandle,
		ArgCount:    argCount,
		RetType:     retType,
		ArgTypes:    argTypes,
		GuestFnName: guestFnName,
	}
	resp, err := callSatellite(h, req)
	if err != nil || resp.ErrCode != 0 {
		h.mu.Lock()
		delete(h.handles, cbHandle)
		h.nextHandle-- // Optional
		h.mu.Unlock()
		writeOverlapped(m, ovPtr, wasiENOSYS, 0, 0)
		return
	}

	h.mu.Lock()
	cbState := &CallbackState{
		Parent:  libHandle,
		Sig:     sig,
		GuestFn: guestFnName,
	}
	h.handles[cbHandle] = cbState
	libState.Children = append(libState.Children, cbHandle)
	h.mu.Unlock()

	writeOverlapped(m, ovPtr, 0, 0, cbHandle)
}

func (h *HostEnv) sys_dylib_callback_respond(ctx context.Context, m api.Module, stack []uint64) {
	cbHandle := stack[0]
	invocationId := uint32(stack[1])
	retValue := int64(stack[2])
	_ = cbHandle

	// The guest callback has finished running; leave reentrant mode so later
	// dylib reads route to the worker thread again.
	h.callbackDepth = 0
	h.resolveInvocation(invocationId, uintptr(retValue))
}

// resolveInvocation unblocks the satellite goroutine waiting on a callback's
// return value.
func (h *HostEnv) resolveInvocation(invocID uint32, ret uintptr) {
	h.mu.Lock()
	ch, ok := h.activeInvocations[invocID]
	if ok {
		delete(h.activeInvocations, invocID)
	}
	h.mu.Unlock()
	if ok {
		ch <- ret
	}
}

func makeCallbackClosure(h *HostEnv, m api.Module, cbHandle uint64, sig CallbackSig, guestFnName string) interface{} {
	switch sig.ArgCount {
	case 0:
		return func() uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{})
		}
	case 1:
		return func(a1 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1})
		}
	case 2:
		return func(a1, a2 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2})
		}
	case 3:
		return func(a1, a2, a3 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2, a3})
		}
	case 4:
		return func(a1, a2, a3, a4 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2, a3, a4})
		}
	case 5:
		return func(a1, a2, a3, a4, a5 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2, a3, a4, a5})
		}
	case 6:
		return func(a1, a2, a3, a4, a5, a6 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2, a3, a4, a5, a6})
		}
	case 7:
		return func(a1, a2, a3, a4, a5, a6, a7 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2, a3, a4, a5, a6, a7})
		}
	case 8:
		return func(a1, a2, a3, a4, a5, a6, a7, a8 uintptr) uintptr {
			return h.invokeCallback(m, cbHandle, sig, guestFnName, []uintptr{a1, a2, a3, a4, a5, a6, a7, a8})
		}
	}
	return nil
}

// currentReentrant reports whether the owning goroutine is presently nested in a
// native callback, so a dylib call it issues must be routed to that callback's
// parked satellite thread.
func (h *HostEnv) currentReentrant() bool {
	return h.callbackDepth > 0
}

func (h *HostEnv) invokeCallback(m api.Module, cbHandle uint64, sig CallbackSig, guestFnName string, args []uintptr) uintptr {
	gid := getGID()
	h.mu.Lock()
	sameThread := (gid == h.owningGID)
	h.mu.Unlock()

	// Look up the CallbackState to get the signature for marshalling.
	h.mu.Lock()
	cbAny, cbOk := h.handles[cbHandle]
	h.mu.Unlock()
	var cbState *CallbackState
	if cbOk {
		cbState, _ = cbAny.(*CallbackState)
	}

	var ret uintptr
	if sameThread {
		fn := m.ExportedFunction(guestFnName)
		if fn == nil {
			return 0
		}

		// Marshal host pointers into guest memory.
		var wargs []uint64
		if cbState != nil {
			wargs = h.marshalCallbackArgs(m, cbState, args, nil)
		} else {
			wargs = make([]uint64, len(args))
			for i, arg := range args {
				wargs[i] = uint64(arg)
			}
		}

		results, err := fn.Call(context.Background(), wargs...)
		if err != nil {
			return 0
		}

		if len(results) > 0 {
			ret = uintptr(results[0])
		}
	} else {
		respChan := make(chan uintptr, 1)
		ev := &CallbackEvent{
			CallbackHandle: cbHandle,
			Args:           args,
			RespChan:       respChan,
		}
		h.callbackQueue <- ev
		h.fileOpsQueue <- func() {}

		ret = <-respChan
	}

	return ret
}

func (h *HostEnv) sys_dylib_read_cstr(ctx context.Context, m api.Module, stack []uint64) {
	hostPtr := stack[0]
	guestBufPtr := uint32(stack[1])
	maxLen := uint32(stack[2])

	if hostPtr == 0 {
		stack[0] = 0
		return
	}

	req := DylibRequest{
		Op:        "ReadCstr",
		Ptr:       hostPtr, // Notice hostPtr here is remote satellite pointer
		MaxLen:    maxLen,
		Reentrant: h.currentReentrant(),
	}
	resp, err := callSatellite(h, req)
	if err != nil {
		stack[0] = 0
		return
	}

	buf := resp.BytesRes
	mem := m.Memory()
	if len(buf) > 0 {
		mem.Write(guestBufPtr, buf)
	}
	stack[0] = uint64(len(buf))
}

func (h *HostEnv) sys_dylib_read_mem(ctx context.Context, m api.Module, stack []uint64) {
	hostPtr := stack[0]
	guestBufPtr := uint32(stack[1])
	length := uint32(stack[2])

	if hostPtr == 0 || length == 0 {
		stack[0] = 0
		return
	}

	req := DylibRequest{
		Op:        "ReadMem",
		Ptr:       hostPtr,
		MaxLen:    length,
		Reentrant: h.currentReentrant(),
	}
	resp, err := callSatellite(h, req)
	if err != nil {
		stack[0] = 0
		return
	}

	buf := resp.BytesRes
	mem := m.Memory()
	if len(buf) > 0 {
		mem.Write(guestBufPtr, buf)
	}
	stack[0] = uint64(len(buf))
}

func (h *HostEnv) invokeCallbackFromSatellite(cbHandle uint64, args []uint64, bufs [][]byte) uintptr {
	h.mu.Lock()
	cbAny, cbOk := h.handles[cbHandle]
	h.mu.Unlock()

	if !cbOk {
		return 0
	}

	if _, ok := cbAny.(*CallbackState); !ok {
		return 0
	}

	// Allocate an invocation id + response channel. The guest pump goroutine
	// runs the callback on its own (growable) stack and replies via
	// dylib_callback_respond, so the callback body never executes on the g0
	// scheduler stack (which cannot grow) — the cause of morestack aborts.
	respChan := make(chan uintptr, 1)
	h.mu.Lock()
	h.nextInvocationID++
	invocID := h.nextInvocationID
	h.activeInvocations[invocID] = respChan
	h.mu.Unlock()

	pcb := &pendingCallback{invocID: invocID, cbHandle: cbHandle, args: args}

	h.cbMu.Lock()
	w := h.cbWaiter
	if w != nil {
		h.cbWaiter = nil
	} else {
		h.cbPending = pcb
	}
	h.cbMu.Unlock()

	if w != nil {
		// A guest pump is parked: complete its wait on the module thread.
		h.fileOpsQueue <- func() { h.deliverToWaiter(w, pcb) }
	} else {
		// No parked pump yet: leave the callback queued and wake the driver
		// loop so the guest is scheduled to call dylib_callback_wait.
		h.fileOpsQueue <- func() {}
	}

	return <-respChan
}

// deliverToWaiter completes a parked guest pump's wait with a queued callback.
// Runs on the module thread (via fileOpsQueue).
func (h *HostEnv) deliverToWaiter(w *callbackWaiter, pcb *pendingCallback) {
	if !h.IsOpActive(w.ovPtr, w.state.opID) {
		// The wait was cancelled (stream ended); unblock the satellite.
		h.resolveInvocation(pcb.invocID, 0)
		return
	}
	h.mu.Lock()
	delete(h.activeOps, w.ovPtr)
	h.mu.Unlock()
	h.writeCallbackDelivery(w.mod, w.ovPtr, w.outPtr, w.outLen, pcb)
	h.DecOpsFor(w.state)
}

// writeCallbackDelivery hands a pending callback to the guest pump by writing
// its scalar args into the guest buffer and completing the overlapped. It raises
// callbackDepth so dylib reads issued by the callback route reentrant to the
// satellite's parked callback thread; sys_dylib_callback_respond lowers it.
func (h *HostEnv) writeCallbackDelivery(mod api.Module, ovPtr, outPtr, outLen uint32, pcb *pendingCallback) {
	n := len(pcb.args)
	if uint32(n*8) > outLen {
		n = int(outLen / 8)
	}
	if n > 0 {
		buf := make([]byte, n*8)
		for i := 0; i < n; i++ {
			binary.LittleEndian.PutUint64(buf[i*8:], pcb.args[i])
		}
		mod.Memory().Write(outPtr, buf)
	}
	h.callbackDepth = 1
	continued := uint64(pcb.invocID) | (uint64(n) << 32)
	writeOverlapped(mod, ovPtr, 0, continued, pcb.cbHandle)
}

// sys_dylib_callback_wait blocks the calling guest goroutine until a native
// callback is available, then returns its handle, invocation id and scalar args
// (cbHandle in resultExt, invocId|argCount<<32 in continued, args in the buffer).
func (h *HostEnv) sys_dylib_callback_wait(ctx context.Context, m api.Module, stack []uint64) {
	ovPtr := uint32(stack[0])
	outPtr := uint32(stack[1])
	outLen := uint32(stack[2])

	h.cbMu.Lock()
	if pcb := h.cbPending; pcb != nil {
		h.cbPending = nil
		h.cbMu.Unlock()
		h.writeCallbackDelivery(m, ovPtr, outPtr, outLen, pcb)
		return
	}
	state := h.RegisterOp(ovPtr, nil)
	h.cbWaiter = &callbackWaiter{ovPtr: ovPtr, outPtr: outPtr, outLen: outLen, mod: m, state: state}
	h.cbMu.Unlock()
	// Return without completing; the guest goparks until deliverToWaiter or
	// sys_dylib_callback_stop completes this overlapped.
}

// sys_dylib_callback_stop cancels a parked pump wait so the guest pump loop can
// exit once the stream is finished.
func (h *HostEnv) sys_dylib_callback_stop(ctx context.Context, m api.Module, stack []uint64) {
	h.callbackDepth = 0
	h.cbMu.Lock()
	w := h.cbWaiter
	h.cbWaiter = nil
	h.cbMu.Unlock()
	if w == nil {
		return
	}
	if h.IsOpActive(w.ovPtr, w.state.opID) {
		h.mu.Lock()
		delete(h.activeOps, w.ovPtr)
		h.mu.Unlock()
		writeOverlapped(w.mod, w.ovPtr, wasiECANCELED, 0, 0)
		h.DecOpsFor(w.state)
	}
}
