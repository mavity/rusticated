package main

import (
	"context"
	"encoding/binary"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
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

	_ = dlclose(libState.Handle)
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

	state := h.RegisterOp(ovPtr, nil)
	go func() {
		lib, err := dlopen(path, 0x2|0x8) // RTLD_NOW | RTLD_GLOBAL
		retCode := uint32(0)
		var libHandle uint64
		if err != nil {
			retCode = mapErrno(err)
		} else {
			// Mute any underlying glog/Abseil logging globally in the loaded library's memory space
			if sym, err := dlsym(lib, "FLAGS_minloglevel"); err == nil && sym != 0 {
				*(*int32)(unsafe.Pointer(sym)) = 2 // 2 = ERROR
			}
			if sym, err := dlsym(lib, "FLAGS_logtostderr"); err == nil && sym != 0 {
				*(*bool)(unsafe.Pointer(sym)) = false
			}
			if sym, err := dlsym(lib, "FLAGS_alsologtostderr"); err == nil && sym != 0 {
				*(*bool)(unsafe.Pointer(sym)) = false
			}

			h.mu.Lock()
			libHandle = h.nextHandle
			h.nextHandle++
			h.handles[libHandle] = &DylibState{
				Handle:   lib,
				Children: []uint64{},
			}
			h.mu.Unlock()
		}

		h.fileOpsQueue <- func() {
			defer h.DecOpsFor(state)
			if !h.IsOpActive(ovPtr, state.opID) {
				if err == nil {
					_ = dlclose(lib)
				}
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

	sym, err := dlsym(libState.Handle, name)
	if err != nil || sym == 0 {
		writeOverlapped(m, ovPtr, mapErrno(err), 0, 0)
		return
	}

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
	var puregoArgs []uintptr
	var pinReferences []interface{}
	var bufArgs []BufArg
	hasCallback := false

	for i := byte(0); i < argCount; i++ {
		if offset >= len(descBuf) {
			writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
			return
		}
		tag := descBuf[offset]
		offset++

		switch tag {
		case 0x01: // i32
			if offset+4 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			val := int32(binary.LittleEndian.Uint32(descBuf[offset : offset+4]))
			puregoArgs = append(puregoArgs, uintptr(val))
			offset += 4

		case 0x02: // i64
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			val := int64(binary.LittleEndian.Uint64(descBuf[offset : offset+8]))
			puregoArgs = append(puregoArgs, uintptr(val))
			offset += 8

		case 0x03: // u32
			if offset+4 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			val := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
			puregoArgs = append(puregoArgs, uintptr(val))
			offset += 4

		case 0x04: // u64
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			val := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
			puregoArgs = append(puregoArgs, uintptr(val))
			offset += 8

		case 0x05: // f32
			if offset+4 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			valBits := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
			puregoArgs = append(puregoArgs, uintptr(valBits))
			offset += 4

		case 0x06: // f64
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			valBits := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
			puregoArgs = append(puregoArgs, uintptr(valBits))
			offset += 8

		case 0x07: // ptr
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			val := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
			puregoArgs = append(puregoArgs, uintptr(val))
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
			pinReferences = append(pinReferences, hostBuf)
			bufArgs = append(bufArgs, BufArg{gPtr: gPtr, gLen: gLen, hostBuf: hostBuf})

			var addr uintptr
			if gLen > 0 {
				addr = uintptr(unsafe.Pointer(&hostBuf[0]))
			}
			puregoArgs = append(puregoArgs, addr)

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
			pinReferences = append(pinReferences, hostBuf)

			var addr uintptr
			addr = uintptr(unsafe.Pointer(&hostBuf[0]))
			puregoArgs = append(puregoArgs, addr)

		case 0x0A: // cb: callback handle
			if offset+8 > len(descBuf) {
				writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
				return
			}
			cbHandle := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
			offset += 8

			h.mu.Lock()
			cbAny, cbOk := h.handles[cbHandle]
			h.mu.Unlock()
			if !cbOk {
				writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
				return
			}
			cbState, cbOk := cbAny.(*CallbackState)
			if !cbOk {
				writeOverlapped(m, ovPtr, wasiEBADF, 0, 0)
				return
			}
			puregoArgs = append(puregoArgs, cbState.Trampoline)
			hasCallback = true

		default:
			writeOverlapped(m, ovPtr, wasiEINVAL, 0, 0)
			return
		}
	}

	state := h.RegisterOp(ovPtr, nil)
	if hasCallback {
		h.mu.Lock()
		h.activeCallState = state
		h.activeCallOvPtr = ovPtr
		h.mu.Unlock()
	}

	go func() {
		r1, _, _ := purego.SyscallN(symState.Handle, puregoArgs...)

		if hasCallback {
			h.mu.Lock()
			h.activeCallRet = r1
			h.mu.Unlock()
		} else {
			h.fileOpsQueue <- func() {
				defer h.DecOpsFor(state)
				if !h.IsOpActive(ovPtr, state.opID) {
					return
				}
				h.mu.Lock()
				delete(h.activeOps, ovPtr)
				h.mu.Unlock()

				// Copy-out
				for _, ba := range bufArgs {
					if ba.gLen > 0 {
						mem.Write(ba.gPtr, ba.hostBuf)
					}
				}

				_ = pinReferences

				writeOverlapped(m, ovPtr, 0, 0, uint64(r1))
			}
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

	closure := makeCallbackClosure(h, m, cbHandle, sig, guestFnName)
	if closure == nil {
		writeOverlapped(m, ovPtr, wasiENOSYS, 0, 0)
		return
	}

	trampoline := purego.NewCallback(closure)

	h.mu.Lock()
	cbState := &CallbackState{
		Trampoline: trampoline,
		Parent:     libHandle,
		Sig:        sig,
		GuestFn:    guestFnName,
		Closure:    closure, // Keep the closure pinned from GC!
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

	h.mu.Lock()
	ch, ok := h.activeInvocations[invocationId]
	if ok {
		delete(h.activeInvocations, invocationId)
	}
	h.mu.Unlock()

	_ = cbHandle
	if ok {
		ch <- uintptr(retValue)
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

	// Determine if this is the final callback in the stream
	isFinal := false
	if len(args) >= 2 && cbState != nil && len(cbState.Sig.ArgTypes) >= 2 {
		if cbState.Sig.ArgTypes[1] == 0x0B {
			structPtr := args[1]
			if structPtr != 0 {
				isFinalVal := *(*byte)(unsafe.Pointer(structPtr + 8))
				if isFinalVal != 0 {
					isFinal = true
				}
			}
		} else if len(args) >= 3 && args[2] != 0 {
			// Legacy hardcoded check for SysV
			isFinal = true
		}
	} else if len(args) >= 3 && args[2] != 0 {
		isFinal = true
	}

	if isFinal {
		h.mu.Lock()
		activeState := h.activeCallState
		activeOvPtr := h.activeCallOvPtr
		activeRet := h.activeCallRet
		h.activeCallState = nil
		h.activeCallOvPtr = 0
		h.mu.Unlock()

		if activeState != nil {
			h.fileOpsQueue <- func() {
				defer h.DecOpsFor(activeState)
				if !h.IsOpActive(activeOvPtr, activeState.opID) {
					return
				}
				h.mu.Lock()
				delete(h.activeOps, activeOvPtr)
				h.mu.Unlock()
				writeOverlapped(m, activeOvPtr, 0, 0, uint64(activeRet))
			}
		}
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

	var buf []byte
	for i := uintptr(0); ; i++ {
		b := *(*byte)(unsafe.Pointer(uintptr(hostPtr) + i))
		if b == 0 {
			break
		}
		buf = append(buf, b)
		if uint32(len(buf)) >= maxLen {
			break
		}
	}

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

	_, ok := cbAny.(*CallbackState)
	if !ok {
		return 0
	}

	var ret uintptr
	respChan := make(chan uintptr, 1)
	var uintptrArgs []uintptr
	for _, a := range args {
		uintptrArgs = append(uintptrArgs, uintptr(a))
	}

	ev := &CallbackEvent{
		CallbackHandle: cbHandle,
		Args:           uintptrArgs,
		RespChan:       respChan,
	}

	h.callbackQueue <- ev
	h.fileOpsQueue <- func() {}

	ret = <-respChan

	// We don't do isFinal checking here because satellite manages it, or maybe we do need it.
	return ret
}
