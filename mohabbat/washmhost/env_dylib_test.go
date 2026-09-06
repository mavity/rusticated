package main

import (
	"context"
	"encoding/binary"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/ebitengine/purego"
	"github.com/tetratelabs/wazero/api"
)

func TestParsePlatformOverride(t *testing.T) {
	goos, goarch, err := parsePlatformOverride("windows-amd64")
	if err != nil {
		t.Fatalf("parsePlatformOverride returned error: %v", err)
	}
	if goos != "windows" || goarch != "amd64" {
		t.Fatalf("parsePlatformOverride returned %s/%s, want windows/amd64", goos, goarch)
	}

	goos, goarch, err = parsePlatformOverride("amd64")
	if err != nil {
		t.Fatalf("parsePlatformOverride alias returned error: %v", err)
	}
	if goos != runtime.GOOS || goarch != "amd64" {
		t.Fatalf("parsePlatformOverride alias returned %s/%s, want %s/amd64", goos, goarch, runtime.GOOS)
	}
}

func TestDetectDylibTargetForHostRuntime(t *testing.T) {
	var lib string
	switch runtime.GOOS {
	case "windows":
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		lib = filepath.Join(root, "System32", "kernel32.dll")
	case "darwin":
		lib = "/usr/lib/libSystem.B.dylib"
	default:
		lib = "/lib/x86_64-linux-gnu/libc.so.6"
		if _, err := os.Stat(lib); err != nil {
			lib = "libc.so.6"
		}
	}

	if _, err := os.Stat(lib); err != nil {
		t.Skipf("native library for host runtime unavailable: %s (%v)", lib, err)
	}

	tgt, err := detectDylibTarget(lib)
	if err != nil {
		t.Fatalf("detectDylibTarget(%q) failed: %v", lib, err)
	}
	if runtime.GOOS == "windows" {
		if tgt.goos != "windows" {
			t.Fatalf("detectDylibTarget(%q) = %s/%s, want windows/<arch>", lib, tgt.goos, tgt.goarch)
		}
		if tgt.goarch != "amd64" && tgt.goarch != "arm64" && tgt.goarch != "386" {
			t.Fatalf("detectDylibTarget(%q) returned unexpected windows arch %q", lib, tgt.goarch)
		}
	} else if tgt.goos != runtime.GOOS || tgt.goarch != runtime.GOARCH {
		t.Fatalf("detectDylibTarget(%q) = %s/%s, want %s/%s", lib, tgt.goos, tgt.goarch, runtime.GOOS, runtime.GOARCH)
	}

	hEnv := NewHostEnv()
	defer hEnv.Close()
	if got := len(hEnv.dylibRegistry.entries); got != 0 {
		t.Fatalf("new HostEnv should not have target entries yet: got %d", got)
	}
}

func TestResolveDylibTargetMatchesDetectedLibraryTarget(t *testing.T) {
	var lib string
	switch runtime.GOOS {
	case "windows":
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		lib = filepath.Join(root, "System32", "kernel32.dll")
	case "darwin":
		lib = "/usr/lib/libSystem.B.dylib"
	default:
		lib = "/lib/x86_64-linux-gnu/libc.so.6"
		if _, err := os.Stat(lib); err != nil {
			lib = "libc.so.6"
		}
	}
	if _, err := os.Stat(lib); err != nil {
		t.Skipf("native library for host runtime unavailable: %s (%v)", lib, err)
	}

	hEnv := NewHostEnv()
	defer hEnv.Close()

	tgt, err := detectDylibTarget(lib)
	if err != nil {
		t.Fatalf("detectDylibTarget(%q) failed: %v", lib, err)
	}
	key := hEnv.resolveDylibTarget(lib)
	if key.goos != tgt.goos || key.goarch != tgt.goarch {
		t.Fatalf("resolveDylibTarget(%q) returned %s/%s, want %s/%s", lib, key.goos, key.goarch, tgt.goos, tgt.goarch)
	}
}

func TestHostEnvDylibLifecycle(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping Unix library test on Windows")
	}

	hEnv := NewHostEnv()
	defer hEnv.Close()

	// Connect satellite directly via pipe
	rHost, wSat := io.Pipe()
	rSat, wHost := io.Pipe()

	sat := NewSatellite(rSat, wSat)
	go func() {
		_ = sat.Serve()
	}()
	hEnv.dylibMgr.SetSatelliteStream(rHost, wHost, wHost)

	mod := newMockModule(0x20000)
	hEnv.mod = mod

	// 1. Test sys_dylib_open
	t.Log("1. sys_dylib_open")
	libPath := "/usr/lib/libSystem.B.dylib"
	if runtime.GOOS == "linux" {
		libPath = "libc.so.6"
	}

	pathPtr := uint32(0x1000)
	mod.Memory().Write(pathPtr, []byte(libPath))

	openOv := uint32(0x2000)
	hEnv.sys_dylib_open(context.Background(), mod, []uint64{
		uint64(openOv),
		uint64(pathPtr),
		uint64(len(libPath)),
		0,
	})

	// Poll until open completion
	pollUntilComplete(t, hEnv, mod, openOv)
	t.Log("1. sys_dylib_open completed")

	ovBuf, _ := mod.Memory().Read(openOv, 24)
	libHandle := binary.LittleEndian.Uint64(ovBuf[16:24])
	if libHandle == 0 {
		t.Fatalf("dylib_open failed: ovBuf=%v", ovBuf)
	}

	// 2. Test sys_dylib_sym (strlen)
	symName := "strlen"
	namePtr := uint32(0x1100)
	mod.Memory().Write(namePtr, []byte(symName))

	symOv := uint32(0x2100)
	hEnv.sys_dylib_sym(context.Background(), mod, []uint64{
		uint64(symOv),
		libHandle,
		uint64(namePtr),
		uint64(len(symName)),
	})

	pollUntilComplete(t, hEnv, mod, symOv)

	ovBuf, _ = mod.Memory().Read(symOv, 24)
	strlenSym := binary.LittleEndian.Uint64(ovBuf[16:24])
	if strlenSym == 0 {
		t.Fatalf("dylib_sym failed: ovBuf=%v", ovBuf)
	}

	// 3. Test sys_dylib_alloc
	allocOv := uint32(0x2200)
	hEnv.sys_dylib_alloc(context.Background(), mod, []uint64{
		uint64(allocOv),
		libHandle,
		64,
		16,
	})
	pollUntilComplete(t, hEnv, mod, allocOv)

	ovBuf, _ = mod.Memory().Read(allocOv, 24)
	nativePtr := binary.LittleEndian.Uint64(ovBuf[16:24])
	if nativePtr == 0 || nativePtr%16 != 0 {
		t.Fatalf("dylib_alloc failed: ptr=0x%x", nativePtr)
	}

	// 4. Test sys_dylib_write_mem
	testStr := "washmhost-ffi\x00"
	guestStrPtr := uint32(0x1200)
	mod.Memory().Write(guestStrPtr, []byte(testStr))

	writeOv := uint32(0x2300)
	hEnv.sys_dylib_write_mem(context.Background(), mod, []uint64{
		uint64(writeOv),
		libHandle,
		nativePtr,
		uint64(guestStrPtr),
		uint64(len(testStr)),
	})
	pollUntilComplete(t, hEnv, mod, writeOv)

	// 5. Test sys_dylib_read_mem
	guestReadBuf := uint32(0x1300)
	readOv := uint32(0x2400)
	hEnv.sys_dylib_read_mem(context.Background(), mod, []uint64{
		uint64(readOv),
		libHandle,
		nativePtr,
		uint64(guestReadBuf),
		uint64(len(testStr)),
	})
	pollUntilComplete(t, hEnv, mod, readOv)

	readBytes, _ := mod.Memory().Read(guestReadBuf, uint32(len(testStr)))
	if string(readBytes) != testStr {
		t.Fatalf("dylib_read_mem mismatch: got %q, want %q", readBytes, testStr)
	}

	// 6. Test sys_dylib_call (strlen(nativePtr))
	sigBytes := EncodeFuncType([]byte{WasmTypeI64}, []byte{WasmTypeI64})
	sigPtr := uint32(0x1400)
	mod.Memory().Write(sigPtr, sigBytes)

	argsPtr := uint32(0x1500)
	argsBuf := make([]byte, 8)
	binary.LittleEndian.PutUint64(argsBuf, nativePtr)
	mod.Memory().Write(argsPtr, argsBuf)

	resBufPtr := uint32(0x1550)
	callOv := uint32(0x2500)
	hEnv.sys_dylib_call(context.Background(), mod, []uint64{
		uint64(callOv),
		strlenSym,
		uint64(sigPtr),
		uint64(len(sigBytes)),
		uint64(argsPtr),
		1, // 1 argument
		0, // parentInvID = 0
		uint64(resBufPtr),
		1, // resultsCap = 1
	})
	pollUntilComplete(t, hEnv, mod, callOv)

	ovBuf, _ = mod.Memory().Read(callOv, 24)
	strlenResult := binary.LittleEndian.Uint64(ovBuf[16:24])
	if strlenResult != uint64(len("washmhost-ffi")) {
		t.Fatalf("dylib_call strlen mismatch: got %d, want %d", strlenResult, len("washmhost-ffi"))
	}
	resFromBuf, _ := mod.Memory().Read(resBufPtr, 8)
	if binary.LittleEndian.Uint64(resFromBuf) != uint64(len("washmhost-ffi")) {
		t.Fatalf("resultsBuf mismatch: got %d", binary.LittleEndian.Uint64(resFromBuf))
	}

	// 7. Test sys_dylib_free
	freeOv := uint32(0x2600)
	hEnv.sys_dylib_free(context.Background(), mod, []uint64{
		uint64(freeOv),
		libHandle,
		nativePtr,
	})
	pollUntilComplete(t, hEnv, mod, freeOv)

	// 8. Test sys_dylib_callback_register, BeforeRun projection, and AfterRun unwinding
	cbSig := EncodeFuncType([]byte{WasmTypeI64, WasmTypeI64}, []byte{WasmTypeI64})
	cbSigPtr := uint32(0x1600)
	mod.Memory().Write(cbSigPtr, cbSig)

	cbOvPtr := uint32(0x3000)
	cbRegOv := uint32(0x2700)
	hEnv.sys_dylib_callback_register(context.Background(), mod, []uint64{
		uint64(cbRegOv),
		libHandle,
		uint64(cbSigPtr),
		uint64(len(cbSig)),
		uint64(cbOvPtr),
		CbOvTotalSize,
	})
	pollUntilComplete(t, hEnv, mod, cbRegOv)

	ovBuf, _ = mod.Memory().Read(cbRegOv, 24)
	trampoline := binary.LittleEndian.Uint64(ovBuf[16:24])
	if trampoline == 0 {
		t.Fatalf("dylib_callback_register failed: ovBuf=%v", ovBuf)
	}

	// Native caller invokes the trampoline on a background thread
	cbReturnVal := uint64(0)
	cbDone := make(chan struct{})
	go func() {
		r1, _, _ := purego.SyscallN(uintptr(trampoline), 50, 75)
		cbReturnVal = uint64(r1)
		close(cbDone)
	}()

	// Wait for the invocation event to arrive at the host
	start := time.Now()
	for {
		hEnv.Poll(context.Background(), mod)
		if len(hEnv.dylibMgr.incomingInvocations) > 0 {
			break
		}
		if time.Since(start) > time.Second*3 {
			t.Fatal("timed out waiting for callback invocation event")
		}
	}

	// Run BeforeRun: should project invocation into guest cbOvPtr!
	hEnv.dylibMgr.BeforeRun(context.Background(), mod)

	cbBuf, _ := mod.Memory().Read(cbOvPtr, CbOvTotalSize)
	flags, hostErr, cbID, invID, argLen, _ := ReadCallbackOverlappedHeader(cbBuf)
	if flags&1 == 0 || cbID != 1 || invID == 0 || argLen != 16 {
		t.Fatalf("unexpected projected callback overlapped: flags=%d cbID=%d invID=%d argLen=%d", flags, cbID, invID, argLen)
	}
	arg1 := binary.LittleEndian.Uint64(cbBuf[48:56])
	arg2 := binary.LittleEndian.Uint64(cbBuf[56:64])
	if arg1 != 50 || arg2 != 75 {
		t.Fatalf("projected args mismatch: got (%d, %d), want (50, 75)", arg1, arg2)
	}
	_ = hostErr

	// Guest handles invocation asynchronously: clears completed flag
	binary.LittleEndian.PutUint32(cbBuf[0:4], 0)
	mod.Memory().Write(cbOvPtr, cbBuf[:4])
	hEnv.dylibMgr.AfterRun(context.Background(), mod)

	// Frame should now be in GuestAsync state
	frame := hEnv.dylibMgr.allFrames[invID]
	if frame == nil || frame.state != FrameStateGuestAsync {
		t.Fatalf("frame expected in GuestAsync state, got: %+v", frame)
	}

	// Guest finishes: writes results (50 + 75 = 125), sets completed flag to 1
	resultAreaOff := 48 + argLen
	binary.LittleEndian.PutUint64(cbBuf[resultAreaOff:resultAreaOff+8], 125)
	binary.LittleEndian.PutUint32(cbBuf[0:4], 1)
	mod.Memory().Write(cbOvPtr, cbBuf)

	// Run AfterRun: captures result and sends return to satellite
	hEnv.dylibMgr.AfterRun(context.Background(), mod)

	select {
	case <-cbDone:
		if cbReturnVal != 125 {
			t.Fatalf("trampoline returned %d, want 125", cbReturnVal)
		}
	case <-time.After(time.Second * 3):
		t.Fatal("timed out waiting for trampoline to unblock")
	}

	// 9. Close library
	closeOv := uint32(0x2800)
	hEnv.sys_dylib_close(context.Background(), mod, []uint64{
		uint64(closeOv),
		libHandle,
	})
	pollUntilComplete(t, hEnv, mod, closeOv)
}

func TestHostEnvCallbackParking(t *testing.T) {
	hEnv := NewHostEnv()
	defer hEnv.Close()

	rHost, wSat := io.Pipe()
	rSat, wHost := io.Pipe()

	sat := NewSatellite(rSat, wSat)
	go func() {
		_ = sat.Serve()
	}()
	hEnv.dylibMgr.SetSatelliteStream(rHost, wHost, wHost)

	mod := newMockModule(0x20000)
	hEnv.mod = mod

	libPath := "/usr/lib/libSystem.B.dylib"
	if runtime.GOOS == "windows" {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		libPath = filepath.Join(root, "System32", "kernel32.dll")
	} else if runtime.GOOS == "linux" {
		libPath = "libc.so.6"
	}
	hEnv.dylibRegistry.SetReadyManager(hEnv.resolveDylibTarget(libPath), hEnv.dylibMgr)
	pathPtr := uint32(0x1900)
	mod.Memory().Write(pathPtr, []byte(libPath))
	openOv := uint32(0x1a00)
	hEnv.sys_dylib_open(context.Background(), mod, []uint64{
		uint64(openOv),
		uint64(pathPtr),
		uint64(len(libPath)),
		0,
	})
	pollUntilComplete(t, hEnv, mod, openOv)
	ovBuf, _ := mod.Memory().Read(openOv, 24)
	libHandle := binary.LittleEndian.Uint64(ovBuf[16:24])
	if libHandle == 0 {
		t.Fatalf("dylib_open failed for callback test: ovBuf=%v", ovBuf)
	}

	// Register two callbacks A (ID=1) and B (ID=2)
	cbSig := EncodeFuncType([]byte{WasmTypeI64}, []byte{WasmTypeI64})
	mod.Memory().Write(0x1000, cbSig)

	cbOvA := uint32(0x3000)
	hEnv.sys_dylib_callback_register(context.Background(), mod, []uint64{
		0x2000, libHandle, 0x1000, uint64(len(cbSig)), uint64(cbOvA), CbOvTotalSize,
	})
	pollUntilComplete(t, hEnv, mod, 0x2000)

	cbOvB := uint32(0x3200)
	hEnv.sys_dylib_callback_register(context.Background(), mod, []uint64{
		0x2100, libHandle, 0x1000, uint64(len(cbSig)), uint64(cbOvB), CbOvTotalSize,
	})
	pollUntilComplete(t, hEnv, mod, 0x2100)

	// Simulate Thread 1 invoking callback A1, then reentrantly invoking callback B
	threadID := uint64(999)
	invA1 := uint64(101)
	invB := uint64(102)

	hEnv.dylibMgr.incomingInvocations <- &DylibCbInvokeEvent{
		CbID:     1,
		InvID:    invA1,
		ThreadID: threadID,
		Args:     []uint64{10},
	}
	hEnv.dylibMgr.BeforeRun(context.Background(), mod)

	// Guest accepts A1 asynchronously: clears completion flag
	bufA, _ := mod.Memory().Read(cbOvA, CbOvTotalSize)
	binary.LittleEndian.PutUint32(bufA[0:4], 0)
	mod.Memory().Write(cbOvA, bufA[:4])
	hEnv.dylibMgr.AfterRun(context.Background(), mod)

	// Now Thread 1 invokes B (nested inside A1 on Thread 1's stack)
	hEnv.dylibMgr.incomingInvocations <- &DylibCbInvokeEvent{
		CbID:     2,
		InvID:    invB,
		ThreadID: threadID,
		Args:     []uint64{20},
	}
	hEnv.dylibMgr.BeforeRun(context.Background(), mod)

	// Guest accepts B asynchronously: clears completion flag on cbOvB
	bufB, _ := mod.Memory().Read(cbOvB, CbOvTotalSize)
	binary.LittleEndian.PutUint32(bufB[0:4], 0)
	mod.Memory().Write(cbOvB, bufB[:4])
	hEnv.dylibMgr.AfterRun(context.Background(), mod)

	// Check that stack for Thread 1 has [A1, B]
	hEnv.dylibMgr.mu.Lock()
	stack := hEnv.dylibMgr.threadStacks[threadID]
	if len(stack) != 2 || stack[0].invID != invA1 || stack[1].invID != invB {
		hEnv.dylibMgr.mu.Unlock()
		t.Fatalf("unexpected thread stack: %+v", stack)
	}
	hEnv.dylibMgr.mu.Unlock()

	// NOW OUT OF ORDER COMPLETION:
	// A1 completes while B is still pending!
	// Guest writes return value for A1 into cbOvA and sets completed = 1
	binary.LittleEndian.PutUint64(bufA[32:40], invA1)     // invID = 101
	binary.LittleEndian.PutUint64(bufA[48+8:48+16], 1000) // result = 1000
	binary.LittleEndian.PutUint32(bufA[0:4], 1)
	mod.Memory().Write(cbOvA, bufA)

	// Host runs AfterRun
	hEnv.dylibMgr.AfterRun(context.Background(), mod)

	// A1 should be marked FrameStateCompleted, but PARKED because B is top of stack!
	hEnv.dylibMgr.mu.Lock()
	frameA1 := hEnv.dylibMgr.allFrames[invA1]
	if frameA1 == nil || frameA1.state != FrameStateCompleted {
		hEnv.dylibMgr.mu.Unlock()
		t.Fatalf("frame A1 should be in FrameStateCompleted (parked), got: %+v", frameA1)
	}
	// Stack should still have both [A1, B]
	stack = hEnv.dylibMgr.threadStacks[threadID]
	if len(stack) != 2 {
		hEnv.dylibMgr.mu.Unlock()
		t.Fatalf("stack should still have 2 frames, got %d", len(stack))
	}
	hEnv.dylibMgr.mu.Unlock()

	// Now B completes!
	bufB, _ = mod.Memory().Read(cbOvB, CbOvTotalSize)
	binary.LittleEndian.PutUint64(bufB[32:40], invB)
	binary.LittleEndian.PutUint64(bufB[48+8:48+16], 2000)
	binary.LittleEndian.PutUint32(bufB[0:4], 1)
	mod.Memory().Write(cbOvB, bufB)

	// Host runs AfterRun: B completes, unparks both B and A1, unwinding the entire stack!
	hEnv.dylibMgr.AfterRun(context.Background(), mod)

	hEnv.dylibMgr.mu.Lock()
	stack = hEnv.dylibMgr.threadStacks[threadID]
	if len(stack) != 0 {
		hEnv.dylibMgr.mu.Unlock()
		t.Fatalf("thread stack should be fully unwound, got %d frames", len(stack))
	}
	hEnv.dylibMgr.mu.Unlock()
}

func pollUntilComplete(t *testing.T, hEnv *HostEnv, mod api.Module, ovPtr uint32) {
	start := time.Now()
	for {
		hEnv.Poll(context.Background(), mod)
		buf, ok := mod.Memory().Read(ovPtr, 8)
		if ok {
			flags := binary.LittleEndian.Uint32(buf[0:4])
			if flags&1 != 0 {
				return
			}
		}
		if time.Since(start) > time.Second*3 {
			t.Fatalf("operation on ovPtr 0x%x timed out", ovPtr)
		}
	}
}
