package main

import (
	"encoding/gob"
	"fmt"
	"io"
	"math"
	"os"
	"reflect"
	"runtime"
	"sync"
	"sync/atomic"
	"unsafe"

	"github.com/ebitengine/purego"
)

func init() {
	gob.Register(&DylibOpenReq{})
	gob.Register(&DylibOpenResp{})
	gob.Register(&DylibSymReq{})
	gob.Register(&DylibSymResp{})
	gob.Register(&DylibCloseReq{})
	gob.Register(&DylibCloseResp{})
	gob.Register(&DylibCallReq{})
	gob.Register(&DylibCallResp{})
	gob.Register(&DylibAllocReq{})
	gob.Register(&DylibAllocResp{})
	gob.Register(&DylibFreeReq{})
	gob.Register(&DylibFreeResp{})
	gob.Register(&DylibReadMemReq{})
	gob.Register(&DylibReadMemResp{})
	gob.Register(&DylibWriteMemReq{})
	gob.Register(&DylibWriteMemResp{})
	gob.Register(&DylibCbRegisterReq{})
	gob.Register(&DylibCbRegisterResp{})
	gob.Register(&DylibCbInvokeEvent{})
	gob.Register(&DylibCbReturnReq{})
	gob.Register(&DylibCbReturnResp{})
}

type callCacheKey struct {
	sym uintptr
	sig string
}

type Satellite struct {
	mu           sync.Mutex
	enc          *gob.Encoder
	dec          *gob.Decoder
	nextHandle   uint64
	libraries    map[uint64]uintptr
	symbols      map[uint64]uintptr
	callbacks    map[uint64]*SatelliteCallback
	nextInvID    uint64
	activePumps  map[uint64]chan *DylibCallReq
	activeReturn map[uint64]chan *DylibCbReturnReq
	allocFn      uintptr
	freeFn       uintptr
	winHeap      uintptr
	callFnsMu    sync.RWMutex
	callFns      map[callCacheKey]reflect.Value
}

type SatelliteCallback struct {
	cbID       uint64
	params     []byte
	results    []byte
	trampoline uintptr
}

func NewSatellite(r io.Reader, w io.Writer) *Satellite {
	s := &Satellite{
		enc:          gob.NewEncoder(w),
		dec:          gob.NewDecoder(r),
		nextHandle:   1,
		libraries:    make(map[uint64]uintptr),
		symbols:      make(map[uint64]uintptr),
		callbacks:    make(map[uint64]*SatelliteCallback),
		activePumps:  make(map[uint64]chan *DylibCallReq),
		activeReturn: make(map[uint64]chan *DylibCbReturnReq),
		callFns:      make(map[callCacheKey]reflect.Value),
	}
	s.initAllocator()
	return s
}

func (s *Satellite) initAllocator() {
	if runtime.GOOS == "windows" {
		k32, err := dlopen("kernel32.dll", RTLD_NOW)
		if err == nil {
			getHeap, err1 := dlsym(k32, "GetProcessHeap")
			hAlloc, err2 := dlsym(k32, "HeapAlloc")
			hFree, err3 := dlsym(k32, "HeapFree")
			if err1 == nil && err2 == nil && err3 == nil {
				hHeap, _, _ := purego.SyscallN(getHeap)
				s.winHeap = hHeap
				s.allocFn = hAlloc
				s.freeFn = hFree
			}
		}
	} else if runtime.GOOS == "darwin" {
		libSys, err := dlopen("/usr/lib/libSystem.B.dylib", RTLD_GLOBAL)
		if err == nil {
			s.allocFn, _ = dlsym(libSys, "malloc")
			s.freeFn, _ = dlsym(libSys, "free")
		}
	} else {
		libc, err := dlopen("libc.so.6", RTLD_GLOBAL)
		if err != nil {
			libc, err = dlopen("", RTLD_GLOBAL)
		}
		if err == nil {
			s.allocFn, _ = dlsym(libc, "malloc")
			s.freeFn, _ = dlsym(libc, "free")
		}
	}
}

func (s *Satellite) allocNative(size uint64) (uintptr, error) {
	if s.allocFn == 0 {
		return 0, fmt.Errorf("native allocator not initialized")
	}
	if runtime.GOOS == "windows" {
		ptr, _, _ := purego.SyscallN(s.allocFn, s.winHeap, 0, uintptr(size))
		if ptr == 0 {
			return 0, fmt.Errorf("HeapAlloc failed for size %d", size)
		}
		return ptr, nil
	}
	ptr, _, _ := purego.SyscallN(s.allocFn, uintptr(size))
	if ptr == 0 {
		return 0, fmt.Errorf("malloc failed for size %d", size)
	}
	return ptr, nil
}

func (s *Satellite) freeNative(ptr uintptr) error {
	if ptr == 0 {
		return nil
	}
	if s.freeFn == 0 {
		return fmt.Errorf("native deallocator not initialized")
	}
	if runtime.GOOS == "windows" {
		purego.SyscallN(s.freeFn, s.winHeap, 0, ptr)
		return nil
	}
	purego.SyscallN(s.freeFn, ptr)
	return nil
}

func (s *Satellite) send(env *IPCEnvelope) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.enc.Encode(env)
}

// Serve reads and processes messages from the host until EOF.
func (s *Satellite) Serve() error {
	for {
		var env IPCEnvelope
		err := s.dec.Decode(&env)
		if err != nil {
			return err
		}

		if env.OpenReq != nil {
			go s.handleOpen(env.OpenReq)
		} else if env.SymReq != nil {
			go s.handleSym(env.SymReq)
		} else if env.CloseReq != nil {
			go s.handleClose(env.CloseReq)
		} else if env.CallReq != nil {
			s.dispatchCall(env.CallReq)
		} else if env.AllocReq != nil {
			go s.handleAlloc(env.AllocReq)
		} else if env.FreeReq != nil {
			go s.handleFree(env.FreeReq)
		} else if env.ReadMemReq != nil {
			go s.handleReadMem(env.ReadMemReq)
		} else if env.WriteMemReq != nil {
			go s.handleWriteMem(env.WriteMemReq)
		} else if env.CbRegisterReq != nil {
			go s.handleCbRegister(env.CbRegisterReq)
		} else if env.CbReturnReq != nil {
			s.dispatchCbReturn(env.CbReturnReq)
		}
	}
}

func (s *Satellite) handleOpen(req *DylibOpenReq) {
	flags := req.Flags
	if flags == 0 {
		flags = RTLD_NOW | RTLD_GLOBAL
	}
	h, err := dlopen(req.Path, flags)
	resp := &DylibOpenResp{ReqID: req.ReqID}
	if err != nil {
		resp.Error = fmt.Sprintf("%s (satellite %s/%s)", err.Error(), runtime.GOOS, runtime.GOARCH)
	} else {
		s.mu.Lock()
		id := s.nextHandle
		s.nextHandle++
		s.libraries[id] = h
		s.mu.Unlock()
		resp.Handle = id
	}
	_ = s.send(&IPCEnvelope{OpenResp: resp})
}

func (s *Satellite) handleSym(req *DylibSymReq) {
	s.mu.Lock()
	h, ok := s.libraries[req.Handle]
	s.mu.Unlock()

	resp := &DylibSymResp{ReqID: req.ReqID}
	if !ok {
		resp.Error = "invalid library handle"
		_ = s.send(&IPCEnvelope{SymResp: resp})
		return
	}

	sym, err := dlsym(h, req.Name)
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Symbol = uint64(sym)
	}
	_ = s.send(&IPCEnvelope{SymResp: resp})
}

func (s *Satellite) handleClose(req *DylibCloseReq) {
	s.mu.Lock()
	h, ok := s.libraries[req.Handle]
	delete(s.libraries, req.Handle)
	s.mu.Unlock()

	resp := &DylibCloseResp{ReqID: req.ReqID}
	if ok && h != 0 {
		if err := dlclose(h); err != nil {
			resp.Error = err.Error()
		}
	}
	_ = s.send(&IPCEnvelope{CloseResp: resp})
}

func (s *Satellite) dispatchCall(req *DylibCallReq) {
	if req.ParentInvocationID != 0 {
		s.mu.Lock()
		pumpChan, ok := s.activePumps[req.ParentInvocationID]
		s.mu.Unlock()
		if ok {
			// Deliver to the pumping thread
			pumpChan <- req
			return
		}
	}
	// Execute on general worker pool
	go func() {
		resp := s.executeCall(req)
		_ = s.send(&IPCEnvelope{CallResp: resp})
	}()
}

func (s *Satellite) getOrRegisterCallFn(sym uintptr, sigBytes []byte, params, results []byte) (reflect.Value, error) {
	key := callCacheKey{sym: sym, sig: string(sigBytes)}
	s.callFnsMu.RLock()
	fn, ok := s.callFns[key]
	s.callFnsMu.RUnlock()
	if ok {
		return fn, nil
	}

	inTypes := make([]reflect.Type, len(params))
	for i, p := range params {
		inTypes[i] = wasmTypeToReflect(p)
	}
	outTypes := make([]reflect.Type, len(results))
	for i, r := range results {
		outTypes[i] = wasmTypeToReflect(r)
	}

	fnType := reflect.FuncOf(inTypes, outTypes, false)
	fnPtrVal := reflect.New(fnType)
	purego.RegisterFunc(fnPtrVal.Interface(), sym)
	callable := fnPtrVal.Elem()

	s.callFnsMu.Lock()
	s.callFns[key] = callable
	s.callFnsMu.Unlock()
	return callable, nil
}

func (s *Satellite) executeCall(req *DylibCallReq) *DylibCallResp {
	resp := &DylibCallResp{ReqID: req.ReqID}
	if req.Symbol == 0 {
		resp.Error = "invalid symbol address"
		return resp
	}

	sym := uintptr(req.Symbol)

	params, results, err := DecodeFuncType(req.SigBytes)
	if err != nil {
		resp.Error = fmt.Sprintf("invalid signature: %v", err)
		return resp
	}

	hasFloats := false
	for _, p := range params {
		if p == WasmTypeF32 || p == WasmTypeF64 {
			hasFloats = true
			break
		}
	}
	for _, r := range results {
		if r == WasmTypeF32 || r == WasmTypeF64 {
			hasFloats = true
			break
		}
	}

	if hasFloats {
		if len(results) > 1 {
			resp.Error = fmt.Sprintf("satellite: multi-return floating-point calls not supported (%d results)", len(results))
			return resp
		}

		callable, err := s.getOrRegisterCallFn(sym, req.SigBytes, params, results)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}

		inVals := make([]reflect.Value, len(params))
		for i, p := range params {
			var raw uint64
			if i < len(req.Args) {
				raw = req.Args[i]
			}
			inVals[i] = uint64ToReflectValue(raw, p)
		}

		outVals := callable.Call(inVals)
		respResults := make([]uint64, len(results))
		for i, v := range outVals {
			respResults[i] = reflectValueToUint64(v)
		}
		resp.Results = respResults
		return resp
	}

	if len(results) > 2 {
		resp.Error = fmt.Sprintf("satellite: calls returning %d values not supported", len(results))
		return resp
	}

	// For integer-only calls (up to 2 return values), use purego.SyscallN
	sysArgs := make([]uintptr, len(req.Args))
	for i, a := range req.Args {
		sysArgs[i] = uintptr(a)
	}

	r1, r2, _ := purego.SyscallN(sym, sysArgs...)

	respResults := make([]uint64, len(results))
	if len(results) > 0 {
		respResults[0] = uint64(r1)
		if len(results) > 1 {
			respResults[1] = uint64(r2)
		}
	}
	resp.Results = respResults
	return resp
}

func (s *Satellite) handleAlloc(req *DylibAllocReq) {
	ptr, err := s.allocNative(req.Size)
	resp := &DylibAllocResp{ReqID: req.ReqID}
	if err != nil {
		resp.Error = err.Error()
	} else {
		resp.Ptr = uint64(ptr)
	}
	_ = s.send(&IPCEnvelope{AllocResp: resp})
}

func (s *Satellite) handleFree(req *DylibFreeReq) {
	err := s.freeNative(uintptr(req.Ptr))
	resp := &DylibFreeResp{ReqID: req.ReqID}
	if err != nil {
		resp.Error = err.Error()
	}
	_ = s.send(&IPCEnvelope{FreeResp: resp})
}

func (s *Satellite) handleReadMem(req *DylibReadMemReq) {
	resp := &DylibReadMemResp{ReqID: req.ReqID}
	if req.Ptr < 4096 || req.Len == 0 {
		resp.Data = []byte{}
		if req.Ptr != 0 && req.Ptr < 4096 {
			resp.Error = "invalid pointer dereference"
		}
		_ = s.send(&IPCEnvelope{ReadMemResp: resp})
		return
	}
	data := make([]byte, req.Len)
	src := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(req.Ptr))), req.Len)
	copy(data, src)
	resp.Data = data
	_ = s.send(&IPCEnvelope{ReadMemResp: resp})
}

func (s *Satellite) handleWriteMem(req *DylibWriteMemReq) {
	resp := &DylibWriteMemResp{ReqID: req.ReqID}
	if req.Ptr < 4096 {
		resp.Error = "invalid pointer dereference"
		_ = s.send(&IPCEnvelope{WriteMemResp: resp})
		return
	}
	if len(req.Data) > 0 {
		dst := unsafe.Slice((*byte)(unsafe.Pointer(uintptr(req.Ptr))), len(req.Data))
		copy(dst, req.Data)
	}
	_ = s.send(&IPCEnvelope{WriteMemResp: resp})
}

func (s *Satellite) handleCbRegister(req *DylibCbRegisterReq) {
	params, results, err := DecodeFuncType(req.SigBytes)
	resp := &DylibCbRegisterResp{ReqID: req.ReqID}
	if err != nil {
		resp.Error = fmt.Sprintf("invalid callback signature: %v", err)
		_ = s.send(&IPCEnvelope{CbRegisterResp: resp})
		return
	}

	cbID := req.CbID
	trampoline := s.buildTrampoline(cbID, params, results)
	s.mu.Lock()
	s.callbacks[cbID] = &SatelliteCallback{
		cbID:       cbID,
		params:     params,
		results:    results,
		trampoline: trampoline,
	}
	s.mu.Unlock()

	resp.Trampoline = uint64(trampoline)
	_ = s.send(&IPCEnvelope{CbRegisterResp: resp})
}

func (s *Satellite) buildTrampoline(cbID uint64, params, results []byte) uintptr {
	// Build reflect.Type matching params and results
	inTypes := make([]reflect.Type, len(params))
	for i, p := range params {
		inTypes[i] = wasmTypeToReflect(p)
	}
	effectiveResults := results
	outTypes := make([]reflect.Type, len(results))
	if runtime.GOOS == "windows" {
		effectiveResults = []byte{WasmTypeI64}
		outTypes = []reflect.Type{reflect.TypeOf(uintptr(0))}
	} else {
		for i, r := range results {
			outTypes[i] = wasmTypeToReflect(r)
		}
	}

	fnType := reflect.FuncOf(inTypes, outTypes, false)

	// Create MakeFunc closure
	closure := reflect.MakeFunc(fnType, func(args []reflect.Value) []reflect.Value {
		invID := atomic.AddUint64(&s.nextInvID, 1)
		threadID := uint64(getGID())

		argVals := make([]uint64, len(args))
		for i, v := range args {
			argVals[i] = reflectValueToUint64(v)
		}

		pumpChan := make(chan *DylibCallReq, 16)
		retChan := make(chan *DylibCbReturnReq, 1)

		s.mu.Lock()
		s.activePumps[invID] = pumpChan
		s.activeReturn[invID] = retChan
		s.mu.Unlock()

		defer func() {
			s.mu.Lock()
			delete(s.activePumps, invID)
			delete(s.activeReturn, invID)
			s.mu.Unlock()
		}()

		// Send invocation event to host
		_ = s.send(&IPCEnvelope{
			CbInvokeEvent: &DylibCbInvokeEvent{
				CbID:     cbID,
				InvID:    invID,
				ThreadID: threadID,
				Args:     argVals,
			},
		})

		// Reentrant pump loop on this native thread
		for {
			select {
			case ret := <-retChan:
				outVals := make([]reflect.Value, len(outTypes))
				for i, resType := range effectiveResults {
					var raw uint64
					if i < len(ret.Results) {
						raw = ret.Results[i]
					}
					if runtime.GOOS == "windows" {
						outVals[i] = reflect.ValueOf(uintptr(raw))
					} else {
						outVals[i] = uint64ToReflectValue(raw, resType)
					}
				}
				if runtime.GOOS == "windows" && len(ret.Results) == 0 {
					outVals[0] = reflect.ValueOf(uintptr(0))
				}
				return outVals

			case callReq := <-pumpChan:
				callResp := s.executeCall(callReq)
				_ = s.send(&IPCEnvelope{CallResp: callResp})
			}
		}
	})

	return purego.NewCallback(closure.Interface())
}

func (s *Satellite) dispatchCbReturn(msg *DylibCbReturnReq) {
	s.mu.Lock()
	ch, ok := s.activeReturn[msg.InvID]
	s.mu.Unlock()
	if ok {
		ch <- msg
	}
}

func wasmTypeToReflect(t byte) reflect.Type {
	switch t {
	case WasmTypeI32:
		return reflect.TypeOf(int32(0))
	case WasmTypeI64:
		return reflect.TypeOf(uint64(0))
	case WasmTypeF32:
		return reflect.TypeOf(float32(0))
	case WasmTypeF64:
		return reflect.TypeOf(float64(0))
	default:
		return reflect.TypeOf(uint64(0))
	}
}

func reflectValueToUint64(v reflect.Value) uint64 {
	switch v.Kind() {
	case reflect.Int32, reflect.Int, reflect.Int16, reflect.Int8:
		return uint64(uint32(v.Int()))
	case reflect.Int64:
		return uint64(v.Int())
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32:
		return uint64(uint32(v.Uint()))
	case reflect.Uint64, reflect.Uintptr:
		return v.Uint()
	case reflect.Float32:
		return uint64(math.Float32bits(float32(v.Float())))
	case reflect.Float64:
		return math.Float64bits(v.Float())
	case reflect.Bool:
		if v.Bool() {
			return 1
		}
		return 0
	case reflect.Pointer, reflect.UnsafePointer:
		return uint64(v.Pointer())
	default:
		return 0
	}
}

func uint64ToReflectValue(raw uint64, wasmType byte) reflect.Value {
	switch wasmType {
	case WasmTypeI32:
		return reflect.ValueOf(int32(raw))
	case WasmTypeI64:
		return reflect.ValueOf(raw)
	case WasmTypeF32:
		f := math.Float32frombits(uint32(raw))
		return reflect.ValueOf(f)
	case WasmTypeF64:
		f := math.Float64frombits(raw)
		return reflect.ValueOf(f)
	default:
		return reflect.ValueOf(raw)
	}
}

// runSatelliteMain is the entry point when washmhost is run with --dylib-satellite.
func runSatelliteMain() {
	initWatchdog()
	sat := NewSatellite(os.Stdin, os.Stdout)
	if err := sat.send(&IPCEnvelope{Handshake: &DylibHandshake{IsHandshake: true, HGOOS: runtime.GOOS, HGOARCH: runtime.GOARCH}}); err != nil {
		fmt.Fprintf(os.Stderr, "satellite handshake failed: %v\n", err)
		os.Exit(1)
	}
	if err := sat.Serve(); err != nil && err != io.EOF {
		fmt.Fprintf(os.Stderr, "satellite exited: %v\n", err)
		os.Exit(1)
	}
	os.Exit(0)
}
