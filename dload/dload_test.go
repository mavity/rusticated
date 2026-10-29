package dload

import (
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
	"unsafe"
)

func TestDloadOpenConfiguredLibrary(t *testing.T) {
	path := os.Getenv("DLOAD_TEST_LIBRARY")
	if path == "" {
		t.Skip("set DLOAD_TEST_LIBRARY to test opening a native library through the satellite")
	}

	handle, err := DylibOpen(path, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", path, err)
	}
	if err := DylibClose(handle); err != nil {
		t.Fatalf("DylibClose(%d): %v", handle, err)
	}
}

func isWindowsHost() bool {
	if runtime.GOOS == "windows" {
		return true
	}
	if os.PathSeparator == '\\' {
		return true
	}
	for _, pair := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(pair), "SYSTEMROOT=") || strings.HasPrefix(strings.ToUpper(pair), "WINDIR=") {
			return true
		}
	}
	return false
}

func TestDloadBasic(t *testing.T) {
	libName := "kernel32.dll"
	fnName := "GetCurrentProcessId"
	if !isWindowsHost() {
		libName = "libc.so.6"
		fnName = "getpid"
	}

	handle, err := DylibOpen(libName, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", libName, err)
	}
	defer DylibClose(handle)

	sym, err := DylibSym(handle, fnName)
	if err != nil {
		t.Fatalf("DylibSym(%q): %v", fnName, err)
	}
	if sym == 0 {
		t.Fatalf("expected non-zero symbol address")
	}

	// Call function (0 args, returns uint64)
	res, err := DylibCall(sym, []byte{0x60, 0, 1, WasmTypeI64}, nil, 0)
	if err != nil {
		t.Fatalf("DylibCall: %v", err)
	}
	if len(res) == 0 || res[0] == 0 {
		t.Fatalf("expected non-zero PID, got %v", res)
	}
	t.Logf("Called %s -> returned %d", fnName, res[0])

	// Test Memory Allocation, Write, and Read
	allocSize := uint64(64)
	memPtr, err := DylibAlloc(handle, allocSize)
	if err != nil {
		t.Fatalf("DylibAlloc: %v", err)
	}
	defer DylibFree(handle, memPtr)

	testData := []byte("hello from dload!")
	if err := DylibWriteMem(handle, memPtr, testData); err != nil {
		t.Fatalf("DylibWriteMem: %v", err)
	}

	readBuf := make([]byte, len(testData))
	if err := DylibReadMem(handle, memPtr, readBuf); err != nil {
		t.Fatalf("DylibReadMem: %v", err)
	}
	if string(readBuf) != string(testData) {
		t.Fatalf("memory read mismatch: got %q, want %q", string(readBuf), string(testData))
	}
}

func TestNormalizeNativeLibraryPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{`\C:\Users\mihai\AppData\Local\kabibi\litert_cache\lib\litert_lm_ext.dll`, `C:\Users\mihai\AppData\Local\kabibi\litert_cache\lib\litert_lm_ext.dll`},
		{`/C:/Users/mihai/AppData/Local/kabibi/litert_cache/lib/litert_lm_ext.dll`, `C:\Users\mihai\AppData\Local\kabibi\litert_cache\lib\litert_lm_ext.dll`},
		{`\c:/Users/test/file.dll`, `c:\Users\test\file.dll`},
		{`/d:\dir\file.dll`, `d:\dir\file.dll`},
		{`C:\Users\mihai\file.dll`, `C:\Users\mihai\file.dll`},
		{`/usr/local/lib/libfoo.so`, `/usr/local/lib/libfoo.so`},
		{`kernel32.dll`, `kernel32.dll`},
		{``, ``},
	}
	for _, tc := range tests {
		if got := normalizeNativeLibraryPath(tc.in); got != tc.want {
			t.Errorf("normalizeNativeLibraryPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestDloadOpenAbsoluteAndLeadingSlashWindowsPaths(t *testing.T) {
	if !isWindowsHost() {
		t.Skip("Windows DLL path test requires Windows host")
	}
	sysRoot := `C:\Windows`
	for _, pair := range os.Environ() {
		if k, v, ok := strings.Cut(pair, "="); ok && strings.EqualFold(k, "SystemRoot") && v != "" {
			sysRoot = v
			break
		}
	}
	absDLL := sysRoot + `\System32\kernel32.dll`
	if _, err := os.Stat(absDLL); err != nil {
		t.Skipf("cannot stat %s: %v", absDLL, err)
	}

	variants := []string{
		absDLL,
		`\` + absDLL,                                           // \C:\Windows\System32\kernel32.dll
		`/` + strings.ReplaceAll(absDLL, `\`, `/`),             // /C:/Windows/System32/kernel32.dll
		`\` + strings.ReplaceAll(absDLL, `\`, `/`),             // \C:/Windows/System32/kernel32.dll
	}
	for _, v := range variants {
		t.Run(v, func(t *testing.T) {
			h, err := DylibOpen(v, RTLD_NOW)
			if err != nil {
				t.Fatalf("DylibOpen(%q) failed: %v", v, err)
			}
			defer DylibClose(h)

			// Also test passing a file path through CString to GetFileAttributesA in kernel32.dll
			sym, err := DylibSym(h, "GetFileAttributesA")
			if err != nil || sym == 0 {
				t.Fatalf("DylibSym(GetFileAttributesA): %v", err)
			}
			cStr, freeFn, err := CString(h, normalizeNativeLibraryPath(v))
			if err != nil {
				t.Fatalf("CString(%q): %v", v, err)
			}
			defer freeFn()
			res, err := DylibCall(sym, []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}, []uint64{cStr}, 0)
			if err != nil || len(res) == 0 || uint32(res[0]) == 0xFFFFFFFF {
				t.Fatalf("GetFileAttributesA(%q) returned %v, err=%v (expected valid attributes != INVALID_FILE_ATTRIBUTES)", v, res, err)
			}
		})
	}
}

func TestDloadSynchronousCallbackOnMainThread(t *testing.T) {
	libName := "kernel32.dll"
	if !isWindowsHost() {
		libName = "libc.so.6"
	}
	handle, err := DylibOpen(libName, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", libName, err)
	}
	defer DylibClose(handle)

	var cbMarker int
	sig := []byte{0x60, 2, WasmTypeI64, WasmTypeI64, 1, WasmTypeI64}
	trampoline, err := DylibRegisterCallbackWithContext(handle, sig, unsafe.Pointer(&cbMarker), 0,
		func(invID uint64, threadID uint64, args []uint64) []uint64 {
			if invID == 0 || threadID == 0 {
				t.Errorf("expected non-zero invID (%d) and threadID (%d)", invID, threadID)
			}
			return []uint64{args[0] + args[1] + 42}
		})
	if err != nil {
		t.Fatalf("DylibRegisterCallbackWithContext: %v", err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&cbMarker))

	done := make(chan []uint64, 1)
	errCh := make(chan error, 1)
	go func() {
		res, callErr := DylibCall(trampoline, sig, []uint64{100, 200}, 0)
		if callErr != nil {
			errCh <- callErr
			return
		}
		done <- res
	}()

	select {
	case callErr := <-errCh:
		t.Fatalf("DylibCall(trampoline) error: %v", callErr)
	case res := <-done:
		if len(res) == 0 || res[0] != 342 {
			t.Fatalf("DylibCall(trampoline) = %v, want [342]", res)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: synchronous callback on dload main reader thread did not complete")
	}
}

func TestDloadSynchronousCallbackWithReentrantCallsAndMemoryOps(t *testing.T) {
	libName := "kernel32.dll"
	fnName := "GetCurrentProcessId"
	if !isWindowsHost() {
		libName = "libc.so.6"
		fnName = "getpid"
	}
	handle, err := DylibOpen(libName, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", libName, err)
	}
	defer DylibClose(handle)

	pidSym, err := DylibSym(handle, fnName)
	if err != nil {
		t.Fatalf("DylibSym(%q): %v", fnName, err)
	}

	var cbMarker int
	sig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI64}
	trampoline, err := DylibRegisterCallbackWithContext(handle, sig, unsafe.Pointer(&cbMarker), 0,
		func(invID uint64, _ uint64, args []uint64) []uint64 {
			// 1. Re-entrant memory alloc/write/read/free inside callback
			ptr, allocErr := DylibAlloc(handle, 32)
			if allocErr != nil || ptr == 0 {
				t.Errorf("re-entrant DylibAlloc failed: %v", allocErr)
				return []uint64{0}
			}
			defer DylibFree(handle, ptr)
			msg := []byte("reentrant-ok")
			if wErr := DylibWriteMem(handle, ptr, msg); wErr != nil {
				t.Errorf("re-entrant DylibWriteMem failed: %v", wErr)
				return []uint64{0}
			}
			got := make([]byte, len(msg))
			if rErr := DylibReadMem(handle, ptr, got); rErr != nil || string(got) != string(msg) {
				t.Errorf("re-entrant DylibReadMem = %q, %v; want %q", got, rErr, msg)
				return []uint64{0}
			}
			// 2. Re-entrant DylibCall using parentInvID = invID
			pidRes, cErr := DylibCall(pidSym, []byte{0x60, 0, 1, WasmTypeI64}, nil, invID)
			if cErr != nil || len(pidRes) == 0 || pidRes[0] == 0 {
				t.Errorf("re-entrant DylibCall(%s) = %v, %v", fnName, pidRes, cErr)
				return []uint64{0}
			}
			return []uint64{pidRes[0] + args[0]}
		})
	if err != nil {
		t.Fatalf("DylibRegisterCallbackWithContext: %v", err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&cbMarker))

	outerPid, err := DylibCall(pidSym, []byte{0x60, 0, 1, WasmTypeI64}, nil, 0)
	if err != nil || len(outerPid) == 0 {
		t.Fatalf("outer DylibCall(%s): %v", fnName, err)
	}

	res, err := DylibCall(trampoline, sig, []uint64{7}, 0)
	if err != nil || len(res) == 0 || res[0] != outerPid[0]+7 {
		t.Fatalf("re-entrant callback result = %v, %v; want %d", res, err, outerPid[0]+7)
	}
}

func TestDloadNestedCallbacksLIFO(t *testing.T) {
	libName := "kernel32.dll"
	if !isWindowsHost() {
		libName = "libc.so.6"
	}
	handle, err := DylibOpen(libName, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", libName, err)
	}
	defer DylibClose(handle)

	var markerInner, markerOuter int
	sig2 := []byte{0x60, 2, WasmTypeI64, WasmTypeI64, 1, WasmTypeI64}

	trampolineInner, err := DylibRegisterCallbackWithContext(handle, sig2, unsafe.Pointer(&markerInner), 0,
		func(invID uint64, _ uint64, args []uint64) []uint64 {
			return []uint64{args[0] * args[1]}
		})
	if err != nil {
		t.Fatalf("register inner callback: %v", err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&markerInner))

	trampolineOuter, err := DylibRegisterCallbackWithContext(handle, sig2, unsafe.Pointer(&markerOuter), 0,
		func(outerInvID uint64, _ uint64, args []uint64) []uint64 {
			innerRes, innerErr := DylibCall(trampolineInner, sig2, []uint64{3, 4}, outerInvID)
			if innerErr != nil || len(innerRes) == 0 {
				t.Errorf("nested inner DylibCall failed: %v", innerErr)
				return []uint64{0}
			}
			return []uint64{args[0] + args[1] + innerRes[0]}
		})
	if err != nil {
		t.Fatalf("register outer callback: %v", err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&markerOuter))

	res, err := DylibCall(trampolineOuter, sig2, []uint64{10, 20}, 0)
	if err != nil || len(res) == 0 || res[0] != 42 {
		t.Fatalf("nested LIFO callback result = %v, %v; want 42", res, err)
	}
}

func TestDloadBackgroundThreadCallbackFunnelsParentInvIDToSameOSThread(t *testing.T) {
	if !isWindowsHost() {
		t.Skip("CreateThread OS thread-affinity test runs on Windows host")
	}
	handle, err := DylibOpen("kernel32.dll", RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(kernel32.dll): %v", err)
	}
	defer DylibClose(handle)

	symCreateThread, err := DylibSym(handle, "CreateThread")
	if err != nil {
		t.Fatalf("DylibSym(CreateThread): %v", err)
	}
	symWaitForSingleObject, err := DylibSym(handle, "WaitForSingleObject")
	if err != nil {
		t.Fatalf("DylibSym(WaitForSingleObject): %v", err)
	}
	symCloseHandle, err := DylibSym(handle, "CloseHandle")
	if err != nil {
		t.Fatalf("DylibSym(CloseHandle): %v", err)
	}
	symGetCurrentThreadId, err := DylibSym(handle, "GetCurrentThreadId")
	if err != nil {
		t.Fatalf("DylibSym(GetCurrentThreadId): %v", err)
	}

	// Query the main reader thread's OS thread ID
	mainTIDRes, err := DylibCall(symGetCurrentThreadId, []byte{0x60, 0, 1, WasmTypeI32}, nil, 0)
	if err != nil || len(mainTIDRes) == 0 {
		t.Fatalf("GetCurrentThreadId (main): %v", err)
	}
	mainTID := uint32(mainTIDRes[0])

	var cbMarker int
	var reportedThreadID uint32
	var reentrantThreadID uint32
	var paramSeen uint64

	// ThreadProc signature: DWORD WINAPI ThreadProc(LPVOID lpParameter)
	cbSig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}
	trampoline, err := DylibRegisterCallbackWithContext(handle, cbSig, unsafe.Pointer(&cbMarker), 0,
		func(invID uint64, threadID uint64, args []uint64) []uint64 {
			reportedThreadID = uint32(threadID)
			paramSeen = args[0]
			// Call GetCurrentThreadId with parentInvID = invID so dload funnels it onto this background OS thread!
			tidRes, callErr := DylibCall(symGetCurrentThreadId, []byte{0x60, 0, 1, WasmTypeI32}, nil, invID)
			if callErr == nil && len(tidRes) > 0 {
				reentrantThreadID = uint32(tidRes[0])
			}
			return []uint64{77}
		})
	if err != nil {
		t.Fatalf("DylibRegisterCallbackWithContext: %v", err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&cbMarker))

	// Spawn a real OS thread in the worker: CreateThread(NULL, 0, trampoline, 0x1234, 0, NULL)
	createSig := []byte{0x60, 6, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI32, WasmTypeI64, 1, WasmTypeI64}
	thRes, err := DylibCall(symCreateThread, createSig, []uint64{0, 0, trampoline, 0x1234, 0, 0}, 0)
	if err != nil || len(thRes) == 0 || thRes[0] == 0 {
		t.Fatalf("CreateThread failed: res=%v err=%v", thRes, err)
	}
	threadHandle := thRes[0]
	defer DylibCall(symCloseHandle, []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}, []uint64{threadHandle}, 0)

	// Wait up to 3000ms for the background thread to finish
	waitRes, err := DylibCall(symWaitForSingleObject, []byte{0x60, 2, WasmTypeI64, WasmTypeI32, 1, WasmTypeI32}, []uint64{threadHandle, 3000}, 0)
	if err != nil || len(waitRes) == 0 || uint32(waitRes[0]) != 0 {
		t.Fatalf("WaitForSingleObject returned %v, err=%v (expected WAIT_OBJECT_0 = 0)", waitRes, err)
	}

	if paramSeen != 0x1234 {
		t.Fatalf("background thread param = 0x%x, want 0x1234", paramSeen)
	}
	if reportedThreadID == 0 || reportedThreadID == mainTID {
		t.Fatalf("reported background threadID = %d, mainTID = %d (expected distinct background OS thread)", reportedThreadID, mainTID)
	}
	if reentrantThreadID != reportedThreadID {
		t.Fatalf("reentrant GetCurrentThreadId(parentInvID=invID) ran on thread %d, want callback OS thread %d (mainTID=%d)",
			reentrantThreadID, reportedThreadID, mainTID)
	}
}

func TestDloadOuterCallWhileCallbackInFlightDoesNotDeadlock(t *testing.T) {
	if !isWindowsHost() {
		t.Skip("CreateThread concurrent outer-call test runs on Windows host")
	}
	handle, err := DylibOpen("kernel32.dll", RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(kernel32.dll): %v", err)
	}
	defer DylibClose(handle)

	symCreateThread, _ := DylibSym(handle, "CreateThread")
	symWaitForSingleObject, _ := DylibSym(handle, "WaitForSingleObject")
	symCloseHandle, _ := DylibSym(handle, "CloseHandle")
	symGetCurrentProcessId, _ := DylibSym(handle, "GetCurrentProcessId")

	var cbMarker int
	cbEntered := make(chan struct{})
	outerSubmitted := make(chan struct{})
	outerDone := make(chan error, 1)

	cbSig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}
	trampoline, err := DylibRegisterCallbackWithContext(handle, cbSig, unsafe.Pointer(&cbMarker), 0,
		func(invID uint64, _ uint64, _ []uint64) []uint64 {
			close(cbEntered)
			// Wait until the outer goroutine has submitted its parentInvID=0 call
			<-outerSubmitted
			time.Sleep(20 * time.Millisecond)
			return []uint64{0}
		})
	if err != nil {
		t.Fatal(err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&cbMarker))

	createSig := []byte{0x60, 6, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI32, WasmTypeI64, 1, WasmTypeI64}
	thRes, err := DylibCall(symCreateThread, createSig, []uint64{0, 0, trampoline, 0, 0, 0}, 0)
	if err != nil || len(thRes) == 0 || thRes[0] == 0 {
		t.Fatalf("CreateThread failed: %v", err)
	}
	threadHandle := thRes[0]
	defer DylibCall(symCloseHandle, []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}, []uint64{threadHandle}, 0)

	<-cbEntered
	go func() {
		close(outerSubmitted)
		_, cErr := DylibCall(symGetCurrentProcessId, []byte{0x60, 0, 1, WasmTypeI32}, nil, 0)
		outerDone <- cErr
	}()

	select {
	case cErr := <-outerDone:
		if cErr != nil {
			t.Fatalf("outer DylibCall failed: %v", cErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: outer DylibCall while callback was in flight")
	}

	_, _ = DylibCall(symWaitForSingleObject, []byte{0x60, 2, WasmTypeI64, WasmTypeI32, 1, WasmTypeI32}, []uint64{threadHandle, 3000}, 0)
}

func TestDloadConcurrentBackgroundCallbacksStress(t *testing.T) {
	if !isWindowsHost() {
		t.Skip("CreateThread concurrent stress test runs on Windows host")
	}
	handle, err := DylibOpen("kernel32.dll", RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(kernel32.dll): %v", err)
	}
	defer DylibClose(handle)

	symCreateThread, _ := DylibSym(handle, "CreateThread")
	symWaitForSingleObject, _ := DylibSym(handle, "WaitForSingleObject")
	symCloseHandle, _ := DylibSym(handle, "CloseHandle")
	symGetCurrentThreadId, _ := DylibSym(handle, "GetCurrentThreadId")

	var cbMarker int
	cbSig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}
	errCh := make(chan error, 32)

	trampoline, err := DylibRegisterCallbackWithContext(handle, cbSig, unsafe.Pointer(&cbMarker), 0,
		func(invID uint64, threadID uint64, args []uint64) []uint64 {
			// Perform a re-entrant memory allocation/read/write + OS thread ID check
			buf, aErr := DylibAlloc(handle, 16)
			if aErr != nil || buf == 0 {
				errCh <- aErr
				return []uint64{1}
			}
			defer DylibFree(handle, buf)
			payload := []byte("stress-test!")
			if wErr := DylibWriteMem(handle, buf, payload); wErr != nil {
				errCh <- wErr
				return []uint64{1}
			}
			out := make([]byte, len(payload))
			if rErr := DylibReadMem(handle, buf, out); rErr != nil || string(out) != string(payload) {
				errCh <- rErr
				return []uint64{1}
			}
			tidRes, cErr := DylibCall(symGetCurrentThreadId, []byte{0x60, 0, 1, WasmTypeI32}, nil, invID)
			if cErr != nil || len(tidRes) == 0 || uint32(tidRes[0]) != uint32(threadID) {
				errCh <- cErr
				return []uint64{1}
			}
			return []uint64{args[0]}
		})
	if err != nil {
		t.Fatal(err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&cbMarker))

	const numThreads = 8
	threads := make([]uint64, 0, numThreads)
	createSig := []byte{0x60, 6, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI32, WasmTypeI64, 1, WasmTypeI64}
	for i := 0; i < numThreads; i++ {
		thRes, err := DylibCall(symCreateThread, createSig, []uint64{0, 0, trampoline, uint64(100 + i), 0, 0}, 0)
		if err != nil || len(thRes) == 0 || thRes[0] == 0 {
			t.Fatalf("CreateThread(%d) failed: %v", i, err)
		}
		threads = append(threads, thRes[0])
	}
	for i, th := range threads {
		waitRes, err := DylibCall(symWaitForSingleObject, []byte{0x60, 2, WasmTypeI64, WasmTypeI32, 1, WasmTypeI32}, []uint64{th, 5000}, 0)
		_, _ = DylibCall(symCloseHandle, []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}, []uint64{th}, 0)
		if err != nil || len(waitRes) == 0 || uint32(waitRes[0]) != 0 {
			t.Fatalf("WaitForSingleObject(thread %d) = %v, %v", i, waitRes, err)
		}
	}
	select {
	case cbErr := <-errCh:
		t.Fatalf("stress callback reported error: %v", cbErr)
	default:
	}
}

func isDarwinHost() bool {
	if runtime.GOOS == "darwin" {
		return true
	}
	if _, err := os.Stat("/usr/lib/libSystem.B.dylib"); err == nil {
		return true
	}
	return false
}

// TestDloadAsyncStreamingCallbacksWithoutBlockingMainThread reproduces the
// READER_BUSY race when background OS threads fire callbacks while the main
// dload worker thread is NOT parked inside a synchronous WaitForSingleObject /
// pthread_join call (mirroring litert_lm_conversation_send_message_stream).
func TestDloadAsyncStreamingCallbacksWithoutBlockingMainThread(t *testing.T) {
	var (
		handle     uint64
		pthreadLib uint64
		err        error
		symSelf    uint64
		selfSig    []byte
		spawnAsync func(trampoline, param uint64) error
	)

	if isWindowsHost() {
		handle, err = DylibOpen("kernel32.dll", RTLD_NOW)
		if err != nil {
			t.Fatalf("DylibOpen(kernel32.dll): %v", err)
		}
		defer DylibClose(handle)

		symCreateThread, sErr := DylibSym(handle, "CreateThread")
		if sErr != nil || symCreateThread == 0 {
			t.Fatalf("DylibSym(CreateThread): %v", sErr)
		}
		symCloseHandle, sErr := DylibSym(handle, "CloseHandle")
		if sErr != nil || symCloseHandle == 0 {
			t.Fatalf("DylibSym(CloseHandle): %v", sErr)
		}
		symGetCurrentThreadId, sErr := DylibSym(handle, "GetCurrentThreadId")
		if sErr != nil || symGetCurrentThreadId == 0 {
			t.Fatalf("DylibSym(GetCurrentThreadId): %v", sErr)
		}
		symSelf = symGetCurrentThreadId
		selfSig = []byte{0x60, 0, 1, WasmTypeI32}
		createSig := []byte{0x60, 6, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI32, WasmTypeI64, 1, WasmTypeI64}
		closeSig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}
		spawnAsync = func(trampoline, param uint64) error {
			res, cErr := DylibCall(symCreateThread, createSig, []uint64{0, 0, trampoline, param, 0, 0}, 0)
			if cErr != nil {
				return cErr
			}
			if len(res) == 0 || res[0] == 0 {
				return os.ErrInvalid
			}
			_, _ = DylibCall(symCloseHandle, closeSig, []uint64{res[0]}, 0)
			return nil
		}
	} else {
		libName := "libc.so.6"
		if isDarwinHost() {
			libName = "/usr/lib/libSystem.B.dylib"
		} else if isAndroidEnvironment() {
			libName = "libc.so"
		}
		handle, err = DylibOpen(libName, RTLD_NOW)
		if err != nil {
			t.Fatalf("DylibOpen(%q): %v", libName, err)
		}
		defer DylibClose(handle)

		pthreadLib = handle
		symPthreadCreate, sErr := DylibSym(pthreadLib, "pthread_create")
		if (sErr != nil || symPthreadCreate == 0) && !isDarwinHost() && !isAndroidEnvironment() {
			if ph, pErr := DylibOpen("libpthread.so.0", RTLD_NOW); pErr == nil {
				pthreadLib = ph
				defer DylibClose(ph)
				symPthreadCreate, sErr = DylibSym(pthreadLib, "pthread_create")
			}
		}
		if sErr != nil || symPthreadCreate == 0 {
			t.Fatalf("DylibSym(pthread_create): %v", sErr)
		}
		symPthreadDetach, sErr := DylibSym(pthreadLib, "pthread_detach")
		if sErr != nil || symPthreadDetach == 0 {
			t.Fatalf("DylibSym(pthread_detach): %v", sErr)
		}
		symPthreadSelf, sErr := DylibSym(pthreadLib, "pthread_self")
		if sErr != nil || symPthreadSelf == 0 {
			t.Fatalf("DylibSym(pthread_self): %v", sErr)
		}
		tidBuf, aErr := DylibAlloc(handle, 16)
		if aErr != nil || tidBuf == 0 {
			t.Fatalf("DylibAlloc(tidBuf): %v", aErr)
		}
		defer DylibFree(handle, tidBuf)

		symSelf = symPthreadSelf
		selfSig = []byte{0x60, 0, 1, WasmTypeI64}
		createSig := []byte{0x60, 4, WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64, 1, WasmTypeI32}
		detachSig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI32}
		spawnAsync = func(trampoline, param uint64) error {
			res, cErr := DylibCall(symPthreadCreate, createSig, []uint64{tidBuf, 0, trampoline, param}, 0)
			if cErr != nil {
				return cErr
			}
			if len(res) == 0 || int32(res[0]) != 0 {
				return os.ErrInvalid
			}
			var rawTid [8]byte
			if rErr := DylibReadMem(handle, tidBuf, rawTid[:]); rErr != nil {
				return rErr
			}
			tid := uint64(rawTid[0]) | uint64(rawTid[1])<<8 | uint64(rawTid[2])<<16 | uint64(rawTid[3])<<24 |
				uint64(rawTid[4])<<32 | uint64(rawTid[5])<<40 | uint64(rawTid[6])<<48 | uint64(rawTid[7])<<56
			_, _ = DylibCall(symPthreadDetach, detachSig, []uint64{tid}, 0)
			return nil
		}
	}

	sharedBuf, err := DylibAlloc(handle, 64)
	if err != nil || sharedBuf == 0 {
		t.Fatalf("DylibAlloc(sharedBuf): %v", err)
	}
	defer DylibFree(handle, sharedBuf)
	wantChunk := []byte("{\"role\":\"assistant\",\"content\":[{\"type\":\"text\",\"text\":\"Pont Neuf\"}]}\x00")
	if err := DylibWriteMem(handle, sharedBuf, wantChunk[:64]); err != nil {
		t.Fatalf("DylibWriteMem(sharedBuf): %v", err)
	}

	// In `dload/src/main.rs`:
	//   let frame_res = read_frame();
	//   release_reader_lock();
	//   match frame_res {
	//       Ok((hdr, body)) => dispatch_frame(hdr, &body),
	//       Err(()) => break,
	//   }
	// When the main thread finishes `read_frame()` for a re-entrant `OP_CALL_REQ`
	// (`parent_inv_id = invID`), it calls `release_reader_lock()` (`READER_BUSY = false`)
	// BEFORE `dispatch_frame` parses `args` and calls `try_delegate_call` to transition
	// `inv_slot.state` from `SLOT_WAITING` to `SLOT_HAS_CALL`.
	// If the background callback thread wakes from `Sleep(1)` / `usleep(500)` during
	// that window, it observes `inv_slot.state == SLOT_WAITING` and `READER_BUSY == false`,
	// steals `READER_BUSY`, and calls `read_frame()` on an empty stdin pipe while Go
	// is waiting for `OP_CALL_RESP` and the main thread spins on `try_acquire_reader_lock()`.
	manyArgs := make([]uint64, 16_000_000)
	var cbMarker int
	cbSig := []byte{0x60, 1, WasmTypeI64, 1, WasmTypeI64}
	cbEntered := make(chan uint64, 8)
	deadlockTrampDone := make(chan error, 1)

	dlTramp, err := DylibRegisterCallbackWithContext(handle, cbSig, unsafe.Pointer(&cbMarker), 0,
		func(invID uint64, _ uint64, _ []uint64) []uint64 {
			cbEntered <- invID
			if _, cErr := DylibCall(symSelf, selfSig, manyArgs, invID); cErr != nil {
				deadlockTrampDone <- cErr
				return []uint64{0}
			}
			var tmp [64]byte
			if rErr := DylibReadMem(handle, sharedBuf, tmp[:]); rErr != nil {
				deadlockTrampDone <- rErr
				return []uint64{0}
			}
			deadlockTrampDone <- nil
			return []uint64{0}
		})
	if err != nil {
		t.Fatalf("DylibRegisterCallbackWithContext: %v", err)
	}
	defer DylibUnregisterCallback(unsafe.Pointer(&cbMarker))

	for attempt := 1; attempt <= 5; attempt++ {
		spawnDone := make(chan error, 1)
		go func() {
			spawnDone <- spawnAsync(dlTramp, uint64(attempt))
		}()
		select {
		case sErr := <-spawnDone:
			if sErr != nil {
				t.Fatalf("spawnAsync(%d): %v", attempt, sErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("DEADLOCK REPRODUCED on attempt %d inside spawnAsync: main thread released READER_BUSY before dispatch_frame, background callback thread stole READER_BUSY and blocked in read_frame()", attempt)
		}
		select {
		case gotInv := <-cbEntered:
			t.Logf("attempt %d: callback entered Go with invID=%d", attempt, gotInv)
		case <-time.After(1 * time.Second):
			t.Fatalf("attempt %d: callback NEVER entered Go after QueueUserWorkItem!", attempt)
		}
		select {
		case cErr := <-deadlockTrampDone:
			if cErr != nil {
				t.Fatalf("attempt %d re-entrant DylibCall failed: %v", attempt, cErr)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("DEADLOCK REPRODUCED on attempt %d: main thread released READER_BUSY before dispatch_frame, background callback thread stole READER_BUSY and blocked in read_frame()", attempt)
		}
	}
}

func TestDloadDynamicTrampolineMultiPageScaling(t *testing.T) {
	libName := "kernel32.dll"
	if !isWindowsHost() {
		libName = "libc.so.6"
		if isDarwinHost() {
			libName = "/usr/lib/libSystem.B.dylib"
		} else if isAndroidEnvironment() {
			libName = "libc.so"
		}
	}
	handle, err := DylibOpen(libName, RTLD_NOW)
	if err != nil {
		t.Fatalf("DylibOpen(%q): %v", libName, err)
	}
	defer DylibClose(handle)

	// Register 300 simultaneous callbacks (spanning 3 dynamic RW->RX pages of 128
	// thunks each), invoke all 300, and verify each one dispatches its distinct
	// closure and preserves all 8 arguments.
	const numCallbacks = 300
	markers := make([]int, numCallbacks)
	trampolines := make([]uint64, numCallbacks)
	sig := []byte{
		0x60, 8,
		WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64,
		WasmTypeI64, WasmTypeI64, WasmTypeI64, WasmTypeI64,
		1, WasmTypeI64,
	}

	for i := 0; i < numCallbacks; i++ {
		idx := uint64(i + 1)
		tramp, rErr := DylibRegisterCallbackWithContext(handle, sig, unsafe.Pointer(&markers[i]), 0,
			func(_ uint64, _ uint64, args []uint64) []uint64 {
				var sum uint64
				for _, a := range args {
					sum += a
				}
				return []uint64{idx*1000 + sum}
			})
		if rErr != nil || tramp == 0 {
			t.Fatalf("register callback #%d: tramp=0x%x err=%v", i, tramp, rErr)
		}
		trampolines[i] = tramp
	}
	defer func() {
		for i := range markers {
			DylibUnregisterCallback(unsafe.Pointer(&markers[i]))
		}
	}()

	for i := 0; i < numCallbacks; i++ {
		args := []uint64{1, 2, 3, 4, 5, 6, 7, 8} // sum = 36
		res, cErr := DylibCall(trampolines[i], sig, args, 0)
		want := uint64(i+1)*1000 + 36
		if cErr != nil || len(res) == 0 || res[0] != want {
			t.Fatalf("callback #%d returned %v, err=%v; want [%d]", i, res, cErr, want)
		}
	}
}






