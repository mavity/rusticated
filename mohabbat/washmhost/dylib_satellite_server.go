package main

import (
	"encoding/binary"
	"encoding/gob"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
)

var (
	satSrvHandles          = make(map[uint64]interface{})
	satSrvNext      uint64 = 1
	satSrvMu        sync.Mutex
	satSrvEncoder   *gob.Encoder
	satSrvCallbacks        = make(map[uint32]chan int64)
	satSrvCbInvoc   uint32 = 1
)

var (
	// satWorkerInbox feeds the single OS-thread-locked worker that runs all
	// top-level (non-reentrant) requests, giving the library a stable thread.
	satWorkerInbox chan DylibRequest
	// satDispatchStack is the LIFO of parked callback trampolines; the innermost
	// one runs reentrant guest requests on its own (callback) thread.
	satDispatchStack []chan DylibRequest
	satDispatchMu    sync.Mutex
)

type SatCbState struct {
	Handle     uint64
	Trampoline uintptr
	Sig        CallbackSig
}

func runSatellite() {
	decoder := gob.NewDecoder(os.Stdin)
	satSrvEncoder = gob.NewEncoder(os.Stdout)

	// First framed message: report our arch so the host can assert compatibility
	// before it issues any DLL operation.
	satSrvEncoder.Encode(&DylibResponse{IsHandshake: true, HGOOS: runtime.GOOS, HGOARCH: runtime.GOARCH})

	// Disable stdout/stderr so we don't corrupt the protocol
	os.Stdout = os.Stderr

	// One persistent OS-thread-locked worker runs every top-level call, so the
	// native library sees a stable thread identity across calls.
	satWorkerInbox = make(chan DylibRequest, 256)
	go satelliteWorker()

	for {
		var req DylibRequest
		err := decoder.Decode(&req)
		if err != nil {
			if err != io.EOF {
				fmt.Fprintf(os.Stderr, "satellite read error: %v\n", err)
			}
			os.Exit(0)
		}

		routeSatelliteRequest(req)
	}
}

// satelliteWorker runs top-level requests serially on a single locked OS thread.
func satelliteWorker() {
	runtime.LockOSThread()
	for req := range satWorkerInbox {
		if resp := processSatelliteRequest(req); resp != nil {
			encodeResp(resp)
		}
	}
}

// routeSatelliteRequest delivers each request to the thread that must run it: a
// callback response unblocks its parked trampoline; a reentrant request runs on
// the innermost parked callback thread; everything else runs on the worker.
func routeSatelliteRequest(req DylibRequest) {
	if req.Op == "CallbackRespond" {
		satSrvMu.Lock()
		ch, ok := satSrvCallbacks[req.InvocId]
		if ok {
			delete(satSrvCallbacks, req.InvocId)
		}
		satSrvMu.Unlock()
		if ok {
			ch <- req.CbRet
		}
		return
	}

	if req.Reentrant {
		satDispatchMu.Lock()
		var inbox chan DylibRequest
		if n := len(satDispatchStack); n > 0 {
			inbox = satDispatchStack[n-1]
		}
		satDispatchMu.Unlock()
		if inbox != nil {
			inbox <- req
			return
		}
		// No parked callback to host it; fall through to the worker.
	}
	satWorkerInbox <- req
}

func encodeResp(resp *DylibResponse) {
	satSrvMu.Lock()
	satSrvEncoder.Encode(resp)
	satSrvMu.Unlock()
}

func pushDispatch(inbox chan DylibRequest) {
	satDispatchMu.Lock()
	satDispatchStack = append(satDispatchStack, inbox)
	satDispatchMu.Unlock()
}

func popDispatch(inbox chan DylibRequest) {
	satDispatchMu.Lock()
	for i := len(satDispatchStack) - 1; i >= 0; i-- {
		if satDispatchStack[i] == inbox {
			satDispatchStack = append(satDispatchStack[:i], satDispatchStack[i+1:]...)
			break
		}
	}
	satDispatchMu.Unlock()
}

func processSatelliteRequest(req DylibRequest) *DylibResponse {
	resp := DylibResponse{ID: req.ID}

	switch req.Op {
	case "Open":
		lib, err := dlopen(req.Path, 0x2|0x8)
		if err != nil {
			resp.ErrCode = mapErrno(err)
		} else {
			if sym, err := dlsym(lib, "FLAGS_minloglevel"); err == nil && sym != 0 {
				*(*int32)(unsafe.Pointer(sym)) = 2
			}
			satSrvMu.Lock()
			id := satSrvNext
			satSrvNext++
			satSrvHandles[id] = lib
			satSrvMu.Unlock()
			resp.Handle = id
		}

	case "Close":
		satSrvMu.Lock()
		libAny, ok := satSrvHandles[req.LibHandle]
		if ok {
			delete(satSrvHandles, req.LibHandle)
		}
		satSrvMu.Unlock()
		if ok {
			_ = dlclose(libAny.(uintptr))
		} else {
			resp.ErrCode = 9 // EBADF
		}

	case "Sym":
		satSrvMu.Lock()
		libAny, ok := satSrvHandles[req.LibHandle]
		satSrvMu.Unlock()
		if ok {
			sym, err := dlsym(libAny.(uintptr), req.Name)
			if err == nil && sym != 0 {
				satSrvMu.Lock()
				id := satSrvNext
				satSrvNext++
				satSrvHandles[id] = sym
				satSrvMu.Unlock()
				resp.Handle = id
			} else {
				resp.ErrCode = mapErrno(err)
			}
		} else {
			resp.ErrCode = 9 // EBADF
		}

	case "Call":
		satSrvMu.Lock()
		symAny, ok := satSrvHandles[req.SymHandle]
		satSrvMu.Unlock()
		if !ok {
			resp.ErrCode = 9
			break
		}

		descBuf := req.DescBuf
		_ = descBuf[0]
		argCount := descBuf[1]

		offset := 4
		var puregoArgs []uintptr
		var pinReferences []interface{}
		bufIdx := 0

		for i := byte(0); i < argCount; i++ {
			if offset >= len(descBuf) {
				resp.ErrCode = 22 // EINVAL
				break
			}
			tag := descBuf[offset]
			offset++

			switch tag {
			case 0x01: // i32
				val := int32(binary.LittleEndian.Uint32(descBuf[offset : offset+4]))
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 4
			case 0x02: // i64
				val := int64(binary.LittleEndian.Uint64(descBuf[offset : offset+8]))
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 8
			case 0x03: // u32
				val := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 4
			case 0x04: // u64
				val := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 8
			case 0x05: // f32
				valBits := binary.LittleEndian.Uint32(descBuf[offset : offset+4])
				puregoArgs = append(puregoArgs, uintptr(valBits))
				offset += 4
			case 0x06: // f64
				valBits := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				puregoArgs = append(puregoArgs, uintptr(valBits))
				offset += 8
			case 0x07: // ptr
				val := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				puregoArgs = append(puregoArgs, uintptr(val))
				offset += 8
			case 0x08, 0x09: // buf, cstr
				_ = binary.LittleEndian.Uint32(descBuf[offset : offset+4])
				gLen := binary.LittleEndian.Uint32(descBuf[offset+4 : offset+8])
				offset += 8

				if bufIdx < len(req.BufParams) {
					hostBuf := req.BufParams[bufIdx]
					pinReferences = append(pinReferences, hostBuf)
					var addr uintptr
					if gLen > 0 {
						addr = uintptr(unsafe.Pointer(&hostBuf[0]))
					}
					puregoArgs = append(puregoArgs, addr)
					bufIdx++
				}
			case 0x0A: // cb
				cbHandle := binary.LittleEndian.Uint64(descBuf[offset : offset+8])
				offset += 8

				satSrvMu.Lock()
				cbAny, cbOk := satSrvHandles[cbHandle]
				satSrvMu.Unlock()
				if cbOk {
					puregoArgs = append(puregoArgs, cbAny.(*SatCbState).Trampoline)
				}
			}
		}

		if resp.ErrCode == 0 {
			r1, _, _ := purego.SyscallN(symAny.(uintptr), puregoArgs...)
			_ = pinReferences

			resp.Result = uint64(r1)
			resp.BufParams = req.BufParams // Contains mutated buffers
		}

	case "ReadCstr":
		hostPtr := req.Ptr
		var buf []byte
		if hostPtr != 0 {
			for i := uintptr(0); ; i++ {
				b := *(*byte)(unsafe.Pointer(uintptr(hostPtr) + i))
				if b == 0 {
					break
				}
				buf = append(buf, b)
				if uint32(len(buf)) >= req.MaxLen {
					break
				}
			}
		}
		resp.BytesRes = buf

	case "ReadMem":
		hostPtr := req.Ptr
		n := req.MaxLen
		var buf []byte
		if hostPtr != 0 && n > 0 {
			buf = make([]byte, n)
			for i := uint32(0); i < n; i++ {
				buf[i] = *(*byte)(unsafe.Pointer(uintptr(hostPtr) + uintptr(i)))
			}
		}
		resp.BytesRes = buf

	case "CallbackCreate":
		closure := makeSatCallbackClosure(req.CbHandle, req.ArgCount, req.RetType, req.ArgTypes, req.GuestFnName)
		if closure != nil {
			trampoline := purego.NewCallback(closure)
			satSrvMu.Lock()
			satSrvHandles[req.CbHandle] = &SatCbState{
				Handle:     req.CbHandle,
				Trampoline: trampoline,
				Sig: CallbackSig{
					ArgCount: req.ArgCount,
					RetType:  req.RetType,
					ArgTypes: req.ArgTypes,
				},
			}
			satSrvMu.Unlock()
			resp.ErrCode = 0
		} else {
			resp.ErrCode = 1 // ENOSYS
		}

	case "CallbackRespond":
		// Delivered by routeSatelliteRequest before reaching a worker/dispatch.
		return nil
	}

	return &resp
}

func makeSatCallbackClosure(cbHandle uint64, argCount uint8, retType uint8, argTypes []uint8, guestFnName string) interface{} {
	dispatch := func(args []uintptr) uintptr {
		satSrvMu.Lock()
		invocId := satSrvCbInvoc
		satSrvCbInvoc++
		respCh := make(chan int64, 1)
		satSrvCallbacks[invocId] = respCh

		var uintArgs []uint64
		var bufs [][]byte
		for i, a := range args {
			uintArgs = append(uintArgs, uint64(a))
			tag := uint8(0)
			if i < len(argTypes) {
				tag = argTypes[i]
			}

			if tag == 0x09 && a != 0 {
				var buf []byte
				for offset := uintptr(0); offset < 1048576; offset++ {
					b := *(*byte)(unsafe.Pointer(a + offset))
					if b == 0 {
						break
					}
					buf = append(buf, b)
				}
				bufs = append(bufs, buf)
			}
		}

		// This trampoline runs on the DLL's callback thread. Register it as the
		// innermost parked thread BEFORE emitting the callback, so reentrant guest
		// requests are routed here, then service them inline until the guest replies.
		myInbox := make(chan DylibRequest, 8)
		pushDispatch(myInbox)

		resp := DylibResponse{
			IsCallback: true,
			CbHandle:   cbHandle,
			CbArgs:     uintArgs,
			CbInvocId:  invocId,
			BufParams:  bufs,
		}
		satSrvEncoder.Encode(&resp)
		satSrvMu.Unlock()

		for {
			select {
			case ret := <-respCh:
				popDispatch(myInbox)
				return uintptr(ret)
			case r := <-myInbox:
				if out := processSatelliteRequest(r); out != nil {
					encodeResp(out)
				}
			}
		}
	}

	switch argCount {
	case 0:
		return func() uintptr { return dispatch([]uintptr{}) }
	case 1:
		return func(a1 uintptr) uintptr { return dispatch([]uintptr{a1}) }
	case 2:
		return func(a1, a2 uintptr) uintptr { return dispatch([]uintptr{a1, a2}) }
	case 3:
		return func(a1, a2, a3 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3}) }
	case 4:
		return func(a1, a2, a3, a4 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4}) }
	case 5:
		return func(a1, a2, a3, a4, a5 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4, a5}) }
	case 6:
		return func(a1, a2, a3, a4, a5, a6 uintptr) uintptr { return dispatch([]uintptr{a1, a2, a3, a4, a5, a6}) }
	case 7:
		return func(a1, a2, a3, a4, a5, a6, a7 uintptr) uintptr {
			return dispatch([]uintptr{a1, a2, a3, a4, a5, a6, a7})
		}
	case 8:
		return func(a1, a2, a3, a4, a5, a6, a7, a8 uintptr) uintptr {
			return dispatch([]uintptr{a1, a2, a3, a4, a5, a6, a7, a8})
		}
	}
	return nil
}
