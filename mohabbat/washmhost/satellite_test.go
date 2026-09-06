package main

import (
	"encoding/gob"
	"io"
	"math"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/ebitengine/purego"
)

func TestWasmFuncTypeEncoding(t *testing.T) {
	params := []byte{WasmTypeI32, WasmTypeI64, WasmTypeF32}
	results := []byte{WasmTypeI64}

	encoded := EncodeFuncType(params, results)
	decParams, decResults, err := DecodeFuncType(encoded)
	if err != nil {
		t.Fatalf("DecodeFuncType failed: %v", err)
	}

	if len(decParams) != len(params) {
		t.Fatalf("params len mismatch: got %d, want %d", len(decParams), len(params))
	}
	for i := range params {
		if decParams[i] != params[i] {
			t.Errorf("param %d mismatch: got %v, want %v", i, decParams[i], params[i])
		}
	}

	if len(decResults) != len(results) {
		t.Fatalf("results len mismatch: got %d, want %d", len(decResults), len(results))
	}
	for i := range results {
		if decResults[i] != results[i] {
			t.Errorf("result %d mismatch: got %v, want %v", i, decResults[i], results[i])
		}
	}
}

func TestSatelliteMemoryAndLifecycle(t *testing.T) {
	// Create duplex pipe between test and Satellite
	rHost, wSat := io.Pipe()
	rSat, wHost := io.Pipe()

	sat := NewSatellite(rSat, wSat)
	go func() {
		_ = sat.Serve()
	}()

	hostEnc := gob.NewEncoder(wHost)
	hostDec := gob.NewDecoder(rHost)

	// 1. Alloc
	reqID := uint64(1)
	allocReq := &DylibAllocReq{ReqID: reqID, Size: 64, Align: 16}
	if err := hostEnc.Encode(&IPCEnvelope{AllocReq: allocReq}); err != nil {
		t.Fatalf("encode alloc failed: %v", err)
	}

	var respEnv IPCEnvelope
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode alloc resp failed: %v", err)
	}
	allocResp := respEnv.AllocResp
	if allocResp == nil || allocResp.Error != "" || allocResp.Ptr == 0 {
		t.Fatalf("alloc failed: %+v", allocResp)
	}
	ptr := allocResp.Ptr

	// Verify 16-byte alignment
	if ptr%16 != 0 {
		t.Errorf("allocated pointer not 16-byte aligned: 0x%x", ptr)
	}

	// 2. WriteMem
	writeReq := &DylibWriteMemReq{
		ReqID: 2,
		Ptr:   ptr,
		Data:  []byte("hello satellite memory"),
	}
	if err := hostEnc.Encode(&IPCEnvelope{WriteMemReq: writeReq}); err != nil {
		t.Fatalf("encode write failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode write resp failed: %v", err)
	}
	writeResp := respEnv.WriteMemResp
	if writeResp == nil || writeResp.Error != "" {
		t.Fatalf("write failed: %+v", writeResp)
	}

	// 3. ReadMem
	readReq := &DylibReadMemReq{
		ReqID: 3,
		Ptr:   ptr,
		Len:   uint32(len("hello satellite memory")),
	}
	if err := hostEnc.Encode(&IPCEnvelope{ReadMemReq: readReq}); err != nil {
		t.Fatalf("encode read failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode read resp failed: %v", err)
	}
	readResp := respEnv.ReadMemResp
	if readResp == nil || readResp.Error != "" || string(readResp.Data) != "hello satellite memory" {
		t.Fatalf("read failed: got %q, want %q", readResp.Data, "hello satellite memory")
	}

	// 4. Free
	freeReq := &DylibFreeReq{ReqID: 4, Ptr: ptr}
	if err := hostEnc.Encode(&IPCEnvelope{FreeReq: freeReq}); err != nil {
		t.Fatalf("encode free failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode free resp failed: %v", err)
	}
	freeResp := respEnv.FreeResp
	if freeResp == nil || freeResp.Error != "" {
		t.Fatalf("free failed: %+v", freeResp)
	}

	// Clean up pipes
	_ = wHost.Close()
	_ = wSat.Close()
}

func TestSatelliteLibraryAndCallback(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping Unix library test on Windows")
	}

	rHost, wSat := io.Pipe()
	rSat, wHost := io.Pipe()

	sat := NewSatellite(rSat, wSat)
	go func() {
		_ = sat.Serve()
	}()

	hostEnc := gob.NewEncoder(wHost)
	hostDec := gob.NewDecoder(rHost)

	// 1. Open libSystem on darwin or libc on linux
	libPath := "/usr/lib/libSystem.B.dylib"
	if runtime.GOOS == "linux" {
		libPath = "libc.so.6"
	}

	openReq := &DylibOpenReq{ReqID: 10, Path: libPath}
	if err := hostEnc.Encode(&IPCEnvelope{OpenReq: openReq}); err != nil {
		t.Fatalf("encode open failed: %v", err)
	}

	var respEnv IPCEnvelope
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode open resp failed: %v", err)
	}
	openResp := respEnv.OpenResp
	if openResp == nil || openResp.Error != "" || openResp.Handle == 0 {
		t.Fatalf("open failed: %+v", openResp)
	}

	// 2. Resolve symbol (e.g., strlen)
	symReq := &DylibSymReq{ReqID: 11, Handle: openResp.Handle, Name: "strlen"}
	if err := hostEnc.Encode(&IPCEnvelope{SymReq: symReq}); err != nil {
		t.Fatalf("encode sym failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode sym resp failed: %v", err)
	}
	symResp := respEnv.SymResp
	if symResp == nil || symResp.Error != "" || symResp.Symbol == 0 {
		t.Fatalf("sym failed: %+v", symResp)
	}

	// 3. Alloc memory for a test string, write into it, and call strlen!
	testStr := "rusticated-callbacks\x00"
	allocReq := &DylibAllocReq{ReqID: 12, Size: uint64(len(testStr)), Align: 8}
	_ = hostEnc.Encode(&IPCEnvelope{AllocReq: allocReq})
	_ = hostDec.Decode(&respEnv)
	strPtr := respEnv.AllocResp.Ptr

	writeReq := &DylibWriteMemReq{ReqID: 13, Ptr: strPtr, Data: []byte(testStr)}
	_ = hostEnc.Encode(&IPCEnvelope{WriteMemReq: writeReq})
	_ = hostDec.Decode(&respEnv)

	// strlen signature: params: [i64], results: [i64]
	strlenSig := EncodeFuncType([]byte{WasmTypeI64}, []byte{WasmTypeI64})
	callReq := &DylibCallReq{
		ReqID:    14,
		Symbol:   symResp.Symbol,
		SigBytes: strlenSig,
		Args:     []uint64{strPtr},
	}
	if err := hostEnc.Encode(&IPCEnvelope{CallReq: callReq}); err != nil {
		t.Fatalf("encode call failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode call resp failed: %v", err)
	}
	callResp := respEnv.CallResp
	if callResp == nil || callResp.Error != "" {
		t.Fatalf("call failed: %+v", callResp)
	}
	if len(callResp.Results) != 1 || callResp.Results[0] != uint64(len("rusticated-callbacks")) {
		t.Fatalf("strlen result mismatch: got %v, want %d", callResp.Results, len("rusticated-callbacks"))
	}

	// Free test string
	freeReq := &DylibFreeReq{ReqID: 15, Ptr: strPtr}
	_ = hostEnc.Encode(&IPCEnvelope{FreeReq: freeReq})
	_ = hostDec.Decode(&respEnv)

	// 3b. Test floating-point calling convention with sqrt(64.0) -> 8.0!
	sqrtSymReq := &DylibSymReq{ReqID: 151, Handle: openResp.Handle, Name: "sqrt"}
	_ = hostEnc.Encode(&IPCEnvelope{SymReq: sqrtSymReq})
	_ = hostDec.Decode(&respEnv)
	if respEnv.SymResp != nil && respEnv.SymResp.Symbol != 0 {
		sqrtSig := EncodeFuncType([]byte{WasmTypeF64}, []byte{WasmTypeF64})
		sqrtCall := &DylibCallReq{
			ReqID:    152,
			Symbol:   respEnv.SymResp.Symbol,
			SigBytes: sqrtSig,
			Args:     []uint64{math.Float64bits(64.0)},
		}
		_ = hostEnc.Encode(&IPCEnvelope{CallReq: sqrtCall})
		_ = hostDec.Decode(&respEnv)
		if respEnv.CallResp == nil || len(respEnv.CallResp.Results) != 1 {
			t.Fatalf("sqrt call failed: %+v", respEnv.CallResp)
		}
		resFloat := math.Float64frombits(respEnv.CallResp.Results[0])
		if resFloat != 8.0 {
			t.Fatalf("sqrt(64.0) result mismatch: got %f, want 8.0", resFloat)
		}
	}

	// 4. Callback Registration and Invocation test
	// Callback signature: params: [i64, i64], results: [i64]
	cbSig := EncodeFuncType([]byte{WasmTypeI64, WasmTypeI64}, []byte{WasmTypeI64})
	cbRegReq := &DylibCbRegisterReq{
		ReqID:    16,
		CbID:     42,
		SigBytes: cbSig,
	}
	if err := hostEnc.Encode(&IPCEnvelope{CbRegisterReq: cbRegReq}); err != nil {
		t.Fatalf("encode cb register failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode cb register resp failed: %v", err)
	}
	cbRegResp := respEnv.CbRegisterResp
	if cbRegResp == nil || cbRegResp.Error != "" || cbRegResp.Trampoline == 0 {
		t.Fatalf("cb register failed: %+v", cbRegResp)
	}

	// Spawn a goroutine to invoke the native trampoline directly!
	trampoline := cbRegResp.Trampoline
	var invokeResult uint64
	doneChan := make(chan struct{})

	go func() {
		// Call trampoline(100, 200) -> expect sum 300
		r1, _, _ := purego.SyscallN(uintptr(trampoline), 100, 200)
		invokeResult = uint64(r1)
		close(doneChan)
	}()

	// Host should receive DylibCbInvokeEvent
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode cb invoke event failed: %v", err)
	}
	invokeEv := respEnv.CbInvokeEvent
	if invokeEv == nil || invokeEv.CbID != 42 {
		t.Fatalf("unexpected invoke event: %+v", respEnv)
	}
	if len(invokeEv.Args) != 2 || invokeEv.Args[0] != 100 || invokeEv.Args[1] != 200 {
		t.Fatalf("invoke args mismatch: got %v", invokeEv.Args)
	}

	// Host sends DylibCbReturnReq with result = 300
	retReq := &DylibCbReturnReq{
		InvID:   invokeEv.InvID,
		Results: []uint64{300},
	}
	if err := hostEnc.Encode(&IPCEnvelope{CbReturnReq: retReq}); err != nil {
		t.Fatalf("encode cb return failed: %v", err)
	}

	// Waiting goroutine should complete with 300!
	select {
	case <-doneChan:
		if invokeResult != 300 {
			t.Fatalf("trampoline returned %d, want 300", invokeResult)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("trampoline invocation timed out")
	}

	// Close library
	closeReq := &DylibCloseReq{ReqID: 17, Handle: openResp.Handle}
	_ = hostEnc.Encode(&IPCEnvelope{CloseReq: closeReq})
	_ = hostDec.Decode(&respEnv)

	_ = wHost.Close()
	_ = wSat.Close()
}

func TestSatelliteReentrantCallbackPump(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping Unix library test on Windows")
	}

	rHost, wSat := io.Pipe()
	rSat, wHost := io.Pipe()

	sat := NewSatellite(rSat, wSat)
	go func() {
		_ = sat.Serve()
	}()

	hostEnc := gob.NewEncoder(wHost)
	hostDec := gob.NewDecoder(rHost)

	libPath := "/usr/lib/libSystem.B.dylib"
	if runtime.GOOS == "linux" {
		libPath = "libc.so.6"
	}

	// 1. Open library and resolve strlen
	openReq := &DylibOpenReq{ReqID: 100, Path: libPath}
	_ = hostEnc.Encode(&IPCEnvelope{OpenReq: openReq})
	var respEnv IPCEnvelope
	_ = hostDec.Decode(&respEnv)
	libHandle := respEnv.OpenResp.Handle

	symReq := &DylibSymReq{ReqID: 101, Handle: libHandle, Name: "strlen"}
	_ = hostEnc.Encode(&IPCEnvelope{SymReq: symReq})
	_ = hostDec.Decode(&respEnv)
	strlenSym := respEnv.SymResp.Symbol

	// 2. Register callback: params [i64], results [i64]
	cbSig := EncodeFuncType([]byte{WasmTypeI64}, []byte{WasmTypeI64})
	cbRegReq := &DylibCbRegisterReq{
		ReqID:    102,
		CbID:     99,
		SigBytes: cbSig,
	}
	_ = hostEnc.Encode(&IPCEnvelope{CbRegisterReq: cbRegReq})
	_ = hostDec.Decode(&respEnv)
	trampoline := respEnv.CbRegisterResp.Trampoline

	// 3. Alloc test string
	testStr := "reentrant\x00"
	allocReq := &DylibAllocReq{ReqID: 103, Size: uint64(len(testStr)), Align: 8}
	_ = hostEnc.Encode(&IPCEnvelope{AllocReq: allocReq})
	_ = hostDec.Decode(&respEnv)
	strPtr := respEnv.AllocResp.Ptr

	writeReq := &DylibWriteMemReq{ReqID: 104, Ptr: strPtr, Data: []byte(testStr)}
	_ = hostEnc.Encode(&IPCEnvelope{WriteMemReq: writeReq})
	_ = hostDec.Decode(&respEnv)

	// 4. Invoke trampoline on a separate goroutine (mimicking native C calling thread)
	var finalCallbackResult uint64
	doneChan := make(chan struct{})

	go func() {
		r1, _, _ := purego.SyscallN(uintptr(trampoline), 42)
		finalCallbackResult = uint64(r1)
		close(doneChan)
	}()

	// 5. Host receives DylibCbInvokeEvent
	_ = hostDec.Decode(&respEnv)
	invokeEv := respEnv.CbInvokeEvent
	if invokeEv == nil || invokeEv.InvID == 0 {
		t.Fatalf("expected valid invoke event: %+v", respEnv)
	}
	invID := invokeEv.InvID

	// 6. While that thread is waiting, Host sends a REENTRANT DylibCallReq
	// targeting ParentInvocationID = invID!
	reentrantCall := &DylibCallReq{
		ReqID:              105,
		Symbol:             strlenSym,
		SigBytes:           EncodeFuncType([]byte{WasmTypeI64}, []byte{WasmTypeI64}),
		Args:               []uint64{strPtr},
		ParentInvocationID: invID,
	}
	if err := hostEnc.Encode(&IPCEnvelope{CallReq: reentrantCall}); err != nil {
		t.Fatalf("encode reentrant call failed: %v", err)
	}

	// 7. Host should receive DylibCallResp pumped by that blocked thread!
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode reentrant call resp failed: %v", err)
	}
	callResp := respEnv.CallResp
	if callResp == nil || len(callResp.Results) != 1 || callResp.Results[0] != uint64(len("reentrant")) {
		t.Fatalf("reentrant call failed: %+v", callResp)
	}

	// 8. Now Host delivers the final callback return!
	retReq := &DylibCbReturnReq{
		InvID:   invID,
		Results: []uint64{callResp.Results[0] * 2}, // 9 * 2 = 18
	}
	if err := hostEnc.Encode(&IPCEnvelope{CbReturnReq: retReq}); err != nil {
		t.Fatalf("encode cb return failed: %v", err)
	}

	select {
	case <-doneChan:
		if finalCallbackResult != 18 {
			t.Fatalf("expected callback return 18, got %d", finalCallbackResult)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("reentrant callback test timed out")
	}

	_ = wHost.Close()
	_ = wSat.Close()
}

func TestSatelliteSubprocess(t *testing.T) {
	if os.Getenv("TEST_RUN_SATELLITE") == "1" {
		runSatelliteMain()
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestSatelliteSubprocess$")
	cmd.Env = append(os.Environ(), "TEST_RUN_SATELLITE=1")

	inPipe, err := cmd.StdinPipe()
	if err != nil {
		t.Fatalf("stdin pipe: %v", err)
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatalf("cmd start: %v", err)
	}
	defer func() {
		_ = inPipe.Close()
		_ = cmd.Process.Kill()
	}()

	hostEnc := gob.NewEncoder(inPipe)
	hostDec := gob.NewDecoder(outPipe)

	var handshakeEnv IPCEnvelope
	if err := hostDec.Decode(&handshakeEnv); err != nil {
		t.Fatalf("decode handshake failed: %v", err)
	}
	if handshakeEnv.Handshake == nil || !handshakeEnv.Handshake.IsHandshake {
		t.Fatalf("expected handshake, got %+v", handshakeEnv.Handshake)
	}

	// Test Alloc over real subprocess pipes
	allocReq := &DylibAllocReq{ReqID: 1, Size: 128, Align: 16}
	if err := hostEnc.Encode(&IPCEnvelope{AllocReq: allocReq}); err != nil {
		t.Fatalf("encode alloc failed: %v", err)
	}

	var respEnv IPCEnvelope
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode alloc failed: %v", err)
	}

	if respEnv.AllocResp == nil || respEnv.AllocResp.Ptr == 0 || respEnv.AllocResp.Error != "" {
		t.Fatalf("subprocess alloc failed: %+v", respEnv.AllocResp)
	}

	// Test Free over real subprocess pipes
	freeReq := &DylibFreeReq{ReqID: 2, Ptr: respEnv.AllocResp.Ptr}
	if err := hostEnc.Encode(&IPCEnvelope{FreeReq: freeReq}); err != nil {
		t.Fatalf("encode free failed: %v", err)
	}
	if err := hostDec.Decode(&respEnv); err != nil {
		t.Fatalf("decode free failed: %v", err)
	}
	if respEnv.FreeResp == nil || respEnv.FreeResp.Error != "" {
		t.Fatalf("subprocess free failed: %+v", respEnv.FreeResp)
	}
}
